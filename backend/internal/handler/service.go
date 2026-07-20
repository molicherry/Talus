package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
)

type createServiceRequest struct {
	Name            string            `json:"name"`
	DisplayName     string            `json:"display_name"`
	BaseURL         string            `json:"base_url"`
	Credentials     map[string]string `json:"credentials"`
	CredentialHints map[string]string `json:"credential_hints"`
	Description     *string           `json:"description,omitempty"`
	ServerID        *uint             `json:"server_id,omitempty"`
}

type relayRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// ServiceHandler exposes the service management and relay endpoints.
type ServiceHandler struct {
	svc *service.ServiceRelayService
}

// NewServiceHandler creates a ServiceHandler with the given service.
func NewServiceHandler(svc *service.ServiceRelayService) *ServiceHandler {
	return &ServiceHandler{svc: svc}
}

// Create handles POST /api/v1/services.
func (h *ServiceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	var details []server.ErrorDetail
	if req.Name == "" {
		details = append(details, server.ErrorDetail{Field: "name", Message: "name is required"})
	}
	if req.BaseURL == "" {
		details = append(details, server.ErrorDetail{Field: "base_url", Message: "base_url is required"})
	}
	if len(req.Credentials) == 0 {
		details = append(details, server.ErrorDetail{Field: "credentials", Message: "at least one credential is required"})
	}
	if len(details) > 0 {
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
		ServerID:        req.ServerID,
	}

	svc, err := h.svc.Create(r.Context(), input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusCreated, svc)
}

// List handles GET /api/v1/services.
func (h *ServiceHandler) List(w http.ResponseWriter, r *http.Request) {
	var serverID *uint
	if sidStr := r.URL.Query().Get("server_id"); sidStr != "" {
		sid, err := strconv.ParseUint(sidStr, 10, 64)
		if err != nil {
			server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server_id"))
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
	server.WriteJSON(w, http.StatusOK, services)
}

// Relay handles POST /api/v1/services/{id}/relay.
func (h *ServiceHandler) Relay(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid service id"))
		return
	}

	claims := mw.GetUserClaims(r.Context())
	svc, getErr := h.svc.Get(r.Context(), uint(id))
	if getErr == nil && svc.ServerID != nil {
		if !mw.CheckServerAccess(claims, *svc.ServerID) {
			server.WriteError(w, r, server.NewAppError(http.StatusForbidden, "access denied: api key does not have access to the server this service is bound to"))
			return
		}
	}

	var req relayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	relayErr := h.svc.Relay(r.Context(), uint(id), service.RelayInput{
		Method:  req.Method,
		Path:    req.Path,
		Headers: req.Headers,
		Body:    req.Body,
	}, w)
	if relayErr != nil {
		server.WriteError(w, r, relayErr)
	}
}

// Get handles GET /api/v1/services/{id}.
func (h *ServiceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid service id"))
		return
	}

	svc, err := h.svc.Get(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, svc)
}

// Update handles PUT /api/v1/services/{id}.
func (h *ServiceHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid service id"))
		return
	}

	var req createServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	var details []server.ErrorDetail
	if req.Name == "" {
		details = append(details, server.ErrorDetail{Field: "name", Message: "name is required"})
	}
	if req.BaseURL == "" {
		details = append(details, server.ErrorDetail{Field: "base_url", Message: "base_url is required"})
	}
	if len(req.Credentials) == 0 {
		details = append(details, server.ErrorDetail{Field: "credentials", Message: "at least one credential is required"})
	}
	if len(details) > 0 {
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
		ServerID:        req.ServerID,
	}

	updated, err := h.svc.Update(r.Context(), id, input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, updated)
}

// Delete handles DELETE /api/v1/services/{id}.
func (h *ServiceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseServiceID(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid service id"))
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		server.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseServiceID extracts a uint path parameter named "id" from the request URL.
func parseServiceID(r *http.Request) (uint, error) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
