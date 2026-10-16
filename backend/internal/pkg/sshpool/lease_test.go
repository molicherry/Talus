package sshpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func leaseTestPool(t *testing.T, slots int) *Pool {
	t.Helper()
	p := NewPool(time.Minute, slots, time.Second)
	p.probe = func(_ *ssh.Client) bool { return true }
	t.Cleanup(p.Close)
	return p
}

func TestLeaseOldReturnCannotPolluteNewGeneration(t *testing.T) {
	p := leaseTestPool(t, 2)
	old, err := p.AcquireContext(context.Background(), 1, "old")
	if err != nil {
		t.Fatal(err)
	}
	oldClient := newTestClient(t)
	old.SetClient(oldClient)
	p.Invalidate(1)
	current, err := p.AcquireContext(context.Background(), 1, "new")
	if err != nil {
		t.Fatal(err)
	}
	old.Release()
	current.Release()
	next, err := p.AcquireContext(context.Background(), 1, "new")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Discard()
	if next.Client != nil {
		t.Fatal("old client was cached under the new fingerprint")
	}
	if old.Valid() {
		t.Fatal("released old lease remains valid")
	}
}

func TestLeaseDuplicateReleaseUsesOriginalSlotOnce(t *testing.T) {
	p := leaseTestPool(t, 1)
	lease, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	lease.SetClient(newTestClient(t))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); lease.Release(); lease.Discard() }()
	}
	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("duplicate release blocked")
	}
	next, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	next.Discard()
}

func TestLeaseWaitingAcrossInvalidationReturnsStale(t *testing.T) {
	p := leaseTestPool(t, 1)
	first, err := p.AcquireContext(context.Background(), 1, "old")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		lease, err := p.AcquireContext(context.Background(), 1, "old")
		if lease != nil {
			lease.Discard()
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		waiters := p.conns[1].waiters
		p.mu.Unlock()
		if waiters > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter did not start")
		}
		time.Sleep(time.Millisecond)
	}
	p.Invalidate(1)
	first.Release()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStaleLease) {
			t.Fatalf("wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter remained blocked")
	}
	next, err := p.AcquireContext(context.Background(), 1, "new")
	if err != nil {
		t.Fatal(err)
	}
	next.Discard()
}

func TestLeaseInvalidationDoesNotInterruptActiveClient(t *testing.T) {
	p := leaseTestPool(t, 1)
	lease, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	client, conn := probeTestClient()
	lease.SetClient(client)
	p.Invalidate(1)
	select {
	case <-conn.closed:
		t.Fatal("invalidation closed active transport")
	default:
	}
	if lease.Valid() {
		t.Fatal("invalidated lease is still valid")
	}
	lease.Release()
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("retired lease did not close transport")
	}
}

func TestLeasePoolCloseClosesBorrowedChain(t *testing.T) {
	p := leaseTestPool(t, 1)
	lease, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	client, conn := probeTestClient()
	lease.SetClient(client)
	p.Close()
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("pool close left active transport open")
	}
	lease.Release()
}

func TestLegacyOldCachedReturnAfterNewGetCannotPollute(t *testing.T) {
	p := leaseTestPool(t, 2)
	seed, err := p.AcquireContext(context.Background(), 1, "old")
	if err != nil {
		t.Fatal(err)
	}
	seed.SetClient(newTestClient(t))
	seed.Release()
	old, err := p.Get(1, "old")
	if err != nil {
		t.Fatal(err)
	}
	p.Invalidate(1)
	current, err := p.Get(1, "new")
	if err != nil {
		t.Fatal(err)
	}
	p.Release(1, old)
	p.Release(1, current)
	next, err := p.Get(1, "new")
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		p.Discard(1, next)
		t.Fatal("legacy return cached old client under new fingerprint")
	}
	p.Release(1, nil)
}

func TestLegacyUnidentifiedGenerationsNeverCacheWrongClient(t *testing.T) {
	p := leaseTestPool(t, 2)
	if _, err := p.Get(1, "old"); err != nil {
		t.Fatal(err)
	}
	p.Invalidate(1)
	if _, err := p.Get(1, "new"); err != nil {
		t.Fatal(err)
	}
	// The legacy API supplies no token for newly dialed clients. Return the
	// new generation first: both ambiguous returns must be discarded safely.
	p.Release(1, newTestClient(t))
	p.Release(1, newTestClient(t))
	next, err := p.Get(1, "new")
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		p.Discard(1, next)
		t.Fatal("ambiguous legacy return was cached")
	}
	p.Release(1, nil)
}
