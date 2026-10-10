package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// drainUploadOK consumes the upload stream and reports a clean remote exit,
// so the client's session.Run succeeds.
func drainUploadOK(ch ssh.Channel) {
	_, _ = io.Copy(io.Discard, ch)
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	_ = ch.Close()
}

// routeUploads handles the CopyFile "cat > ..." command with the supplied
// upload behaviour and answers anything else with a clean exit 0.
func routeUploads(upload func(ssh.Channel, <-chan struct{})) func(string, ssh.Channel, <-chan struct{}) {
	return func(cmd string, ch ssh.Channel, tc <-chan struct{}) {
		if strings.HasPrefix(cmd, "cat >") {
			upload(ch, tc)
			return
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	}
}

func writeLocalArtifact(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCopyFileSettlesQuotaAcrossPaths pins TC-06-01: repeated successful and
// failing uploads must never exhaust the single-slot quota.
func TestCopyFileSettlesQuotaAcrossPaths(t *testing.T) {
	address, hostKey := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
		drainUploadOK(ch)
	}))
	pool := newTestPool(t) // maxConns = 1, slotTimeout = 1s
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	local := writeLocalArtifact(t)

	for i := 0; i < 3; i++ {
		if err := svc.CopyFile(context.Background(), 1, local, "/tmp/agent"); err != nil {
			t.Fatalf("successful upload %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := svc.CopyFile(context.Background(), 1, filepath.Join(t.TempDir(), "missing"), "/tmp/agent"); err == nil {
			t.Fatal("a missing local file must fail")
		}
	}

	// A leaked slot would make this block for slotTimeout and fail.
	if _, err := svc.Exec(context.Background(), 1, "noop", time.Second); err != nil {
		t.Fatalf("exec after uploads: %v", err)
	}
}

// TestCopyFileRemoteRejectSettlesQuota pins the remote-rejection path.
func TestCopyFileRemoteRejectSettlesQuota(t *testing.T) {
	address, hostKey := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
		_, _ = io.Copy(io.Discard, ch)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
		_ = ch.Close()
	}))
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	local := writeLocalArtifact(t)

	for i := 0; i < 3; i++ {
		if err := svc.CopyFile(context.Background(), 1, local, "/tmp/agent"); err == nil {
			t.Fatal("a remote rejection must surface as an error")
		}
	}
	if _, err := svc.Exec(context.Background(), 1, "noop", time.Second); err != nil {
		t.Fatalf("exec after rejected uploads: %v", err)
	}
}

// TestCopyFileCancellationSettlesQuotaAndJoins pins TC-06-02: a blocked upload
// that is cancelled must release the quota and join its worker.
func TestCopyFileCancellationSettlesQuotaAndJoins(t *testing.T) {
	var calls int32
	address, hostKey := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, tc <-chan struct{}) {
		if atomic.AddInt32(&calls, 1) == 1 {
			<-tc // block the first upload until the transport is closed
			return
		}
		drainUploadOK(ch)
	}))
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	local := writeLocalArtifact(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := svc.CopyFile(ctx, 1, local, "/tmp/agent"); err == nil {
		t.Fatal("a blocked upload must not report success")
	}

	// The bounded teardown may force-close the transport; re-seed so the next
	// upload has a healthy client (the fake server source cannot dial).
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	if err := svc.CopyFile(context.Background(), 1, local, "/tmp/agent"); err != nil {
		t.Fatalf("upload after cancellation: %v", err)
	}
}

// TestCopyFileRemoteRejectWithoutReadingStdinIsBounded pins the P1 fix: a remote
// that rejects the exec request without draining stdin must not block the copy
// worker forever, even when the file exceeds the SSH window.
func TestCopyFileRemoteRejectWithoutReadingStdinIsBounded(t *testing.T) {
	address, hostKey := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
		// Reject without reading stdin; the client's copy worker blocks once the
		// SSH window fills and must be reclaimed by the bounded teardown.
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
		_ = ch.Close()
	}))
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)

	big := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 8<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- svc.CopyFile(context.Background(), 1, big, "/tmp/agent") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a rejected upload must fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rejected upload did not return within the bounded teardown")
	}
}

// startRejectExecSSHServer answers the exec request with a rejection and keeps
// the channel open, so the client's copy worker can only be reclaimed by the
// bounded teardown.
func startRejectExecSSHServer(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				transport, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer transport.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, reqs, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						// Reject exec and keep the channel open, never draining stdin.
						for req := range reqs {
							_ = req.Reply(false, nil)
						}
						_ = channel
					}()
				}
			}()
		}
	}()
	return listener.Addr().String(), signer.PublicKey()
}

// TestCopyFileRejectedExecIsBounded pins the P1 fix: a remote that rejects the
// exec request and keeps the channel open must not block the upload forever.
func TestCopyFileRejectedExecIsBounded(t *testing.T) {
	address, hostKey := startRejectExecSSHServer(t)
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)

	big := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 8<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- svc.CopyFile(context.Background(), 1, big, "/tmp/agent") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a rejected exec must fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rejected exec did not return within the bounded teardown")
	}
}

// TestCopyFileReadErrorDoesNotReportSuccess pins the P2 fix: a local read error
// (a directory) must not be swallowed into a successful upload.
func TestCopyFileReadErrorDoesNotReportSuccess(t *testing.T) {
	address, hostKey := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) { drainUploadOK(ch) }))
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)

	done := make(chan error, 1)
	go func() { done <- svc.CopyFile(context.Background(), 1, t.TempDir(), "/tmp/agent") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a local read error must not report success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a local read error did not return in time")
	}
}
