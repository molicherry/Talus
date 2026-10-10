package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// serverSource is the subset of repository.ServerRepo the SSH service needs.
// An interface keeps the dial path testable without a database.
type serverSource interface {
	FindByID(ctx context.Context, id uint) (*model.Server, error)
	SetHostKeyIfUnchanged(ctx context.Context, id uint, host string, port int, hostKey []byte) (bool, error)
	RecordHostKeyMismatch(ctx context.Context, id uint, host string, port int, seen []byte) (bool, error)
	ClearHostKeyMismatch(ctx context.Context, id uint) error
}

// SSHService provides SSH command execution and connection management.
type SSHService struct {
	pool               *sshpool.Pool
	serverRepo         serverSource
	credSvc            *CredentialService
	sshDialTimeout     time.Duration
	execDefaultTimeout time.Duration
	// uploadTeardownGrace bounds the graceful close phase of an upload before the
	// transport is force-closed (REQUIREMENTS §7.1.1: 100ms).
	uploadTeardownGrace time.Duration
	// outputLimit caps the total retained stdout+stderr bytes; outputStreamLimit
	// caps each stream. Beyond the limit bytes are still read and discarded, so
	// the remote never blocks on output backpressure (REQUIREMENTS §7.1.2).
	outputLimit       int64
	outputStreamLimit int64
}

// NewSSHService creates an SSHService with the given dependencies.
func NewSSHService(pool *sshpool.Pool, serverRepo serverSource, credSvc *CredentialService, sshDialTimeout, execDefaultTimeout time.Duration) *SSHService {
	return &SSHService{
		pool:                pool,
		serverRepo:          serverRepo,
		credSvc:             credSvc,
		sshDialTimeout:      sshDialTimeout,
		execDefaultTimeout:  execDefaultTimeout,
		uploadTeardownGrace: 100 * time.Millisecond,
		outputLimit:         8 << 20,
		outputStreamLimit:   4 << 20,
	}
}

// SetOutputLimits sets the retained output caps (total and per stream).
// Non-positive values are ignored so the safe defaults remain in place.
func (s *SSHService) SetOutputLimits(total, perStream int64) {
	if total > 0 {
		s.outputLimit = total
	}
	if perStream > 0 {
		s.outputStreamLimit = perStream
	}
}

// ExecResult holds the output of a remote command execution.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	// Truncation metadata. A stream is only marked truncated when bytes were
	// actually discarded; reaching the limit exactly is not truncation.
	OutputTruncated bool  `json:"output_truncated"`
	StdoutTruncated bool  `json:"stdout_truncated"`
	StderrTruncated bool  `json:"stderr_truncated"`
	StdoutRetained  int64 `json:"stdout_retained_bytes"`
	StderrRetained  int64 `json:"stderr_retained_bytes"`
	StdoutDiscarded int64 `json:"stdout_discarded_bytes"`
	StderrDiscarded int64 `json:"stderr_discarded_bytes"`
}

// Exec runs a command on a target server via SSH.
func (s *SSHService) Exec(ctx context.Context, serverID uint, command string, timeout time.Duration) (*ExecResult, error) {
	if timeout <= 0 {
		timeout = s.execDefaultTimeout
	}
	start := time.Now()
	lease, err := s.AcquireLease(ctx, serverID)
	if err != nil {
		return nil, err
	}

	result, err := lease.Exec(ctx, command, timeout)
	if result != nil {
		result.DurationMs = time.Since(start).Milliseconds()
	}
	return result, err
}

