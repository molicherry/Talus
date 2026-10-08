package service

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

// An explicit remote exit status is stronger evidence than the deadline that
// eventually makes us close a channel whose peer forgot to finish its streams.
func TestExecRetainsExitStatusPublishedBeforeChannelCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  uint32
		timeout bool
	}{
		{name: "cancelled_success", status: 0},
		{name: "cancelled_failure", status: 7},
		{name: "timed_out_success", status: 0, timeout: true},
		{name: "timed_out_failure", status: 7, timeout: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			statusSent := make(chan struct{})
			address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, transportClosed <-chan struct{}) {
				_, _ = io.WriteString(channel, "before exit")
				_, _ = io.WriteString(channel.Stderr(), "exit detail")
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{test.status}))
				close(statusSent)
				// Intentionally leave the remote channel open after reporting the
				// real exit. Closing our session must preserve that status.
				<-transportClosed
				_ = channel.Close()
			})
			pool := newTestPool(t)
			original := dialTestSSH(t, address, hostKey)
			seedPool(t, pool, 1, original)
			svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := time.Minute
			if test.timeout {
				timeout = 100 * time.Millisecond
			}
			type completion struct {
				result *ExecResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, err := svc.Exec(ctx, 1, "known exit", timeout)
				done <- completion{result, err}
			}()
			select {
			case <-statusSent:
			case <-time.After(3 * time.Second):
				t.Fatal("remote did not publish its exit status")
			}
			if !test.timeout {
				select {
				case got := <-done:
					t.Fatalf("execution finished before channel cancellation: %#v", got)
				default:
				}
				cancel()
			}
			select {
			case got := <-done:
				if got.err != nil || got.result == nil || got.result.ExitCode != int(test.status) || got.result.Stdout != "before exit" || got.result.Stderr != "exit detail" {
					t.Fatalf("known remote exit lost during teardown: result=%#v err=%v", got.result, got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("execution did not join after closing its channel")
			}
			client, err := pool.Get(1, testFP(1))
			if err != nil {
				t.Fatal(err)
			}
			pool.Release(1, client)
			if client != original {
				t.Fatal("completed channel cleanup discarded the healthy transport")
			}
		})
	}
}

// Observe an actual SSH packet write parked by gatedConn, so teardown ordering
// is pinned by protocol events instead of relying on scheduler-dependent sleeps.
type execBlockedWriteConn struct {
	*gatedConn
	blockedWrite chan struct{}
}

func (c *execBlockedWriteConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	blocked := c.block
	c.mu.Unlock()
	if blocked {
		select {
		case c.blockedWrite <- struct{}{}:
		default:
		}
	}
	return c.gatedConn.Write(p)
}

func dialExecBlockedSSH(t *testing.T, address string, hostKey ssh.PublicKey) (*ssh.Client, *execBlockedWriteConn) {
	t.Helper()
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn := &execBlockedWriteConn{gatedConn: &gatedConn{Conn: raw}, blockedWrite: make(chan struct{}, 1)}
	transport, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second,
	})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return ssh.NewClient(transport, channels, requests), conn
}

func TestExecCancellationDiscardsOnlyWhenSessionCloseIsWedged(t *testing.T) {
	started := make(chan struct{})
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, transportClosed <-chan struct{}) {
		// Run first sends EOF for its empty stdin. Wait for that setup write
		// before parking later writes, so the observed packet is teardown.
		_, _ = io.Copy(io.Discard, channel)
		close(started)
		<-transportClosed
		_ = channel.Close()
	})
	pool := newTestPool(t)
	client, conn := dialExecBlockedSSH(t, address, hostKey)
	seedPool(t, pool, 1, client)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.Exec(ctx, 1, "wait", time.Minute); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not start")
	}
	conn.blockWrites()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wedged cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wedged session close did not join")
	}
	select {
	case <-conn.blockedWrite:
	default:
		t.Fatal("cancellation did not attempt to close its channel first")
	}
	got, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	pool.Release(1, got)
	if got != nil {
		t.Fatal("force-closed transport was returned to the pool")
	}
}

func TestExecOwnTimeoutKeepsOriginalReasonDuringTeardown(t *testing.T) {
	started := make(chan struct{})
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, transportClosed <-chan struct{}) {
		_, _ = io.Copy(io.Discard, channel)
		close(started)
		<-transportClosed
		_ = channel.Close()
	})
	client, conn := dialExecBlockedSSH(t, address, hostKey)
	defer client.Close()
	svc := NewSSHService(nil, nil, nil, time.Second, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completion struct {
		err    error
		closed bool
	}
	done := make(chan completion, 1)
	go func() {
		_, err, closed := svc.runCommand(ctx, client, "wait", 100*time.Millisecond)
		done <- completion{err, closed}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not start")
	}
	conn.blockWrites()
	select {
	case <-conn.blockedWrite:
		// The service's deadline has already started channel cleanup. A
		// parent cancellation now must not change the recorded timeout.
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("own timeout did not start channel cleanup")
	}
	select {
	case got := <-done:
		if !errors.Is(got.err, server.ErrSSHTimeout) || errors.Is(got.err, context.Canceled) || !got.closed {
			t.Fatalf("timeout changed during teardown: err=%v closed=%t", got.err, got.closed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed-out session did not join")
	}
}
