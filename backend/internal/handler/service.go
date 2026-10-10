package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
)

// maxUsageGuideRunes caps the length of a service usage guide (rune count).
const maxUsageGuideRunes = 20000

// validateServiceRequest checks create/update input and returns any error details.
// Create and Update share this helper so the rules cannot drift.
func validateServiceRequest(req createServiceRequest) []server.ErrorDetail {
	var details []server.ErrorDetail
	if req.Name == "" {
		details = append(details, server.NewErrorDetail("name", server.ReasonRequired, nil))
	}
	if req.BaseURL == "" {
		details = append(details, server.NewErrorDetail("base_url", server.ReasonRequired, nil))
	}
	if len(req.Credentials) == 0 {
		details = append(details, server.NewErrorDetail("credentials", server.ReasonAtLeastOne, nil))
	}
	if req.UsageGuide != nil && len([]rune(*req.UsageGuide)) > maxUsageGuideRunes {
		details = append(details, server.NewErrorDetail("usage_guide", server.ReasonMaxLength, map[string]any{"max": 20000}))
	}
	return details
}

type createServiceRequest struct {
	Name            string            `json:"name"`
	DisplayName     string            `json:"display_name"`
	BaseURL         string            `json:"base_url"`
	Credentials     map[string]string `json:"credentials"`
	CredentialHints map[string]string `json:"credential_hints"`
	Description     *string           `json:"description,omitempty"`
	UsageGuide      *string           `json:"usage_guide,omitempty"`
	ServerID        *uint             `json:"server_id,omitempty"`
}

type relayRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Mode    string            `json:"mode"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// ServiceHandler exposes the service management and relay endpoints.
type ServiceHandler struct {
	svc       *service.ServiceRelayService
	auditRepo *repository.AuditEventRepo
}

// NewServiceHandler creates a ServiceHandler with the given service.
func NewServiceHandler(svc *service.ServiceRelayService, auditRepo *repository.AuditEventRepo) *ServiceHandler {
	return &ServiceHandler{svc: svc, auditRepo: auditRepo}
}

// Create handles POST /api/v1/services.
func (h *ServiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}

	if details := validateServiceRequest(req); len(details) > 0 {
		server.WriteError(w, r, server.NewValidationError(details))
		return
	}

	input := service.CreateServiceInput{
		Name:            req.Name,
		DisplayName:     req.DisplayName,
		BaseURL:         req.BaseURL,
		Credentials:     req.Credentials,
		CredentialHints: req.CredentialHints,
		Description:     req.Description,
		UsageGuide:      req.UsageGuide,
		ServerID:        req.ServerID,
	}

	svc, err := h.svc.Create(r.Context(), input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageResource(r, "service", svc.ID, svc.Name, svc.ServerID)
	usageCommitted(r)
	server.WriteJSON(w, http.StatusCreated, svc)
}

// List handles GET /api/v1/services.
func (h *ServiceHandler) List(w http.ResponseWriter, r *http.Request) {
	var serverID *uint
	if sidStr := r.URL.Query().Get("server_id"); sidStr != "" {
		sid, err := strconv.ParseUint(sidStr, 10, strconv.IntSize)
		if err != nil {
			server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID))
			return
		}
		uid := uint(sid)
		serverID = &uid
	}

	services, err := h.svc.List(r.Context(), serverID)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	// Only surface services the caller may reach — mirrors the server-scope
	// check the relay handler applies, so a key scoped to one server cannot
	// read usage guides of services bound to other servers.
	claims := mw.GetUserClaims(r.Context())
	out := make([]model.Service, 0, len(services))
	for _, s := range services {
		if s.ServerID != nil && !mw.CheckServerAccess(claims, *s.ServerID) {
			continue
		}
		out = append(out, s)
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// Relay handles POST /api/v1/services/{id}/relay.
func (h *ServiceHandler) Relay(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServiceID))
		return
	}

	claims := mw.GetUserClaims(r.Context())
	svc, getErr := h.svc.Get(r.Context(), id)
	if getErr != nil {
		server.WriteError(w, r, getErr)
		return
	}
	usageResource(r, "service", id, svc.Name, svc.ServerID)
	if getErr == nil && svc.ServerID != nil {
		if !mw.CheckServerAccess(claims, *svc.ServerID) {
			server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonAPIKeyServiceDenied))
			return
		}
	}

	var req relayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}
	if !service.RelayModes[req.Mode] {
		// mode is a control field; it is never forwarded upstream.
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}

	op := usage.FromContext(r.Context())
	op.SetMetadata("method", req.Method)
	op.Begin()
	result, relayErr := h.svc.RelayWithResult(r.Context(), id, service.RelayInput{
		Method:  req.Method,
		Path:    req.Path,
		Mode:    req.Mode,
		Headers: req.Headers,
		Body:    req.Body,
	}, w)
	if result.UpstreamStatus != 0 {
		op.SetRelayResult(result.UpstreamStatus, result.BytesCopied)
	}
	if result.Outcome != "" {
		op.SetResult(result.Outcome, result.Reason)
	}
	if relayErr != nil && !result.HeadersWritten {
		server.WriteError(w, r, relayErr)
	}
}

// Get handles GET /api/v1/services/{id}.
func (h *ServiceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServiceID))
		return
	}

	svc, err := h.svc.Get(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	if svc.ServerID != nil {
		claims := mw.GetUserClaims(r.Context())
		if !mw.CheckServerAccess(claims, *svc.ServerID) {
			server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonAPIKeyServiceDenied))
			return
		}
	}
	server.WriteJSON(w, http.StatusOK, svc)
}

// GetCredentials handles GET /api/v1/services/{id}/credentials.
func (h *ServiceHandler) GetCredentials(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServiceID))
		return
	}
	snapshot, err := h.svc.Get(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageResource(r, "service", id, snapshot.Name, snapshot.ServerID)
	if snapshot.ServerID != nil && !mw.CheckServerAccess(mw.GetUserClaims(r.Context()), *snapshot.ServerID) {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, server.ReasonAPIKeyServiceDenied))
		return
	}
	creds, err := h.svc.GetCredentials(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}

	writeRevealAudit(r, h.auditRepo, "service.credentials", "service", id)
	usageCommitted(r)

	server.WriteJSON(w, http.StatusOK, creds)
}

// Update handles PUT /api/v1/services/{id}.
func (h *ServiceHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServiceID))
		return
	}

	var req createServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest))
		return
	}

	if details := validateServiceRequest(req); len(details) > 0 {
		server.WriteError(w, r, server.NewValidationError(details))
		return
	}

	input := service.CreateServiceInput{
		Name:            req.Name,
		DisplayName:     req.DisplayName,
		BaseURL:         req.BaseURL,
		Credentials:     req.Credentials,
		CredentialHints: req.CredentialHints,
		Description:     req.Description,
		UsageGuide:      req.UsageGuide,
		ServerID:        req.ServerID,
	}

	updated, err := h.svc.Update(r.Context(), id, input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageResource(r, "service", updated.ID, updated.Name, updated.ServerID)
	usageCommitted(r)
	server.WriteJSON(w, http.StatusOK, updated)
}

// Delete handles DELETE /api/v1/services/{id}.
func (h *ServiceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServiceID))
		return
	}

	if snapshot, err := h.svc.Get(r.Context(), id); err == nil {
		usageResource(r, "service", id, snapshot.Name, snapshot.ServerID)
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		server.WriteError(w, r, err)
		return
	}
	usageCommitted(r)
	w.WriteHeader(http.StatusNoContent)
}

// parseServiceID extracts a uint path parameter named "id" from the request URL.
func parseServiceID(r *http.Request) (uint, error) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(idStr, 10, strconv.IntSize)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
