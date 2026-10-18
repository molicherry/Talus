package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func testDeploymentQueue(t *testing.T, execute func(context.Context, uint, Generation) error) (*agentDeploymentQueue, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	s := NewAgentDeploymentService(nil, "")
	q := newAgentDeploymentQueue(s)
	q.execute = execute
	q.current = func(uint, Generation) bool { return true }
	q.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := q.start(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("deployment queue failed to join on shutdown")
		}
	})
	return q, cancel, done
}

func TestAgentDeploymentQueueDeduplicatesBoundsConcurrencyAndJoins(t *testing.T) {
	var active, peak, starts atomic.Int32
	q, cancel, done := testDeploymentQueue(t, func(ctx context.Context, _ uint, _ Generation) error {
		starts.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		deadline, _ := ctx.Deadline()
		if remaining := time.Until(deadline); remaining < 170*time.Second || remaining > 3*time.Minute {
			t.Errorf("deployment admission budget = %s", remaining)
		}
		<-ctx.Done()
		return ctx.Err()
	})
	q.sync([]uint{1, 2, 3, 4, 5})
	for id := uint(1); id <= 5; id++ {
		for repeat := 0; repeat < 10; repeat++ {
			q.request(id, 1)
		}
	}
	waitFor(t, "four independent deployments", func() bool { return active.Load() == 4 })
	q.mu.Lock()
	if len(q.jobs) != 5 {
		t.Errorf("queued nodes = %d, want 5", len(q.jobs))
	}
	q.mu.Unlock()
	if peak.Load() != 4 || starts.Load() != 4 {
		t.Fatal("duplicate requests exceeded deployment concurrency")
	}
	cancel()
	<-done
	if active.Load() != 0 {
		t.Fatal("shutdown returned before workers joined")
	}
}

func TestAgentDeploymentQueueGenerationReplacementWaitsForOldWorker(t *testing.T) {
	started := make(chan Generation, 2)
	releaseOld := make(chan struct{})
	defer close(releaseOld)
	q, _, _ := testDeploymentQueue(t, func(ctx context.Context, _ uint, generation Generation) error {
		started <- generation
		<-ctx.Done()
		if generation == 1 {
			<-releaseOld
		}
		return ctx.Err()
	})
	q.sync([]uint{1})
	q.request(1, 1)
	if got := <-started; got != 1 {
		t.Fatalf("initial generation = %d", got)
	}
	q.request(1, 2)
	q.mu.Lock()
	if q.active != 1 || !q.jobs[1].running {
		t.Error("generation replacement released old worker admission")
	}
	q.mu.Unlock()
	select {
	case <-started:
		t.Fatal("replacement started before the old worker joined")
	default:
	}
	releaseOld <- struct{}{}
	select {
	case generation := <-started:
		if generation != 2 {
			t.Fatalf("replacement generation = %d", generation)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not start after the previous worker joined")
	}
	q.sync(nil) // deletion cancels the replacement and removes its request
	waitFor(t, "deleted node's deployment to join", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.active == 0 && len(q.jobs) == 0
	})
}

func TestAgentDeploymentQueueRetryDoesNotDependOnCollections(t *testing.T) {
	var attempts atomic.Int32
	q, _, _ := testDeploymentQueue(t, func(context.Context, uint, Generation) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient upload failure")
		}
		return nil
	})
	q.retry = 40 * time.Millisecond
	q.sync([]uint{1})
	q.request(1, 1)
	waitFor(t, "first failed deployment", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		job := q.jobs[1]
		return job != nil && job.failures == 1
	})
	q.mu.Lock()
	due := q.jobs[1].due
	q.mu.Unlock()
	q.request(1, 1)
	q.mu.Lock()
	if !q.jobs[1].due.Equal(due) {
		t.Error("a collection reset deployment retry backoff")
	}
	q.mu.Unlock()
	waitFor(t, "independent deployment retry", func() bool { return attempts.Load() == 2 })
}

func TestAgentDeploymentQueueTaskTimeoutKeepsApplicationAlive(t *testing.T) {
	var attempts atomic.Int32
	firstDeadline := make(chan error, 1)
	q, _, _ := testDeploymentQueue(t, func(ctx context.Context, _ uint, _ Generation) error {
		if attempts.Add(1) == 1 {
			<-ctx.Done()
			firstDeadline <- ctx.Err()
			return ctx.Err()
		}
		return nil
	})
	q.budget, q.retry = 40*time.Millisecond, 10*time.Millisecond
	q.sync([]uint{1})
	q.request(1, 1)
	select {
	case err := <-firstDeadline:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("task deadline = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deployment task did not honor its own budget")
	}
	waitFor(t, "retry after task deadline", func() bool { return attempts.Load() == 2 })
}

func TestAgentDeploymentQueueRetainsAdmissionDuringPendingCleanup(t *testing.T) {
	settled := make(chan struct{})
	var attempts atomic.Int32
	q, _, _ := testDeploymentQueue(t, func(context.Context, uint, Generation) error {
		if attempts.Add(1) == 1 {
			return &sshCleanupPending{done: settled}
		}
		return nil
	})
	q.sync([]uint{1})
	q.request(1, 1)
	waitFor(t, "pending deployment cleanup", func() bool { return attempts.Load() == 1 })
	q.request(1, 2)
	q.mu.Lock()
	if q.active != 1 || !q.jobs[1].running {
		t.Error("pending cleanup relinquished admission")
	}
	q.mu.Unlock()
	if attempts.Load() != 1 {
		t.Fatal("replacement started during pending cleanup")
	}
	close(settled)
	waitFor(t, "replacement after cleanup settlement", func() bool { return attempts.Load() == 2 })
}
