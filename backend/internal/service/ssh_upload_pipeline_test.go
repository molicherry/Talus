package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A high-latency peer must receive more than one data packet before the first
// write is acknowledged. Waiting for every 32 KiB acknowledgement caps upload
// throughput at one packet per network round trip, regardless of bandwidth.
func TestAgentUploadPipelinesUnacknowledgedWrites(t *testing.T) {
	for _, privateDirectory := range []bool{false, true} {
		name := "linux"
		if privateDirectory {
			name = "windows-private-directory"
		}
		for _, outcome := range []string{"success", "write-failure", "canceled"} {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				writes := make(chan struct{}, 64)
				acknowledge := make(chan struct{})
				defer func() {
					select {
					case <-acknowledge:
					default:
						close(acknowledge)
					}
				}()
				received := make(chan []byte, 1)
				address, key := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
					serveUploadPeer(ch, &uploadPeerFS{
						write: func() { writes <- struct{}{}; <-acknowledge },
						writeError: func(offset int64) error {
							if outcome == "write-failure" && offset >= 32<<10 {
								return os.ErrPermission
							}
							return nil
						},
						finish: func(_ string, data []byte) error {
							received <- data
							return nil
						},
					})
				}))
				pool := newTestPool(t)
				seedPool(t, pool, 1, dialTestSSH(t, address, key))
				svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
				lease, err := svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
				payload := bytes.Repeat([]byte("agent-payload"), 24<<10)
				local := filepath.Join(t.TempDir(), "agent")
				if err := os.WriteFile(local, payload, 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if privateDirectory {
						done <- lease.uploadPrivateDirectory(ctx, local, "/stage/agent.exe", 5*time.Second)
					} else {
						done <- lease.Upload(ctx, local, "/stage/agent", 5*time.Second)
					}
				}()
				for i := 0; i < 2; i++ {
					select {
					case <-writes:
					case err := <-done:
						t.Fatalf("upload ended before pipelining data: %v", err)
					case <-time.After(2 * time.Second):
						t.Fatal("upload waited for a write acknowledgement before sending the next packet")
					}
				}
				if outcome == "canceled" {
					cancel()
				} else {
					close(acknowledge)
				}
				select {
				case err := <-done:
					if (err == nil) != (outcome == "success") {
						t.Fatalf("upload result for %s: %v", outcome, err)
					}
					if outcome == "canceled" && !errors.Is(err, context.Canceled) {
						t.Fatalf("upload lost cancellation reason: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("pipelined upload did not complete")
				}
				if outcome == "success" {
					if got := <-received; !bytes.Equal(got, payload) {
						t.Fatal("pipelined upload changed artifact bytes")
					}
				}
				if err := lease.releaseAndWait(); err != nil {
					t.Fatal(err)
				}
				if outcome == "canceled" {
					// Cancellation may discard the transport, but must return its slot.
					client, err := pool.Get(1, testFP(1))
					if err != nil {
						t.Fatalf("canceled upload leaked its SSH slot: %v", err)
					}
					pool.Release(1, client)
				} else {
					if _, err := svc.Exec(context.Background(), 1, "after-upload", time.Second); err != nil {
						t.Fatalf("pipelined upload leaked its SSH slot: %v", err)
					}
				}
			})
		}
	}
}
