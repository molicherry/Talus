package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
)

var errSSHLeaseUnusable = fmt.Errorf("SSH lease is closed or unusable: %w", server.ErrSSHConnection)

// sshLease owns one pinned connection until its owner explicitly releases it.
// Only one primitive may run at a time. Closing an active lease delays quota
// settlement until that primitive has joined all of its workers.
type sshLease struct {
	svc       *SSHService
	h         *sshpool.Handle
	mu        sync.Mutex
	active    bool
	closing   bool
	settled   bool
	unhealthy bool
	cancel    context.CancelCauseFunc
	cleanup   *sshCleanupClock
	done      chan struct{}
}

func newSSHLease(s *SSHService, h *sshpool.Handle) *sshLease {
	// The concurrency gauge is owned by the pool, which reserved the slot before the
	// probe: this lease must not count it a second time (P2-02).
	return &sshLease{svc: s, h: h, done: make(chan struct{})}
}

func (l *sshLease) client() *ssh.Client { return l.h.Client }

// ServerID reports the server this lease is pinned to, so telemetry from one
// operation can be correlated by node.
func (l *sshLease) ServerID() uint         { return l.h.ServerID() }
func (l *sshLease) Generation() Generation { return Generation(l.h.Generation()) }

// Release requests healthy return. It cannot make an active connection visible
// to another borrower. Done closes only after actual pool settlement.
func (l *sshLease) Release() { l.finish(false, nil) }
func (l *sshLease) Discard() { l.finish(true, fmt.Errorf("SSH lease discarded: %w", context.Canceled)) }
func (l *sshLease) Cancel(reason string) {
	l.finish(true, fmt.Errorf("%s: %w", reason, context.Canceled))
}
func (l *sshLease) Done() <-chan struct{} { return l.done }

func (l *sshLease) finish(discard bool, cause error) {
	l.mu.Lock()
	if l.settled {
		l.mu.Unlock()
		return
	}
	l.closing = true
	l.unhealthy = l.unhealthy || discard
	if cause != nil && l.cancel != nil {
		l.cancel(cause)
	}
	l.settleLocked()
	l.mu.Unlock()
}

func (l *sshLease) settleLocked() {
	if !l.closing || l.active || l.settled {
		return
	}
	l.settled = true
	// Pool settlement may close a transport (including a stale healthy lease).
	// Keep that work outside the lock and retain ownership until it finishes.
	discard := l.unhealthy
	go func() {
		if discard {
			l.svc.pool.DiscardHandle(l.h)
		} else {
			l.svc.pool.ReleaseHandle(l.h)
		}
		// The pool releases the slot exactly here, and reports it: the gauge follows
		// the real settlement rather than the caller's earlier foreground return
		// (N03), and a probe or dial that never produced a lease is still covered
		// because the reservation already counted (P2-02).
		close(l.done)
	}()
}

// SettlementDeadline reports the immutable absolute deadline of this lease's
// cleanup budget. It is the same deadline the resource owner uses for its own
// teardown wait, so a second waiter consumes the remainder instead of restarting
// the budget (N03).
func (l *sshLease) SettlementDeadline() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cleanupDeadlinesLocked().total
}

// cleanupDeadlinesLocked lazily anchors the cleanup clock. The first caller - the
// teardown path or the settlement waiter - starts the documented bound, and every
// later caller reads the same absolute deadline.
func (l *sshLease) cleanupDeadlinesLocked() sshCleanupDeadlines {
	if l.cleanup == nil {
		l.cleanup = &sshCleanupClock{}
	}
	return l.cleanup.deadlines(execTeardownGrace)
}

// releaseAndWait is used by one-shot operations. Settlement consumes the same
// absolute teardown budget as the operation; a pending owner retains the slot.
func (l *sshLease) releaseAndWait() error {
	l.Release()
	l.mu.Lock()
	deadline := l.cleanupDeadlinesLocked().total
	l.mu.Unlock()
	timer := time.NewTimer(max(time.Until(deadline), 0))
	defer timer.Stop()
	select {
	case <-l.done:
		return nil
	default:
	}
	select {
	case <-l.done:
		return nil
	case <-timer.C:
		return &sshCleanupPending{done: l.done}
	}
}

