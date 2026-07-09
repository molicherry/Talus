package handler

import (
	"encoding/json"
	"net/http"

	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type SetupResponse struct {
	Needed bool `json:"needed"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type UserProfile struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type AuthHandler struct {
	authSvc *service.AuthService
}

func NewAuthHandler(authSvc *service.AuthService) *AuthHandler {
	return &AuthHandler{authSvc: authSvc}
}

func (h *AuthHandler) Setup(w http.ResponseWriter, r *http.Request) {
	needed, err := h.authSvc.NeedsSetup(r.Context())
	if err != nil {
		server.WriteError(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, SetupResponse{Needed: needed})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	var details []server.ErrorDetail
	if len(req.Username) < 3 || len(req.Username) > 64 {
		details = append(details, server.ErrorDetail{Field: "username", Message: "must be between 3 and 64 characters"})
	}
	if len(req.Password) < 3 || len(req.Password) > 64 {
		details = append(details, server.ErrorDetail{Field: "password", Message: "must be between 3 and 64 characters"})
	}
	if len(details) > 0 {
		server.WriteError(w, r, server.NewValidationError(details))
		return
	}

	token, err := h.authSvc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		server.WriteError(w, r, err)
		return
	}

	server.WriteJSON(w, http.StatusOK, LoginResponse{Token: token})
}

func (h *AuthHandler) Profile(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetUserClaims(r.Context())
	if claims == nil {
		server.WriteError(w, r, server.ErrUnauthorized)
		return
	}
	server.WriteJSON(w, http.StatusOK, UserProfile{
		ID:       claims.UserID,
		Username: claims.Username,
		Role:     claims.Role,
	})
}

func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid request body"))
		return
	}

	if len(req.NewPassword) < 3 || len(req.NewPassword) > 64 {
		server.WriteError(w, r, server.NewValidationError([]server.ErrorDetail{{Field: "new_password", Message: "must be between 3 and 64 characters"}}))
		return
	}

	claims := mw.GetUserClaims(r.Context())
	if claims == nil {
		server.WriteError(w, r, server.ErrUnauthorized)
		return
	}

	if err := h.authSvc.ChangePassword(r.Context(), claims.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		server.WriteError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
