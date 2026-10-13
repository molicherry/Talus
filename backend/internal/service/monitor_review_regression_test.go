package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
)

// TestReviewMonitorDoesNotBusyPollDuringHealthyCollection pins R01: while a
// healthy collection is in flight the loop must sleep until the next real slot
// (or a completion), not re-read the node list with a zero-delay timer. The
// manual-clock scheduler tests cannot see this, so this test uses the real clock.
func TestReviewMonitorDoesNotBusyPollDuringHealthyCollection(t *testing.T) {
	src := &fakeServerSource{}
	src.set(1)

	started := make(chan struct{})
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.serverRepo = src
	svc.clock = realMonitorClock{}
	svc.taskBudget = time.Minute
	svc.taskFn = func(ctx context.Context, _ uint) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}

	appCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Start(appCtx)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the first collection never started")
	}
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&src.lists); got > 5 {
		t.Fatalf("node list read %d times in 300ms while a collection was in flight (busy loop)", got)
	}

	stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
}

// TestReviewGenerationInvalidationPreventsActivation pins R02: invalidating the
// target between the upload and the activation must stop the deployment before
// any rename or execution reaches the old target.
func TestReviewGenerationInvalidationPreventsActivation(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
	h.remote.afterUpload = func() { h.pool.Invalidate(1) }

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	_, err = deployer.Collect(context.Background(), lease)
	if err == nil {
		t.Fatal("a target change between upload and activation must fail the deployment")
	}
	if !isStaleGeneration(err) {
		t.Fatalf("err = %v, want a stale generation refusal", err)
	}
	if got := h.remote.count("chmod 0755"); got != 0 {
		t.Fatalf("activations = %d, want none after the target changed", got)
	}
	if got := h.remote.count(agentPath + " --format json"); got != 1 {
		t.Fatalf("agent runs = %d, want only the initial probe run", got)
	}
}

// TestReviewGenerationInvalidationIsCancellationNotFailure pins the classification:
// a target change cancels the collection instead of counting as a failure.
func TestReviewGenerationInvalidationIsCancellationNotFailure(t *testing.T) {
	src := &fakeServerSource{}
	src.set(1)
	svc, _ := newLoopTestMonitor(t, src, func(context.Context, uint) error {
		return fmt.Errorf("lease: %w", sshpool.ErrStaleGeneration)
	}, time.Minute)

	if outcome := svc.runTask(context.Background(), 1); outcome != outcomeCancelled {
		t.Fatalf("outcome = %v, want cancelled", outcome)
	}
}

// isStaleGeneration unwraps the deployment error chain looking for the sentinel.
func isStaleGeneration(err error) bool {
	for err != nil {
		if err == sshpool.ErrStaleGeneration {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
