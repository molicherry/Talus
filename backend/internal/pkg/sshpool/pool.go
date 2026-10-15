package sshpool

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const probeTimeout = 3 * time.Second

var ErrSlotTimeout = errors.New("sshpool: too many concurrent sessions for this server")
var ErrClosed = errors.New("sshpool: pool closed")

// ErrStaleLease means configuration changed while acquiring a connection.
var ErrStaleLease = errors.New("sshpool: connection configuration changed")

// ErrStaleGeneration is the Handle API name for the same stale acquisition.
var ErrStaleGeneration = ErrStaleLease

type Pool struct {
	mu             sync.Mutex
	conns          map[uint]*connEntry
	maxIdle        time.Duration
	maxConns       int
	slotTimeout    time.Duration
	done           chan struct{}
	shutdown       context.Context
	cancelShutdown context.CancelFunc
	closed         bool
	closeOnce      sync.Once
	probe          func(*ssh.Client) bool
	leases         map[*Lease]struct{}
	legacy         map[uint][]*Lease
	quotaObserver  QuotaObserver
}

type connEntry struct {
	client      *ssh.Client
	fingerprint string
	generation  uint64
	lastUsed    time.Time
	sem         chan struct{}
	waiters     int
}

// Lease owns one concurrency slot and, optionally, an entire SSH transport.
// Release and Discard are idempotent. A connection can only return to the
// entry and configuration generation from which it was borrowed.
type Lease struct {
	Client      *ssh.Client
	mu          sync.Mutex
	pool        *Pool
	entry       *connEntry
	serverID    uint
	generation  uint64
	fingerprint string
	done        bool
	cacheable   bool
	legacy      bool
	original    *ssh.Client
	consumer    string
}

func NewPool(maxIdle time.Duration, maxConns int, slotTimeout time.Duration) *Pool {
	if maxConns < 1 {
		maxConns = 1
	}
	shutdown, cancelShutdown := context.WithCancel(context.Background())
	p := &Pool{shutdown: shutdown, cancelShutdown: cancelShutdown, conns: make(map[uint]*connEntry), maxIdle: maxIdle, maxConns: maxConns, slotTimeout: slotTimeout, done: make(chan struct{}), probe: keepAliveProbe, leases: make(map[*Lease]struct{}), legacy: make(map[uint][]*Lease)}
	go p.evictLoop()
	return p
}

// AcquireContext transfers a cached client, or a vacant slot for a new dial.
func (p *Pool) AcquireContext(ctx context.Context, serverID uint, fingerprint string) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	entry := p.conns[serverID]
	var stale *ssh.Client
	if entry == nil {
		entry = &connEntry{fingerprint: fingerprint, generation: 1, sem: make(chan struct{}, p.maxConns)}
		p.conns[serverID] = entry
	} else if entry.fingerprint != fingerprint {
		stale, entry.client = entry.client, nil
		entry.fingerprint = fingerprint
		entry.generation++
	}
	generation := entry.generation
	entry.waiters++
	p.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}
	timer := time.NewTimer(p.slotTimeout)
	defer timer.Stop()
	var err error
	select {
	case entry.sem <- struct{}{}:
	case <-timer.C:
		err = ErrSlotTimeout
	case <-ctx.Done():
		err = ctx.Err()
	case <-p.done:
		err = ErrClosed
	}
	consumer := quotaConsumer(ctx)
	if err == nil {
		// Reserve accounting before a probe or dial can block, matching the
		// actual slot lifetime rather than the service lease lifetime.
		p.notifyQuota(consumer, 1)
	}
	p.mu.Lock()
	entry.waiters--
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if p.closed {
		err = ErrClosed
	} else if entry.generation != generation || entry.fingerprint != fingerprint {
		err = ErrStaleLease
	}
	if err != nil {
		p.mu.Unlock()
		p.releaseSlot(entry.sem, consumer)
		return nil, err
	}
	client := entry.client
	entry.client = nil
	entry.lastUsed = time.Now()
	p.mu.Unlock()
	if client != nil {
		probed := make(chan bool, 1)
		go func() { probed <- p.probe(client) }()
		var healthy bool
		select {
		case healthy = <-probed:
		case <-ctx.Done():
			_ = client.Close()
			<-probed
			p.releaseSlot(entry.sem, consumer)
			return nil, ctx.Err()
		case <-p.done:
			_ = client.Close()
			<-probed
			p.releaseSlot(entry.sem, consumer)
			return nil, ErrClosed
		}
		if !healthy {
			_ = client.Close()
			client = nil
		}
	}
	p.mu.Lock()
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if p.closed {
		err = ErrClosed
	} else if entry.generation != generation || entry.fingerprint != fingerprint {
		err = ErrStaleLease
	}
	if err != nil {
		p.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		p.releaseSlot(entry.sem, consumer)
		return nil, err
	}
	lease := &Lease{Client: client, pool: p, entry: entry, serverID: serverID, generation: generation, fingerprint: fingerprint, cacheable: true, consumer: consumer}
	p.leases[lease] = struct{}{}
	p.mu.Unlock()
	return lease, nil
}

