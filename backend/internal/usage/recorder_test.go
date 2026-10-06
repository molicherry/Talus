package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
)

type fakeStore struct {
	mu            sync.Mutex
	rows          map[string]model.UsageLog
	owners        map[string]bool
	retired       map[string]bool
	calls         []model.UsageLog
	tracked       []string
	unknown       []string
	registerError error
	write         func(context.Context, model.UsageLog) error
	cleanup       func(context.Context) error
	cleanupCalls  int
	heartbeats    int
	reconciles    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]model.UsageLog{}, owners: map[string]bool{}, retired: map[string]bool{}}
}
func (s *fakeStore) Upsert(ctx context.Context, row *model.UsageLog) error {
	s.mu.Lock()
	s.calls = append(s.calls, *row)
	fn := s.write
	if row.OwnerInstanceID != nil {
		if s.retired[*row.OwnerInstanceID] {
			s.mu.Unlock()
			return repository.ErrUsageOwnerRetired
		}
		if !s.owners[*row.OwnerInstanceID] {
			s.mu.Unlock()
			return repository.ErrUsageOwnerMissing
		}
	}
	s.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, *row); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.rows[row.OperationID]
	if exists && old.StateSeq >= row.StateSeq {
		return nil
	}
	s.rows[row.OperationID] = *row
	return nil
}
func (s *fakeStore) RegisterInstance(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registerError != nil {
		return s.registerError
	}
	s.owners[id] = true
	return nil
}
func (s *fakeStore) Heartbeat(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats++
	if s.retired[id] {
		return repository.ErrUsageOwnerRetired
	}
	if !s.owners[id] {
		return repository.ErrUsageOwnerMissing
	}
	return nil
}
func (s *fakeStore) Reconcile(_ context.Context, _ string, tracked []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracked = append([]string(nil), tracked...)
	s.reconciles++
	return nil
}
func (s *fakeStore) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	s.cleanupCalls++
	fn := s.cleanup
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}
func (s *fakeStore) OwnerUnknown(context.Context, string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.unknown...), nil
}
func (s *fakeStore) get(id string) model.UsageLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}
func (s *fakeStore) callCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }
func (s *fakeStore) cleanupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupCalls
}

type fakeAudit struct {
	mu    sync.Mutex
	calls int
	rows  map[string]model.AuditEvent
	fail  bool
}

func (a *fakeAudit) Create(_ context.Context, e *model.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.fail {
		return errors.New("secret=must-never-log")
	}
	a.rows[*e.OperationID] = *e
	return nil
}
func newFakeAudit() *fakeAudit { return &fakeAudit{rows: map[string]model.AuditEvent{}} }
func newTestRecorder(s *fakeStore, a *fakeAudit) *Recorder {
	r := NewRecorder(s, a, Options{HeartbeatInterval: time.Hour, ReconcileInterval: time.Hour, CleanupInterval: time.Hour})
	r.registerOwner()
	return r
}
func authed(r *Recorder) *Operation {
	op := r.NewOperation("server.exec", "/api/v1/servers/{id}/exec", "req", "POST", "127.0.0.1:1234")
	u := uint(7)
	op.SetPrincipal(Principal{AuthType: "jwt", UserID: &u, Username: "admin", Role: "admin"})
	return op
}

func TestStartFailureStillPersistsCompleteFinal(t *testing.T) {
	s := newFakeStore()
	s.write = func(_ context.Context, row model.UsageLog) error {
		if row.Outcome == "running" {
			return errors.New("insert failed")
		}
		return nil
	}
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	if !op.Begin() {
		t.Fatal("long operation was not admitted")
	}
	op.SetExecResult(ptr(1))
	op.SetResult("failed", "ssh_nonzero_exit")
	r.Finish(op, 200)
	row := s.get(op.OperationID())
	if row.Outcome != "failed" || row.HTTPStatus == nil || *row.HTTPStatus != 200 || row.ExitCode == nil || *row.ExitCode != 1 || row.FinishedAt == nil || row.DurationMS == nil {
		t.Fatalf("final result = %+v", row)
	}
	r.Finish(op, 500)
	if s.callCount() != 2 {
		t.Fatalf("duplicate Finish wrote again: %d", s.callCount())
	}
	if st := r.Stats(); st.Active != 0 || st.Pending != 0 || st.WriteFailed != 1 {
		t.Fatalf("stats=%+v", st)
	}
}

