package handler

import (
	"encoding/json"
	"net/http"

	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/service"
)

type APIKeyHandler struct {
	svc *service.APIKeyService
}

func NewAPIKeyHandler(svc *service.APIKeyService) *APIKeyHandler {
	return &APIKeyHandler{svc: svc}
}

type createAPIKeyRequest struct {
	Name      string   `json:"name,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	ServerIDs []uint   `json:"server_ids,omitempty"`
}

func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}
	result, err := h.svc.Create(r.Context(), req.Name, req.Scopes, req.ServerIDs)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
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
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid key id"))
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		server.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIKeyHandler) Reveal(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid key id"))
		return
	}
	rawKey, err := h.svc.Reveal(r.Context(), id)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, rawKey)
}
