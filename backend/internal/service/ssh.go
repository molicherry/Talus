package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/ssh"
)

// SSHService provides SSH command execution and connection management.
type SSHService struct {
	pool               *sshpool.Pool
	serverRepo         serverSource
	credSvc            *CredentialService
	sshDialTimeout     time.Duration
	execDefaultTimeout time.Duration
}

// NewSSHService creates an SSHService with the given dependencies.
func NewSSHService(pool *sshpool.Pool, serverRepo serverSource, credSvc *CredentialService, sshDialTimeout, execDefaultTimeout time.Duration) *SSHService {
	return &SSHService{
		pool:               pool,
		serverRepo:         serverRepo,
		credSvc:            credSvc,
		sshDialTimeout:     sshDialTimeout,
		execDefaultTimeout: execDefaultTimeout,
	}
}

// ExecResult holds the output of a remote command execution.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

// Exec runs a command on a target server via SSH.
func (s *SSHService) Exec(ctx context.Context, serverID uint, command string, timeout time.Duration) (*ExecResult, error) {
	if timeout <= 0 {
		timeout = s.execDefaultTimeout
	}

	start := time.Now()

	lease, err := s.GetLease(ctx, serverID)
	if err != nil {
		return nil, err
	}

	client := lease.Client
	result, err, transportClosed := s.runCommand(ctx, client, command, timeout)
	duration := time.Since(start).Milliseconds()

	if result != nil {
		result.DurationMs = duration
	}

	// Never cache a connection whose transport failed — reuse would
	// immediately fail again on the next call.
	if transportClosed || isConnectionError(err) {
		lease.Discard()
	} else {
		lease.Release()
	}

	return result, err
}

const execTeardownGrace = 100 * time.Millisecond

// runCommand reports transport ownership separately from command outcome: a
// cancelled channel can leave a healthy connection available for the next call.
func (s *SSHService) runCommand(parent context.Context, client *ssh.Client, command string, timeout time.Duration) (*ExecResult, error, bool) {
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	done := make(chan error, 1)
	joined := make(chan struct{})
	ready := make(chan *ssh.Session, 1)
	go func() {
		defer close(joined)
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		session, err := client.NewSession()
		if err != nil {
			done <- err
			return
		}
		session.Stdout = &stdout
		session.Stderr = &stderr
		ready <- session
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		usage.FromContext(parent).SetPhase("ready")
		// Intentional remote shell execution: the exec route authenticates the
		// caller, requires servers:exec for API keys, and checks target access.
		// The caller supplies the complete command for the selected SSH server;
		// it is never executed by a local shell on the Talus host.
		// codeql[go/command-injection]
		runErr := session.Run(command)
		done <- runErr
	}()

	var runErr error
	var cancellation error
	completed := false
	select {
	case runErr = <-done:
		completed = true
	case <-ctx.Done():
		// Freeze the original reason before teardown: a later parent cancel
		// must not turn the command's own timeout into client cancellation.
		cancellation = execCancellation(parent)
		select {
		case runErr = <-done:
			completed = true
		default:
		}
	}
	if completed && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) && cancellation == nil {
		cancellation = execCancellation(parent)
	}
	transportClosed := closeExecSession(client, ready, joined)
	if !completed {
		// Run can publish a real exit while cancellation is closing its
		// channel. Read it after joining instead of losing that exit status.
		runErr = <-done
	}
	if runErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitErr.ExitStatus()}, nil, transportClosed
		}
		if cancellation != nil {
			return nil, fmt.Errorf("run command: %w", cancellation), transportClosed
		}
		return nil, fmt.Errorf("run command: %w", wrapSSHError(runErr)), transportClosed
	}
	// Run returns nil only after receiving a successful remote exit status.
	// That explicit fact also wins if closing the channel ended a hung stream;
	// a channel without exit-status instead returns ExitMissingError above.
	return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}, nil, transportClosed
}

