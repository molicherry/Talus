package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
)

// CreateServerRequest is the JSON body for creating a server.
type CreateServerRequest struct {
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	Port         int     `json:"port"`
	Description  *string `json:"description,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	CredentialID *uint   `json:"credential_id,omitempty"`
}

// UpdateServerRequest is the JSON body for updating a server.
type UpdateServerRequest struct {
	Name         *string `json:"name,omitempty"`
	Host         *string `json:"host,omitempty"`
	Port         *int    `json:"port,omitempty"`
	Description  *string `json:"description,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	CredentialID *uint   `json:"credential_id,omitempty"`
}

// ServerHandler exposes the server CRUD endpoints.
type ServerHandler struct {
	svc *service.ServerService
}

// NewServerHandler creates a ServerHandler with the given service.
func NewServerHandler(svc *service.ServerService) *ServerHandler {
	return &ServerHandler{svc: svc}
}

// List handles GET /api/v1/servers.
func (h *ServerHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetUserClaims(r.Context())
	var servers []model.Server
	var err error
	if claims != nil && len(claims.ServerIDs) > 0 {
		servers, err = h.svc.ListFiltered(r.Context(), claims.ServerIDs)
	} else {
		servers, err = h.svc.List(r.Context())
	}
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, servers)
}

// Get handles GET /api/v1/servers/{id}.
func (h *ServerHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
		return
	}

	claims := mw.GetUserClaims(r.Context())
	if !mw.CheckServerAccess(claims, id) {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, "access denied: api key does not have access to this server"))
		return
	}

	srv, err := h.svc.Get(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, srv)
}

// Create handles POST /api/v1/servers.
func (h *ServerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	var details []server.ErrorDetail
	if req.Name == "" {
		details = append(details, server.ErrorDetail{Field: "name", Message: "name is required"})
	}
	if req.Host == "" {
		details = append(details, server.ErrorDetail{Field: "host", Message: "host is required"})
	}
	if len(details) > 0 {
		server.WriteError(w, r, server.NewValidationError(details))
		return
	}

	srv := &model.Server{
		Name:         req.Name,
		Host:         req.Host,
		Port:         req.Port,
		Description:  req.Description,
		Notes:        req.Notes,
		CredentialID: req.CredentialID,
	}

	claims := mw.GetUserClaims(r.Context())
	if claims != nil {
		srv.OwnerID = claims.UserID
	}

	created, err := h.svc.Create(r.Context(), srv)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusCreated, created)
}

// Update handles PUT /api/v1/servers/{id}.
func (h *ServerHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
		return
	}

	claims := mw.GetUserClaims(r.Context())
	if !mw.CheckServerAccess(claims, id) {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, "access denied: api key does not have access to this server"))
		return
	}

	var req UpdateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	input := &model.Server{}
	if req.Name != nil {
		input.Name = *req.Name
	}
	if req.Host != nil {
		input.Host = *req.Host
	}
	if req.Port != nil {
		input.Port = *req.Port
	}
	if req.Description != nil {
		input.Description = req.Description
	}
	if req.Notes != nil {
		input.Notes = req.Notes
	}
	if req.CredentialID != nil {
		input.CredentialID = req.CredentialID
	}

	updated, err := h.svc.Update(r.Context(), id, input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, updated)
}

// Delete handles DELETE /api/v1/servers/{id}.
func (h *ServerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		server.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseIDParam extracts a uint path parameter named "id" from the request URL.
func parseIDParam(r *http.Request) (uint, error) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
