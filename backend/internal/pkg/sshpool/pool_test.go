package sshpool

import (
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSSHConn implements ssh.Conn by embedding the interface (nil) and
// overriding Close with a safe net.Conn close. SendRequest and friends
// would panic, but tests override the pool probe so they are never called.
type fakeSSHConn struct {
	ssh.Conn
	netConn net.Conn
}

func (f *fakeSSHConn) Close() error { return f.netConn.Close() }

// Wait is called by ssh.NewClient's teardown goroutine; report done.
func (f *fakeSSHConn) Wait() error { return nil }

// newTestClient returns an *ssh.Client backed by a net.Pipe so Close() is
// safe. The transport never completes a handshake — tests always override
// the pool probe, so no real keepalive traffic is sent.
func newTestClient(t *testing.T) *ssh.Client {
	t.Helper()
	clientConn, _ := net.Pipe()
	return ssh.NewClient(
		&fakeSSHConn{netConn: clientConn},
		make(chan ssh.NewChannel),
		make(chan *ssh.Request),
	)
}

// waitForEntry polls until the pool holds an entry for serverID, so tests
// don't race the lazy entry creation in Get.
func waitForEntry(t *testing.T, p *Pool, serverID uint, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		_, ok := p.conns[serverID]
		p.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("pool entry not created in time")
}

func TestGetDropsDeadConnection(t *testing.T) {
	p := NewPool(time.Minute, 1)
	// Override probe: first call reports the connection dead.
	p.probe = func(*ssh.Client) bool { return false }

	// Simulate a cached connection: insert a fake client directly.
	fake := newTestClient(t)
	p.mu.Lock()
	p.conns[1] = &connEntry{client: fake, sem: make(chan struct{}, 1), lastUsed: time.Now()}
	p.mu.Unlock()

	got := p.Get(1)
	if got != nil {
		t.Fatalf("expected dead cached connection to be dropped, got non-nil client")
	}

	// The connection must not have been returned to the pool.
	p.mu.Lock()
	entry := p.conns[1]
	p.mu.Unlock()
	if entry != nil && entry.client != nil {
		t.Fatalf("dead connection was cached back into the pool")
	}
}

func TestGetReturnsLiveConnection(t *testing.T) {
	p := NewPool(time.Minute, 1)
	p.probe = func(*ssh.Client) bool { return true }

	fake := newTestClient(t)
	p.mu.Lock()
	p.conns[1] = &connEntry{client: fake, sem: make(chan struct{}, 1), lastUsed: time.Now()}
	p.mu.Unlock()

	got := p.Get(1)
	if got != fake {
		t.Fatalf("expected live cached connection to be returned, got %v", got)
	}
	// Ownership transferred: pool must no longer hold it.
	p.mu.Lock()
	entry := p.conns[1]
	p.mu.Unlock()
	if entry == nil || entry.client != nil {
		t.Fatalf("connection not transferred out of the pool")
	}
}

func TestDiscardDoesNotCache(t *testing.T) {
	p := NewPool(time.Minute, 1)
	p.probe = func(*ssh.Client) bool { return true }

	fake := newTestClient(t)
	p.mu.Lock()
	p.conns[1] = &connEntry{client: fake, sem: make(chan struct{}, 1), lastUsed: time.Now()}
	p.mu.Unlock()

	// Take it out, then discard instead of release.
	got := p.Get(1)
	if got != fake {
		t.Fatalf("expected to acquire the cached client")
	}
	p.Discard(1, got)

	p.mu.Lock()
	entry := p.conns[1]
	p.mu.Unlock()
	if entry == nil || entry.client != nil {
		t.Fatalf("discarded connection must not be cached back")
	}
}

func TestReleaseCachesConnection(t *testing.T) {
	p := NewPool(time.Minute, 1)
	p.probe = func(*ssh.Client) bool { return true }

	fake := newTestClient(t)
	p.mu.Lock()
	p.conns[1] = &connEntry{client: fake, sem: make(chan struct{}, 1), lastUsed: time.Now()}
	p.mu.Unlock()

	got := p.Get(1)
	p.Release(1, got)

	p.mu.Lock()
	entry := p.conns[1]
	p.mu.Unlock()
	if entry == nil || entry.client != got {
		t.Fatalf("released connection must be cached back")
	}
}

func TestEvictRemovesIdleEntry(t *testing.T) {
	p := NewPool(50*time.Millisecond, 1)
	p.probe = func(*ssh.Client) bool { return true }

	// Create the entry by calling Get (returns nil since nothing cached).
	// Then release nothing — the entry stays with no cached client.
	_ = p.Get(2)
	// Entry now exists with client == nil and one held sem slot; release the
	// slot by Discard with nil.
	p.Discard(2, nil)

	waitForEntry(t, p, 2, time.Second)

	time.Sleep(120 * time.Millisecond)
	p.evict()

	p.mu.Lock()
	_, ok := p.conns[2]
	p.mu.Unlock()
	if ok {
		t.Fatalf("idle empty entry should have been evicted")
	}
}
