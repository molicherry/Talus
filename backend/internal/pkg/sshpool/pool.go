package sshpool

import (
	"context"
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
var ErrClosed = errors.New("sshpool: pool closed")

type Pool struct {
	mu          sync.Mutex
	conns       map[uint]*connEntry
	maxIdle     time.Duration
	maxConns    int
	slotTimeout time.Duration
	done        chan struct{}
	closed      bool
	closeOnce   sync.Once
	// probe reports whether a pooled connection is still usable. It is
	// overridable in tests; the default performs a keepalive round-trip.
	probe func(*ssh.Client) bool
}

// connEntry tracks a single cached SSH client and its usage.
type connEntry struct {
	client *ssh.Client
	// fingerprint identifies the connection parameters the cached client was
	// dialed with (host, port, credential revision). It is compared on every
	// Get so a cached client can never be used for different parameters.
	fingerprint string
	generation  uint64
	lastUsed    time.Time
	sem         chan struct{} // concurrency limiter per server
	waiters     int
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
//
// fingerprint describes the dial parameters (host, port, credential revision).
// A cached client dialed with a different fingerprint is closed and dropped:
// reusing it would send commands to the previous host or authenticate with the
// previous credential. Callers compute it from current server state, so a
// reconfigured or deleted server can never hit a stale cache entry.
func (p *Pool) Get(serverID uint, fingerprint string) (*ssh.Client, error) {
	return p.GetContext(context.Background(), serverID, fingerprint)
}

// GetContext also cancels slot waits and joins a cancelled keepalive probe.
// On error it owns no slot and returns no client; callers must not Release.
func (p *Pool) GetContext(ctx context.Context, serverID uint, fingerprint string) (*ssh.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	entry, ok := p.conns[serverID]
	if !ok {
		entry = &connEntry{
			fingerprint: fingerprint,
			generation:  1,
			sem:         make(chan struct{}, p.maxConns),
		}
		p.conns[serverID] = entry
	} else if entry.fingerprint != fingerprint {
		if entry.client != nil {
			entry.client.Close()
			entry.client = nil
		}
		entry.fingerprint = fingerprint
		entry.generation++
	}
	entry.waiters++
	p.mu.Unlock()

	// Acquire concurrency slot (bounded wait; never block forever)
	timer := time.NewTimer(p.slotTimeout)
	defer timer.Stop()
	var waitErr error
	select {
	case entry.sem <- struct{}{}:
	case <-timer.C:
		waitErr = ErrSlotTimeout
	case <-ctx.Done():
		waitErr = ctx.Err()
	case <-p.done:
		waitErr = ErrClosed
	}

	p.mu.Lock()
	entry.waiters--
	if waitErr != nil {
		p.mu.Unlock()
		return nil, waitErr
	}
	if p.closed || ctx.Err() != nil {
		waitErr = ctx.Err()
		if waitErr == nil {
			waitErr = ErrClosed
		}
		p.mu.Unlock()
		<-entry.sem
		return nil, waitErr
	}
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
	if client != nil {
		probed := make(chan bool, 1)
		go func() { probed <- p.probe(client) }()
		var healthy bool
		select {
		case healthy = <-probed:
		case <-ctx.Done():
			_ = client.Close()
			<-probed // the probe cannot retain ownership after returning
			<-entry.sem
			return nil, ctx.Err()
		case <-p.done:
			_ = client.Close()
			<-probed
			<-entry.sem
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			_ = client.Close()
			<-entry.sem
			return nil, err
		}
		if !healthy {
			_ = client.Close()
			client = nil
		}
	}

	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || ctx.Err() != nil {
		if client != nil {
			_ = client.Close()
		}
		<-entry.sem
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrClosed
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

// Invalidate drops any cached connection for the server. Call it when the
// server's connection parameters change (host/port/credential) or it is
// deleted, so the next Get cannot reuse a connection dialed with the old
// parameters. Safe to call for an unknown server.
func (p *Pool) Invalidate(serverID uint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.conns[serverID]
	if !ok {
		return
	}
	if entry.client != nil {
		entry.client.Close()
		entry.client = nil
	}
	// Clearing the fingerprint makes any client returned by an in-flight
	// session stale too: the next Get passes the current fingerprint and
	// closes whatever it finds (a stale release included).
	entry.fingerprint = ""
	entry.generation++
}

// Close shuts down the eviction goroutine and closes all cached connections.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed = true
		close(p.done)
		for _, entry := range p.conns {
			if entry.client != nil {
				entry.client.Close()
			}
		}
		p.conns = nil
	})
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
		if entry.client == nil && len(entry.sem) == 0 && entry.waiters == 0 {
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
		_ = client.Close()
		<-done
		return false
	}
}

// Handle is a borrowed concurrency slot plus (optionally) a cached client,
// pinned to the target generation observed at acquisition time. ReleaseHandle
// or DiscardHandle must be called exactly once; a client is only cached when
// the generation still matches, otherwise the stale client is closed.
type Handle struct {
	Client     *ssh.Client
	serverID   uint
	generation uint64
	sem        chan struct{}
	once       sync.Once
}

// Generation reports the target generation this handle was pinned to.
func (h *Handle) Generation() uint64 { return h.generation }

// AcquireContext acquires a concurrency slot for the server and returns a
// Handle. Client is non-nil when a healthy cached client exists; otherwise the
// caller dials one and assigns h.Client before ReleaseHandle so it is cached.
func (p *Pool) AcquireContext(ctx context.Context, serverID uint, fingerprint string) (*Handle, error) {
	client, err := p.GetContext(ctx, serverID, fingerprint)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	entry := p.conns[serverID]
	var gen uint64
	var sem chan struct{}
	if entry != nil {
		gen = entry.generation
		sem = entry.sem
	}
	p.mu.Unlock()
	if sem == nil {
		// The pool was closed between acquiring the slot and capturing it. There
		// is nothing to return the slot to, so fail instead of handing back a
		// Handle that would block forever in Release/Discard.
		if client != nil {
			_ = client.Close()
		}
		return nil, ErrClosed
	}
	return &Handle{Client: client, serverID: serverID, generation: gen, sem: sem}, nil
}

// ReleaseHandle returns the client to the pool while its generation still
// matches the current entry; a stale client is closed instead of cached. The
// slot is released exactly once.
func (p *Pool) ReleaseHandle(h *Handle) {
	if h == nil || h.sem == nil {
		return
	}
	h.once.Do(func() {
		if h.Client != nil {
			p.mu.Lock()
			entry, ok := p.conns[h.serverID]
			stale := !ok || entry.generation != h.generation
			if stale || entry.client != nil {
				p.mu.Unlock()
				_ = h.Client.Close()
			} else {
				entry.client = h.Client
				entry.lastUsed = time.Now()
				p.mu.Unlock()
			}
		}
		<-h.sem
	})
}

// DiscardHandle closes the client (if any) without caching it and releases the
// slot exactly once.
func (p *Pool) DiscardHandle(h *Handle) {
	if h == nil || h.sem == nil {
		return
	}
	h.once.Do(func() {
		if h.Client != nil {
			_ = h.Client.Close()
		}
		<-h.sem
	})
}
