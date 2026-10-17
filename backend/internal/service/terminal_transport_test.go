package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/pkg/token"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
)

func TestTerminalSSHOutputUsesBinaryFrames(t *testing.T) {
	shells := make(chan ssh.Channel, 1)
	address, key := startTestSSHServer(t, terminalPeerHooks{shell: func(ch ssh.Channel) { shells <- ch }})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	svc := NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second))
	h := beginSessionWithProtocol(t, svc, 1, nil, TerminalBinaryProtocol)
	if h.ws.Subprotocol() != TerminalBinaryProtocol {
		t.Fatalf("protocol = %q", h.ws.Subprotocol())
	}
	var ch ssh.Channel
	select {
	case ch = <-shells:
	case <-time.After(time.Second):
		t.Fatal("SSH server did not expose the ready shell")
	}
	// Reading each frame before writing the next forces the split to cross
	// actual SSH reads and WebSocket frames, including an invalid UTF-8 byte.
	for _, part := range [][]byte{{0xe4}, {0xb8, 0xad}, []byte("\x1b[32m"), {0xf0, 0x9f}, {0x99, 0x82}, {0xff, 0x00}, []byte(`{"type":"error"}`)} {
		if _, err := ch.Write(part); err != nil {
			t.Fatal(err)
		}
		kind, got, err := h.ws.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || !bytes.Equal(got, part) {
			t.Fatalf("output frame = (%d, %x, %v), want binary %x", kind, got, err, part)
		}
	}
	_ = h.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalLegacyOutputRetainsSplitUTF8(t *testing.T) {
	errors := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			errors <- err
			return
		}
		defer conn.Close()
		output := terminalOutputWriter{writer: newTerminalWriter(conn)}
		for _, part := range [][]byte{{0xe4}, {0xb8}, {0xad, 0xf0}, {0x9f}, {0x99, 0x82}, {0xe4}} {
			if err := output.write(part, false); err != nil {
				errors <- err
				return
			}
		}
		errors <- output.write(nil, true)
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(time.Second))
	var text strings.Builder
	for text.Len() < len("中🙂�") {
		kind, data, err := ws.ReadMessage()
		if err != nil || kind != websocket.TextMessage {
			t.Fatalf("legacy frame = %d, %v", kind, err)
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil || msg.Type != "output" {
			t.Fatalf("legacy message = %s, %v", data, err)
		}
		text.WriteString(msg.Data)
	}
	if text.String() != "中🙂�" {
		t.Fatalf("legacy output = %q", text.String())
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
}

// Reading is necessary for gorilla's default Ping handler to answer with Pong,
// just as the browser's networking stack does without JavaScript timers.
func drainTerminalSocket(h *sessionHarness) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := h.ws.ReadMessage(); err != nil {
				return
			}
		}
	}()
	return done
}

func keepaliveTestService(t *testing.T, hook terminalPeerHooks) (*TerminalService, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	onRequest := hook.globalRequest
	hook.globalRequest = func(req *ssh.Request) {
		if req.Type != "keepalive@openssh.com" {
			_ = req.Reply(false, nil) // the pool's pre-borrow probe
			return
		}
		count.Add(1)
		if onRequest != nil {
			onRequest(req)
		} else {
			_ = req.Reply(false, nil)
		}
	}
	address, key := startTestSSHServer(t, hook)
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	svc := NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second))
	svc.keepaliveInterval, svc.keepaliveTimeout = 50*time.Millisecond, 200*time.Millisecond
	return svc, count
}

func TestTerminalActiveKeepaliveAcceptsRejectedRequestsAndStops(t *testing.T) {
	svc, count := keepaliveTestService(t, terminalPeerHooks{})
	h := beginSession(t, svc, 1)
	drained := drainTerminalSocket(h)
	deadline := time.Now().Add(2 * time.Second)
	for count.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count.Load() < 3 {
		t.Fatal("active SSH session was not periodically probed")
	}
	_ = h.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	<-drained
	result := <-h.facts
	if result.Outcome != "succeeded" || result.Forced {
		t.Fatalf("healthy keepalive result = %+v", result)
	}
	last := count.Load()
	time.Sleep(3 * svc.keepaliveInterval)
	if count.Load() != last {
		t.Fatal("keepalive continued after the session and lease were returned")
	}
}

func TestTerminalKeepaliveTimeoutDiscardsTransport(t *testing.T) {
	svc, count := keepaliveTestService(t, terminalPeerHooks{globalRequest: func(*ssh.Request) {}})
	h := beginSession(t, svc, 1)
	drained := drainTerminalSocket(h)
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	<-drained
	result := <-h.facts
	if result.Outcome != "failed" || result.Reason != "terminal_keepalive_timeout" || !result.Forced || result.CleanupPending {
		t.Fatalf("SSH keepalive timeout = %+v", result)
	}
	if count.Load() != 1 {
		t.Fatalf("overlapping keepalive requests: %d", count.Load())
	}
	client, err := svc.sshSvc.pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	svc.sshSvc.pool.Release(1, client)
	if client != nil {
		t.Fatal("a timed-out SSH transport returned to the cache")
	}
}

func TestTerminalMissingWebSocketPongEndsSession(t *testing.T) {
	svc, count := keepaliveTestService(t, terminalPeerHooks{})
	h := beginSession(t, svc, 1)
	// Do not read: this client's stack never handles Ping or sends Pong.
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	result := <-h.facts
	if result.Outcome != "failed" || result.Reason != "terminal_transport_timeout" || count.Load() == 0 {
		t.Fatalf("missing Pong = %+v (SSH requests %d)", result, count.Load())
	}
}

func TestTerminalCancellationJoinsPendingKeepalive(t *testing.T) {
	oldGrace := sessionTeardownGrace
	sessionTeardownGrace = 30 * time.Millisecond
	t.Cleanup(func() { sessionTeardownGrace = oldGrace })
	for _, reason := range []string{"client_cancelled", "session_revoked"} {
		t.Run(reason, func(t *testing.T) {
			requested := make(chan struct{}, 1)
			svc, _ := keepaliveTestService(t, terminalPeerHooks{globalRequest: func(*ssh.Request) { requested <- struct{}{} }})
			svc.keepaliveTimeout = time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			registry := gate.NewRegistry()
			if reason == "session_revoked" {
				svc.SetSessionRegistry(registry)
				svc.SetUserGate(gate.New(func(context.Context, uint) (int64, error) { return 0, nil }))
				version := int64(0)
				ctx = mw.WithUserClaims(ctx, &token.Claims{UserID: 7, TokenVersion: &version})
			}
			h := beginSessionWithContext(t, svc, 1, ctx)
			drained := drainTerminalSocket(h)
			select {
			case <-requested:
			case <-time.After(time.Second):
				t.Fatal("no in-flight keepalive to cancel")
			}
			if reason == "session_revoked" {
				registry.MarkRevoked(7)
			} else {
				cancel()
			}
			if err := h.wait(t); err != nil {
				t.Fatal(err)
			}
			<-drained
			result := <-h.facts
			if result.Outcome != "cancelled" || result.Reason != reason || !result.Forced || result.CleanupPending || registry.LiveCount() != 0 {
				t.Fatalf("cancelled keepalive = %+v (registry entries %d)", result, registry.LiveCount())
			}
		})
	}
}
