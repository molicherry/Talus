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
	if grace <= 0 || grace > execTeardownGrace {
		grace = execTeardownGrace
	}
	ready := make(chan *ssh.Session, 1)
	joined := make(chan struct{})
	result := make(chan error, 1)
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
		err = fn(workerContext, client)
		if err != nil && bounded.remaining.Load() == 0 {
			err = SFTPError("sftp_directory_too_large")
		}
		result <- err
		_ = client.Close()
	}()
	var runErr error
	select {
	case runErr = <-result:
	case <-ctx.Done():
		runErr = context.Cause(ctx)
		// HTTP body cancellation can unblock a transfer, allowing its worker to
		// remove a temporary file before its SFTP channel is forced closed.
		timer := time.NewTimer(grace)
		select {
		case <-result:
		case <-timer.C:
		}
		timer.Stop()
	}
	if ctx.Err() != nil {
		runErr = context.Cause(ctx)
	}
	forced, cleanupErr := closeSSHSession(transport, ready, joined, sshTeardownDeadlines(ctx, grace))

	return errors.Join(runErr, cleanupErr), forced || cleanupErr != nil
}
