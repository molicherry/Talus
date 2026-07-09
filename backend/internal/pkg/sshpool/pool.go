package sshpool

import (
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Pool manages a cache of SSH client connections keyed by server ID.
// It limits concurrent sessions per server and evicts idle connections.
type Pool struct {
	mu       sync.Mutex
	conns    map[uint]*connEntry
	maxIdle  time.Duration
	maxConns int
	done     chan struct{}
}

// connEntry tracks a single cached SSH client and its usage.
type connEntry struct {
	client   *ssh.Client
	lastUsed time.Time
	sem      chan struct{} // concurrency limiter per server
}

// NewPool creates a connection pool that evicts idle connections after maxIdle
// and limits concurrent sessions per server to maxConns.
func NewPool(maxIdle time.Duration, maxConns int) *Pool {
	p := &Pool{
		conns:    make(map[uint]*connEntry),
		maxIdle:  maxIdle,
		maxConns: maxConns,
		done:     make(chan struct{}),
	}
	go p.evictLoop()
	return p
}

// Get acquires a concurrency slot for the given server and returns a cached
// client if one is available. Returns nil if no cached client exists — the
// caller must dial a new connection and pass it to Release when finished.
// Blocks if maxConns sessions are already active for this server.
func (p *Pool) Get(serverID uint) *ssh.Client {
	p.mu.Lock()
	entry, ok := p.conns[serverID]
	if !ok {
		entry = &connEntry{
			sem: make(chan struct{}, p.maxConns),
		}
		p.conns[serverID] = entry
	}
	p.mu.Unlock()

	// Acquire concurrency slot (blocks if at capacity)
	entry.sem <- struct{}{}

	p.mu.Lock()
	client := entry.client
	if client != nil {
		entry.client = nil // transfer ownership to caller
	}
	entry.lastUsed = time.Now()
	p.mu.Unlock()

	return client
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
