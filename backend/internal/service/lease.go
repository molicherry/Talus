package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
)

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
	return &sshLease{svc: s, h: h, done: make(chan struct{})}
}

func (l *sshLease) client() *ssh.Client    { return l.h.Client }
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
		close(l.done)
	}()
}

// releaseAndWait is used by one-shot operations. Settlement consumes the same
// absolute teardown budget as the operation; a pending owner retains the slot.
func (l *sshLease) releaseAndWait() error {
	l.Release()
	l.mu.Lock()
	clock := l.cleanup
	if clock == nil {
		clock = &sshCleanupClock{}
		l.cleanup = clock
	}
	l.mu.Unlock()
	deadline := clock.deadlines(execTeardownGrace).total
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
		return nil, errors.New("SSH lease is closed or unusable")
	}
	if l.active {
		return nil, errors.New("SSH lease already has an active operation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, cancel := context.WithCancelCause(ctx)
	l.active, l.cancel = true, cancel
	l.cleanup = &sshCleanupClock{}
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
	operation, err := l.begin(ctx)
	if err != nil {
		return err
	}
	if budget <= 0 {
		budget = l.svc.execDefaultTimeout
	}
	if budget > maxExecTimeout {
		budget = maxExecTimeout
	}
	err, transportClosed := l.svc.copyFileOnClient(operation, l.h.Client, localPath, remotePath, budget)
	l.end(transportClosed || isConnectionError(err), err)
	return err
}

// copyFileOnClient streams localPath to remotePath over an exclusively owned
// client. Foreground cleanup has one absolute deadline. If workers outlive it,
// the lease retains ownership in a background finalizer until they join.
func (s *SSHService) copyFileOnClient(parent context.Context, client *ssh.Client, localPath, remotePath string, budget time.Duration) (error, bool) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err, false
	}
	// Uploads accept regular artifacts only. In particular, opening a FIFO before
	// cancellation supervision could block indefinitely in the local filesystem.
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat source: %w", err), false
	}
	if !info.Mode().IsRegular() {
		return errors.New("upload source must be a regular file"), false
	}
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err), false
	}
	defer f.Close()

	var stderrMu sync.Mutex
	var stderrTotal int64
	stderr := newCappedWriter(&stderrMu, &stderrTotal, maxExecOutput, maxExecStreamOutput)
	var copyErr error
	ready := make(chan *ssh.Session, 1)
	runDone := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		if err := ctx.Err(); err != nil {
			runDone <- err
			return
		}
		session, err := client.NewSession()
		if err != nil {
			runDone <- fmt.Errorf("create session: %w", err)
			return
		}
		ready <- session
		if err := ctx.Err(); err != nil {
			runDone <- err
			return
		}
		pipe, err := session.StdinPipe()
		if err != nil {
			runDone <- fmt.Errorf("stdin pipe: %w", err)
			return
		}
		copyDone := make(chan struct{})
		go func() {
			defer close(copyDone)
			_, copyErr = io.Copy(pipe, f)
			_ = pipe.Close()
		}()
		session.Stderr = stderr
		cmd := fmt.Sprintf("cat > %s && chmod +x %s", remotePath, remotePath)
		runDone <- session.Run(cmd)
		<-copyDone
	}()

	var runErr, cause error
	completed := false
	select {
	case runErr = <-runDone:
		completed = true
	case <-ctx.Done():
		cause = context.Cause(ctx)
	}
	// NewSession, stdin transfer, Run and Close are all supervised. Closing the
	// local source also interrupts pending reads before joining the copy worker.
	_ = f.Close()
	grace := s.uploadTeardownGrace
	if grace <= 0 || grace > execTeardownGrace {
		grace = execTeardownGrace
	}
	forceClosed, cleanupErr := closeSSHSession(client, ready, joined, sshTeardownDeadlines(ctx, grace))
	if cleanupErr != nil {
		return errors.Join(cause, runErr, cleanupErr), true
	}
	if !completed {
		runErr = <-runDone
	}
	if cause != nil {
		return cause, forceClosed
	}
	if runErr != nil {
		return fmt.Errorf("copy failed: %w (stderr: %s)", runErr, stderr.String()), forceClosed
	}
	if copyErr != nil {
		return fmt.Errorf("copy file: %w", copyErr), forceClosed
	}
	return nil, forceClosed
}
