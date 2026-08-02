package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
)

// upgrader configures WebSocket connection upgrades.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// authTimeout bounds how long a client may take to send its first
// authentication message after the WebSocket handshake.
const authTimeout = 10 * time.Second

// TerminalHandler exposes the WebSocket terminal endpoint.
type TerminalHandler struct {
	terminalSvc *service.TerminalService
	jwtSvc      *token.JWTService
}

// NewTerminalHandler creates a TerminalHandler with the given dependencies.
func NewTerminalHandler(terminalSvc *service.TerminalService, jwtSvc *token.JWTService) *TerminalHandler {
	return &TerminalHandler{
		terminalSvc: terminalSvc,
		jwtSvc:      jwtSvc,
	}
}

// wsMessage is the wire format for client->server terminal messages.
type wsMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
}

// Handle handles GET /api/v1/servers/{id}/terminal.
//
// Authentication: browsers cannot set custom headers on WebSocket
// connections, so the handshake is allowed through the auth middleware and
// the client must authenticate with its FIRST message: {"type":"auth",
// "data":"<jwt>"}. Clients that already presented an X-API-Key header during
// the handshake (API access, e.g. the talus skill) skip the first-message
// requirement.
func (h *TerminalHandler) Handle(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, "invalid server id"))
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

	claims := mw.GetUserClaims(r.Context())
	if claims == nil {
		// No X-API-Key was presented at handshake — require the client's
		// first message to carry a valid JWT.
		conn.SetReadDeadline(time.Now().Add(authTimeout))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			slog.Warn("terminal auth message read failed", "server_id", id, "error", err)
			return
		}
		conn.SetReadDeadline(time.Time{})

		if msg.Type != "auth" || msg.Data == "" {
			_ = conn.WriteJSON(wsMessage{Type: "error", Data: "authentication required"})
			slog.Warn("terminal connection missing auth message", "server_id", id)
			return
		}

		parsed, verifyErr := h.jwtSvc.ValidateToken(msg.Data)
		if verifyErr != nil {
			_ = conn.WriteJSON(wsMessage{Type: "error", Data: "invalid or expired token"})
			slog.Warn("terminal auth failed", "server_id", id, "error", verifyErr)
			return
		}
		claims = parsed
	}

	if !mw.CheckServerAccess(claims, id) {
		_ = conn.WriteJSON(wsMessage{Type: "error", Data: "access denied: no access to this server"})
		slog.Warn("terminal access denied", "server_id", id)
		return
	}

	if err := h.terminalSvc.StartSession(r.Context(), id, conn); err != nil {
		slog.Error("terminal session failed", "server_id", id, "error", err)
	}
}
