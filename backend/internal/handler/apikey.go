package handler

import (
	"encoding/json"
	"net/http"

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
	server.WriteJSON(w, http.StatusOK, keys)
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
