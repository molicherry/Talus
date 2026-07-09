package handler

import (
	"encoding/json"
	"net/http"

	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/service"
)

// CreateCredentialRequest is the JSON body for creating an SSH credential.
type CreateCredentialRequest struct {
	Name       string `json:"name,omitempty"`
	AuthType   string `json:"auth_type"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// UpdateCredentialRequest is the JSON body for updating an SSH credential.
type UpdateCredentialRequest struct {
	Username   *string `json:"username,omitempty"`
	Password   *string `json:"password,omitempty"`
	PrivateKey *string `json:"private_key,omitempty"`
}

// CredentialHandler exposes the credential management endpoints.
type CredentialHandler struct {
	svc *service.CredentialService
}

// NewCredentialHandler creates a CredentialHandler with the given service.
func NewCredentialHandler(svc *service.CredentialService) *CredentialHandler {
	return &CredentialHandler{svc: svc}
}

// Create handles POST /api/v1/credentials.
func (h *CredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	var details []server.ErrorDetail
	if req.AuthType == "" {
		details = append(details, server.ErrorDetail{Field: "auth_type", Message: "auth_type is required"})
	}
	if req.Username == "" {
		details = append(details, server.ErrorDetail{Field: "username", Message: "username is required"})
	}
	if req.AuthType == "password" && req.Password == "" {
		details = append(details, server.ErrorDetail{Field: "password", Message: "password is required for auth_type 'password'"})
	}
	if req.AuthType == "private_key" && req.PrivateKey == "" {
		details = append(details, server.ErrorDetail{Field: "private_key", Message: "private_key is required for auth_type 'private_key'"})
	}
	if len(details) > 0 {
		server.WriteError(w, r, server.NewValidationError(details))
		return
	}

	input := service.CreateCredentialInput{
		Name:       req.Name,
		AuthType:   req.AuthType,
		Username:   req.Username,
		Password:   req.Password,
		PrivateKey: req.PrivateKey,
	}

	cred, err := h.svc.Create(r.Context(), input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusCreated, cred)
}

// List handles GET /api/v1/credentials.
func (h *CredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	creds, err := h.svc.List(r.Context())
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, creds)
}

// Update handles PUT /api/v1/credentials/{id}.
func (h *CredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid credential id"))
		return
	}

	var req UpdateCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	input := service.UpdateCredentialInput{
		Username:   req.Username,
		Password:   req.Password,
		PrivateKey: req.PrivateKey,
	}

	cred, err := h.svc.Update(r.Context(), id, input)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, cred)
}

// Delete handles DELETE /api/v1/credentials/{id}.
func (h *CredentialHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid credential id"))
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		server.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