// Valid reports whether this lease still belongs to the current generation.
func (l *Lease) Valid() bool {
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()
	if done {
		return false
	}
	p := l.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && p.conns[l.serverID] == l.entry && l.entry.generation == l.generation && l.entry.fingerprint == l.fingerprint
}

// Handle is the service primitive API name for the same owning lease.
// Both APIs share generation checks, quota accounting and idempotent return.
type Handle = Lease

func (l *Lease) ServerID() uint { return l.serverID }
func (l *Lease) Generation() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.generation
}

// RebindFingerprint adopts an already verified, self-induced identity update
// such as TOFU pinning. The caller must first verify the complete current
// database snapshot and must never use this to adopt an external change.
// Only the still-current original generation may advance itself; other leases
// and waiters retain their old generation and become stale. This first-use
// transport may finish its primitives but is conservatively not cached.
func (l *Lease) RebindFingerprint(fingerprint string) bool {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return false
	}
	p := l.pool
	p.mu.Lock()
	if p.closed || p.conns[l.serverID] != l.entry || l.entry.generation != l.generation || l.entry.fingerprint != l.fingerprint {
		p.mu.Unlock()
		l.mu.Unlock()
		return false
	}
	var stale *ssh.Client
	if fingerprint != l.fingerprint {
		stale, l.entry.client = l.entry.client, nil
		l.entry.fingerprint = fingerprint
		l.entry.generation++
		l.fingerprint, l.generation = fingerprint, l.entry.generation
		l.cacheable = false
	}
	p.mu.Unlock()
	l.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}
	return true
}

// IsCurrent guards admission of every service primitive. An invalidation
// retires a borrowed connection even when its current primitive may finish.
func (p *Pool) IsCurrent(serverID uint, generation uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.conns[serverID]
	return !p.closed && entry != nil && entry.generation == generation
}

func (p *Pool) ReleaseHandle(h *Handle) {
	if h != nil {
		h.Release()
	}
}
func (p *Pool) DiscardHandle(h *Handle) {
	if h != nil {
		h.Discard()
	}
}

// DialContext links an in-progress dial to both its request and pool
// shutdown. Call cleanup after ownership has transferred to SetClient (or the
// dial failed). Cleanup stops or joins the pool callback before cancelling
// the derived context; a successful route has already detached its temporary
// cancellation watchers, so this does not close an established client.
func (l *Lease) DialContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()
	if done || l.pool.shutdown.Err() != nil {
		cancel()
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(l.pool.shutdown, func() {
		defer close(joined)
		cancel()
	})
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if !stop() {
				<-joined
			}
			cancel()
		})
	}
	return ctx, cleanup
}

