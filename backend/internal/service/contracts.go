package service

import (
	"context"
	"fmt"
	"time"

	"github.com/vpsmanager/backend/internal/gate"
)

// This file defines the shared contracts for SSH leases, terminal sessions and
// command cleanup. Compile-time assertions below keep the implementations
// aligned with these contracts.

// Generation identifies a pool entry's target snapshot (host, port, credential,
// updated_at). A lease may only be returned while its generation still matches
// the entry; a stale generation is discarded, never silently reused (REQ-06).
type Generation uint64

// StageClass classifies the failing stage of an SSH operation, matching the
// telemetry reason vocabulary.
type StageClass string

const (
	StageQuota      StageClass = "quota"
	StageDial       StageClass = "dial"
	StageHostKey    StageClass = "host_key"
	StageAuth       StageClass = "auth"
	StageSession    StageClass = "session"
	StageTransfer   StageClass = "transfer"
	StageRemoteExit StageClass = "remote_exit"
	StageTimeout    StageClass = "timeout"
	StageCanceled   StageClass = "canceled"
	StageProtocol   StageClass = "protocol"
	StageIntegrity  StageClass = "integrity"
	StagePermission StageClass = "permission"
	StageTeardown   StageClass = "teardown"
)

// StageError carries the failing stage so callers and telemetry can classify a
// failure without matching error strings.
type StageError struct {
	Stage StageClass
	Err   error
}

func (e *StageError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return string(e.Stage)
	}
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Lease is the target-pinned SSH handle contract (REQ-06). It may be reused for
// several primitives on the same target snapshot but never nests a borrow.
// Release and Discard are idempotent and mutually exclusive: exactly one of them
// returns the borrowed quota.
type Lease interface {
	Exec(ctx context.Context, command string, budget time.Duration) (*ExecResult, error)
	Upload(ctx context.Context, localPath, remotePath string, budget time.Duration) error
	Cancel(reason string)
	Done() <-chan struct{}
	// SettlementDeadline is the immutable absolute deadline by which this lease's
	// resource owner must have reclaimed the slot. A waiter that starts after the
	// operation must consume *this* deadline instead of starting a fresh cleanup
	// budget, otherwise a nested wait doubles the documented bound (N03).
	SettlementDeadline() time.Time
	ServerID() uint
	Generation() Generation
	Release()
	Discard()
}

// SessionState is the terminal session lifecycle.
type SessionState = gate.SessionState

const (
	SessionPreparing SessionState = gate.StatePreparing
	SessionReady     SessionState = gate.StateReady
	SessionRevoked   SessionState = gate.StateRevoked
)

// TerminalSession is the registry record for one JWT terminal.
type TerminalSession = gate.Session

// UserGate serializes admission and password revocation for one user id. The
// password transaction COMMIT is the revocation linearization point; the gate
// is stable for as long as the user has in-flight work.
type UserGate interface {
	Admit(ctx context.Context, userID uint, fn func(version int64) error) error
	Update(ctx context.Context, userID uint, fn func(context.Context, int64) (bool, error)) error
	Recover(ctx context.Context, userID uint) error
}

// TerminalRegistry tracks JWT terminal sessions so revocation can cancel and
// close them. Done is idempotent; only the session's own resources are closed.
type TerminalRegistry interface {
	Register(userID uint, version int64, cancel, force func()) *TerminalSession
	Admit(sessionID uint) bool
	InputPermit(sessionID uint) bool
	MarkRevoked(userID uint) []*TerminalSession
	Await(ctx context.Context, sessions []*TerminalSession) gate.CleanupResult
	Done(sessionID uint)
}

var (
	_ Lease            = (*sshLease)(nil)
	_ UserGate         = (*gate.Gate)(nil)
	_ TerminalRegistry = (*gate.Registry)(nil)
)
