package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// withSFTPClient opens a subsystem on the caller's pinned SSH transport. It does
// not acquire another pool slot: deployments retain one route and generation
// across probe, SFTP upload, verification and activation. API file operations
// use the same supervisor for initialization, cancellation and bounded teardown.
func (s *SSHService) withSFTPClient(ctx context.Context, transport *ssh.Client, metadata bool, fn func(context.Context, *sftp.Client) error) (error, bool) {
	grace := s.uploadTeardownGrace
	if grace <= 0 || grace > sshCompletedTeardownGrace {
		grace = sshCompletedTeardownGrace
	}
	ready := make(chan *ssh.Session, 1)
	joined := make(chan struct{})
	result := make(chan error, 1)
	initialized := make(chan struct{})
	go func() {
		defer close(joined)
		session, err := transport.NewSession()
		if err != nil {
			result <- err
			return
		}
		ready <- session
		defer session.Close()
		input, err := session.StdinPipe()
		if err != nil {
			result <- err
			return
		}
		output, err := session.StdoutPipe()
		if err != nil {
			result <- err
			return
		}
		stderr, err := session.StderrPipe()
		if err != nil {
			result <- err
			return
		}
		drained := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, stderr); close(drained) }()
		// Join stderr after the subsystem/session has closed.
		defer func() { _ = session.Close(); <-drained }()
		if err = session.RequestSubsystem("sftp"); err != nil {
			result <- SFTPError("sftp_unavailable")
			return
		}

		bounded := &sftpBoundedReader{source: output}
		bounded.remaining.Store(-1)
		if metadata {
			bounded.remaining.Store(sftpDirectoryWireLimit)
		}
		workerContext := context.WithValue(ctx, sftpWireContextKey{}, bounded)
		var reader io.Reader = bounded
		client, err := sftp.NewClientPipe(reader, input, sftp.UseConcurrentReads(false))
		if err != nil {
			result <- err
			return
		}
		close(initialized)
		if ctx.Err() != nil {
			result <- context.Cause(ctx)
			_ = client.Close()
			return
		}
		err = fn(workerContext, client)
		if err != nil && bounded.remaining.Load() == 0 {
			err = SFTPError("sftp_directory_too_large")
		}
		result <- err
		_ = client.Close()
	}()
	var runErr error
	var deadlines sshCleanupDeadlines
	select {
	case runErr = <-result:
	case <-ctx.Done():
		runErr = context.Cause(ctx)
		grace = execTeardownGrace
		deadlines = sshTeardownDeadlines(ctx, grace)
		select {
		case <-initialized:
			// Body cancellation can let a running transfer remove its temporary
			// file. Reserve half the grace for channel close afterwards.
			timer := time.NewTimer(max(time.Until(deadlines.grace)/2, 0))
			select {
			case <-result:
			case <-timer.C:
			}
			timer.Stop()
		default:
			// Initialization has no callback to clean up. Close its channel now
			// so a missing SFTP VERSION reply can join during the full grace.
		}
	}
	if ctx.Err() != nil {
		runErr = context.Cause(ctx)
		grace = execTeardownGrace
	}
	if deadlines.total.IsZero() {
		deadlines = sshTeardownDeadlines(ctx, grace)
	}
	forced, cleanupErr := closeSSHSessionContext(ctx, transport, ready, joined, deadlines)
	if ctx.Err() != nil {
		runErr = context.Cause(ctx)
	}
	if forced && runErr == nil && cleanupErr == nil {
		// A multi-step caller must not observe success with a dead lease.
		cleanupErr = &StageError{Stage: StageTeardown, Err: errSSHTransportClosed}
	}

	return errors.Join(runErr, cleanupErr), forced || cleanupErr != nil
}
