package sshpool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestLeaseQuotaCountsProbeAndBalancesCancellation(t *testing.T) {
	p := leaseTestPool(t, 1)
	client, conn := probeTestClient()
	seed, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	seed.SetClient(client)
	seed.Release()
	started := make(chan struct{})
	p.probe = func(*ssh.Client) bool { close(started); <-conn.closed; return false }
	var mu sync.Mutex
	count := 0
	var labels []string
	p.SetQuotaObserver(func(consumer string, delta int) {
		mu.Lock()
		count += delta
		labels = append(labels, consumer)
		mu.Unlock()
	})
	ctx, cancel := context.WithCancel(WithQuotaConsumer(context.Background(), "exec"))
	defer cancel()
	result := make(chan error, 1)
	go func() {
		h, err := p.AcquireContext(ctx, 1, "fp")
		if h != nil {
			p.DiscardHandle(h)
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	mu.Lock()
	reserved := count
	mu.Unlock()
	if reserved != 1 {
		t.Fatalf("probe has %d reserved slots, want 1", reserved)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled probe did not join")
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 0 || len(labels) != 2 || labels[0] != "exec" || labels[1] != "exec" {
		t.Fatalf("quota did not balance with original consumer: count=%d labels=%v", count, labels)
	}
}

func TestLeaseQuotaBalancesStaleAcquire(t *testing.T) {
	p := leaseTestPool(t, 1)
	total := 0
	var labels []string
	p.SetQuotaObserver(func(consumer string, delta int) {
		total += delta
		labels = append(labels, consumer)
		if delta == 1 {
			p.Invalidate(1)
		}
	})
	h, err := p.AcquireContext(WithQuotaConsumer(context.Background(), "monitor"), 1, "fp")
	if h != nil {
		h.Discard()
		t.Fatal("invalidated acquisition returned a lease")
	}
	if !errors.Is(err, ErrStaleGeneration) || !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale error = %v", err)
	}
	if total != 0 || len(labels) != 2 || labels[0] != "monitor" || labels[1] != "monitor" {
		t.Fatalf("stale acquisition quota: total=%d labels=%v", total, labels)
	}
}

func TestRebindFingerprintKeepsVerifiedLeaseAndRetiresOthers(t *testing.T) {
	p := leaseTestPool(t, 3)
	owner, err := p.AcquireContext(context.Background(), 1, "before-tofu")
	if err != nil {
		t.Fatal(err)
	}
	ownerClient, ownerConn := probeTestClient()
	owner.SetClient(ownerClient)
	old, err := p.AcquireContext(context.Background(), 1, "before-tofu")
	if err != nil {
		t.Fatal(err)
	}
	oldClient, oldConn := probeTestClient()
	old.SetClient(oldClient)
	cached, err := p.AcquireContext(context.Background(), 1, "before-tofu")
	if err != nil {
		t.Fatal(err)
	}
	cachedClient, cachedConn := probeTestClient()
	cached.SetClient(cachedClient)
	cached.Release()
	oldGen := owner.Generation()
	if !owner.RebindFingerprint("after-tofu") {
		t.Fatal("current verified lease could not adopt its own TOFU update")
	}
	if owner.Generation() == oldGen || !p.IsCurrent(owner.ServerID(), owner.Generation()) || !owner.Valid() {
		t.Fatal("verified lease did not move to the new current generation")
	}
	if old.Valid() || p.IsCurrent(old.ServerID(), old.Generation()) {
		t.Fatal("another old lease was incorrectly adopted")
	}
	select {
	case <-cachedConn.closed:
	default:
		t.Fatal("rebind retained idle connection from old fingerprint")
	}
	select {
	case <-ownerConn.closed:
		t.Fatal("rebind interrupted its active transport")
	default:
	}
	owner.Release()
	select {
	case <-ownerConn.closed:
	default:
		t.Fatal("first-use TOFU chain was cached")
	}
	old.Release()
	select {
	case <-oldConn.closed:
	default:
		t.Fatal("retired old lease was cached")
	}
	next, err := p.AcquireContext(context.Background(), 1, "after-tofu")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Discard()
	if next.Client != nil {
		t.Fatal("old first-use chain survived into the confirmed identity")
	}
}

func TestRebindFingerprintCannotReviveStaleLease(t *testing.T) {
	p := leaseTestPool(t, 1)
	h, err := p.AcquireContext(context.Background(), 1, "old")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Discard()
	p.Invalidate(1)
	if h.RebindFingerprint("external-change") {
		t.Fatal("stale lease adopted an external generation")
	}
	if p.IsCurrent(h.ServerID(), h.Generation()) {
		t.Fatal("stale generation was revived")
	}
}

func TestAcquireFreshSkipsCachedProbeAndPreservesActiveGeneration(t *testing.T) {
	p := leaseTestPool(t, 2)
	counts := make(map[string]int)
	p.SetQuotaObserver(func(consumer string, delta int) { counts[consumer] += delta })
	active, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	activeClient, activeConn := probeTestClient()
	active.SetClient(activeClient)
	defer active.Discard()
	seed, err := p.AcquireContext(context.Background(), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	cachedClient, cachedConn := probeTestClient()
	seed.SetClient(cachedClient)
	seed.Release()
	probed := false
	p.probe = func(*ssh.Client) bool { probed = true; return true }
	before := active.Generation()
	fresh, err := p.AcquireFreshContext(WithQuotaConsumer(context.Background(), "connection_test"), 1, "fp")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Discard()
	if probed {
		t.Fatal("fresh acquisition probed the obsolete cached client")
	}
	if fresh.Client != nil {
		t.Fatal("fresh acquisition returned the obsolete cached client")
	}
	select {
	case <-cachedConn.closed:
	default:
		t.Fatal("obsolete cached client remained open")
	}
	if !active.Valid() || active.Generation() != before || fresh.Generation() != before {
		t.Fatal("fresh acquisition invalidated another active lease")
	}
	select {
	case <-activeConn.closed:
		t.Fatal("fresh acquisition closed another active transport")
	default:
	}
	if counts["connection_test"] != 1 || counts[""] != 1 {
		t.Fatalf("fresh quota reservation has wrong consumer or count: %v", counts)
	}
	fresh.Discard()
	fresh.Discard()
	active.Discard()
	if counts["connection_test"] != 0 || counts[""] != 0 {
		t.Fatalf("fresh/active quota did not settle exactly once: %v", counts)
	}
}
