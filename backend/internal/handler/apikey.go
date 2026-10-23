package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/service"
)

type APIKeyHandler struct {
	svc       *service.APIKeyService
	auditRepo *repository.AuditEventRepo
}

func NewAPIKeyHandler(svc *service.APIKeyService, auditRepo *repository.AuditEventRepo) *APIKeyHandler {
	return &APIKeyHandler{svc: svc, auditRepo: auditRepo}
}

type createAPIKeyRequest struct {
	Name string `json:"name,omitempty"`
	// Scopes is a pointer so an omitted field (nil → default scopes) is
	// distinguishable from an explicit empty list (→ rejected).
	Scopes    *[]string `json:"scopes"`
	ServerIDs []uint    `json:"server_ids,omitempty"`
}

func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}
	result, err := h.svc.Create(r.Context(), req.Name, req.Scopes, req.ServerIDs)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageResource(r, "api_key", result.APIKey.ID, result.APIKey.Name, nil)
	usageCommitted(r)
	server.WriteJSON(w, http.StatusCreated, result)
}

func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.List(r.Context())
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	if keys == nil {
		keys = []model.APIKey{}
	}
	for i := range keys {
		keys[i] = apiKeyView(keys[i])
	}
	server.WriteJSON(w, http.StatusOK, keys)
}

func apiKeyView(key model.APIKey) model.APIKey {
	if key.Scopes == nil {
		key.Scopes = []string{}
	}
	return key
}

func (h *APIKeyHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := parseIDParam(r)
	if err != nil || id == 0 {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidKeyID))
		return
	}
	revision, err := decodeAPIKeyRotation(w, r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}
	result, err := h.svc.Rotate(r.Context(), id, revision)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	*result.APIKey = apiKeyView(*result.APIKey)
	usageResource(r, "api_key", id, result.APIKey.Name, nil)
	usageCommitted(r)
	server.WriteJSON(w, http.StatusOK, result)
}

func decodeAPIKeyRotation(w http.ResponseWriter, r *http.Request) (int64, error) {
	var req struct {
		ExpectedRevision string `json:"expected_revision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	invalid := server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
	if err := decoder.Decode(&req); err != nil {
		return 0, invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return 0, invalid
	}
	revision, err := strconv.ParseInt(req.ExpectedRevision, 10, 64)
	if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != req.ExpectedRevision {
		return 0, invalid
	}
	return revision, nil
}

func (h *APIKeyHandler) UpdatePermissions(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil || id == 0 {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidKeyID))
		return
	}
	input, err := decodeAPIKeyPermissions(w, r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}
	key, err := h.svc.UpdatePermissions(r.Context(), id, input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageResource(r, "api_key", id, key.Name, nil)
	usageCommitted(r)
	server.WriteJSON(w, http.StatusOK, apiKeyView(*key))
}

// RawMessage distinguishes omission from null; a pointer-to-slice alone does
// not. Unknown fields and trailing JSON must not look like successful edits.
func decodeAPIKeyPermissions(w http.ResponseWriter, r *http.Request) (service.UpdateAPIKeyPermissionsInput, error) {
	var req struct {
		ExpectedRevision string          `json:"expected_revision"`
		Scopes           json.RawMessage `json:"scopes"`
		ServerIDs        json.RawMessage `json:"server_ids"`
	}
	var input service.UpdateAPIKeyPermissionsInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	decoder.DisallowUnknownFields()
	invalid := server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
	if err := decoder.Decode(&req); err != nil {
		return input, invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return input, invalid
	}
	revision, err := strconv.ParseInt(req.ExpectedRevision, 10, 64)
	if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != req.ExpectedRevision {
		return input, invalid
	}
	input.ExpectedRevision = revision
	if req.Scopes != nil {
		var scopes []string
		if bytes.Equal(bytes.TrimSpace(req.Scopes), []byte("null")) || json.Unmarshal(req.Scopes, &scopes) != nil {
			return input, invalid
		}
		input.Scopes = &scopes
	}
	if req.ServerIDs != nil {
		var ids []uint
		if bytes.Equal(bytes.TrimSpace(req.ServerIDs), []byte("null")) || json.Unmarshal(req.ServerIDs, &ids) != nil {
			return input, invalid
		}
		input.ServerIDs = &ids
	}
	if input.Scopes == nil && input.ServerIDs == nil {
		return input, invalid
	}
	return input, nil
}

func (h *APIKeyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidKeyID))
		return
	}
	h.svc.CaptureSnapshot(r.Context(), id)
	if err := h.svc.Delete(r.Context(), id); err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageCommitted(r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIKeyHandler) Reveal(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidKeyID))
		return
	}
	rawKey, err := h.svc.Reveal(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}

	writeRevealAudit(r, h.auditRepo, "api_key.reveal", "api_key", id)
	usageCommitted(r)

	server.WriteJSON(w, http.StatusOK, rawKey)
}
