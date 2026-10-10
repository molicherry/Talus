package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
)

// closedCh is a pre-closed channel used by no-op Done() implementations.
var closedCh = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// sshLease is the concrete Lease (contracts.go) over a pool Handle. It owns the
// borrowed quota for a pinned target generation; Release and Discard are
// idempotent and mutually exclusive.
type sshLease struct {
	svc  *SSHService
	h    *sshpool.Handle
	once sync.Once
}

func (l *sshLease) client() *ssh.Client { return l.h.Client }

// Generation reports the target generation this lease is pinned to.
func (l *sshLease) Generation() Generation { return Generation(l.h.Generation()) }

// Release returns a healthy connection to the pool (or closes it when the
// generation is stale) and frees the quota exactly once.
func (l *sshLease) Release() {
	l.once.Do(func() { l.svc.pool.ReleaseHandle(l.h) })
}

// Discard closes the connection without caching it and frees the quota exactly
// once.
func (l *sshLease) Discard() {
	l.once.Do(func() { l.svc.pool.DiscardHandle(l.h) })
}

// Cancel and Done are lease-scoped; per-operation cancellation is carried by the
// operation context. Later REQ-07 work wires the shared absolute deadline.
func (l *sshLease) Cancel(string)         {}
func (l *sshLease) Done() <-chan struct{} { return closedCh }

// Exec runs a command on the pinned client and always settles the quota.
func (l *sshLease) Exec(ctx context.Context, command string, budget time.Duration) (*ExecResult, error) {
	if budget <= 0 {
		budget = l.svc.execDefaultTimeout
	}
	result, err, transportClosed := l.svc.runCommand(ctx, l.h.Client, command, budget)
	if transportClosed || isConnectionError(err) {
		l.Discard()
	} else {
		l.Release()
	}
	return result, err
}

// Upload pipes a local file to a remote path on the pinned client and always
// settles the quota, including every error and cancellation path.
func (l *sshLease) Upload(ctx context.Context, localPath, remotePath string, budget time.Duration) error {
	err, transportClosed := l.svc.copyFileOnClient(ctx, l.h.Client, localPath, remotePath, budget)
	if transportClosed || isConnectionError(err) {
		l.Discard()
		return err
	}
	l.Release()
	return err
}

// copyFileOnClient streams localPath to remotePath over an exclusively owned
// client. It enforces a total budget (when budget > 0), joins both the copy and
// the remote command worker on every exit path, and never leaks a goroutine.
func (s *SSHService) copyFileOnClient(ctx context.Context, client *ssh.Client, localPath, remotePath string, budget time.Duration) (error, bool) {
	forceClosed := false
	grace := s.uploadTeardownGrace
	if grace <= 0 {
		grace = 100 * time.Millisecond
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err), false
	}
	defer f.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("create session: %w", err), false
	}

	pipe, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("stdin pipe: %w", err), false
	}

	copyDone := make(chan error, 1)
	go func() {
		_, perr := io.Copy(pipe, f)
		_ = pipe.Close()
		copyDone <- perr
	}()

	var stderr bytes.Buffer
	session.Stderr = &stderr
	cmd := fmt.Sprintf("cat > %s && chmod +x %s", remotePath, remotePath)

	runDone := make(chan error, 1)
	go func() { runDone <- session.Run(cmd) }()

	var timeout <-chan time.Time
	if budget > 0 {
		timer := time.NewTimer(budget)
		defer timer.Stop()
		timeout = timer.C
	}

	var runErr, copyErr error
	var cause error
	runOutstanding := true
	select {
	case runErr = <-runDone:
		runOutstanding = false
	case <-ctx.Done():
		cause = ctx.Err()
	case <-timeout:
		cause = context.DeadlineExceeded
	}

	// Settle with one shared deadline. The copy worker is the sole closer of the
	// pipe (closing it here would race an in-flight write). Each worker is joined
	// within the grace; if one stays stuck (a blocked TCP write, or a remote that
	// keeps the channel open), the transport is force-closed so it cannot leak.
	deadline := time.Now().Add(grace)
	if runOutstanding && !joinWithinErr(deadline, runDone, &runErr) {
		_ = client.Close()
		forceClosed = true
		runErr = <-runDone
	}
	if !joinWithinErr(deadline, copyDone, &copyErr) {
		_ = client.Close()
		forceClosed = true
		copyErr = <-copyDone
	}
	// The session close itself can block; it is supervised by the same deadline
	// so the total teardown never re-arms the grace.
	closeSessionBounded(session, client, deadline, &forceClosed)

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

// joinWithinErr waits for a worker channel until deadline, stores the received
// error, and reports whether it finished in time.
func joinWithinErr(deadline time.Time, ch <-chan error, out *error) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case *out = <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// closeSessionBounded closes the session under the remaining shared teardown
// budget (never a fresh grace) and force-closes the transport if the close
// itself blocks.
func closeSessionBounded(session *ssh.Session, client *ssh.Client, deadline time.Time, forceClosed *bool) {
	done := make(chan struct{})
	go func() {
		_ = session.Close()
		close(done)
	}()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		_ = client.Close()
		*forceClosed = true
		<-done
		return
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = client.Close()
		*forceClosed = true
		<-done
	}
}