// SetClient transfers ownership of a newly dialed client to the lease.
func (l *Lease) SetClient(client *ssh.Client) {
	l.mu.Lock()
	old := l.Client
	if l.done {
		l.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return
	}
	select {
	case <-l.pool.done:
		l.Client = nil
		l.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		if client != nil && client != old {
			_ = client.Close()
		}
		return
	default:
	}
	l.Client = client
	l.mu.Unlock()
	if old != nil && old != client {
		_ = old.Close()
	}
}

func (l *Lease) Release() { l.finish(false) }
func (l *Lease) Discard() { l.finish(true) }
func (l *Lease) finish(discard bool) {
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	client := l.Client
	l.Client = nil
	l.mu.Unlock()
	p := l.pool
	p.mu.Lock()
	delete(p.leases, l)
	if l.legacy {
		pending := p.legacy[l.serverID]
		for i, candidate := range pending {
			if candidate == l {
				pending = append(pending[:i], pending[i+1:]...)
				break
			}
		}
		if len(pending) == 0 {
			delete(p.legacy, l.serverID)
		} else {
			p.legacy[l.serverID] = pending
		}
	}
	if !discard && l.cacheable && !p.closed && p.conns[l.serverID] == l.entry && l.entry.generation == l.generation && l.entry.fingerprint == l.fingerprint && l.entry.client == nil {
		l.entry.client = client
		l.entry.lastUsed = time.Now()
		client = nil
	}
	p.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	p.releaseSlot(l.entry.sem, l.consumer)
}

// Get and its return-by-serverID counterparts remain for legacy callers.
// New code must retain a Lease: a newly dialed client cannot be unambiguously
// associated with a serverID-only Get when multiple generations overlap.
func (p *Pool) Get(serverID uint, fingerprint string) (*ssh.Client, error) {
	return p.GetContext(context.Background(), serverID, fingerprint)
}
func (p *Pool) GetContext(ctx context.Context, serverID uint, fingerprint string) (*ssh.Client, error) {
	lease, err := p.AcquireContext(ctx, serverID, fingerprint)
	if err != nil {
		return nil, err
	}
	return lease.LegacyClient(), nil
}

// LegacyClient opts this lease into the serverID-only compatibility API.
// Use only when an older caller cannot retain the lease token.
func (l *Lease) LegacyClient() *ssh.Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return nil
	}
	if l.legacy {
		return l.Client
	}
	l.legacy = true
	l.original = l.Client
	p := l.pool
	p.mu.Lock()
	if l.original == nil {
		for _, previous := range p.legacy[l.serverID] {
			if previous.original == nil && (previous.entry != l.entry || previous.generation != l.generation) {
				// New clients have no token in the legacy API. All overlapping,
				// unidentifiable generations must conservatively avoid caching.
				l.cacheable = false
				previous.cacheable = false
			}
		}
	}
	p.legacy[l.serverID] = append(p.legacy[l.serverID], l)
	p.mu.Unlock()
	return l.Client
}

func (p *Pool) legacyLease(serverID uint, client *ssh.Client) *Lease {
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := p.legacy[serverID]
	index := -1
	if client != nil {
		for i, lease := range pending {
			if lease.original == client {
				index = i
				break
			}
		}
	}
	if index < 0 {
		for i, lease := range pending {
			if lease.original == nil {
				index = i
				break
			}
		}
	}
	if index < 0 {
		return nil
	}
	lease := pending[index]
	p.legacy[serverID] = append(pending[:index], pending[index+1:]...)
	if len(p.legacy[serverID]) == 0 {
		delete(p.legacy, serverID)
	}
	return lease
}
func (p *Pool) Release(serverID uint, client *ssh.Client) {
	lease := p.legacyLease(serverID, client)
	if lease == nil {
		if client != nil {
			_ = client.Close()
		}
		return
	}
	lease.SetClient(client)
	lease.Release()
}
func (p *Pool) Discard(serverID uint, client *ssh.Client) {
	lease := p.legacyLease(serverID, client)
	if lease == nil {
		if client != nil {
			_ = client.Close()
		}
		return
	}
	lease.SetClient(client)
	lease.Discard()
}

