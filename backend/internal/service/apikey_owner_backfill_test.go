package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
)

type ownerBinderFunc func(context.Context) (int64, error)

func (f ownerBinderFunc) BindUnownedToDefaultAdmin(ctx context.Context) (int64, error) {
	return f(ctx)
}

func TestLegacyOwnerWorkerLimitsEachRound(t *testing.T) {
	var calls atomic.Int32
	b := NewLegacyAPIKeyOwnerBackfill(ownerBinderFunc(func(ctx context.Context) (int64, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("owner assignment has no bounded database deadline")
		}
		calls.Add(1)
		return repository.APIKeyOwnerBackfillBatchSize, nil
	}))
	b.runBatch(context.Background())
	if calls.Load() != 8 {
		t.Fatalf("one round ran %d batches, want bounded 8", calls.Load())
	}
}

func TestLegacyOwnerWorkerPropagatesShutdownToDatabase(t *testing.T) {
	started := make(chan struct{})
	queryStopped := make(chan struct{})
	b := NewLegacyAPIKeyOwnerBackfill(ownerBinderFunc(func(ctx context.Context) (int64, error) {
		close(started)
		<-ctx.Done()
		close(queryStopped)
		return 0, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker never started")
	}
	// Repeated setup notifications must remain nonblocking while SQL is busy.
	for range 100 {
		b.Trigger()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel its active query")
	}
	select {
	case <-queryStopped:
	default:
		t.Fatal("shutdown returned before database cancellation")
	}
}
