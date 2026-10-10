package service

import (
	"context"
	"fmt"
	"time"
)

// This file freezes the cross-requirement contracts agreed in
// REQUIREMENTS §5/§6/§7 and IMPLEMENTATION §3. Phase 2 implements them; the
// shapes live here so REQ-05/07/08 compile against a single definition.

// Generation identifies a pool entry's target snapshot (host, port, credential,
// updated_at). A lease may only be returned while its generation still matches
// the entry; a stale generation is discarded, never silently reused (REQ-06).
type Generation uint64

// StageClass classifies the failing stage of an SSH operation, matching the
// telemetry reason vocabulary (REQUIREMENTS §12.1).
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
	Generation() Generation
	Release()
	Discard()
}

// SessionState is the terminal session lifecycle (REQ-05 §5.2).
type SessionState string

const (
	SessionPreparing SessionState = "preparing"
	SessionReady     SessionState = "ready"
	SessionRevoked   SessionState = "revoked"
)

// TerminalSession is the registry record for one JWT terminal.
type TerminalSession struct {
	SessionID uint
	UserID    uint
	Version   int64
	State     SessionState
}

// UserGate serializes admission and password revocation for one user id. The
// password transaction COMMIT is the revocation linearization point; the gate
// is stable for as long as the user has in-flight work.
type UserGate interface {
	Admit(ctx context.Context, userID uint, fn func(version int64) error) error
}

// TerminalRegistry tracks JWT terminal sessions so revocation can cancel and
// close them. Done is idempotent; only the session's own resources are closed.
type TerminalRegistry interface {
	Register(ctx context.Context, s TerminalSession) error
	Admit(ctx context.Context, sessionID uint) error
	RevokeUser(ctx context.Context, userID uint, currentVersion int64)
	Done(sessionID uint)
}
