package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/server"
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
