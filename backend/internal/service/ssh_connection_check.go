package service

import (
	"context"
	"time"
)

// ConnectionTestResult describes a fresh handshake of the saved configuration.
type ConnectionTestResult struct {
	Mode      string `json:"mode"`
	LatencyMS int64  `json:"latency_ms"`
	Success   bool   `json:"success"`
}

// TestConnection never substitutes a cached connection for the requested test
// and never allocates a command or PTY on either SSH server.
func (s *SSHService) TestConnection(ctx context.Context, id uint) (*ConnectionTestResult, error) {
	start := time.Now()
	lease, err := s.getLease(ctx, id, true)
	if err != nil {
		return nil, err
	}
	defer lease.Discard()
	snap, err := s.readRoute(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ConnectionTestResult{Mode: snap.mode(), LatencyMS: time.Since(start).Milliseconds(), Success: true}, nil
}