// AcquireLease borrows a concurrency slot and returns a target-pinned lease,
// dialing a new connection when no healthy cached client exists. The lease owns
// the quota: callers must Release or Discard exactly once.
func (s *SSHService) AcquireLease(ctx context.Context, serverID uint) (*sshLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Always read current server state, even on a cache hit: a pooled client is
	// only valid for the host/port/credential it was dialed with, and a deleted
	// server must not stay reachable through a stale connection.
	srv, err := s.serverRepo.FindByID(ctx, serverID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, server.ErrNotFound)
		}
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	usage.FromContext(ctx).SetResource("server", &srv.ID, srv.Name, &srv.ID)

	h, err := s.pool.AcquireContext(ctx, serverID, serverFingerprint(srv))
	if err != nil {
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, err)
	}
	if err := ctx.Err(); err != nil {
		// AcquireContext already validated the connection. Cancellation before
		// any session uses it does not make its transport unhealthy.
		s.pool.ReleaseHandle(h)
		return nil, err
	}
	if h.Client != nil {
		return &sshLease{svc: s, h: h}, nil
	}

	// Pool returned nil — dial a new connection.
	if srv.CredentialID == nil {
		s.pool.DiscardHandle(h)
		return nil, fmt.Errorf("get ssh client for server %d: no credential configured", serverID)
	}

	username, password, privateKey, err := s.credSvc.GetDecryptedByID(ctx, *srv.CredentialID)
	if err != nil {
		s.pool.DiscardHandle(h)
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, err)
	}

	authMethod, err := buildAuthMethod(password, privateKey)
	if err != nil {
		s.pool.DiscardHandle(h)
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, server.ErrInternal)
	}

	var knownHostKey []byte
	if srv.HostKey != nil {
		knownHostKey = *srv.HostKey
	}

	client, capturedKey, err := sshpool.DialSSHContext(ctx, srv.Host, srv.Port, username, authMethod, knownHostKey, s.sshDialTimeout)
	if contextErr := ctx.Err(); contextErr != nil {
		if client != nil {
			_ = client.Close()
		}
		s.pool.DiscardHandle(h)
		return nil, contextErr
	}
	if err != nil {
		s.pool.DiscardHandle(h)
		// A host key change is fail-closed but must not be silent: record the
		// presented key so the UI can show its fingerprint and let the operator
		// decide whether to trust it.
		var mismatch *sshpool.HostKeyMismatchError
		if errors.As(err, &mismatch) {
			if updated, recErr := s.serverRepo.RecordHostKeyMismatch(ctx, srv.ID, srv.Host, srv.Port, mismatch.Presented); recErr != nil {
				slog.Warn("failed to record host key mismatch", "server_id", serverID, "error", recErr)
			} else if !updated {
				slog.Warn("host key mismatch not recorded: server changed during dial", "server_id", serverID)
			}
			return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, server.ErrSSHHostKeyMismatch)
		}
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, wrapSSHError(err))
	}

	// Persist the host key on first connection. A conditional update (keyed on
	// the host/port that was dialed) avoids a full-row save reverting a
	// concurrent edit — the row was read before the dial, which can take seconds.
	if len(knownHostKey) == 0 && len(capturedKey) > 0 {
		updated, updateErr := s.serverRepo.SetHostKeyIfUnchanged(ctx, srv.ID, srv.Host, srv.Port, capturedKey)
		if updateErr != nil {
			slog.Warn("failed to persist host key", "server_id", serverID, "error", updateErr)
		} else if !updated {
			slog.Warn("host key not persisted: server changed during dial", "server_id", serverID)
		}
	}
	// The connection succeeds, so any pending mismatch is resolved.
	if srv.HostKeyMismatchAt != nil {
		if clrErr := s.serverRepo.ClearHostKeyMismatch(ctx, srv.ID); clrErr != nil {
			slog.Warn("failed to clear host key mismatch", "server_id", serverID, "error", clrErr)
		}
	}

	h.Client = client
	return &sshLease{svc: s, h: h}, nil
}

// serverFingerprint identifies everything a pooled SSH connection depends on.
// Two calls with the same fingerprint may reuse a connection; any change (host,
// port, credential id or credential revision) must miss the cache.
func serverFingerprint(srv *model.Server) string {
	credential := "none"
	if srv.CredentialID != nil {
		if srv.Credential != nil {
			credential = fmt.Sprintf("%d@%d", *srv.CredentialID, srv.Credential.UpdatedAt.UnixNano())
		} else {
			credential = fmt.Sprintf("%d@missing", *srv.CredentialID)
		}
	}
	return fmt.Sprintf("%s:%d:%s", srv.Host, srv.Port, credential)
}

const execTeardownGrace = 100 * time.Millisecond

// runCommand reports transport ownership separately from command outcome: a
// cancelled channel can leave a healthy connection available for the next call.
func (s *SSHService) runCommand(parent context.Context, client *ssh.Client, command string, timeout time.Duration) (*ExecResult, error, bool) {
	total := int64(0)
	var outMu sync.Mutex
	totalLimit, streamLimit := s.outputLimit, s.outputStreamLimit
	if totalLimit <= 0 {
		totalLimit = 8 << 20
	}
	if streamLimit <= 0 {
		streamLimit = 4 << 20
	}
	stdout := newCappedWriter(&outMu, &total, totalLimit, streamLimit)
	stderr := newCappedWriter(&outMu, &total, totalLimit, streamLimit)
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
		session.Stdout = stdout
		session.Stderr = stderr
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
			return execResult(stdout, stderr, exitErr.ExitStatus()), nil, transportClosed
		}
		if cancellation != nil {
			return nil, fmt.Errorf("run command: %w", cancellation), transportClosed
		}
		return nil, fmt.Errorf("run command: %w", wrapSSHError(runErr)), transportClosed
	}
	// Run returns nil only after receiving a successful remote exit status.
	// That explicit fact also wins if closing the channel ended a hung stream;
	// a channel without exit-status instead returns ExitMissingError above.
	return execResult(stdout, stderr, 0), nil, transportClosed
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
func (s *SSHService) CopyFile(ctx context.Context, serverID uint, localPath, remotePath string) error {
	lease, err := s.AcquireLease(ctx, serverID)
	if err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return lease.Upload(ctx, localPath, remotePath, s.execDefaultTimeout)
}
