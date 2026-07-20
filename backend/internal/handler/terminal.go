package handler

import (
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
)

// upgrader configures WebSocket connection upgrades.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// TerminalHandler exposes the WebSocket terminal endpoint.
type TerminalHandler struct {
	terminalSvc *service.TerminalService
}

// NewTerminalHandler creates a TerminalHandler with the given dependencies.
func NewTerminalHandler(terminalSvc *service.TerminalService) *TerminalHandler {
	return &TerminalHandler{
		terminalSvc: terminalSvc,
	}
}

// Handle handles GET /api/v1/servers/{id}/terminal.
func (h *TerminalHandler) Handle(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetUserClaims(r.Context())
	if claims == nil {
		server.WriteError(w, r, server.ErrUnauthorized)
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
		return
	}

	if !mw.CheckServerAccess(claims, id) {
		server.WriteError(w, r, server.NewAppError(http.StatusForbidden, "access denied: api key does not have access to this server"))
		return
	}

	// Upgrade to WebSocket.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("terminal websocket upgrade failed", "error", err)
		server.WriteError(w, r, server.ErrInternal)
		return
	}
	defer conn.Close()

	if err := h.terminalSvc.StartSession(r.Context(), id, conn); err != nil {
		slog.Error("terminal session failed", "server_id", id, "error", err)
	}
}
