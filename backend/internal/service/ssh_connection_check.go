package service

import (
	"context"
	"errors"
	"time"

	"github.com/vpsmanager/backend/internal/server"
)

// ConnectionTestResult describes a fresh handshake of the saved configuration.
type ConnectionTestResult struct {
	Mode      string `json:"mode"`
	LatencyMS int64  `json:"latency_ms"`
	Success   bool   `json:"success"`
}

// Leave response time within the ordinary HTTP endpoint's 15s write budget,
// including when SSH_TIMEOUT permits longer interactive connection attempts.
const maxConnectionTestBudget = 10 * time.Second

// TestConnection never substitutes a cached connection for the requested test
// and never allocates a command or PTY on either SSH server. One budget covers
// snapshot reads, quota waits, retries, handshakes and identity commits.
func (s *SSHService) TestConnection(ctx context.Context, id uint) (*ConnectionTestResult, error) {
	s = s.settingsSnapshot()
	start := time.Now()
	budget := s.sshDialTimeout
	if budget <= 0 || budget > maxConnectionTestBudget {
		budget = maxConnectionTestBudget
	}
	operation, cancel := context.WithTimeoutCause(ctx, budget, server.ErrSSHTimeout)
	defer cancel()
	testError := func(err error) error {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if operation.Err() != nil {
			var app *server.AppError
			if errors.As(err, &app) && app.Reason == server.ReasonSSHTimeout {
				return err
			}
			return safeRouteError(operation, context.DeadlineExceeded)
		}
		return err
	}
	lease, snap, err := s.getLeaseWithSnapshot(operation, id, true)
	if err != nil {
		return nil, testError(err)
	}
	defer lease.Discard()
	if operation.Err() != nil {
		return nil, testError(context.Cause(operation))
	}
	return &ConnectionTestResult{Mode: snap.mode(), LatencyMS: time.Since(start).Milliseconds(), Success: true}, nil
}