func TestFinalRetryKeepsActualTimesAndTracksInFlight(t *testing.T) {
	s := newFakeStore()
	fail := true
	s.write = func(_ context.Context, row model.UsageLog) error {
		if row.Outcome != "running" && fail {
			return errors.New("update failed")
		}
		return nil
	}
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	op.Begin()
	op.SetResult("succeeded", "")
	r.Finish(op, 200)
	r.mu.Lock()
	pending := r.pending[op.OperationID()]
	if pending == nil {
		t.Fatal("missing retry")
	}
	fixed := pending.row
	pending.next = time.Now().Add(-time.Second)
	r.mu.Unlock()
	r.reconcile()
	s.mu.Lock()
	tracked := append([]string(nil), s.tracked...)
	s.mu.Unlock()
	if len(tracked) != 1 || tracked[0] != op.OperationID() {
		t.Fatalf("pending was untracked: %v", tracked)
	}
	fail = false
	r.retryPending()
	row := s.get(op.OperationID())
	if row.FinishedAt == nil || !row.FinishedAt.Equal(*fixed.FinishedAt) || *row.DurationMS != *fixed.DurationMS || row.StateSeq != fixed.StateSeq {
		t.Fatal("retry recomputed business facts")
	}
	if r.Stats().Pending != 0 {
		t.Fatal("completed retry remained queued")
	}
}

func TestSynchronousFinishStaysTrackedUntilAtomicQueueTransfer(t *testing.T) {
	s := newFakeStore()
	entered := make(chan struct{})
	resume := make(chan struct{})
	s.write = func(_ context.Context, row model.UsageLog) error {
		if row.Outcome != "running" {
			close(entered)
			<-resume
			return errors.New("failed")
		}
		return nil
	}
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	op.Begin()
	done := make(chan struct{})
	go func() { r.Finish(op, 200); close(done) }()
	<-entered
	r.reconcile()
	s.mu.Lock()
	tracked := append([]string(nil), s.tracked...)
	s.mu.Unlock()
	if len(tracked) != 1 || tracked[0] != op.OperationID() {
		t.Fatalf("finishing operation untracked: %v", tracked)
	}
	close(resume)
	<-done
	if st := r.Stats(); st.Active != 0 || st.Pending != 1 {
		t.Fatalf("atomic transfer failed: %+v", st)
	}
}

func TestCompletionQueueOverflowAndExpirationReleaseTracking(t *testing.T) {
	s := newFakeStore()
	s.write = func(_ context.Context, row model.UsageLog) error {
		if row.Outcome != "running" {
			return errors.New("unavailable")
		}
		return nil
	}
	r := newTestRecorder(s, newFakeAudit())
	r.opts.RetryItems = 1
	first := authed(r)
	first.Begin()
	r.Finish(first, 200)
	second := authed(r)
	second.Begin()
	r.Finish(second, 200)
	if st := r.Stats(); st.Active != 0 || st.Pending != 1 || st.FinalizationLost != 1 {
		t.Fatalf("capacity not bounded: %+v", st)
	}
	r.mu.Lock()
	r.pending[first.OperationID()].born = time.Now().Add(-3 * time.Minute)
	r.mu.Unlock()
	r.retryPending()
	r.reconcile()
	s.mu.Lock()
	tracked := len(s.tracked)
	s.mu.Unlock()
	if st := r.Stats(); st.Pending != 0 || st.PendingBytes != 0 || st.FinalizationLost != 2 || tracked != 0 {
		t.Fatalf("expired retry still tracked: %+v tracked=%d", st, tracked)
	}
}

