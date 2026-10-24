package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/config"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/ssh"
)

// SSHService provides SSH command execution and connection management.
const maxExecTimeout = 300 * time.Second
const maxExecOutput = 8 << 20
const maxExecStreamOutput = 4 << 20

// settingsSnapshot copies this value. Do not add mutexes, atomic values, or
// other fields that cannot be copied; keep shared mutable state behind pointers.
type SSHService struct {
	runtimeConfig      *config.Runtime
	pool               *sshpool.Pool
	serverRepo         serverSource
	credSvc            *CredentialService
	sshDialTimeout     time.Duration
	execDefaultTimeout time.Duration
	// uploadTeardownGrace bounds the graceful close phase of an upload before the
	// transport is force-closed. Cancellation uses execTeardownGrace instead.
	uploadTeardownGrace time.Duration
	// outputLimit caps the total retained stdout+stderr bytes; outputStreamLimit
	// caps each stream. Beyond the limit bytes are still read and discarded, so
	// the remote never blocks on output backpressure.
	outputLimit       int64
	outputStreamLimit int64
	// leaseObserver receives the in-use gauge deltas of the SSH pool slots. The pool
	// owns the counting; this field only remembers the caller's observer so the
	// translation into the frozen consumer label lives in one place (P2-02).
	leaseObserver func(consumer LeaseConsumer, delta float64)
}

// NewSSHService creates an SSHService with the given dependencies.
func NewSSHService(pool *sshpool.Pool, serverRepo serverSource, credSvc *CredentialService, sshDialTimeout, execDefaultTimeout time.Duration) *SSHService {
	return &SSHService{
		pool:                pool,
		serverRepo:          serverRepo,
		credSvc:             credSvc,
		sshDialTimeout:      sshDialTimeout,
		execDefaultTimeout:  execDefaultTimeout,
		uploadTeardownGrace: sshCompletedTeardownGrace,
		outputLimit:         8 << 20,
		outputStreamLimit:   4 << 20,
	}
}

// SetOutputLimits sets the retained output caps (total and per stream).
// Non-positive values are ignored so the safe defaults remain in place.
func (s *SSHService) SetOutputLimits(total, perStream int64) {
	if total > 0 && total <= maxExecOutput {
		s.outputLimit = total
	}
	if perStream > 0 && perStream <= maxExecStreamOutput {
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

// LeaseConsumer identifies the layer that borrowed an SSH slot. It is the frozen
// `consumer` label of talus_ssh_leases_in_use, and it is optional at the call
// site so a non-production borrower (a test, tooling) cannot mislabel the
// measurement.
type LeaseConsumer string

const (
	LeaseConsumerExec     LeaseConsumer = "exec"
	LeaseConsumerTerminal LeaseConsumer = "terminal"
	LeaseConsumerAgent    LeaseConsumer = "agent"
	LeaseConsumerSFTP     LeaseConsumer = "sftp"
)

// SetLeaseObserver wires the shared concurrency gauge. The measurement belongs to
// whoever owns the slot - the pool - so this installs a pool observer that carries
// the caller's consumer label: the count follows the real reservation, covering the
// cached-connection probe and the dial, and falls only when the slot is returned
// (N03, P2-02). An unlabelled borrow measures nothing rather than the wrong layer.
func (s *SSHService) SetLeaseObserver(observer func(consumer LeaseConsumer, delta float64)) {
	s.leaseObserver = observer
	if s.pool == nil {
		return
	}
	s.pool.SetQuotaObserver(func(consumer string, delta int) {
		if observer == nil || consumer == "" {
			return
		}
		observer(LeaseConsumer(consumer), float64(delta))
	})
}

// Exec runs a command on a target server via SSH.
func (s *SSHService) Exec(ctx context.Context, serverID uint, command string, timeout time.Duration) (*ExecResult, error) {
	s = s.settingsSnapshot()
	if timeout <= 0 {
		timeout = s.execDefaultTimeout
	}
	if timeout > maxExecTimeout {
		timeout = maxExecTimeout
	}
	start := time.Now()
	operation, cancel := context.WithTimeoutCause(ctx, timeout, server.ErrSSHTimeout)
	defer cancel()
	lease, err := s.AcquireLease(operation, serverID, LeaseConsumerExec)
	if err != nil {
		if operation.Err() != nil {
			return nil, fmt.Errorf("acquire SSH lease: %w", context.Cause(operation))
		}
		return nil, err
	}
	defer lease.Release()
	result, err := lease.Exec(operation, command, timeout)
	err = errors.Join(err, lease.releaseAndWait())
	if result != nil {
		result.DurationMs = time.Since(start).Milliseconds()
	}
	return result, err
}

// leaseConsumer returns the first declared consumer, or "" when the borrower did
// not declare one (a test or tooling): an unattributed slot is measured by nobody
// rather than by the wrong label.
func leaseConsumer(consumers []LeaseConsumer) LeaseConsumer {
	if len(consumers) == 0 {
		return ""
	}
	return consumers[0]
}

const execTeardownGrace = 100 * time.Millisecond

// A completed transfer still needs the peer's EOF/CLOSE before its readers can
// join. Give healthy high-latency peers time to reply; cancellation stays fast.
const sshCompletedTeardownGrace = 2 * time.Second

var errSSHTransportClosed = fmt.Errorf("SSH transport force-closed during teardown: %w", server.ErrSSHConnection)

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
	grace := execTeardownGrace
	if completed && cancellation == nil {
		grace = sshCompletedTeardownGrace
	}
	transportClosed, cleanupErr := closeSSHSessionContext(ctx, client, ready, joined, sshTeardownDeadlines(ctx, grace))
	if cleanupErr != nil {
		if cancellation != nil {
			// Preserve the reason frozen before teardown. A worker may return a
			// generic context error after the server's own execution timeout.
			return nil, errors.Join(cancellation, cleanupErr), true
		}
		return nil, errors.Join(cancellation, runErr, cleanupErr), true
	}
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
		return context.Cause(parent)
	}
	return server.ErrSSHTimeout
}