func (l *sshLease) begin(ctx context.Context) (context.Context, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing || l.settled || l.unhealthy {
		return nil, &StageError{Stage: StageSession, Err: errSSHLeaseUnusable}
	}
	// Admission is the short critical section that races with Invalidate: once the
	// pinned generation is stale no stage (probe, upload, hash check, activation,
	// execution) may start against the old target.
	if !l.svc.pool.IsCurrent(l.h.ServerID(), l.h.Generation()) {
		l.unhealthy = true
		return nil, fmt.Errorf("SSH lease target generation changed: %w", sshpool.ErrStaleGeneration)
	}
	if l.active {
		return nil, errors.New("SSH lease already has an active operation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, cancel := context.WithCancelCause(ctx)
	l.active, l.cancel = true, cancel
	// Deployment cleanup can carry the previous operation's absolute budget
	// through a replacement connection. Ordinary primitives get a new clock.
	l.cleanup, _ = ctx.Value(sshCleanupClockKey{}).(*sshCleanupClock)
	if l.cleanup == nil {
		l.cleanup = &sshCleanupClock{}
	}
	return context.WithValue(operation, sshCleanupClockKey{}, l.cleanup), nil
}

func (l *sshLease) end(unhealthy bool, err error) {
	var pending *sshCleanupPending
	if errors.As(err, &pending) {
		go func() { <-pending.done; l.end(true, nil) }()
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancel(nil)
	l.cancel = nil
	l.active = false
	l.unhealthy = l.unhealthy || unhealthy
	l.settleLocked()
}

// Exec and Upload preserve ownership across a deployment's sequential
// primitives. Their caller must eventually Release or Discard the lease.
func (l *sshLease) Exec(ctx context.Context, command string, budget time.Duration) (*ExecResult, error) {
	operation, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	if budget <= 0 {
		budget = l.svc.execDefaultTimeout
	}
	if budget > maxExecTimeout {
		budget = maxExecTimeout
	}
	result, err, transportClosed := l.svc.runCommand(operation, l.h.Client, command, budget)
	l.end(transportClosed || isConnectionError(err), err)
	return result, err
}

func (l *sshLease) Upload(ctx context.Context, localPath, remotePath string, budget time.Duration) error {
	return l.upload(ctx, localPath, remotePath, budget, true)
}

// uploadPrivateDirectory is reserved for a Windows deployment directory whose
// SYSTEM/Administrators-only ACL has already been applied. Windows OpenSSH
// does not implement POSIX chmod; ordinary uploads still require chmod(0600).
func (l *sshLease) uploadPrivateDirectory(ctx context.Context, localPath, remotePath string, budget time.Duration) error {
	return l.upload(ctx, localPath, remotePath, budget, false)
}

func (l *sshLease) upload(ctx context.Context, localPath, remotePath string, budget time.Duration, chmod bool) error {
	operation, err := l.begin(ctx)
	if err != nil {
		return err
	}
	if budget <= 0 {
		budget = l.svc.execDefaultTimeout
	}
	// Transfers honor their own budget and the caller's parent deadline; the
	// command execution ceiling must not silently shorten an artifact upload.
	err, transportClosed := l.svc.copyFileWithPermissions(operation, l.h.Client, localPath, remotePath, budget, chmod)
	l.end(transportClosed || isConnectionError(err), err)
	return err
}

// copyFileOnClient uploads an artifact over the same supervised SFTP channel
// as the file API, while retaining the deployment's existing SSH lease.
func (s *SSHService) copyFileOnClient(parent context.Context, client *ssh.Client, localPath, remotePath string, budget time.Duration) (error, bool) {
	return s.copyFileWithPermissions(parent, client, localPath, remotePath, budget, true)
}

func (s *SSHService) copyFileWithPermissions(parent context.Context, client *ssh.Client, localPath, remotePath string, budget time.Duration, chmod bool) (error, bool) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err, false
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat source: %w", err), false
	}
	if !info.Mode().IsRegular() {
		return errors.New("upload source must be a regular file"), false
	}
	source, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err), false
	}
	defer source.Close()
	// A caller cancellation also interrupts a pending local read before teardown.
	stop := context.AfterFunc(ctx, func() { _ = source.Close() })
	defer stop()
	err, closed := s.withSFTPClient(ctx, client, false, func(ctx context.Context, c *sftp.Client) error {
		remote, err := c.OpenFile(remotePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
		if err != nil {
			return err
		}
		defer remote.Close()
		// Agent artifacts stay private until hash verification and activation.
		if chmod {
			if err = remote.Chmod(0600); err != nil {
				return err
			}
		}
		// Request pipelining explicitly: io.CopyBuffer can select os.File.WriteTo,
		// which sends one small Write at a time and waits a full round trip for
		// every acknowledgement. The SFTP client's per-file request limit bounds
		// concurrency, and its reader joins all outstanding writes before return.
		// This requires a regular remote staging file with offset writes; FIFOs,
		// devices and SFTP servers limited to sequential writes are unsupported.
		count, err := remote.ReadFromWithConcurrency(source, 0)
		if err != nil {
			return err
		}
		if count != info.Size() {
			return SFTPError("sftp_size_mismatch")
		}
		if err = remote.Close(); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err != nil {
		return fmt.Errorf("copy file over SFTP: %w", err), closed
	}
	return nil, closed
}