func TestUnauthenticatedBudgetsPrecedeInsertAndIPMapIsBounded(t *testing.T) {
	s := newFakeStore()
	r := newTestRecorder(s, newFakeAudit())
	for i := 0; i < 20; i++ {
		op := r.NewOperation("server.exec", "/servers/{id}/exec", "req", "POST", "10.0.0.1")
		op.SetResult("rejected", "unauthorized")
		r.Finish(op, 401)
	}
	if n := s.callCount(); n != 5 {
		t.Fatalf("per-IP budget inserted %d, want 5", n)
	}
	r = NewRecorder(s, newFakeAudit(), Options{IPLimit: 2, UnauthenticatedRate: 0.00001, UnauthenticatedBurst: 4})
	base := s.callCount()
	for i := 0; i < 100; i++ {
		op := r.NewOperation("server.exec", "route", "req", "POST", fmt.Sprintf("10.0.0.%d", i+1))
		op.SetResult("rejected", "unauthorized")
		r.Finish(op, 401)
	}
	if n := s.callCount() - base; n != 4 {
		t.Fatalf("global budget inserted=%d", n)
	}
	if len(r.ips) != 2 {
		t.Fatalf("IP map=%d", len(r.ips))
	}
}

func TestRegistrationFailureAndRetiredOwnerCannotCreateOrResurrectRunning(t *testing.T) {
	s := newFakeStore()
	s.registerError = errors.New("registration unavailable")
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	if op.Begin() {
		t.Fatal("unregistered owner admitted")
	}
	r.Finish(op, 200)
	if s.callCount() != 0 {
		t.Fatal("created ownerless usage row")
	}
	s.mu.Lock()
	s.registerError = nil
	s.mu.Unlock()
	r.registerOwner()
	old := authed(r)
	if !old.Begin() {
		t.Fatal("registered owner rejected")
	}
	oldRow := old.snapshot()
	oldOwner := *oldRow.OwnerInstanceID
	s.mu.Lock()
	s.retired[oldOwner] = true
	delete(s.rows, old.OperationID())
	s.mu.Unlock()
	r.heartbeat()
	next := authed(r)
	if !next.Begin() {
		t.Fatal("new owner not admitted")
	}
	if *next.snapshot().OwnerInstanceID == oldOwner {
		t.Fatal("retired instance reused")
	}
	r.Finish(old, 200)
	if _, exists := s.rows[old.OperationID()]; exists {
		t.Fatal("retired operation resurrected")
	}
	if r.Stats().FinalizationLost != 1 {
		t.Fatal("lost retired final was not counted")
	}
}

func TestHeartbeatRestoresOnlyActiveUnknownWithHigherSequence(t *testing.T) {
	s := newFakeStore()
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	op.Begin()
	op.SetPhase("ready")
	before := op.snapshot()
	s.mu.Lock()
	s.unknown = []string{op.OperationID(), "not-locally-tracked"}
	s.mu.Unlock()
	r.heartbeat()
	after := s.get(op.OperationID())
	if after.StateSeq <= before.StateSeq || after.Outcome != "running" || after.Phase != "ready" || after.RetentionAt != nil || after.FinishedAt != nil {
		t.Fatalf("restoration=%+v", after)
	}
}

