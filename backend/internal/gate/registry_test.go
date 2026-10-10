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

// TestAwaitRespectsParentContextCancellation pins that Await does not hang past
// a cancelled parent context.
func TestAwaitRespectsParentContextCancellation(t *testing.T) {
	r := NewRegistry()
	r.grace = 500 * time.Millisecond
	r.budget = time.Second

	s := r.Register(1, 0, func() {}, func() {})
	revoked := r.MarkRevoked(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.Await(ctx, revoked) }()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Await ignored a cancelled parent context")
	}
	if s.State() != StateRevoked {
		t.Fatalf("state = %q", s.State())
	}
}
