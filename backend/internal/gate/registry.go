package gate

import (
	"context"
	"sync"
	"time"
)

// SessionState is the terminal session lifecycle (REQUIREMENTS §5.2).
type SessionState string

const (
	StatePreparing SessionState = "preparing"
	StateReady     SessionState = "ready"
	StateRevoked   SessionState = "revoked"
)

// cleanupGrace is the graceful close window per session; cleanupBudget is the
// absolute total for revoking one user's sessions (REQ-05 §5.3: 2s + 1s = 3s).
const (
	cleanupGrace  = 2 * time.Second
	cleanupBudget = 3 * time.Second
)

// Exported mirrors of the frozen §7.1.1 cleanup contract, so callers and
// acceptance tests can pin the values without duplicating them.
const (
	CleanupGrace  = cleanupGrace
	CleanupBudget = cleanupBudget
)

// Session is one JWT terminal registered for revocation.
type Session struct {
	ID      uint
	UserID  uint
	Version int64

	cancel    func()
	force     func()
	done      chan struct{}
	once      sync.Once
	forceOnce sync.Once

	mu            sync.Mutex
	state         SessionState
	graceDeadline time.Time
	deadline      time.Time
}

// State returns the current state.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Done is closed once the session's local resources are reclaimed.
func (s *Session) Done() <-chan struct{} { return s.done }

// MarkDone is idempotent: the first call closes Done.
func (s *Session) MarkDone() { s.once.Do(func() { close(s.done) }) }

// Registry tracks JWT terminal sessions so a password change can revoke them.
type Registry struct {
	mu       sync.Mutex
	next     uint
	sessions map[uint]*Session
	// grace/budget bound Await; zero falls back to the package defaults so tests
	// can use short values.
	grace  time.Duration
	budget time.Duration
}

// NewRegistry creates an empty terminal registry.
func NewRegistry() *Registry {
	return &Registry{sessions: map[uint]*Session{}, grace: cleanupGrace, budget: cleanupBudget}
}

// Register creates a preparing session. cancel must stop input forwarding and
// close the WS/SSH session; force must close the transport if the graceful close
// does not finish in time. Either may be nil.
func (r *Registry) Register(userID uint, version int64, cancel, force func()) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	s := &Session{ID: r.next, UserID: userID, Version: version, cancel: cancel, force: force, done: make(chan struct{}), state: StatePreparing}
	r.sessions[s.ID] = s
	return s
}

// Admit moves a preparing session to ready, returning false when it was revoked
// (a late connection must not obtain a ready admission).
func (r *Registry) Admit(id uint) bool {
	r.mu.Lock()
	s := r.sessions[id]
	r.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StatePreparing {
		return false
	}
	s.state = StateReady
	return true
}

// Done removes a session after its resources are reclaimed. Idempotent.
func (r *Registry) Done(id uint) {
	r.mu.Lock()
	s := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if s != nil {
		s.MarkDone()
	}
}

// BeginCleanup freezes the first cleanup deadlines. A subsequent revoke or
// Await never grants the same session another grace window.
func (s *Session) BeginCleanup(start time.Time, grace, budget time.Duration) (time.Time, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deadline.IsZero() {
		s.graceDeadline = start.Add(grace)
		s.deadline = start.Add(budget)
	}
	return s.graceDeadline, s.deadline
}

// Force starts closing the exclusively owned transport at most once. Even a
// blocked transport Close cannot hold up Await's absolute budget. Completion is
// signalled only by the resource owner's Done, never by this dispatch.
func (s *Session) Force() {
	s.forceOnce.Do(func() {
		if s.force != nil {
			go s.force()
		}
	})
}

// InputPermit is the linearization point for each input/resize operation.
// MarkRevoked uses the same session lock, so no new permission is granted after
// revocation. A write already permitted may be interrupted by transport cleanup.
func (r *Registry) InputPermit(id uint) bool {
	r.mu.Lock()
	s := r.sessions[id]
	r.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StateReady
}

// MarkRevoked runs inside the user gate. It freezes one absolute cleanup budget
// for the entire batch, marks every session before signalling cancellation, and
// leaves the blocking join to Await outside the gate.
func (r *Registry) MarkRevoked(userID uint) []*Session {
	grace, budget := r.windows()
	started := time.Now()
	r.mu.Lock()
	var sessions []*Session
	for _, s := range r.sessions {
		if s.UserID == userID {
			sessions = append(sessions, s)
			s.mu.Lock()
			s.state = StateRevoked
			if s.deadline.IsZero() {
				s.graceDeadline, s.deadline = started.Add(grace), started.Add(budget)
			}
			s.mu.Unlock()
		}
	}
	r.mu.Unlock()
	for _, s := range sessions {
		if s.cancel != nil {
			s.cancel()
		}
	}
	return sessions
}

// CleanupResult distinguishes resource reclamation from an exhausted budget.
// Pending sessions stay registered until their actual owner reports Done.
type CleanupResult struct {
	Complete bool
	Forced   int
	Pending  int
}

func (r *Registry) windows() (time.Duration, time.Duration) {
	grace, budget := r.grace, r.budget
	if grace <= 0 {
		grace = cleanupGrace
	}
	if budget <= 0 {
		budget = cleanupBudget
	}
	return grace, budget
}

// Await is independent of the originating HTTP request once revocation has
// committed. Every session uses the deadlines frozen by MarkRevoked; delayed
// invocation, repeated calls and multiple sessions cannot restart that budget.
func (r *Registry) Await(_ context.Context, sessions []*Session) CleanupResult {
	grace, budget := r.windows()
	type settled struct{ complete, forced bool }
	results := make(chan settled, len(sessions))
	for _, s := range sessions {
		go func(s *Session) {
			graceAt, deadline := s.BeginCleanup(time.Now(), grace, budget)
			finished := waitClosed(context.Background(), s.done, time.Until(graceAt))
			forced := !finished
			if !finished {
				s.Force()
				finished = waitClosed(context.Background(), s.done, time.Until(deadline))
			}
			if finished {
				r.Done(s.ID)
			}
			results <- settled{finished, forced}
		}(s)
	}
	result := CleanupResult{Complete: true}
	for range sessions {
		r := <-results
		if r.forced {
			result.Forced++
		}
		if !r.complete {
			result.Pending++
			result.Complete = false
		}
	}
	return result
}

// RevokeUser marks and awaits in one call (convenience for callers without a
// gate). It returns the number of sessions revoked.
func (r *Registry) RevokeUser(ctx context.Context, userID uint) int {
	sessions := r.MarkRevoked(userID)
	r.Await(ctx, sessions)
	return len(sessions)
}

// LiveCount reports how many sessions are currently registered (for tests).
func (r *Registry) LiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

func waitClosed(ctx context.Context, done <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}
