// Package gate serializes per-user admission, terminal registration and password
// changes. Version reads use the primary database and never a positive cache.
// API key permissions reuse separate Gate and Registry instances: IDs in those
// instances identify keys, never their owners, so the namespaces cannot collide.
package gate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const DefaultBudget = 2 * time.Second

// ErrUnavailable means admission must fail closed with HTTP 503.
var ErrUnavailable = errors.New("gate: session version unavailable")

type VersionSource func(ctx context.Context, userID uint) (int64, error)

type userState struct {
	lock chan struct{}
	// These fields are protected by lock, not Gate.mu.
	initialized bool
	pending     bool
}

type Gate struct {
	mu       sync.Mutex
	users    map[uint]*userState
	version  VersionSource
	recovery VersionSource
}

// New accepts an optional recovery source for transactional databases. Production
// wiring must supply a primary FOR UPDATE read: an ordinary MVCC read cannot
// exclude an earlier password transaction that is still committing. The one-
// source form is useful for nontransactional sources and test doubles.
func New(version VersionSource, recovery ...VersionSource) *Gate {
	resolve := version
	if len(recovery) > 0 {
		resolve = recovery[0]
	}
	return &Gate{users: map[uint]*userState{}, version: version, recovery: resolve}
}

func (g *Gate) stateFor(userID uint) *userState {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.users[userID]
	if state == nil {
		state = &userState{lock: make(chan struct{}, 1)}
		g.users[userID] = state
	}
	return state
}

// withUser bounds lock acquisition and the version read with the same budget.
// The first read after startup and each read while fenced waits for a row lock,
// so process restart cannot accidentally admit a token from an unresolved commit.
func (g *Gate) withUser(ctx context.Context, userID uint, fn func(context.Context, *userState, int64) error) error {
	if g == nil || g.version == nil || g.recovery == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultBudget)
	defer cancel()
	state := g.stateFor(userID)
	select {
	case state.lock <- struct{}{}:
		defer func() { <-state.lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	source := g.version
	if !state.initialized || state.pending {
		source = g.recovery
	}
	version, err := source(ctx, userID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	state.initialized, state.pending = true, false
	return fn(ctx, state, version)
}

// Admit validates and performs admission inside one critical section.
func (g *Gate) Admit(ctx context.Context, userID uint, fn func(version int64) error) error {
	return g.withUser(ctx, userID, func(_ context.Context, _ *userState, version int64) error { return fn(version) })
}

// Update executes a password mutation and installs its uncertainty fence before
// unlocking. The callback marks registered terminals revoked in this same
// critical section. A known rollback returns uncertain=false and does not fence.
func (g *Gate) Update(ctx context.Context, userID uint, fn func(context.Context, int64) (uncertain bool, err error)) error {
	return g.withUser(ctx, userID, func(ctx context.Context, state *userState, version int64) error {
		uncertain, err := fn(ctx, version)
		if uncertain {
			state.pending = true
		}
		return err
	})
}

// Recover performs a bounded reconciliation attempt. Failure retains the fence;
// later admissions retry the locking read, never the ordinary MVCC source.
func (g *Gate) Recover(ctx context.Context, userID uint) error {
	return g.Admit(ctx, userID, func(int64) error { return nil })
}
