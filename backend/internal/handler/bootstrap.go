package handler

import (
	"net/http"

	"github.com/vpsmanager/backend/internal/server"
)

// BootstrapRequest deliberately separates the one-time ownership proof from
// administrator login credentials. The proof is never part of the response.
type BootstrapRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Bootstrap handles POST /api/v1/auth/setup. The router applies the same
// unauthenticated attempt limiter as login; decoding also has its own byte cap.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req BootstrapRequest
	if err := decodeAuthJSON(w, r, &req); err != nil {
		server.WriteError(w, r, err)
		return
	}
	var details []server.ErrorDetail
	if len(req.Username) < 3 || len(req.Username) > 64 {
		details = append(details, server.NewErrorDetail("username", server.ReasonLength, map[string]any{"min": 3, "max": 64}))
	}
	if len(req.Password) < 8 || len(req.Password) > 64 {
		details = append(details, server.NewErrorDetail("password", server.ReasonLength, map[string]any{"min": 8, "max": 64}))
	}
	if len(details) > 0 {
		server.WriteError(w, r, server.NewValidationError(details))
		return
	}
	if len(req.Token) < 32 || len(req.Token) > 256 {
		server.WriteError(w, r, server.ErrForbidden)
		return
	}
	signed, err := h.authSvc.Bootstrap(r.Context(), req.Token, req.Username, req.Password)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, LoginResponse{Token: signed})
}
