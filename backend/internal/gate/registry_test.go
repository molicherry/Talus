package gate

import (
	"context"
	"testing"
	"time"
)

func TestRegisterAdmitDone(t *testing.T) {
	r := NewRegistry()
	s := r.Register(1, 0, nil, nil)
	if s.State() != StatePreparing {
		t.Fatalf("state = %q, want preparing", s.State())
	}
	if !r.Admit(s.ID) {
		t.Fatal("a preparing session should be admitted")
	}
	if s.State() != StateReady {
		t.Fatalf("state = %q, want ready", s.State())
	}
	if r.LiveCount() != 1 {
		t.Fatalf("live = %d, want 1", r.LiveCount())
	}
	r.Done(s.ID)
	select {
	case <-s.Done():
	default:
		t.Fatal("Done was not closed")
	}
	if r.LiveCount() != 0 {
		t.Fatalf("live = %d, want 0 after Done", r.LiveCount())
	}
	r.Done(s.ID) // idempotent
}

// TestRevokedSessionCannotBeAdmitted pins the ready-admission race: a session
// revoked during PTY setup must not obtain a ready admission.
func TestRevokedSessionCannotBeAdmitted(t *testing.T) {
	r := NewRegistry()
	s := r.Register(1, 0, nil, nil)
	if len(r.MarkRevoked(1)) != 1 {
		t.Fatal("want exactly one revoked session")
	}
	if r.Admit(s.ID) {
		t.Fatal("a revoked session must not be admitted")
	}
	if s.State() != StateRevoked {
		t.Fatalf("state = %q, want revoked", s.State())
	}
}

// TestMarkRevokedCancelsAndAwaitForceCloses pins cancellation on revoke and the
// forced transport close when the graceful window expires.
func TestMarkRevokedCancelsAndAwaitForceCloses(t *testing.T) {
	r := NewRegistry()
	r.grace = 20 * time.Millisecond
	r.budget = 80 * time.Millisecond

	var canceled, forced bool
	var s *Session
	s = r.Register(1, 0, func() { canceled = true }, func() { forced = true; s.MarkDone() })

	revoked := r.MarkRevoked(1)
	if !canceled {
		t.Fatal("MarkRevoked must signal cancellation")
	}
	r.Await(context.Background(), revoked)
	if !forced {
		t.Fatal("Await must force-close a session that misses the graceful window")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done was not closed after the forced close")
	}
}

// TestAwaitDoesNotForceWhenDoneInGrace pins that a session finishing inside the
// graceful window is never force-closed.
func TestAwaitDoesNotForceWhenDoneInGrace(t *testing.T) {
	r := NewRegistry()
	r.grace = 200 * time.Millisecond
	r.budget = 500 * time.Millisecond

	var forced bool
	var s *Session
	s = r.Register(1, 0, func() { s.MarkDone() }, func() { forced = true })

	revoked := r.MarkRevoked(1)
	r.Await(context.Background(), revoked)
	if forced {
		t.Fatal("a session that finished in grace must not be force-closed")
	}
}

// A committed revoke owns an independent budget even if the HTTP client leaves.
func TestAwaitContinuesAfterParentCancellation(t *testing.T) {
	r := NewRegistry()
	r.grace = 25 * time.Millisecond
	r.budget = 80 * time.Millisecond
	forced := make(chan struct{})
	var s *Session
	s = r.Register(1, 0, nil, func() { close(forced); s.MarkDone() })
	revoked := r.MarkRevoked(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := r.Await(ctx, revoked)
	if !result.Complete || result.Forced != 1 || result.Pending != 0 {
		t.Fatalf("cleanup = %+v", result)
	}
	select {
	case <-forced:
	default:
		t.Fatal("client cancellation skipped resource cleanup")
	}
}

func TestAwaitSharesFrozenDeadlineAcrossSessionsAndCalls(t *testing.T) {
	r := NewRegistry()
	r.grace, r.budget = 30*time.Millisecond, 90*time.Millisecond
	forced := make(chan time.Time, 3)
	for range 3 {
		r.Register(1, 0, nil, func() { forced <- time.Now() })
	}
	started := time.Now()
	sessions := r.MarkRevoked(1)
	// Simulate bounded COMMIT verification between marking and awaiting.
	time.Sleep(50 * time.Millisecond)
	result := r.Await(context.Background(), sessions)
	if result.Complete || result.Pending != 3 {
		t.Fatalf("cleanup = %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 180*time.Millisecond {
		t.Fatalf("cleanup restarted its budget: %v", elapsed)
	}
	for range 3 {
		if at := <-forced; at.Sub(started) > 85*time.Millisecond {
			t.Fatalf("force received a new grace period: %v", at.Sub(started))
		}
	}
	if r.LiveCount() != 3 {
		t.Fatal("unfinished sessions must retain their resource ownership")
	}
	started = time.Now()
	result = r.Await(context.Background(), sessions)
	if result.Complete || time.Since(started) > 30*time.Millisecond {
		t.Fatal("repeated Await restarted expired budget")
	}
	select {
	case <-forced:
		t.Fatal("force callback invoked twice")
	default:
	}
	for _, s := range sessions {
		r.Done(s.ID)
	}
}

func TestRevocationStopsNewInputPermissions(t *testing.T) {
	r := NewRegistry()
	s := r.Register(1, 0, nil, nil)
	if r.InputPermit(s.ID) {
		t.Fatal("preparing session received input permission")
	}
	if !r.Admit(s.ID) || !r.InputPermit(s.ID) {
		t.Fatal("ready session lacks permission")
	}
	r.MarkRevoked(1)
	if r.InputPermit(s.ID) {
		t.Fatal("revoked session received a new input permission")
	}
	r.Done(s.ID)
}

func TestAwaitDeadlineSurvivesBlockedForceCallback(t *testing.T) {
	r := NewRegistry()
	r.grace, r.budget = 20*time.Millisecond, 80*time.Millisecond
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var s *Session
	s = r.Register(1, 0, nil, func() {
		close(entered)
		<-release
		r.Done(s.ID)
		close(finished)
	})
	defer func() { close(release); <-finished }()
	started := time.Now()
	result := r.Await(context.Background(), r.MarkRevoked(1))
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("blocked Force extended the absolute cleanup deadline: %v", elapsed)
	}
	if result.Complete || result.Pending != 1 || result.Forced != 1 {
		t.Fatalf("blocked cleanup = %+v", result)
	}
	select {
	case <-entered:
	default:
		t.Fatal("force callback was not started")
	}
	select {
	case <-s.Done():
		t.Fatal("pending cleanup was falsely marked done")
	default:
	}
	if r.LiveCount() != 1 {
		t.Fatal("pending cleanup lost its registry owner")
	}
}
