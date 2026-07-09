package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// SSHService provides SSH command execution and connection management.
type SSHService struct {
	pool              *sshpool.Pool
	serverRepo        *repository.ServerRepo
	credSvc           *CredentialService
	sshDialTimeout    time.Duration
	execDefaultTimeout time.Duration
}

// NewSSHService creates an SSHService with the given dependencies.
func NewSSHService(pool *sshpool.Pool, serverRepo *repository.ServerRepo, credSvc *CredentialService, sshDialTimeout, execDefaultTimeout time.Duration) *SSHService {
	return &SSHService{
		pool:              pool,
		serverRepo:        serverRepo,
		credSvc:           credSvc,
		sshDialTimeout:    sshDialTimeout,
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

	client, err := s.GetClient(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer s.pool.Release(serverID, client)

	result, err := s.runCommand(client, command, timeout)
	duration := time.Since(start).Milliseconds()

	if result != nil {
		result.DurationMs = duration
	}

	return result, err
}

// GetClient returns a pooled SSH client for the given server, dialing a new
// connection if none is cached. The caller must call pool.Release when done.
func (s *SSHService) GetClient(ctx context.Context, serverID uint) (*ssh.Client, error) {
	client := s.pool.Get(serverID)
	if client != nil {
		return client, nil
	}

	// Pool returned nil — dial a new connection.
	srv, err := s.serverRepo.FindByID(ctx, serverID)
	if err != nil {
		s.pool.Release(serverID, nil)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, server.ErrNotFound)
		}
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, err)
	}

	if srv.CredentialID == nil {
		s.pool.Release(serverID, nil)
		return nil, fmt.Errorf("get ssh client for server %d: no credential configured", serverID)
	}

	username, password, privateKey, err := s.credSvc.GetDecryptedByID(ctx, *srv.CredentialID)
	if err != nil {
		s.pool.Release(serverID, nil)
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, err)
	}

	authMethod, err := buildAuthMethod(password, privateKey)
	if err != nil {
		s.pool.Release(serverID, nil)
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, server.ErrInternal)
	}

	var knownHostKey []byte
	if srv.HostKey != nil {
		knownHostKey = *srv.HostKey
	}

	client, capturedKey, err := sshpool.DialSSH(srv.Host, srv.Port, username, authMethod, knownHostKey, s.sshDialTimeout)
	if err != nil {
		s.pool.Release(serverID, nil)
		return nil, fmt.Errorf("get ssh client for server %d: %w", serverID, wrapSSHError(err))
	}

	// Save host key on first connection.
	if len(knownHostKey) == 0 && len(capturedKey) > 0 {
		srv.HostKey = &capturedKey
		if updateErr := s.serverRepo.Update(ctx, srv); updateErr != nil {
			slog.Warn("failed to persist host key", "server_id", serverID, "error", updateErr)
		}
	}

	return client, nil
}

// runCommand executes a command on an SSH session with timeout support.
func (s *SSHService) runCommand(client *ssh.Client, command string, timeout time.Duration) (*ExecResult, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create session: %w", wrapSSHError(err))
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case runErr := <-done:
		if runErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(runErr, &exitErr) {
				return &ExecResult{
					Stdout:   stdout.String(),
					Stderr:   stderr.String(),
					ExitCode: exitErr.ExitStatus(),
				}, nil
			}
			return nil, fmt.Errorf("run command: %w", wrapSSHError(runErr))
		}
		return &ExecResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: 0,
		}, nil
	case <-ctx.Done():
		if err := session.Signal(ssh.SIGKILL); err != nil {
			slog.Warn("failed to send SIGKILL to ssh session", "error", err)
		}
		return nil, fmt.Errorf("run command: %w", server.ErrSSHTimeout)
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

// CopyFile pipes a local file to a remote path over SSH.
func (s *SSHService) CopyFile(ctx context.Context, serverID uint, localPath, remotePath string) error {
	client, err := s.GetClient(ctx, serverID)
	if err != nil {
		return fmt.Errorf("copy file: %w", err)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer f.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	defer session.Close()

	pipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	go func() {
		defer pipe.Close()
		if _, err := io.Copy(pipe, f); err != nil {
			slog.Warn("failed to copy file to ssh pipe", "error", err)
		}
	}()

	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf("cat > %s && chmod +x %s", remotePath, remotePath)
	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("copy failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}