func execCancellation(parent context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	return server.ErrSSHTimeout
}

// closeExecSession owns the session close. It joins both Run and Close before
// the caller reads output or releases its pool slot. A blocked channel close,
// NewSession, or unresponsive peer gets a bounded grace before its exclusively
// borrowed transport is closed; healthy channel cancellation keeps the client.
func closeExecSession(client *ssh.Client, ready <-chan *ssh.Session, joined <-chan struct{}) bool {
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		var session *ssh.Session
		select {
		case session = <-ready:
		case <-joined:
			// NewSession may have completed before the worker exited. Closing
			// that session still belongs to this supervisor.
			select {
			case session = <-ready:
			default:
			}
		}
		if session != nil {
			_ = session.Close()
		}
	}()

	timer := time.NewTimer(execTeardownGrace)
	defer timer.Stop()
	worker, closer := joined, (<-chan struct{})(closed)
	for worker != nil || closer != nil {
		select {
		case <-worker:
			worker = nil
		case <-closer:
			closer = nil
		case <-timer.C:
			// Prefer completed cleanup if it raced the grace deadline.
			select {
			case <-worker:
				worker = nil
			default:
			}
			select {
			case <-closer:
				closer = nil
			default:
			}
			if worker == nil && closer == nil {
				return false
			}
			_ = client.Close()
			if worker != nil {
				<-worker
			}
			if closer != nil {
				<-closer
			}
			return true
		}
	}
	return false
}

// buildAuthMethod creates the appropriate ssh.AuthMethod from decrypted credentials.
func buildAuthMethod(password, privateKey string) (ssh.AuthMethod, error) {
	if password != "" {
		return ssh.Password(password), nil
	}
	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	}
	return nil, fmt.Errorf("no credential available")
}

// wrapSSHError classifies SSH errors into application-level sentinels.
func wrapSSHError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "no supported methods remain") {
		return fmt.Errorf("%w: %v", server.ErrSSHAuth, err)
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "no route to host") || strings.Contains(msg, "i/o timeout") {
		return fmt.Errorf("%w: %v", server.ErrSSHConnection, err)
	}
	return fmt.Errorf("%w: %v", server.ErrSSHConnection, err)
}

// isConnectionError reports whether err indicates the SSH transport itself
// failed (as opposed to the remote command failing with a non-zero exit).
// Connections failing in this way must not be returned to the pool.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, server.ErrSSHConnection) {
		return true
	}
	msg := err.Error()
	for _, frag := range []string{
		"connection closed",
		"connection reset",
		"broken pipe",
		"i/o timeout",
		"EOF",
		"network is unreachable",
		"no route to host",
		"connection refused",
		"unable to authenticate",
	} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// CopyFile pipes a local file to a remote path over SSH.
func (s *SSHService) CopyFile(ctx context.Context, serverID uint, localPath, remotePath string) (err error) {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	lease, err := s.GetLease(ctx, serverID)
	if err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	defer func() {
		if err != nil {
			lease.Discard()
		} else {
			lease.Release()
		}
	}()
	client := lease.Client
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = client.Close(); close(cancelled) })
	defer func() {
		if !stop() {
			<-cancelled
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	defer session.Close()
	pipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	copied := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(pipe, f)
		closeErr := pipe.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		copied <- copyErr
	}()
	var stderr bytes.Buffer
	session.Stderr = &stderr
	quotedPath := "'" + strings.ReplaceAll(remotePath, "'", "'\"'\"'") + "'"
	cmd := fmt.Sprintf("cat > %s && chmod +x %s", quotedPath, quotedPath)
	runErr := session.Run(cmd)
	if runErr != nil {
		_ = client.Close()
	}
	copyErr := <-copied
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil {
		return fmt.Errorf("copy failed: %w (stderr: %s)", runErr, stderr.String())
	}
	if copyErr != nil {
		return fmt.Errorf("copy source: %w", copyErr)
	}
	return nil
}
