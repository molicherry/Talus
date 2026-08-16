package sshpool

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// probeTimeout bounds a single keepalive round-trip used to verify a pooled
// connection is still alive. Connections that do not respond within this
// window are treated as dead and discarded so callers dial a fresh one.
const probeTimeout = 3 * time.Second

// Pool manages a cache of SSH client connections keyed by server ID.
// It limits concurrent sessions per server and evicts idle connections.
// ErrSlotTimeout is returned by Get when the server already has maxConns
// active sessions and no slot frees up within slotTimeout.
var ErrSlotTimeout = errors.New("sshpool: too many concurrent sessions for this server")

type Pool struct {
	mu          sync.Mutex
	conns       map[uint]*connEntry
	maxIdle     time.Duration
	maxConns    int
	slotTimeout time.Duration
	done        chan struct{}
	// probe reports whether a pooled connection is still usable. It is
	// overridable in tests; the default performs a keepalive round-trip.
	probe func(*ssh.Client) bool
}

// connEntry tracks a single cached SSH client and its usage.
type connEntry struct {
	client   *ssh.Client
	lastUsed time.Time
	sem      chan struct{} // concurrency limiter per server
}

// NewPool creates a connection pool that evicts idle connections after maxIdle
// and limits concurrent sessions per server to maxConns. Get blocks for at
// most slotTimeout waiting for a slot before returning ErrSlotTimeout.
func NewPool(maxIdle time.Duration, maxConns int, slotTimeout time.Duration) *Pool {
	p := &Pool{
		conns:       make(map[uint]*connEntry),
		maxIdle:     maxIdle,
		maxConns:    maxConns,
		slotTimeout: slotTimeout,
		done:        make(chan struct{}),
		probe:       keepAliveProbe,
	}
	go p.evictLoop()
	return p
}

// Get acquires a concurrency slot for the given server and returns a cached
// client if one is available and healthy. Returns nil if no cached client
// exists or the cached one is dead — the caller must dial a new connection
// and pass it to Release when finished.
// Blocks up to slotTimeout if maxConns sessions are already active for this
// server, then returns ErrSlotTimeout instead of hanging the caller forever.
func (p *Pool) Get(serverID uint) (*ssh.Client, error) {
	p.mu.Lock()
	entry, ok := p.conns[serverID]
	if !ok {
		entry = &connEntry{
			sem: make(chan struct{}, p.maxConns),
		}
		p.conns[serverID] = entry
	}
	p.mu.Unlock()

	// Acquire concurrency slot (bounded wait; never block forever)
	select {
	case entry.sem <- struct{}{}:
	case <-time.After(p.slotTimeout):
		return nil, ErrSlotTimeout
	}

	p.mu.Lock()
	client := entry.client
	if client != nil {
		entry.client = nil // transfer ownership to caller
	}
	entry.lastUsed = time.Now()
	p.mu.Unlock()

	// A cached connection may have died silently (network drop, server
	// restart, or the peer timing it out). Verify it is still usable before
	// handing it out; otherwise close and drop it so the caller dials a fresh
	// connection instead of reusing a dead one forever.
	if client != nil && !p.probe(client) {
		client.Close()
		client = nil
	}

	return client, nil
}

// Release returns a client to the pool or closes it if the pool already has
// a cached connection. Also releases the concurrency slot acquired by Get.
// Passing nil for client is valid — it only releases the slot.
func (p *Pool) Release(serverID uint, client *ssh.Client) {
	p.mu.Lock()
	entry, ok := p.conns[serverID]
	if !ok {
		p.mu.Unlock()
		if client != nil {
			client.Close()
		}
		return
	}

	if client != nil {
		if entry.client != nil {
			// Pool already has a cached client, close this one
			client.Close()
		} else {
			entry.client = client
		}
		entry.lastUsed = time.Now()
	}
	p.mu.Unlock()

	<-entry.sem // release concurrency slot
}

// Discard closes client without returning it to the pool and releases the
// concurrency slot acquired by Get. Callers should use Discard instead of
// Release when they know the connection is broken, so a dead connection is
// never cached and reused.
func (p *Pool) Discard(serverID uint, client *ssh.Client) {
	p.mu.Lock()
	entry, ok := p.conns[serverID]
	p.mu.Unlock()

	if client != nil {
		client.Close()
	}
	if ok {
		<-entry.sem // release concurrency slot
	}
}

// Close shuts down the eviction goroutine and closes all cached connections.
func (p *Pool) Close() {
	close(p.done)

	p.mu.Lock()
	defer p.mu.Unlock()

	for _, entry := range p.conns {
		if entry.client != nil {
			entry.client.Close()
		}
	}
	p.conns = nil
}

// evictLoop periodically removes idle connections.
func (p *Pool) evictLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.evict()
		case <-p.done:
			return
		}
	}
}

// evict closes and removes connections that have been idle longer than maxIdle.
func (p *Pool) evict() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id, entry := range p.conns {
		if entry.client != nil && time.Since(entry.lastUsed) > p.maxIdle {
			entry.client.Close()
			entry.client = nil
		}
		// Remove entries with no cached client and no active users.
		if entry.client == nil && len(entry.sem) == 0 {
			delete(p.conns, id)
		}
	}
}

// keepAliveProbe performs a bounded keepalive round-trip over the SSH channel.
// It returns true only when the server responds within probeTimeout.
func keepAliveProbe(client *ssh.Client) bool {
	done := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@talus.local", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(probeTimeout):
		return false
	}
}