func TestAuditIsIndependentFromUsageCapacityAndUsesSafeFixedRetry(t *testing.T) {
	s := newFakeStore()
	a := newFakeAudit()
	r := newTestRecorder(s, a)
	for i := 0; i < cap(r.writeSlots); i++ {
		r.writeSlots <- struct{}{}
	}
	op := authed(r)
	event := &model.AuditEvent{Username: "user", Action: "credential.reveal", ResourceType: "credential", ResourceID: 9, Details: "SECRET-credential-value"}
	if err := op.WriteAudit(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(r.writeSlots); i++ {
		<-r.writeSlots
	}
	a.mu.Lock()
	stored := a.rows[op.OperationID()]
	a.fail = true
	a.mu.Unlock()
	if stored.Details != "" {
		t.Fatal("audit queue accepted arbitrary details")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := op.WriteAudit(ctx, event); err == nil {
		t.Fatal("fault not injected")
	}
	r.mu.Lock()
	p := r.auditPending[op.OperationID()]
	p.next = time.Now().Add(-time.Second)
	r.mu.Unlock()
	a.mu.Lock()
	a.fail = false
	a.mu.Unlock()
	r.retryAudits()
	a.mu.Lock()
	count := len(a.rows)
	a.mu.Unlock()
	if count != 1 || r.Stats().AuditPending != 0 {
		t.Fatal("audit retry duplicated event or stayed queued")
	}
}

func TestSafeMetadataSnapshotAndBusinessResultPrecedence(t *testing.T) {
	op := NewOperation("service.relay", "/services/{id}/relay", "req", "POST", "127.0.0.1:2")
	id := uint(4)
	secret := "COMMAND_SECRET"
	op.SetResource("service", &id, "name\x00\n"+strings.Repeat("界", 100), nil)
	op.SetMetadata("command", secret)
	op.SetMetadata("path", "/SECRET-PATH")
	op.SetMetadata("body", secret)
	op.SetMetadata("bytes_copied", int64(99))
	op.SetMetadata("close_reason", secret)
	op.SetResult("succeeded", "")
	op.SetHTTPError(500, "internal_error")
	running := op.snapshot()
	if running.Outcome != "running" {
		t.Fatal("business outcome persisted before Finish")
	}
	row, _ := op.finish(500)
	data, _ := json.Marshal(row)
	if strings.Contains(string(data), "SECRET") || len(row.ResourceNameSnapshot) > 256 || strings.ContainsAny(row.ResourceNameSnapshot, "\x00\n") {
		t.Fatalf("unsafe snapshot: %s", data)
	}
	if row.Outcome != "succeeded" || !strings.Contains(string(row.Metadata), `"response_write_failed":true`) {
		t.Fatalf("committed result overwritten: %+v", row)
	}
}

func TestOperationConcurrentPhasesAndFinishAreSingleFinal(t *testing.T) {
	s := newFakeStore()
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	op.Begin()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); op.SetPhase("ready"); op.SetResult("succeeded", ""); r.Finish(op, 200) }()
	}
	wg.Wait()
	row := s.get(op.OperationID())
	if row.Outcome != "succeeded" || row.FinishedAt == nil || r.Stats().Active != 0 {
		t.Fatalf("final=%+v", row)
	}
	if s.callCount() != 2 {
		t.Fatalf("wrote %d times, want start plus final", s.callCount())
	}
}

func TestPhaseQueueCoalescesAndDropsFinishedOperations(t *testing.T) {
	s := newFakeStore()
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	op.Begin()
	for i := 0; i < 10000; i++ {
		if i%2 == 0 {
			op.SetPhase("ready")
		} else {
			op.SetPhase("authenticated")
		}
	}
	if len(r.phases) != 1 || len(r.phaseQueued) != 1 {
		t.Fatalf("phase queue grew with updates: %d/%d", len(r.phases), len(r.phaseQueued))
	}
	r.Finish(op, 200)
	id := <-r.phases
	r.persistPhase(id)
	if len(r.phaseQueued) != 0 || s.callCount() != 2 {
		t.Fatal("finished phase wrote or stayed queued")
	}
}

func TestUsageWritesAndMaintenanceShareQuarterPoolLimit(t *testing.T) {
	r := NewRecorder(newFakeStore(), newFakeAudit(), Options{DBMaxOpenConnections: 12, WriteConcurrency: 20})
	if cap(r.allSlots) != 3 || cap(r.writeSlots) != 1 {
		t.Fatalf("connection bound=%d/%d", cap(r.allSlots), cap(r.writeSlots))
	}
	for i := 0; i < cap(r.allSlots); i++ {
		r.allSlots <- struct{}{}
	}
	if err := r.ownerWrite(func(context.Context) error { t.Fatal("heartbeat exceeded shared cap"); return nil }); !errors.Is(err, errNoWriteSlot) {
		t.Fatalf("heartbeat error=%v", err)
	}
	if err := r.maintenanceWrite(func(context.Context) error { t.Fatal("maintenance exceeded shared cap"); return nil }); !errors.Is(err, errNoWriteSlot) {
		t.Fatalf("maintenance error=%v", err)
	}
	op := authed(r)
	row, _ := op.finish(200)
	if err := r.writeUsage(row, true); !errors.Is(err, errNoWriteSlot) {
		t.Fatalf("write exceeded pool=%v", err)
	}
}

