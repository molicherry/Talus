package service

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTerminalCleanClosePreservesOriginalResult(t *testing.T) {
	svc, _ := startSeededTerminalService(t, 1)
	h := beginSession(t, svc, 1)
	if err := h.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	result := <-h.facts
	if result.Outcome != "succeeded" || result.Reason != "" || result.CloseReason != "normal_close" {
		t.Fatalf("close result = %#v", result)
	}
	if result.ReadyAt.IsZero() || result.ClosedAt.Before(result.ReadyAt) {
		t.Fatalf("lifecycle times = %#v", result)
	}
}

func TestTerminalBrokenWebSocketReportsOriginalFailure(t *testing.T) {
	svc, _ := startSeededTerminalService(t, 1)
	h := beginSession(t, svc, 1)
	_ = h.ws.Close()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	result := <-h.facts
	if result.Outcome != "failed" || result.Reason != "terminal_transport_failed" || result.CloseReason != "transport_error" {
		t.Fatalf("close result = %#v", result)
	}
}

func TestTerminalClientCancellationIsNotTeardownSideEffect(t *testing.T) {
	svc, _ := startSeededTerminalService(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := beginSessionWithContext(t, svc, 1, ctx)
	cancel()
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	result := <-h.facts
	if result.Outcome != "cancelled" || result.Reason != "client_cancelled" {
		t.Fatalf("close result = %#v", result)
	}
}

func TestTerminalForcedResourceReleaseDoesNotReplaceCleanClose(t *testing.T) {
	oldGrace := sessionTeardownGrace
	sessionTeardownGrace = 30 * time.Millisecond
	t.Cleanup(func() { sessionTeardownGrace = oldGrace })
	address, hostKey := startTestSSHServer(t)
	proxy := startFreezeProxy(t, address)
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, proxy.addr, hostKey))
	svc := NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second))
	h := beginSession(t, svc, 1)
	proxy.freeze()
	if err := h.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	result := <-h.facts
	if result.Outcome != "succeeded" || result.CloseReason != "normal_close" || !result.Forced {
		t.Fatalf("close result = %#v", result)
	}
	client, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	if client != nil {
		pool.Release(1, client)
		t.Fatal("forced teardown cached a dead transport")
	}
	pool.Release(1, nil)
}
