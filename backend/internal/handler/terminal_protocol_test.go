package handler

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/service"
)

func TestTerminalBinaryProtocolAuthenticatesThroughHandler(t *testing.T) {
	f := newTerminalE2EFixture(t)
	close(f.shellResume)
	ws, done := f.connect(t, f.userGate, "", service.TerminalBinaryProtocol)
	if ws.Subprotocol() != service.TerminalBinaryProtocol {
		t.Fatalf("negotiated protocol = %q", ws.Subprotocol())
	}
	terminalE2EReady(t, ws)
	if err := ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	terminalE2EWait(t, done)
	if f.registry.LiveCount() != 0 {
		t.Fatal("binary terminal retained its registry entry after disconnect")
	}
}
