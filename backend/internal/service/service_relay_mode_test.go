package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
)

func TestRelayModesAccepted(t *testing.T) {
	for _, m := range []string{"", "standard", "bounded_stream"} {
		if !RelayModes[m] {
			t.Fatalf("mode %q should be accepted", m)
		}
	}
	for _, m := range []string{"stream", "unlimited", "SSE", "bounded"} {
		if RelayModes[m] {
			t.Fatalf("mode %q should be rejected", m)
		}
	}
}

func relayTestService(headerWait, idle, standard, bounded time.Duration) *ServiceRelayService {
	svc := NewServiceRelayService(nil, nil)
	svc.headerWait = headerWait
	svc.idleTimeout = idle
	svc.standardBudget = standard
	svc.boundedBudget = bounded
	return svc
}

// TestRelayHeaderWaitReturnsGatewayTimeout pins that a slow upstream that never
// sends headers is cut by the header wait, before any response bytes are written.
func TestRelayHeaderWaitReturnsGatewayTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer upstream.Close()

	svc := relayTestService(50*time.Millisecond, time.Second, 2*time.Second, 2*time.Second)
	rec := httptest.NewRecorder()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: upstream.URL},
		RelayInput{Method: "GET", Path: "/"}, rec)
	if err == nil {
		t.Fatal("expected a gateway timeout")
	}
	if result.HeadersWritten {
		t.Fatal("no response headers should have been written")
	}
	if result.Reason != server.ReasonRelayTimeout {
		t.Fatalf("reason = %q, want %q", result.Reason, server.ReasonRelayTimeout)
	}
}

// TestRelayIdleTimeoutAfterHeaders pins that a stalled stream is aborted after
// the idle window, after headers were already written.
func TestRelayIdleTimeoutAfterHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("hello"))
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	svc := relayTestService(time.Second, 50*time.Millisecond, 2*time.Second, 2*time.Second)
	rec := httptest.NewRecorder()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: upstream.URL},
		RelayInput{Method: "GET", Path: "/"}, rec)
	if !result.HeadersWritten {
		t.Fatal("headers should have been written before the body stalled")
	}
	if err == nil {
		t.Fatal("expected the stalled stream to error")
	}
	if result.Reason != "relay_idle_timeout" || result.Outcome != "timeout" {
		t.Fatalf("outcome/reason = %q/%q", result.Outcome, result.Reason)
	}
	if result.BytesCopied == 0 {
		t.Fatal("bytes before the stall should have been copied")
	}
}

// TestRelayBoundedStreamBudgetExceeded pins that a stream making progress is
// still bounded by the total budget (progress never extends it).
func TestRelayBoundedStreamBudgetExceeded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			_, _ = w.Write([]byte("x"))
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	// Budget 200ms < idle 2s: progress keeps the idle timer satisfied, so the
	// total budget is what stops the stream.
	svc := relayTestService(time.Second, 2*time.Second, 5*time.Second, 200*time.Millisecond)
	rec := httptest.NewRecorder()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: upstream.URL},
		RelayInput{Method: "GET", Path: "/", Mode: "bounded_stream"}, rec)
	if err == nil {
		t.Fatal("expected the total budget to stop the stream")
	}
	if result.Reason != "relay_budget_exceeded" || result.Outcome != "timeout" {
		t.Fatalf("outcome/reason = %q/%q", result.Outcome, result.Reason)
	}
}

// TestRelayOrdinaryRequestUsesStandardBudget pins that an omitted mode is a
// normal short request.
func TestRelayOrdinaryRequestUsesStandardBudget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	svc := relayTestService(time.Second, time.Second, 2*time.Second, 2*time.Second)
	rec := httptest.NewRecorder()
	result, err := svc.relayFromService(context.Background(), &model.Service{BaseURL: upstream.URL},
		RelayInput{Method: "GET", Path: "/"}, rec)
	if err != nil {
		t.Fatalf("ordinary relay failed: %v", err)
	}
	if result.UpstreamStatus != http.StatusOK || result.BytesCopied != 2 {
		t.Fatalf("result = %+v", result)
	}
}
