package service

import (
	"context"
	"errors"
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
			address, key := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
				serveUploadPeer(ch, &uploadPeerFS{finish: func(path string, data []byte) error {
					if strings.TrimPrefix(path, "/") != "file's target" {
						copied <- "bad literal path"
					} else {
						copied <- string(data)
					}
					if failure {
						return os.ErrPermission
					}
					return nil
				}})
			}))
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
			if cached == nil {
				t.Fatal("completed remote upload discarded a healthy transport")
			}
		})
	}
}

func TestCopyFileCancellationReturnsSlotAndRetainsOnlyHealthyTransport(t *testing.T) {
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
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled upload stuck")
	}
	client, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatalf("slot leaked: %v", err)
	}
	defer pool.Release(1, client)
	if client != nil {
		if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			t.Fatalf("cancelled upload cached an unhealthy transport: %v", err)
		}
	}
}

// Retain AppError identity for callers classifying a fail-closed host warning.
func TestSSHRouteErrorRetainsHostKeySentinel(t *testing.T) {
	err := safeRouteError(context.Background(), server.ErrSSHHostKeyMismatch)
	if err != server.ErrSSHHostKeyMismatch {
		t.Fatalf("sentinel replaced: %v", err)
	}
}
