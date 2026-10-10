// Package gate provides a per-user, context-aware admission gate. HTTP JWT
// admission, terminal registration/admission and password updates for the same
// user run under the same gate, and the session version is read from the primary
// database inside the gate on every admission — there is no positive version
// cache (REQUIREMENTS §5.2/§5.3).
package gate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// DefaultBudget bounds the gate wait plus the version lookup together
// (REQUIREMENTS §5.3: 2 seconds).
const DefaultBudget = 2 * time.Second

// ErrUnavailable is returned when the version lookup fails or the budget is
// exhausted. Callers must reject admission with 503; they must never fall back
// to a cached version.
var ErrUnavailable = errors.New("gate: session version unavailable")

// VersionSource reads the current session version from the primary database.
type VersionSource func(ctx context.Context, userID uint) (int64, error)

// Gate serializes per-user admission and password updates.
type Gate struct {
	mu      sync.Mutex
	locks   map[uint]chan struct{}
	version VersionSource
}

// New creates a Gate backed by the given primary-database version source.
func New(version VersionSource) *Gate {
	return &Gate{locks: map[uint]chan struct{}{}, version: version}
}

// lockFor returns the stable per-user lock. Entries are never deleted, so a lock
// cannot be recreated while an operation still holds it.
func (g *Gate) lockFor(userID uint) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.locks[userID]
	if !ok {
		ch = make(chan struct{}, 1)
		g.locks[userID] = ch
	}
	return ch
}

// Admit runs fn while holding the user's gate, passing the freshly read session
// version. The version is read inside the gate, so it cannot be older than a
// concurrent password update's COMMIT. A version-lookup failure yields
// ErrUnavailable rather than an admission.
func (g *Gate) Admit(ctx context.Context, userID uint, fn func(version int64) error) error {
	if g == nil || g.version == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultBudget)
	defer cancel()

	lock := g.lockFor(userID)
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return ctx.Err()
	}

	version, err := g.version(ctx, userID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return fn(version)
}
