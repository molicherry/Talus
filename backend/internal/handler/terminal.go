package handler

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
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
	userGate    mw.UserGate
}

// SetUserGate wires the shared per-user gate so the WS first-frame JWT is
// admitted against the current session version.
func (h *TerminalHandler) SetUserGate(g mw.UserGate) {
	h.userGate = g
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
	op := usage.FromContext(r.Context())
	id, err := parseIDParam(r)
	if err != nil {
		server.WriteError(w, r, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidServerID))
		return
	}
	op.SetResource("server", &id, "", &id)

	// Upgrade to WebSocket.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		op.SetResult("rejected", "websocket_upgrade_failed")
		slog.Warn("terminal websocket upgrade failed", "reason", "websocket_upgrade_failed")
		// Upgrade already wrote the actual HTTP failure. Do not replace its
		// status or append a JSON envelope to that response.
		return
	}
	defer conn.Close()
	mw.ReportHTTPStatus(w, http.StatusSwitchingProtocols)
	op.SetHTTPStatus(http.StatusSwitchingProtocols)
	op.SetPhase("handshake")

	claims := mw.GetUserClaims(r.Context())
	if claims == nil {
		// No X-API-Key was presented at handshake — require the client's
		// first message to carry a valid JWT.
		// Best-effort: if the deadline cannot be set the transport is already
		// broken, and the ReadJSON below fails immediately.
		_ = conn.SetReadDeadline(time.Now().Add(authTimeout))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			reason := "terminal_auth_failed"
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				reason = "terminal_auth_timeout"
			}
			op.SetResult("rejected", reason)
			slog.Warn("terminal auth message read failed", "server_id", id, "reason", reason)
			return
		}
		_ = conn.SetReadDeadline(time.Time{})

		if msg.Type != "auth" || msg.Data == "" {
			op.SetResult("rejected", "terminal_auth_failed")
			_ = conn.WriteJSON(wsMessage{Type: "error", Data: "authentication required"})
			slog.Warn("terminal connection missing auth message", "server_id", id)
			return
		}

		parsed, verifyErr := h.jwtSvc.ValidateToken(msg.Data)
		if verifyErr != nil {
			op.SetResult("rejected", "terminal_auth_failed")
			_ = conn.WriteJSON(wsMessage{Type: "error", Data: "invalid or expired token"})
			slog.Warn("terminal auth failed", "server_id", id, "reason", "terminal_auth_failed")
			return
		}
		claims = parsed
		if h.userGate != nil {
			admitErr := h.userGate.Admit(r.Context(), claims.UserID, func(version int64) error {
				if claims.TokenVersion == nil || *claims.TokenVersion != version {
					return errors.New("session revoked")
				}
				return nil
			})
			if admitErr != nil {
				op.SetResult("rejected", "terminal_auth_failed")
				_ = conn.WriteJSON(wsMessage{Type: "error", Data: "session revoked or unavailable"})
				slog.Warn("terminal first-frame session rejected", "server_id", id)
				return
			}
		}
		r = r.WithContext(mw.WithUserClaims(r.Context(), claims))
	}
	op.SetPhase("authenticated")

	if !mw.CheckServerAccess(claims, id) {
		op.SetResult("rejected", "terminal_permission_denied")
		_ = conn.WriteJSON(wsMessage{Type: "error", Data: "access denied: no access to this server"})
		slog.Warn("terminal access denied", "server_id", id)
		return
	}
	op.Begin()

	result, sessionErr := h.terminalSvc.StartSessionWithResult(r.Context(), id, conn)
	op.SetResult(result.Outcome, result.Reason)
	op.SetMetadata("close_reason", result.CloseReason)
	if !result.ReadyAt.IsZero() {
		op.SetMetadata("ready_at", result.ReadyAt)
	}
	op.SetMetadata("closed_at", result.ClosedAt)
	if sessionErr != nil || result.Outcome == "failed" {
		slog.Warn("terminal session failed", "server_id", id, "reason", result.Reason)
	}
}
