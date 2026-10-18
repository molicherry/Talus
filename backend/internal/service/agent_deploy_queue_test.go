package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
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

func TestAgentDeploymentQueueWaitsForJoinedLeaseSettlement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "retry"
		if replacement {
			name = "generation_replacement"
		}
		t.Run(name, func(t *testing.T) {
			address, key := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) { exitStatus(ch, 0) })
			pool := newTestPool(t)
			seedPool(t, pool, 1, dialTestSSH(t, address, key))
			svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
			lease, err := svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			if _, err := lease.begin(context.Background()); err != nil {
				t.Fatal(err)
			}
			lease.cleanup.once.Do(func() {
				lease.cleanup.value = newSSHCleanupDeadlines(time.Now().Add(-sshCleanupBudget-time.Second), execTeardownGrace)
			})
			primitiveJoined := make(chan struct{})
			var primitiveOnce sync.Once
			joinPrimitive := func() { primitiveOnce.Do(func() { close(primitiveJoined) }) }
			defer joinPrimitive()
			primitiveErr := &sshCleanupPending{done: primitiveJoined}
			lease.end(true, primitiveErr)
			// Match ensureDeployed: Collect's wrapped primitive error precedes
			// releaseAndWait's pending lease settlement in the joined error.
			joinedErr := errors.Join(fmt.Errorf("upload agent: %w", primitiveErr), lease.releaseAndWait())
			started := make(chan Generation, 2)
			var attempts atomic.Int32
			q, _, _ := testDeploymentQueue(t, func(_ context.Context, _ uint, generation Generation) error {
				started <- generation
				if attempts.Add(1) == 1 {
					return joinedErr
				}
				return nil
			})
			q.retry = 5 * time.Millisecond
			q.sync([]uint{1})
			q.request(1, 1)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("initial deployment did not start")
			}
			if replacement {
				q.request(1, 2)
			}
			// Keep the primitive owner's l.end from settling the real lease
			// after its worker joins. The queue must still retain admission.
			lease.mu.Lock()
			var unlockOnce sync.Once
			unlock := func() { unlockOnce.Do(lease.mu.Unlock) }
			defer unlock()
			joinPrimitive()
			select {
			case generation := <-started:
				t.Fatalf("generation %d started before the previous lease settled", generation)
			case <-time.After(100 * time.Millisecond):
			}
			q.mu.Lock()
			if q.active != 1 || !q.jobs[1].running || q.jobs[1].failures != 0 {
				t.Error("joined cleanup released admission or started retry backoff early")
			}
			q.mu.Unlock()
			unlock()
			select {
			case <-lease.Done():
			case <-time.After(time.Second):
				t.Fatal("previous lease failed to settle")
			}
			select {
			case generation := <-started:
				want := Generation(1)
				if replacement {
					want = 2
				}
				if generation != want {
					t.Fatalf("next generation = %d, want %d", generation, want)
				}
			case <-time.After(time.Second):
				t.Fatal("deployment did not resume after lease settlement")
			}
		})
	}
}

func TestAgentDeploymentQueueShutdownDuringJoinedCleanup(t *testing.T) {
	primitiveJoined, leaseSettled := make(chan struct{}), make(chan struct{})
	defer close(leaseSettled)
	close(primitiveJoined)
	var attempts atomic.Int32
	q, cancel, done := testDeploymentQueue(t, func(context.Context, uint, Generation) error {
		attempts.Add(1)
		return fmt.Errorf("deploy: %w", errors.Join(
			&sshCleanupPending{done: primitiveJoined}, &sshCleanupPending{done: leaseSettled}))
	})
	q.sync([]uint{1})
	q.request(1, 1)
	waitFor(t, "joined cleanup", func() bool { return attempts.Load() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not interrupt the pending lease settlement wait")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active != 0 || len(q.jobs) != 0 || attempts.Load() != 1 {
		t.Fatal("shutdown retained deployment admission or admitted another attempt")
	}
}