func TestCancelledRequestDoesNotCancelIndependentAuditWrite(t *testing.T) {
	r := newTestRecorder(newFakeStore(), newFakeAudit())
	op := authed(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := op.WriteAudit(ctx, &model.AuditEvent{Action: "credential.reveal"}); err != nil {
		t.Fatalf("request cancellation cancelled audit: %v", err)
	}
}

func waitForOperation(t *testing.T, op *Operation, ready func(*Operation) bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		op.mu.Lock()
		ok := ready(op)
		op.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("operation did not reach expected state")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestFinishDuringBeginAdmissionCannotLeaveTerminalOperationActive(t *testing.T) {
	s := newFakeStore()
	r := newTestRecorder(s, newFakeAudit())
	op := authed(r)
	// Both calls must wait for recorder admission, while Finish can already
	// finalize the operation. The result does not depend on who wins r.mu.
	r.mu.Lock()
	locked := true
	defer func() {
		if locked {
			r.mu.Unlock()
		}
	}()
	begun := make(chan bool, 1)
	go func() { begun <- op.Begin() }()
	waitForOperation(t, op, func(op *Operation) bool { return op.beginAttempted })
	finished := make(chan struct{})
	go func() { r.Finish(op, 200); close(finished) }()
	waitForOperation(t, op, func(op *Operation) bool { return op.finished })
	r.mu.Unlock()
	locked = false
	if <-begun {
		t.Fatal("Begin admitted an already finished operation")
	}
	<-finished
	if st := r.Stats(); st.Active != 0 || st.Pending != 0 || s.callCount() != 0 {
		t.Fatalf("terminal operation remained tracked or was written: %+v calls=%d", st, s.callCount())
	}
}

func shortAuthed(r *Recorder) *Operation {
	op := r.NewOperation("server.create", "/api/v1/servers", "req", "POST", "127.0.0.1")
	op.SetPrincipal(Principal{AuthType: "jwt", UserID: ptr(uint(7)), Username: "admin"})
	return op
}

func TestAuthenticatedShortAdmissionIsDecidedOnceBeforeFinish(t *testing.T) {
	s, a := newFakeStore(), newFakeAudit()
	r := NewRecorder(s, a, Options{AuthenticatedBurst: 1, AuthenticatedRate: 0.000001})
	accepted := shortAuthed(r)
	denied := shortAuthed(r)
	if !accepted.captureDecided || !accepted.admitted || !denied.captureDecided || denied.admitted || s.callCount() != 0 {
		t.Fatal("short capture was not decided at identity verification without DB writes")
	}
	// Repeated identity/admission calls cannot consume another token or change
	// the decision, even when the budget becomes available before Finish.
	accepted.SetPrincipal(Principal{AuthType: "jwt", UserID: ptr(uint(8))})
	r.AdmitShort(accepted)
	r.mu.Lock()
	r.authBudget.tokens = 1
	r.mu.Unlock()
	r.Finish(denied, 200)
	r.Finish(accepted, 200)
	if r.authBudget.tokens != 1 || s.callCount() != 1 || s.get(accepted.OperationID()).Outcome != "succeeded" || r.Stats().CaptureSkipped != 1 {
		t.Fatal("Finish repeated the admission decision or lost an accepted short result")
	}
	if err := denied.WriteAudit(context.Background(), &model.AuditEvent{Action: "server.create"}); err != nil || len(a.rows) != 1 {
		t.Fatalf("denied usage capture prevented independent audit: %v", err)
	}
}

func TestLongAdmissionWaitsForBeginAndMissingUsageStoreSkipsSafely(t *testing.T) {
	for _, action := range []string{"server.exec", "service.relay", "server.terminal"} {
		t.Run(action, func(t *testing.T) {
			s := newFakeStore()
			r := NewRecorder(s, newFakeAudit(), Options{AuthenticatedBurst: 1, AuthenticatedRate: 0.000001})
			r.registerOwner()
			op := r.NewOperation(action, "route", "req", "POST", "127.0.0.1")
			op.SetPrincipal(Principal{AuthType: "jwt", UserID: ptr(uint(7))})
			if op.captureDecided || op.admitted || !r.authBudget.last.IsZero() {
				t.Fatal("long operation consumed admission before Begin")
			}
			first := op.Begin()
			again := op.Begin()
			if !first || !again {
				t.Fatal("long operation was not admitted idempotently")
			}
			r.Finish(op, 200)
			if s.callCount() != 2 || r.Stats().CaptureSkipped != 0 {
				t.Fatal("long admission or completion consumed another budget token")
			}
		})
	}
	a := newFakeAudit()
	r := NewRecorder(nil, a, Options{})
	op := shortAuthed(r)
	op.SetResult("succeeded", "")
	r.Finish(op, 200)
	if st := r.Stats(); st.CaptureSkipped != 1 || st.Pending != 0 || st.WriteFailed != 0 || op.snapshot().Outcome != "succeeded" {
		t.Fatalf("missing usage store changed business result or queued writes: %+v", st)
	}
	if err := op.WriteAudit(context.Background(), &model.AuditEvent{Action: "server.create"}); err != nil {
		t.Fatal(err)
	}
}

func TestShortInitialAndRetryWritesPreserveLongCompletionSlot(t *testing.T) {
	s := newFakeStore()
	r := newTestRecorder(s, newFakeAudit())
	long := authed(r)
	if !long.Begin() {
		t.Fatal("long operation rejected")
	}
	// Saturate the ordinary side of the shared pool, leaving exactly one
	// regular write slot for already accepted long-operation completions.
	for i := 0; i < cap(r.ordinarySlots); i++ {
		r.ordinarySlots <- struct{}{}
		r.writeSlots <- struct{}{}
		r.allSlots <- struct{}{}
	}
	short := shortAuthed(r)
	r.Finish(short, 200)
	r.mu.Lock()
	pending := r.pending[short.OperationID()]
	if pending == nil {
		r.mu.Unlock()
		t.Fatal("short write did not remain in bounded retry queue")
	}
	fixed := pending.row
	pending.next = time.Now().Add(-time.Second)
	r.mu.Unlock()
	r.retryPending()
	if s.callCount() != 1 || r.Stats().Pending != 1 {
		t.Fatal("short first write or retry used reserved completion capacity")
	}
	r.Finish(long, 200)
	if s.get(long.OperationID()).Outcome != "succeeded" || r.Stats().Pending != 1 {
		t.Fatal("ordinary short writes prevented accepted long completion")
	}
	for i := 0; i < cap(r.ordinarySlots); i++ {
		<-r.ordinarySlots
		<-r.writeSlots
		<-r.allSlots
	}
	r.mu.Lock()
	pending.next = time.Now().Add(-time.Second)
	r.mu.Unlock()
	r.retryPending()
	row := s.get(short.OperationID())
	if r.Stats().Pending != 0 || row.FinishedAt == nil || !row.FinishedAt.Equal(*fixed.FinishedAt) || row.StateSeq != fixed.StateSeq {
		t.Fatal("short retry did not persist its fixed final snapshot after capacity returned")
	}
}

func TestSetResourceNormalizesZeroIDsAndCopiesPositiveIDs(t *testing.T) {
	op := NewOperation("server.create", "route", "req", "POST", "127.0.0.1")
	zero := uint(0)
	op.SetResource("server", &zero, "name", &zero)
	zero = 99
	if row := op.snapshot(); row.ResourceID != nil || row.ServerID != nil || row.ResourceNameSnapshot != "name" {
		t.Fatalf("zero resource IDs were not normalized: %+v", row)
	}
	id, server := uint(12), uint(34)
	op.SetResource("service", &id, "service", &server)
	id, server = 0, 0
	row, _ := op.finish(200)
	if row.ResourceID == nil || *row.ResourceID != 12 || row.ServerID == nil || *row.ServerID != 34 {
		t.Fatalf("resource IDs did not preserve copied values: %+v", row)
	}
}

type countedCleanupStore struct {
	*fakeStore
	cleaned atomic.Uint64
}

func (s *countedCleanupStore) CleanedCount() uint64 { return s.cleaned.Load() }

func TestCleanupDefaultsAndStoreWithoutCounterRemainBounded(t *testing.T) {
	if got := (Options{}).defaults().CleanupInterval; got != time.Minute {
		t.Fatalf("default cleanup interval=%s", got)
	}
	if got := (Options{CleanupInterval: 17 * time.Second}).defaults().CleanupInterval; got != 17*time.Second {
		t.Fatalf("explicit cleanup interval=%s", got)
	}
	s := newFakeStore()
	r := NewRecorder(s, newFakeAudit(), Options{})
	if r.drainCleanup(context.Background(), nil) || s.cleanupCalls != 1 {
		t.Fatal("store without progress counter was drained speculatively")
	}
}

func TestCleanupDrainsPartialBatchesWithFreshContextsAndYieldsToReconcile(t *testing.T) {
	s := &countedCleanupStore{fakeStore: newFakeStore()}
	r := NewRecorder(s, newFakeAudit(), Options{})
	r.registerOwner()
	due := make(chan time.Time, 1)
	var contexts []context.Context
	s.cleanup = func(ctx context.Context) error {
		contexts = append(contexts, ctx)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > r.opts.WriteTimeout {
			t.Fatal("cleanup batch lacked an independent bounded deadline")
		}
		switch len(contexts) {
		case 1:
			s.cleaned.Add(200)
			due <- time.Now()
		case 2:
			s.cleaned.Add(7)
			s.mu.Lock()
			if s.reconciles != 1 {
				t.Error("cleanup did not yield to due reconciliation")
			}
			s.mu.Unlock()
			r.heartbeat()
		}
		return nil
	}
	if r.drainCleanup(context.Background(), due) || len(contexts) != 3 || s.CleanedCount() != 207 || s.heartbeats != 1 {
		t.Fatal("cleanup failed to drain a partial batch, stop when empty, or preserve heartbeat capacity")
	}
	for i, ctx := range contexts {
		if !errors.Is(ctx.Err(), context.Canceled) || (i > 0 && contexts[i-1] == ctx) {
			t.Fatal("cleanup reused a context or kept a finished batch alive")
		}
	}
}

func TestCleanupRoundBatchCapAndTimeCapScheduleShortFollowUp(t *testing.T) {
	t.Run("batch cap", func(t *testing.T) {
		s := &countedCleanupStore{fakeStore: newFakeStore()}
		s.cleanup = func(context.Context) error { s.cleaned.Add(1); return nil }
		r := NewRecorder(s, newFakeAudit(), Options{})
		if !r.drainCleanup(context.Background(), nil) || s.cleanupCalls != cleanupRoundBatches {
			t.Fatalf("cleanup round was not capped: batches=%d", s.cleanupCalls)
		}
	})
	t.Run("time cap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := &countedCleanupStore{fakeStore: newFakeStore()}
			s.cleanup = func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(50 * time.Millisecond):
					s.cleaned.Add(1)
					return nil
				}
			}
			r := NewRecorder(s, newFakeAudit(), Options{WriteTimeout: time.Second})
			started := time.Now()
			if !r.drainCleanup(context.Background(), nil) || time.Since(started) != cleanupRoundTimeout || s.cleanupCalls != 40 {
				t.Fatalf("cleanup exceeded time budget or lost follow-up: duration=%s batches=%d", time.Since(started), s.cleanupCalls)
			}
		})
	})
}

