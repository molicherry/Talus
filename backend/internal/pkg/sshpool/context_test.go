package sshpool

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGetContextCancelledSlotWaitDoesNotAcquire(t *testing.T) {
	pool := NewPool(time.Minute, 1, time.Minute)
	defer pool.Close()
	if _, err := pool.Get(1, "fp"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := pool.GetContext(ctx, 1, "fp"); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		pool.mu.Lock()
		waiting := pool.conns[1].waiters
		pool.mu.Unlock()
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot waiter did not enter")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slot wait ignored cancellation")
	}
	pool.mu.Lock()
	occupied := len(pool.conns[1].sem)
	pool.mu.Unlock()
	if occupied != 1 {
		t.Fatalf("slot occupancy = %d, want original owner's one slot", occupied)
	}
	pool.Release(1, nil)
	if _, err := pool.Get(1, "fp"); err != nil {
		t.Fatal(err)
	}
	pool.Release(1, nil)
}

type contextProbeConn struct {
	ssh.Conn
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (c *contextProbeConn) Close() error { c.closeOnce.Do(func() { close(c.closed) }); return nil }
func (c *contextProbeConn) Wait() error  { return nil }
func (c *contextProbeConn) SendRequest(string, bool, []byte) (bool, []byte, error) {
	c.startOnce.Do(func() { close(c.started) })
	<-c.closed
	return false, nil, net.ErrClosed
}
func probeTestClient() (*ssh.Client, *contextProbeConn) {
	conn := &contextProbeConn{started: make(chan struct{}), closed: make(chan struct{})}
	return ssh.NewClient(conn, make(chan ssh.NewChannel), make(chan *ssh.Request)), conn
}

func TestGetContextCancelsAndJoinsKeepaliveWithoutCaching(t *testing.T) {
	pool := NewPool(time.Minute, 1, time.Minute)
	defer pool.Close()
	client, conn := probeTestClient()
	pool.mu.Lock()
	pool.conns[1] = &connEntry{client: client, fingerprint: "fp", sem: make(chan struct{}, 1), lastUsed: time.Now()}
	pool.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := pool.GetContext(ctx, 1, "fp"); done <- err }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("probe error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive ignored cancellation")
	}
	pool.mu.Lock()
	entry := pool.conns[1]
	cached, slots := entry.client, len(entry.sem)
	pool.mu.Unlock()
	if cached != nil || slots != 0 {
		t.Fatalf("cancelled probe cached client %v or retained %d slots", cached, slots)
	}
	select {
	case <-conn.closed:
	default:
		t.Fatal("cancelled probe left transport open")
	}
}

func TestGetContextLateCancelDoesNotCloseReturnedClient(t *testing.T) {
	pool := NewPool(time.Minute, 1, time.Second)
	defer pool.Close()
	pool.probe = func(*ssh.Client) bool { return true }
	client, conn := probeTestClient()
	pool.mu.Lock()
	pool.conns[1] = &connEntry{client: client, fingerprint: "fp", sem: make(chan struct{}, 1), lastUsed: time.Now()}
	pool.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	got, err := pool.GetContext(ctx, 1, "fp")
	if err != nil || got != client {
		t.Fatalf("GetContext = %v, %v", got, err)
	}
	cancel()
	select {
	case <-conn.closed:
		t.Fatal("late cancellation closed caller-owned client")
	default:
	}
	pool.Release(1, got)
	got, err = pool.Get(1, "fp")
	if err != nil || got != client {
		t.Fatalf("reused client = %v, %v", got, err)
	}
	pool.Discard(1, got)
}

func TestPoolCloseUnblocksProbeAndRejectsFutureGets(t *testing.T) {
	pool := NewPool(time.Minute, 1, time.Second)
	client, conn := probeTestClient()
	pool.mu.Lock()
	pool.conns[1] = &connEntry{client: client, fingerprint: "fp", sem: make(chan struct{}, 1), lastUsed: time.Now()}
	pool.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := pool.GetContext(context.Background(), 1, "fp"); done <- err }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	pool.Close()
	pool.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not stop probe")
	}
	if _, err := pool.Get(1, "fp"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed pool Get = %v", err)
	}
}

func stalledHandshakeServer(t *testing.T) (string, int, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	return host, port, closed
}

func TestDialSSHContextCancelsStalledHandshakeAndClosesSocket(t *testing.T) {
	host, port, peerClosed := stalledHandshakeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client, _, err := DialSSHContext(ctx, host, port, "user", ssh.Password("unused"), nil, time.Minute)
	if client != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled dial = %v, %v", client, err)
	}
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("cancelled handshake kept its TCP socket open")
	}
}

func TestDialSSHTimeoutAlsoBoundsAuthentication(t *testing.T) {
	host, port, peerClosed := stalledHandshakeServer(t)
	client, _, err := DialSSH(host, port, "user", ssh.Password("unused"), nil, 30*time.Millisecond)
	if client != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial timeout = %v, %v", client, err)
	}
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("timed out handshake kept its TCP socket open")
	}
}

func liveSSHServer(t *testing.T) (string, int, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
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
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					_ = raw.Close()
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.UnknownChannelType, "unused")
				}
			}()
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return host, port, signer.PublicKey().Marshal()
}

func TestDialSSHContextTransfersOwnershipAndPreservesHostKeyVerification(t *testing.T) {
	host, port, known := liveSSHServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	client, captured, err := DialSSHContext(ctx, host, port, "user", ssh.Password("unused"), known, 0)
	if err != nil || client == nil || string(captured) != string(known) {
		cancel()
		t.Fatalf("trusted dial = %v, %v", client, err)
	}
	defer client.Close()
	cancel()
	if _, _, err := client.SendRequest("still-alive", true, nil); err != nil {
		t.Fatalf("late cancellation closed transferred transport: %v", err)
	}
	badClient, presented, err := DialSSH(host, port, "user", ssh.Password("unused"), []byte("other key"), time.Second)
	if badClient != nil || !errors.Is(err, ErrHostKeyMismatch) || string(presented) != string(known) {
		t.Fatalf("mismatched dial = %v, key %x, error %v", badClient, presented, err)
	}
}