// Invalidate retires cached and borrowed connections without interrupting an
// operation already using a lease. Old generations are closed on return.
func (p *Pool) Invalidate(serverID uint) {
	p.mu.Lock()
	entry := p.conns[serverID]
	var client *ssh.Client
	if entry != nil {
		client, entry.client = entry.client, nil
		entry.fingerprint = ""
		entry.generation++
	}
	p.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// Close closes both idle transports and active transports during Hub shutdown.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.done)
		clients := make([]*ssh.Client, 0, len(p.conns))
		for _, entry := range p.conns {
			if entry.client != nil {
				clients = append(clients, entry.client)
				entry.client = nil
			}
		}
		active := make([]*Lease, 0, len(p.leases))
		for lease := range p.leases {
			active = append(active, lease)
		}
		p.conns = nil
		p.mu.Unlock()
		p.cancelShutdown()
		for _, client := range clients {
			_ = client.Close()
		}
		for _, lease := range active {
			lease.mu.Lock()
			client := lease.Client
			lease.mu.Unlock()
			if client != nil {
				_ = client.Close()
			}
		}
	})
}

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
func (p *Pool) evict() {
	p.mu.Lock()
	var clients []*ssh.Client
	for id, entry := range p.conns {
		if entry.client != nil && time.Since(entry.lastUsed) > p.maxIdle {
			clients = append(clients, entry.client)
			entry.client = nil
		}
		if entry.client == nil && len(entry.sem) == 0 && entry.waiters == 0 {
			delete(p.conns, id)
		}
	}
	p.mu.Unlock()
	for _, client := range clients {
		_ = client.Close()
	}
}
func keepAliveProbe(client *ssh.Client) bool {
	done := make(chan error, 1)
	go func() { _, _, err := client.SendRequest("keepalive@talus.local", true, nil); done <- err }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(probeTimeout):
		_ = client.Close()
		<-done
		return false
	}
}

// QuotaObserver is notified whenever a borrow takes or returns a concurrency slot.
// The report happens where the slot really changes hands - the reservation and the
// release, including every failure path in between - so a borrow that is still
// probing or dialing is already counted (P2-02).
type QuotaObserver func(consumer string, delta int)

// SetQuotaObserver installs the concurrency observer. It is called once per
// reservation and once per release, always with the consumer label the borrow
// declared (empty when it declared none).
func (p *Pool) SetQuotaObserver(observer QuotaObserver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quotaObserver = observer
}

// SetQuotaObserver is nil-safe: a nil observer (or a nil pool) reports nothing.
func (p *Pool) notifyQuota(consumer string, delta int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	observer := p.quotaObserver
	p.mu.Unlock()
	if observer != nil {
		observer(consumer, delta)
	}
}

// releaseSlot returns a reserved concurrency slot and reports the release. Every
// path that gives a slot back goes through here, so the accounting cannot drift.
func (p *Pool) releaseSlot(sem chan struct{}, consumer string) {
	<-sem
	p.notifyQuota(consumer, -1)
}

// quotaConsumerKey carries the frozen consumer label of a borrow into the pool,
// which owns the slot and therefore owns the measurement.
type quotaConsumerKey struct{}

// WithQuotaConsumer labels the next borrows made with this context.
func WithQuotaConsumer(ctx context.Context, consumer string) context.Context {
	return context.WithValue(ctx, quotaConsumerKey{}, consumer)
}

// quotaConsumer reports the label a borrow declared, if any.
func quotaConsumer(ctx context.Context) string {
	consumer, _ := ctx.Value(quotaConsumerKey{}).(string)
	return consumer
}