// closeExecSession owns the session close. It joins both Run and Close before
// the caller reads output or releases its pool slot. A blocked channel close,
// NewSession, or unresponsive peer gets a bounded grace before its exclusively
// borrowed transport is closed; healthy channel cancellation keeps the client.
const sshCleanupBudget = 3 * time.Second

// sshCleanupPending means the response budget ended before all local workers
// joined. The lease remains occupied until done closes; it must never expose
// an in-use client to another borrower just to satisfy a response deadline.
type sshCleanupPending struct{ done <-chan struct{} }

func (*sshCleanupPending) Error() string { return "SSH cleanup exceeded its absolute 3s budget" }
func (*sshCleanupPending) Unwrap() error {
	// Cleanup is a server-side lifecycle failure, not caller cancellation. A
	// joined command timeout retains its original error and HTTP status.
	return server.NewAppErrorParams(http.StatusServiceUnavailable, server.ReasonUnavailable, map[string]any{"state": "ssh_cleanup_pending"})
}

// sshCleanupDeadlines is immutable and follows one teardown across all nested
// cleanup stages. Neither entering a helper nor finishing a worker restarts it.
type sshCleanupDeadlines struct{ grace, total time.Time }

type sshCleanupClockKey struct{}
type sshCleanupClock struct {
	once  sync.Once
	value sshCleanupDeadlines
}

func (c *sshCleanupClock) deadlines(grace time.Duration) sshCleanupDeadlines {
	c.once.Do(func() { c.value = newSSHCleanupDeadlines(time.Now(), grace) })
	return c.value
}

func sshTeardownDeadlines(ctx context.Context, grace time.Duration) sshCleanupDeadlines {
	if clock, ok := ctx.Value(sshCleanupClockKey{}).(*sshCleanupClock); ok {
		return clock.deadlines(grace)
	}
	return newSSHCleanupDeadlines(time.Now(), grace)
}

func newSSHCleanupDeadlines(start time.Time, grace time.Duration) sshCleanupDeadlines {
	return sshCleanupDeadlines{grace: start.Add(grace), total: start.Add(sshCleanupBudget)}
}

// closeSSHSession only consumes the caller's remaining cleanup budget.
func closeSSHSession(client *ssh.Client, ready <-chan *ssh.Session, joined <-chan struct{}, deadlines sshCleanupDeadlines) (bool, error) {
	return closeSSHSessionContext(context.Background(), client, ready, joined, deadlines)
}

func closeSSHSessionContext(ctx context.Context, client *ssh.Client, ready <-chan *ssh.Session, joined <-chan struct{}, deadlines sshCleanupDeadlines) (bool, error) {
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		var session *ssh.Session
		select {
		case session = <-ready:
		case <-joined:
			select {
			case session = <-ready:
			default:
			}
		}
		if session != nil {
			_ = session.Close()
		}
	}()
	allJoined := make(chan struct{})
	go func() { <-joined; <-closed; close(allJoined) }()
	grace := time.NewTimer(max(time.Until(deadlines.grace), 0))
	defer grace.Stop()
	cancelled := ctx.Done()
waiting:
	for {
		select {
		case <-allJoined:
			return false, nil
		case <-cancelled:
			// Cancellation during normal teardown must shorten the remaining
			// grace, without restarting either the grace or the total budget.
			cancelled = nil
			if time.Until(deadlines.grace) > execTeardownGrace {
				grace.Reset(execTeardownGrace)
			}
		case <-grace.C:
			break waiting
		}
	}
	select {
	case <-allJoined:
		return false, nil
	default:
	}
	transportJoined := make(chan struct{})
	go func() { _ = client.Close(); close(transportJoined) }()
	settled := make(chan struct{})
	go func() { <-allJoined; <-transportJoined; close(settled) }()
	remaining := time.NewTimer(max(time.Until(deadlines.total), 0))
	defer remaining.Stop()
	select {
	case <-settled:
		return true, nil
	case <-remaining.C:
		return true, &sshCleanupPending{done: settled}
	}
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

// CopyFile uploads a local artifact over SFTP on a verified SSH connection.
func (s *SSHService) CopyFile(ctx context.Context, serverID uint, localPath, remotePath string) error {
	s = s.settingsSnapshot()
	budget := min(s.execDefaultTimeout, maxExecTimeout)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	lease, err := s.AcquireLease(ctx, serverID, LeaseConsumerExec)
	if err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	defer lease.Release()
	err = lease.Upload(ctx, localPath, remotePath, budget)
	return errors.Join(err, lease.releaseAndWait())
}

// SetRuntimeConfig is wired before serving requests.
func (s *SSHService) SetRuntimeConfig(r *config.Runtime) { s.runtimeConfig = r }

func (s *SSHService) settingsSnapshot() *SSHService {
	if s.runtimeConfig == nil {
		return s
	}
	c := s.runtimeConfig.Current()
	snapshot := *s
	snapshot.runtimeConfig = nil
	snapshot.sshDialTimeout = time.Duration(c.SSHTimeout) * time.Second
	snapshot.execDefaultTimeout = time.Duration(c.ExecTimeout) * time.Second
	snapshot.outputLimit, snapshot.outputStreamLimit = c.ExecOutputLimit, c.ExecOutputStreamLimit
	return &snapshot
}
