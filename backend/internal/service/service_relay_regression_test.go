package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"gorm.io/gorm"
)

func TestRelayLookupUsesFrozenBudget(t *testing.T) {
	db := authOwnerTestDB(t)
	if err := db.AutoMigrate(&model.Service{}); err != nil {
		t.Fatal(err)
	}
	svc := relayTestService(time.Second, time.Second, 80*time.Millisecond, time.Second)
	svc.repo = repository.NewServiceRepo(db)
	lookupEntered := false
	if err := db.Callback().Query().Before("gorm:query").Register("test:relay_lookup_budget", func(tx *gorm.DB) {
		lookupEntered = true
		if _, ok := tx.Statement.Context.Deadline(); !ok {
			t.Error("service lookup has no business deadline")
			tx.AddError(context.DeadlineExceeded)
			return
		}
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RelayWithResult(context.Background(), 1, RelayInput{Method: "GET"}, httptest.NewRecorder())
	if !lookupEntered || err == nil || result.Outcome != "timeout" || result.Reason != "relay_budget_exceeded" {
		t.Fatalf("lookup result = %+v, error = %v, entered = %v", result, err, lookupEntered)
	}
}

func TestRelayStagesShareRemainingBudget(t *testing.T) {
	svc := relayTestService(time.Second, time.Second, 300*time.Millisecond, time.Second)
	ctx, cancel, err := svc.RelayContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	frozen, _ := ctx.Deadline()
	// Lookup/authorization work consumes part of the original budget.
	time.Sleep(80 * time.Millisecond)
	svc.httpClient = &http.Client{Transport: relayRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, _ := r.Context().Deadline()
		if !deadline.Equal(frozen) {
			t.Errorf("upstream deadline = %s, frozen = %s", deadline, frozen)
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	result, err := svc.relayFromService(ctx, &model.Service{BaseURL: "http://upstream.invalid"}, RelayInput{Method: "GET"}, httptest.NewRecorder())
	if err == nil || result.Reason != "relay_budget_exceeded" || result.HeadersWritten {
		t.Fatalf("remaining-budget result = %+v, error = %v", result, err)
	}
}

// deadlinePipeWriter uses a real transport deadline. Unlike an unblock channel,
// it verifies that ResponseController can interrupt the actual blocked I/O.
type deadlinePipeWriter struct {
	conn       net.Conn
	header     http.Header
	blockFlush bool
	entered    chan struct{}
	once       sync.Once
}

func (w *deadlinePipeWriter) Header() http.Header                { return w.header }
func (*deadlinePipeWriter) WriteHeader(int)                      {}
func (w *deadlinePipeWriter) SetWriteDeadline(d time.Time) error { return w.conn.SetWriteDeadline(d) }
func (w *deadlinePipeWriter) blocked(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.conn.Write(p)
}
func (w *deadlinePipeWriter) Write(p []byte) (int, error) { return w.blocked(p) }
func (w *deadlinePipeWriter) FlushError() error {
	if !w.blockFlush {
		return nil
	}
	_, err := w.blocked([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	return err
}

type relayCompletion struct {
	result RelayResult
	err    error
}

func testRelayBlockedOperation(t *testing.T, blockFlush, cancelRequest bool) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer upstream.Close()
	serverConn, unreadClient := net.Pipe()
	defer serverConn.Close()
	defer unreadClient.Close()
	writer := &deadlinePipeWriter{conn: serverConn, header: make(http.Header), blockFlush: blockFlush, entered: make(chan struct{})}
	svc := relayTestService(time.Second, time.Second, 200*time.Millisecond, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan relayCompletion, 1)
	go func() {
		result, err := svc.relayFromService(ctx, &model.Service{BaseURL: upstream.URL}, RelayInput{Method: "GET"}, writer)
		done <- relayCompletion{result, err}
	}()
	select {
	case <-writer.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("downstream operation was not reached")
	}
	if cancelRequest {
		cancel()
	}
	select {
	case got := <-done:
		want := "relay_budget_exceeded"
		if cancelRequest {
			want = "client_cancelled"
		}
		if got.err == nil || got.result.Reason != want || !got.result.HeadersWritten {
			t.Fatalf("blocked operation result = %+v, error = %v, want %s", got.result, got.err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked transport survived cancellation/total budget")
	}
}

func TestRelayBlockedInitialFlushHonorsCancellation(t *testing.T) {
	testRelayBlockedOperation(t, true, true)
}
func TestRelayBlockedWriteHonorsCancellationAndTotalBudget(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) { testRelayBlockedOperation(t, false, true) })
	t.Run("total budget", func(t *testing.T) { testRelayBlockedOperation(t, false, false) })
	t.Run("initial flush total budget", func(t *testing.T) { testRelayBlockedOperation(t, true, false) })
}

func TestRelayBlockedDownstreamHonorsIdleBudget(t *testing.T) {
	upstreamStopped := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamStopped)
		chunk := strings.Repeat("x", 32*1024)
		for {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	defer upstream.Close()
	svc := relayTestService(time.Second, 100*time.Millisecond, 2*time.Second, 2*time.Second)
	done := make(chan relayCompletion, 1)
	relay := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := svc.relayFromService(r.Context(), &model.Service{BaseURL: upstream.URL}, RelayInput{Method: "GET"}, w)
		done <- relayCompletion{result, err}
		if err != nil && result.HeadersWritten {
			panic(http.ErrAbortHandler)
		}
	}))
	relay.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			_ = c.(*net.TCPConn).SetWriteBuffer(4096)
		}
	}
	relay.Start()
	defer relay.Close()
	conn, err := net.Dial("tcp", relay.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(1024)
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: relay\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Deliberately never read: the kernel send buffer eventually fills, leaving
	// Write or Flush blocked until the relay's own idle deadline interrupts it.
	select {
	case got := <-done:
		if got.err == nil || got.result.Reason != "relay_idle_timeout" || got.result.BytesCopied == 0 {
			t.Fatalf("slow-reader result = %+v, error = %v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not stop a blocked downstream within the idle budget")
	}
	select {
	case <-upstreamStopped:
	case <-time.After(time.Second):
		t.Fatal("upstream worker survived relay teardown")
	}
}
