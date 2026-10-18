package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

// Delay the real transport in each direction, without slowing the handshake or
// bulk upload. Tests enable latency when the complete artifact reaches the peer.
type agentLatencyConn struct {
	net.Conn
	oneWay atomic.Int64
}

func (c *agentLatencyConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		time.Sleep(time.Duration(c.oneWay.Load()))
	}
	return n, err
}

func (c *agentLatencyConn) Write(p []byte) (int, error) {
	time.Sleep(time.Duration(c.oneWay.Load()))
	return c.Conn.Write(p)
}

func delayedAgentDeployment(t *testing.T) (*AgentDeploymentService, *fakeRemote, *SSHService, *agentLatencyConn) {
	t.Helper()
	remote := newFakeRemote()
	remote.agentPayload = `{"agent_version":"v0.11.0","protocol_version":1}`
	remote.files[agentPath] = []byte("old agent")
	address, key := startExecSSHServer(t, remote.handler())
	svc, _ := routeTestFixture(t, address, key)
	raw, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	latency := &agentLatencyConn{Conn: raw}
	conn, channels, requests, err := ssh.NewClientConn(latency, address, &ssh.ClientConfig{
		User: "test", HostKeyCallback: ssh.FixedHostKey(key),
	})
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() { _ = client.Close() })
	snap, err := svc.readRoute(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := svc.pool.AcquireContext(context.Background(), 1, snap.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	handle.SetClient(client)
	handle.Release()
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "v0.11.0")
	deployer := NewAgentDeploymentService(svc, dir)
	deployer.execBudget = 5 * time.Second
	return deployer, remote, svc, latency
}

func TestAgentDeploymentSurvives162msTeardownRTT(t *testing.T) {
	deployer, remote, svc, latency := delayedAgentDeployment(t)
	remote.afterUpload = func() { latency.oneWay.Store(int64(81 * time.Millisecond)) }
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for attempt := 0; attempt < 2; attempt++ {
		out, err := deployer.Collect(context.Background(), lease)
		if err != nil || out == nil || out.AgentVersion != "v0.11.0" {
			t.Fatalf("collection %d after delayed teardown: out=%+v err=%v", attempt, out, err)
		}
	}
	if remote.count("sftp.upload ") != 1 || remote.count("chmod 0755") != 1 {
		t.Fatal("deployment must activate once, then reuse the verified artifact")
	}
	for _, path := range remote.fileKeys() {
		if strings.Contains(path, ".upload-") {
			t.Fatalf("temporary artifact left behind: %s", path)
		}
	}
}

func TestAgentForcedTeardownReportsFailureAndReconnectsOnlyForCleanup(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "same_generation"
		if changed {
			name = "changed_generation"
		}
		t.Run(name, func(t *testing.T) {
			deployer, remote, svc, latency := delayedAgentDeployment(t)
			// Exercise the forced path without spending the production 2s grace.
			svc.uploadTeardownGrace = 20 * time.Millisecond
			remote.afterUpload = func() {
				latency.oneWay.Store(int64(81 * time.Millisecond))
				if changed {
					svc.pool.Invalidate(1)
				}
			}
			var logs bytes.Buffer
			deployer.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			lease, err := svc.AcquireLease(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			_, err = deployer.Collect(context.Background(), lease)
			if !errors.Is(err, errSSHTransportClosed) || !errors.Is(err, server.ErrSSHConnection) {
				t.Fatalf("forced teardown must fail at upload: %v", err)
			}
			record := safeRemoteLogRecord(t, logs.String(), "agent deployment failed")
			assertRemoteLogDiagnostics(t, record, map[string]string{
				"reason_class": telReasonDial, "reason": server.ReasonSSHConnection, "stage": string(StageTeardown),
			})
			if remote.ran("chmod 0755") || remote.ran(agentPath+" --format json") {
				t.Fatal("failed upload must not activate or execute an agent")
			}
			var leftover bool
			for _, path := range remote.fileKeys() {
				leftover = leftover || strings.Contains(path, ".upload-")
			}
			if changed {
				if !leftover || remote.ran("rm -f ") {
					t.Fatal("cleanup must refuse a replacement target generation")
				}
			} else if leftover || !remote.ran("rm -f ") || strings.Contains(logs.String(), "agent upload cleanup failed") {
				t.Fatalf("same-target reconnect did not clean the temporary artifact: %s", logs.String())
			}
			if data, _ := remote.file(agentPath); string(data) != "old agent" {
				t.Fatal("failure replaced the active artifact")
			}
		})
	}
}

func TestAgentUploadCleanupSurvivesCollectionCancellation(t *testing.T) {
	deployer, remote, svc, _ := delayedAgentDeployment(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote.afterUpload = cancel
	lease, err := svc.AcquireLease(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := deployer.Collect(ctx, lease); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collection = %v", err)
	}
	if !remote.ran("rm -f ") {
		t.Fatal("cancelled task context prevented temporary file cleanup")
	}
	for _, path := range remote.fileKeys() {
		if strings.Contains(path, ".upload-") {
			t.Fatalf("temporary artifact left behind: %s", path)
		}
	}
}

func TestSFTPCancellationDuringCompletedTeardownStaysFast(t *testing.T) {
	address, key := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) { drainUploadOK(ch) }))
	client, conn := dialExecBlockedSSH(t, address, key)
	defer client.Close()
	svc := NewSSHService(nil, nil, nil, time.Second, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err, _ := svc.withSFTPClient(ctx, client, false, func(context.Context, *sftp.Client) error {
			conn.blockWrites()
			return nil
		})
		done <- err
	}()
	select {
	case <-conn.blockedWrite:
	case <-time.After(time.Second):
		t.Fatal("normal teardown did not start")
	}
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late cancellation = %v", err)
		}
		if time.Since(started) > 500*time.Millisecond {
			t.Fatal("late cancellation waited for the normal 2s grace")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled teardown did not join promptly")
	}
}

func TestAgentUploadCleanupUsesOriginalDeadline(t *testing.T) {
	var removes atomic.Int64
	address, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
		if strings.HasPrefix(command, "rm -f ") {
			removes.Add(1)
		}
		exitStatus(ch, 0)
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := lease.Exec(context.Background(), "probe", time.Second); err != nil {
		t.Fatal(err)
	}
	// The previous primitive has already spent its entire cleanup budget.
	lease.mu.Lock()
	lease.cleanup.value = newSSHCleanupDeadlines(time.Now().Add(-sshCleanupBudget-time.Second), execTeardownGrace)
	lease.mu.Unlock()
	deployer := NewAgentDeploymentService(svc, t.TempDir())
	started := time.Now()
	_, err = deployer.cleanupUpload(context.Background(), lease, agentPath+".upload-test")
	if !errors.Is(err, context.DeadlineExceeded) || removes.Load() != 0 {
		t.Fatalf("expired cleanup admitted remote work: err=%v removes=%d", err, removes.Load())
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("cleanup restarted an expired deadline")
	}
}
