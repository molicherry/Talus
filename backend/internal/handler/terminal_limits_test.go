package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/pkg/token"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

func TestTerminalAuthenticationMessageBudget(t *testing.T) {
	for _, buffer := range []int{128, 16384} {
		name := "single_frame"
		if buffer == 128 {
			name = "fragmented"
		}
		t.Run(name, func(t *testing.T) {
			// A missing terminal service is intentional: preauthentication input
			// must be rejected before any SSH work can be reached.
			jwt := token.NewJWTService("dummy-terminal-budget-secret", time.Hour)
			h := NewTerminalHandler(nil, jwt)
			done := make(chan struct{})
			router := chi.NewRouter()
			router.Use(mw.Auth(jwt, nil, nil))
			router.Get("/api/v1/servers/{id}/terminal", func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				h.Handle(w, r)
			})
			srv := httptest.NewServer(router)
			defer srv.Close()
			dialer := *websocket.DefaultDialer
			dialer.WriteBufferSize = buffer
			ws, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/servers/1/terminal", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			_ = ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_ = ws.WriteJSON(wsMessage{Type: "auth", Data: strings.Repeat("x", int(terminalAuthMaxBytes))})
			_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err = ws.ReadMessage()
			if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
				t.Fatalf("auth close = %v, want message-too-big", err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("oversize auth handler did not terminate")
			}
		})
	}
}
