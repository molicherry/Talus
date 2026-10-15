package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/pkg/token"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
)

// slowTerminalCloseConn models a transport whose write and Close both stop
// making progress. The test retains control of when the real workers can join.
type slowTerminalCloseConn struct {
	*gatedConn
	entered     chan time.Time
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func (c *slowTerminalCloseConn) unblock() { c.releaseOnce.Do(func() { close(c.release) }) }

func (c *slowTerminalCloseConn) Close() error {
	c.once.Do(func() { c.entered <- time.Now() })
	<-c.release
	return c.gatedConn.Close()
}

func dialSlowTerminalClose(t *testing.T, addr string, hostKey ssh.PublicKey) (*ssh.Client, *slowTerminalCloseConn) {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn := &slowTerminalCloseConn{gatedConn: &gatedConn{Conn: raw}, entered: make(chan time.Time, 1), release: make(chan struct{})}
	t.Cleanup(func() { conn.unblock(); _ = conn.Close() })
	client, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey)})
	if err != nil {
		conn.unblock()
		t.Fatal(err)
	}
	return ssh.NewClient(client, chans, reqs), conn
}

func openTerminalForCleanup(t *testing.T, svc *TerminalService, ctx context.Context) *sessionHarness {
	t.Helper()
	h := &sessionHarness{result: make(chan error, 1), facts: make(chan TerminalResult, 1)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			h.result <- err
			return
		}
		defer conn.Close()
		result, err := svc.StartSessionWithResult(ctx, 1, conn)
		h.facts <- result
		h.result <- err
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.ws = ws
	t.Cleanup(func() { _ = ws.Close() })
	return h
}

type terminalReadyVersionGate struct {
	base    *gate.Gate
	calls   atomic.Int32
	onReady func()
}

func (g *terminalReadyVersionGate) Admit(ctx context.Context, id uint, fn func(int64) error) error {
	if g.calls.Add(1) == 2 {
		g.onReady()
	}
	return g.base.Admit(ctx, id, fn)
}

// Both a live-session revoke and a rejected ready admission must return within
// the same real three-second budget, even if force-closing the transport hangs.
func TestTerminalCleanupDeadlineRetainsOwnershipUntilJoin(t *testing.T) {
	for _, scenario := range []string{"live_revocation", "ready_rejected"} {
		t.Run(scenario, func(t *testing.T) {
			addr, hostKey := startTestSSHServer(t)
			client, conn := dialSlowTerminalClose(t, addr, hostKey)
			// Always unblock the fake before pool cleanup or a failing assertion.
			pool := newTestPool(t)
			t.Cleanup(conn.unblock)
			seedPool(t, pool, 1, client)
			svc := NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second))
			registry := gate.NewRegistry()
			svc.SetSessionRegistry(registry)
			var version atomic.Int64
			base := gate.New(func(context.Context, uint) (int64, error) { return version.Load(), nil })
			svc.SetUserGate(base)
			started := make(chan time.Time, 1)
			if scenario == "ready_rejected" {
				svc.SetUserGate(&terminalReadyVersionGate{base: base, onReady: func() {
					conn.blockWrites()
					version.Add(1)
					started <- time.Now()
				}})
			}
			v := int64(0)
			ctx := mw.WithUserClaims(context.Background(), &token.Claims{UserID: 7, TokenVersion: &v})
			h := openTerminalForCleanup(t, svc, ctx)
			if scenario == "live_revocation" {
				_ = h.ws.SetReadDeadline(time.Now().Add(time.Second))
				var ready wsMessage
				if err := h.ws.ReadJSON(&ready); err != nil || ready.Type != "connected" {
					t.Fatalf("ready frame = %+v, err = %v", ready, err)
				}
				conn.blockWrites()
				started <- time.Now()
				if err := base.Admit(context.Background(), 7, func(int64) error {
					version.Add(1)
					registry.MarkRevoked(7)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			var start time.Time
			select {
			case start = <-started:
			case <-time.After(time.Second):
				t.Fatal("terminal did not reach the cleanup trigger")
			}
			// A second revoke before the grace expires must retain the first clock.
			time.Sleep(500 * time.Millisecond)
			registry.MarkRevoked(7)
			var forced time.Time
			select {
			case forced = <-conn.entered:
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("terminal did not start force close inside the original grace")
			}
			if elapsed := forced.Sub(start); elapsed < 1800*time.Millisecond || elapsed > 2400*time.Millisecond {
				t.Fatalf("two-second grace restarted or fired early: %v", elapsed)
			}
			if err := h.wait(t); !errors.Is(err, errTerminalCleanupPending) {
				t.Fatalf("pending cleanup error = %v", err)
			}
			result := <-h.facts
			if !result.CleanupPending || result.Outcome != "cancelled" || result.Reason != "session_revoked" || result.CloseReason != "revoked" {
				t.Fatalf("pending cleanup result = %+v", result)
			}
			if elapsed := time.Since(start); elapsed < 2800*time.Millisecond || elapsed > 3300*time.Millisecond {
				t.Fatalf("absolute three-second cleanup budget restarted or fired early: %v", elapsed)
			}
			if registry.LiveCount() != 1 {
				t.Fatal("blocked transport lost its registry owner")
			}
			acquireCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			if lease, err := svc.sshSvc.AcquireLease(acquireCtx, 1); err == nil {
				lease.Release()
				cancel()
				t.Fatal("pending terminal returned quota before its workers joined")
			}
			cancel()
			conn.unblock()
			deadline := time.Now().Add(time.Second)
			for registry.LiveCount() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if registry.LiveCount() != 0 {
				t.Fatal("joined terminal retained its registry entry")
			}
			cached, err := pool.Get(1, testFP(1))
			if err != nil {
				t.Fatalf("joined terminal did not return quota: %v", err)
			}
			pool.Release(1, cached)
			if cached != nil {
				t.Fatal("force-closed client was cached")
			}
		})
	}
}
