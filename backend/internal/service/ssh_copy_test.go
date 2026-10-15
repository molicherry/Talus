package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

func TestCopyFileReturnsSlotAfterSuccessAndRemoteFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "remote_failure"
		}
		t.Run(name, func(t *testing.T) {
			copied := make(chan string, 1)
			address, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
				if !strings.Contains(command, "'file'\"'\"'s target'") {
					copied <- "bad-quoting"
				} else {
					data, _ := io.ReadAll(ch)
					copied <- string(data)
				}
				status := uint32(0)
				if failure {
					status = 1
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				_ = ch.Close()
			})
			pool := newTestPool(t)
			seedPool(t, pool, 1, dialTestSSH(t, address, key))
			svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
			local := filepath.Join(t.TempDir(), "agent")
			if err := os.WriteFile(local, []byte("uploaded-agent"), 0600); err != nil {
				t.Fatal(err)
			}
			err := svc.CopyFile(context.Background(), 1, local, "file's target")
			if (err != nil) != failure {
				t.Fatalf("CopyFile = %v", err)
			}
			if got := <-copied; got != "uploaded-agent" {
				t.Fatalf("remote received %q", got)
			}
			cached, err := pool.Get(1, testFP(1))
			if err != nil {
				t.Fatalf("slot leaked: %v", err)
			}
			defer pool.Release(1, cached)
			if failure && cached != nil {
				t.Fatal("failed upload was cached")
			}
			if !failure && cached == nil {
				t.Fatal("healthy upload was not cached")
			}
		})
	}
}

func TestCopyFileCancellationClosesTransportAndReturnsSlot(t *testing.T) {
	started := make(chan struct{})
	address, key := startExecSSHServer(t, func(_ string, ch ssh.Channel, closed <-chan struct{}) { close(started); <-closed; _ = ch.Close() })
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	local := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(local, []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.CopyFile(ctx, 1, local, "agent") }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled upload stuck")
	}
	client, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatalf("slot leaked: %v", err)
	}
	pool.Release(1, client)
	if client != nil {
		t.Fatal("cancelled upload transport cached")
	}
}

// Retain AppError identity for callers classifying a fail-closed host warning.
func TestSSHRouteErrorRetainsHostKeySentinel(t *testing.T) {
	err := safeRouteError(context.Background(), server.ErrSSHHostKeyMismatch)
	if err != server.ErrSSHHostKeyMismatch {
		t.Fatalf("sentinel replaced: %v", err)
	}
}
