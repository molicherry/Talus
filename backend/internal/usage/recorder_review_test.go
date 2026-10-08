package usage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
)

func TestMissingAuditStoreCountsFailedAndLostWithoutRetry(t *testing.T) {
	r := NewRecorder(newFakeStore(), nil, Options{})
	op := shortAuthed(r)
	event := &model.AuditEvent{Action: "credential.reveal"}
	if err := op.WriteAudit(context.Background(), event); !errors.Is(err, ErrRecorderUnavailable) {
		t.Fatalf("missing audit store error=%v", err)
	}
	st := r.Stats()
	if st.AuditWriteFailed != 1 || st.AuditLost != 1 || st.AuditPending != 0 || event.OperationID == nil || *event.OperationID != op.OperationID() {
		t.Fatalf("unavailable audit was not counted once: %+v", st)
	}
}

func TestPermanentUsageFailuresDoNotConsumeCompletionRetryCapacity(t *testing.T) {
	for _, permanent := range []error{repository.ErrUsageInvalid, repository.ErrUsagePermanent} {
		t.Run(permanent.Error(), func(t *testing.T) {
			for _, retry := range []bool{false, true} {
				s := newFakeStore()
				attempts := 0
				fn := func(_ context.Context, row model.UsageLog) error {
					if row.Outcome == "running" {
						return nil
					}
					attempts++
					if retry && attempts == 1 {
						return repository.ErrUsageStorage
					}
					return permanent
				}
				s.write = fn
				r := newTestRecorder(s, newFakeAudit())
				op := authed(r)
				op.Begin()
				r.Finish(op, 200)
				if retry {
					r.mu.Lock()
					if r.pending[op.OperationID()] == nil {
						r.mu.Unlock()
						t.Fatal("temporary failure was not queued")
					}
					r.pending[op.OperationID()].next = time.Now().Add(-time.Second)
					r.mu.Unlock()
					r.retryPending()
				}
				if st := r.Stats(); st.Pending != 0 || st.Active != 0 || st.FinalizationLost != 1 || st.WriteFailed != uint64(attempts) {
					t.Fatalf("permanent failure kept retry capacity: %+v attempts=%d", st, attempts)
				}
			}
		})
	}
}

func TestUsagePolicyDefaultsAndFutureDeleteRetentionMatchCapture(t *testing.T) {
	opts := (Options{LeaseDuration: -time.Second, OrdinaryRetention: 0, SensitiveRetention: -time.Hour}).defaults()
	policy := (model.UsagePolicy{}).WithDefaults()
	if opts.LeaseDuration != policy.LeaseDuration || opts.OrdinaryRetention != policy.OrdinaryRetention || opts.SensitiveRetention != policy.SensitiveRetention {
		t.Fatal("capture lease or zero/negative retention defaults diverged from shared policy")
	}
	r := NewRecorder(newFakeStore(), nil, Options{OrdinaryRetention: time.Hour, SensitiveRetention: 3 * time.Hour})
	retentionAt := time.Now().Add(-2 * time.Hour)
	for _, tc := range []struct {
		action  string
		expired bool
	}{{"future_resource.delete", false}, {"credential.reveal", false}, {"server.host_key.trust", false}, {"server.update", true}, {"future_resource.delete.extra", true}} {
		if got := r.snapshotExpired(model.UsageLog{Action: tc.action, RetentionAt: &retentionAt}, time.Now()); got != tc.expired {
			t.Errorf("retention for %s expired=%t", tc.action, got)
		}
	}
}

type blockedTrackedStore struct {
	*fakeStore
	entered, resume chan struct{}
	snapshot        []string
}

func (s *blockedTrackedStore) ReconcileTracked(ctx context.Context, _ string, snapshot func() []string) error {
	close(s.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.resume:
		s.snapshot = snapshot()
		return nil
	}
}

func TestReconcileDatabaseWaitDoesNotBlockCaptureFinishStatsOrInFlightRetry(t *testing.T) {
	s := &blockedTrackedStore{fakeStore: newFakeStore(), entered: make(chan struct{}), resume: make(chan struct{})}
	retryEntered, retryResume := make(chan struct{}), make(chan struct{})
	var finals atomic.Int32
	s.write = func(_ context.Context, row model.UsageLog) error {
		if row.Outcome == "running" {
			return nil
		}
		if finals.Add(1) == 1 {
			return repository.ErrUsageStorage
		}
		close(retryEntered)
		<-retryResume
		return nil
	}
	r := NewRecorder(s, newFakeAudit(), Options{WriteTimeout: 10 * time.Second})
	r.registerOwner()
	reconciled := make(chan struct{})
	go func() { r.reconcile(); close(reconciled) }()
	<-s.entered
	finished := make(chan *Operation, 1)
	go func() {
		op := authed(r)
		op.Begin()
		r.Finish(op, 200)
		r.Stats()
		shortAuthed(r)
		finished <- op
	}()
	var op *Operation
	select {
	case op = <-finished:
	case <-time.After(time.Second):
		close(s.resume)
		t.Fatal("capture, Finish, or Stats waited on recorder's reconciliation mutex")
	}
	r.mu.Lock()
	pending := r.pending[op.OperationID()]
	if pending == nil {
		r.mu.Unlock()
		close(s.resume)
		t.Fatal("completed failure was not atomically queued")
	}
	fixed := pending.row
	pending.next = time.Now().Add(-time.Second)
	r.mu.Unlock()
	retried := make(chan struct{})
	go func() { r.retryPending(); close(retried) }()
	<-retryEntered
	close(s.resume)
	<-reconciled
	if len(s.snapshot) != 1 || s.snapshot[0] != op.OperationID() || r.Stats().Pending != 1 {
		close(retryResume)
		t.Fatalf("new in-flight completion was excluded from late snapshot: %v", s.snapshot)
	}
	close(retryResume)
	<-retried
	row := s.get(op.OperationID())
	if row.FinishedAt == nil || !row.FinishedAt.Equal(*fixed.FinishedAt) || row.RetentionAt == nil || !row.RetentionAt.Equal(*fixed.RetentionAt) || r.Stats().Pending != 0 {
		t.Fatal("reconciliation changed retry's fixed completion or retention facts")
	}
}

type preparationStore struct {
	*fakeStore
	prepare func(context.Context) error
}

func (s *preparationStore) PrepareCleanup(ctx context.Context) error { return s.prepare(ctx) }

func TestCleanupPreparationHasIndependentReadBudgetAndSharedPoolCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &preparationStore{fakeStore: newFakeStore()}
		s.prepare = func(ctx context.Context) error {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != cleanupReportTimeout {
				t.Fatal("initial cleanup count was restricted to the ordinary 200ms write budget")
			}
			time.Sleep(time.Second)
			return ctx.Err()
		}
		r := NewRecorder(s, nil, Options{})
		if err := r.PrepareCleanupReport(context.Background()); err != nil {
			t.Fatalf("slow initial read failed: %v", err)
		}
		if len(r.allSlots) != 0 || len(r.maintenanceSlot) != 0 || r.opts.WriteTimeout != 200*time.Millisecond {
			t.Fatal("initial count changed write budget or leaked shared slots")
		}
		for i := 0; i < cap(r.allSlots); i++ {
			r.allSlots <- struct{}{}
		}
		if err := r.PrepareCleanupReport(context.Background()); !errors.Is(err, errNoWriteSlot) {
			t.Fatal("initial count exceeded shared quarter-pool capacity")
		}
	})
}
