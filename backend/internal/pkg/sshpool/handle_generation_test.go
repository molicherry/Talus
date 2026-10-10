package sshpool

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestStaleHandleIsNotCachedIntoNewGeneration pins the REQ-06 race: a handle
// borrowed before an invalidation must not return its client into the new
// generation's cache.
func TestStaleHandleIsNotCachedIntoNewGeneration(t *testing.T) {
	p := NewPool(time.Minute, 4, time.Second)
	t.Cleanup(p.Close)
	p.probe = func(*ssh.Client) bool { return true }

	ctx := context.Background()

	// Generation 1: cache a client.
	h1, err := p.AcquireContext(ctx, 1, "fp-A")
	if err != nil {
		t.Fatal(err)
	}
	c1 := newTestClient(t)
	h1.Client = c1
	p.ReleaseHandle(h1)

	// Borrow the cached client (still generation 1).
	hOld, err := p.AcquireContext(ctx, 1, "fp-A")
	if err != nil {
		t.Fatal(err)
	}
	if hOld.Client != c1 {
		t.Fatal("expected the cached generation-1 client")
	}

	// Parameters change: generation advances and the cached client is dropped.
	p.Invalidate(1)

	// Generation 2 caches a different client.
	hNew, err := p.AcquireContext(ctx, 1, "fp-A")
	if err != nil {
		t.Fatal(err)
	}
	if hNew.Client != nil {
		t.Fatal("an invalidated entry must not hand out the previous client")
	}
	c2 := newTestClient(t)
	hNew.Client = c2
	p.ReleaseHandle(hNew)

	// Releasing the stale handle must neither cache c1 nor evict c2.
	p.ReleaseHandle(hOld)

	hCheck, err := p.AcquireContext(ctx, 1, "fp-A")
	if err != nil {
		t.Fatal(err)
	}
	if hCheck.Client != c2 {
		t.Fatal("a stale release corrupted the new generation's cache")
	}
	p.ReleaseHandle(hCheck)
}
