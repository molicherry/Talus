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

// Session is one JWT terminal registered for revocation.
type Session struct {
	ID      uint
	UserID  uint
	Version int64

	cancel func()
	force  func()
	done   chan struct{}
	once   sync.Once

	mu    sync.Mutex
	state SessionState
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

// MarkRevoked marks every live session of the user revoked and signals
// cancellation. It must be called while holding the user gate, before the gate
// is released; the blocking join happens in Await, outside the gate.
func (r *Registry) MarkRevoked(userID uint) []*Session {
	r.mu.Lock()
	var sessions []*Session
	for _, s := range r.sessions {
		if s.UserID == userID {
			sessions = append(sessions, s)
		}
	}
	r.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		if s.state != StateRevoked {
			s.state = StateRevoked
		}
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
	}
	return sessions
}

// Await waits for the given sessions to finish within the total cleanup budget;
// a session that does not finish in its graceful window has its transport
// force-closed. It must run outside the gate.
func (r *Registry) Await(ctx context.Context, sessions []*Session) {
	budget := r.budget
	if budget <= 0 {
		budget = cleanupBudget
	}
	graceWindow := r.grace
	if graceWindow <= 0 {
		graceWindow = cleanupGrace
	}
	deadline := time.Now().Add(budget)
	for _, s := range sessions {
		grace := time.Until(deadline)
		if grace > graceWindow {
			grace = graceWindow
		}
		finished := waitClosed(ctx, s.done, grace)
		if !finished {
			if s.force != nil {
				s.force()
			}
			remaining := time.Until(deadline)
			if remaining > 0 {
				finished = waitClosed(ctx, s.done, remaining)
			}
		}
		if finished {
			// Done is idempotent, so the owning session may still call it. This
			// guarantees the registry does not retain a revoked session.
			r.Done(s.ID)
		}
	}
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
