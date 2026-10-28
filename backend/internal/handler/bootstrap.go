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

// Bootstrap handles POST /api/v1/auth/setup. The router limits unauthenticated
// setup attempts; decoding also has its own byte cap.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req BootstrapRequest
	if err := decodeAuthJSON(w, r, &req); err != nil {
		server.WriteError(w, r, err)
		return
	}
	// Length alone is not proof: even a plausible but incorrect deployment
	// token must be rejected before returning field-level credential feedback.
	if err := h.authSvc.VerifyBootstrapProof(req.Token); err != nil {
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
	signed, err := h.authSvc.Bootstrap(r.Context(), req.Token, req.Username, req.Password)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, LoginResponse{Token: signed})
}