func TestCleanupDeadlineAfterCommittedRowsSchedulesFollowUp(t *testing.T) {
	s := &countedCleanupStore{fakeStore: newFakeStore()}
	s.cleanup = func(context.Context) error { s.cleaned.Add(3); return context.DeadlineExceeded }
	r := NewRecorder(s, newFakeAudit(), Options{})
	if !r.drainCleanup(context.Background(), nil) || s.cleanupCalls != 1 || s.CleanedCount() != 3 {
		t.Fatal("partially committed deadline waited for the ordinary cleanup interval")
	}
	s.cleanup = func(context.Context) error { return nil }
	if r.drainCleanup(context.Background(), nil) || s.cleanupCalls != 2 {
		t.Fatal("empty follow-up did not restore the ordinary cleanup interval")
	}
	if len(r.maintenanceSlot) != 0 || len(r.allSlots) != 0 {
		t.Fatal("cleanup failed to release slots after deadline")
	}
}

func TestMaintenanceCleanupTimerUsesShortFollowUpThenNormalInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &countedCleanupStore{fakeStore: newFakeStore()}
		s.cleanup = func(context.Context) error {
			if s.cleanupCount() == 1 {
				s.cleaned.Add(3)
				return context.DeadlineExceeded
			}
			return nil
		}
		r := NewRecorder(s, newFakeAudit(), Options{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r.wg.Add(1)
		go r.maintenanceLoop(ctx)
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls := s.cleanupCount(); calls != 1 {
			t.Fatalf("initial cleanup calls=%d", calls)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls := s.cleanupCount(); calls != 2 {
			t.Fatalf("partial deadline did not get a one-second follow-up: calls=%d", calls)
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if s.cleanupCount() != 2 {
			t.Fatal("empty follow-up kept polling cleanup every second")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if s.cleanupCount() != 3 {
			t.Fatal("empty follow-up did not restore the normal cleanup interval")
		}
		cancel()
		r.wg.Wait()
	})
}

func TestCleanupNoProgressErrorsAndCapacityDoNotSpin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		progress bool
	}{{"empty", nil, false}, {"deadline", context.DeadlineExceeded, false}, {"database error after partial commit", errors.New("database unavailable"), true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &countedCleanupStore{fakeStore: newFakeStore()}
			s.cleanup = func(context.Context) error {
				if tc.progress {
					s.cleaned.Add(1)
				}
				return tc.err
			}
			r := NewRecorder(s, newFakeAudit(), Options{})
			if r.drainCleanup(context.Background(), nil) || s.cleanupCalls != 1 {
				t.Fatal("cleanup spun despite no progress or a database failure")
			}
		})
	}
	s := &countedCleanupStore{fakeStore: newFakeStore()}
	r := NewRecorder(s, newFakeAudit(), Options{})
	r.maintenanceSlot <- struct{}{}
	if r.drainCleanup(context.Background(), nil) || s.cleanupCalls != 0 || len(r.allSlots) != 0 {
		t.Fatal("cleanup waited on occupied maintenance capacity")
	}
	<-r.maintenanceSlot
}

func TestCleanupCancellationStopsCurrentBatchAndReleasesSlots(t *testing.T) {
	s := &countedCleanupStore{fakeStore: newFakeStore()}
	entered := make(chan struct{})
	s.cleanup = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	r := NewRecorder(s, newFakeAudit(), Options{WriteTimeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- r.drainCleanup(ctx, nil) }()
	<-entered
	cancel()
	select {
	case followUp := <-done:
		if followUp || s.cleanupCalls != 1 || len(r.maintenanceSlot) != 0 || len(r.allSlots) != 0 {
			t.Fatal("cancelled cleanup scheduled more work or held slots")
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup ignored shutdown cancellation")
	}
}

func ptr[T any](v T) *T { return &v }
