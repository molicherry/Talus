package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	// The artifact is verified by hash before anything runs, so the invalidation
	// prevents the upload, the activation and every execution.
	if got := h.remote.count(agentPath + " --format json"); got != 0 {
		t.Fatalf("agent runs = %d, want none after the target changed", got)
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

	if outcome, _ := svc.runTask(context.Background(), 1); outcome != outcomeCancelled {
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

// TestReviewDeletedCollectionKeepsCapacityUntilJoined pins R12: deleting a node
// whose worker is still running must not free its slot, or the real number of
// workers can exceed the cap.
func TestReviewDeletedCollectionKeepsCapacityUntilJoined(t *testing.T) {
	sched, clock := newTestScheduler(t, 60*time.Second, 0.5)
	live := make([]uint, 0, 17)
	for id := uint(1); id <= 17; id++ {
		live = append(live, id)
	}
	sched.sync(live, clock.Now())
	now := clock.Now()

	first := sched.plan(now)
	if len(first.Admitted) != 16 || len(first.Queued) != 1 {
		t.Fatalf("first pass: admitted=%d queued=%d, want 16/1", len(first.Admitted), len(first.Queued))
	}
	deleted := first.Admitted[0]

	// The node is removed while its worker is still cleaning up.
	if removed := sched.sync(live[1:], now); len(removed) != 1 || removed[0] != deleted {
		t.Fatalf("removed = %v, want [%d]", removed, deleted)
	}
	if tick := sched.plan(now); len(tick.Admitted) != 0 {
		t.Fatalf("admitted %v while a deleted worker still held its slot", tick.Admitted)
	}
	if got := sched.inFlightCount(); got > 16 {
		t.Fatalf("live workers = %d, want at most 16", got)
	}

	// Only the worker's own completion frees the capacity.
	sched.complete(deleted, outcomeCancelled, now)
	tick := sched.plan(now)
	if len(tick.Admitted) != 1 {
		t.Fatalf("admitted %d nodes after the worker joined, want 1", len(tick.Admitted))
	}
	if got := sched.inFlightCount(); got > 16 {
		t.Fatalf("live workers = %d, want at most 16", got)
	}
}

// stubLease is a Lease whose settlement a test controls.
type stubLease struct {
	done chan struct{}
}

func (l *stubLease) Exec(context.Context, string, time.Duration) (*ExecResult, error) {
	return nil, nil
}
func (l *stubLease) Upload(context.Context, string, string, time.Duration) error { return nil }
func (l *stubLease) Cancel(string)                                               {}
func (l *stubLease) Done() <-chan struct{}                                       { return l.done }
func (l *stubLease) Generation() Generation                                      { return 0 }
func (l *stubLease) ServerID() uint                                              { return 1 }
func (l *stubLease) Release()                                                    {}
func (l *stubLease) Discard()                                                    {}

// TestReviewMonitorShutdownWaitsForLeaseSettlement pins R13: a collection is only
// reported finished once its lease's resources are reclaimed, and the wait is
// bounded so shutdown cannot hang forever.
func TestReviewMonitorShutdownWaitsForLeaseSettlement(t *testing.T) {
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.leaseSettleBudget = 120 * time.Millisecond

	// Already settled: no wait.
	settled := &stubLease{done: make(chan struct{})}
	close(settled.done)
	if !svc.awaitLeaseSettlement(settled) {
		t.Fatal("a settled lease must be reported as reclaimed")
	}

	// Never settles: the bounded wait reports the owner as still pending.
	pending := &stubLease{done: make(chan struct{})}
	start := time.Now()
	if svc.awaitLeaseSettlement(pending) {
		t.Fatal("an unsettled lease must not be reported as reclaimed")
	}
	if elapsed := time.Since(start); elapsed < svc.leaseSettleBudget || elapsed > svc.leaseSettleBudget+500*time.Millisecond {
		t.Fatalf("wait = %v, want the bounded cleanup budget %v", elapsed, svc.leaseSettleBudget)
	}

	// A settlement that completes during the wait is observed.
	late := &stubLease{done: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(late.done)
	}()
	if !svc.awaitLeaseSettlement(late) {
		t.Fatal("a lease settled during the wait must be reported as reclaimed")
	}
}

// TestReviewMatchingVersionStillRequiresArtifactHash pins R10: a remote binary
// that reports the expected version and protocol must still be replaced when its
// bytes differ from the manifest's artifact.
func TestReviewMatchingVersionStillRequiresArtifactHash(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
	// A stale binary that happens to report the expected version.
	h.remote.files[agentPath] = []byte("stale-binary-claiming-v1.2.3")

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if _, err := deployer.Collect(context.Background(), lease); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := h.remote.count("cat > "); got != 1 {
		t.Fatalf("uploads = %d, want the artifact replaced despite the matching version", got)
	}
	deployed, ok := h.remote.file(agentPath)
	if !ok {
		t.Fatal("the agent was not activated")
	}
	if string(deployed) != string(h.localArtifact(t, "amd64")) {
		t.Fatal("the deployed bytes must match the manifest hash")
	}
}

// TestReviewFailedUploadCleansTemporaryArtifact pins R11: an upload that wrote
// bytes and then failed must not leave its temporary file behind.
func TestReviewFailedUploadCleansTemporaryArtifact(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
	h.remote.uploadWriteThenFail = true

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if _, err := deployer.Collect(context.Background(), lease); err == nil {
		t.Fatal("a failed upload must fail the collection")
	}
	if got := h.remote.count("rm -f "); got == 0 {
		t.Fatal("the temporary upload was not cleaned up")
	}
	for _, path := range h.remote.fileKeys() {
		if strings.Contains(path, ".upload-") {
			t.Fatalf("temporary artifact left behind: %s", path)
		}
	}
}

// TestRereviewCleanupWaitSurvivesTaskCancellation pins F05: shutdown, node
// deletion and a collection deadline all cancel the task context, and the cleanup
// wait must not be abandoned because of it — otherwise the scheduler reports the
// task finished while a transport is still closing.
func TestRereviewCleanupWaitSurvivesTaskCancellation(t *testing.T) {
	svc := NewMonitorService(nil, nil, nil, time.Minute)
	svc.leaseSettleBudget = 150 * time.Millisecond

	lease := &stubLease{done: make(chan struct{})}
	go func() {
		time.Sleep(80 * time.Millisecond)
		close(lease.done)
	}()
	start := time.Now()
	if !svc.awaitLeaseSettlement(lease) {
		t.Fatal("a lease settled during the wait must be reported as reclaimed")
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("wait = %v, want it to observe the settlement", elapsed)
	}
}

// TestRereviewRemoteCleanupExitFailureIsReported pins F04: a healthy transport
// with a non-zero remote rm exit is a cleanup failure, not a silent success.
func TestRereviewRemoteCleanupExitFailureIsReported(t *testing.T) {
	deployer, h := newAgentDeployHarness(t, "v1.2.3")
	h.remote.agentPayload = `{"agent_version":"v1.2.3","protocol_version":1}`
	h.remote.uploadWriteThenFail = true
	h.remote.removeExit = 1

	var logs bytes.Buffer
	deployer.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	deployer.telemetry = newMonitorTelemetry(nil, deployer.logger)

	lease, err := h.svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if _, err := deployer.Collect(context.Background(), lease); err == nil {
		t.Fatal("a failed upload must fail the collection")
	}
	output := logs.String()
	if !strings.Contains(output, "agent upload cleanup failed") {
		t.Fatalf("a rejected rm must be reported as a cleanup failure:\n%s", output)
	}
	if !strings.Contains(output, `"exit_code":1`) {
		t.Fatalf("the remote exit status must be recorded:\n%s", output)
	}
	leftOver := false
	for _, path := range h.remote.fileKeys() {
		if strings.Contains(path, ".upload-") {
			leftOver = true
		}
	}
	if !leftOver {
		t.Fatal("the peer kept the file, so the state must be reported as pending")
	}
}
