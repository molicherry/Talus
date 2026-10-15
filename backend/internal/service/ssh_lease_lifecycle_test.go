package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

func TestLeaseRetainsOwnershipAcrossSequentialPrimitives(t *testing.T) {
	address, hostKey := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) { drainUploadOK(ch) }))
	pool := newTestPool(t)
	original := dialTestSSH(t, address, hostKey)
	seedPool(t, pool, 1, original)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	select {
	case <-lease.Done():
		t.Fatal("new lease is already done")
	default:
	}
	for i, operation := range []func() error{
		func() error { _, err := lease.Exec(context.Background(), "probe", time.Second); return err },
		func() error {
			return lease.Upload(context.Background(), writeLocalArtifact(t), "/tmp/agent", time.Second)
		},
		func() error { _, err := lease.Exec(context.Background(), "verify", time.Second); return err },
	} {
		if err := operation(); err != nil {
			t.Fatalf("primitive %d: %v", i, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		other, err := pool.AcquireContext(ctx, 1, testFP(1))
		cancel()
		if other != nil {
			pool.DiscardHandle(other)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("primitive %d released quota before its owner: %v", i, err)
		}
	}
	lease.Release()
	lease.Release()
	lease.Cancel("already released")
	select {
	case <-lease.Done():
	case <-time.After(time.Second):
		t.Fatal("released lease is not done")
	}
	next, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	pool.Release(1, next)
	if next != original {
		t.Fatal("explicit release did not preserve the healthy client")
	}
	if _, err := lease.Exec(context.Background(), "must not execute", time.Second); err == nil {
		t.Fatal("closed lease admitted another primitive")
	}
}

func TestLeaseCancelJoinsBeforeReturningQuota(t *testing.T) {
	started := make(chan struct{})
	address, hostKey := startExecSSHServer(t, func(_ string, ch ssh.Channel, closed <-chan struct{}) {
		_, _ = io.Copy(io.Discard, ch)
		close(started)
		<-closed
	})
	pool := newTestPool(t)
	client, conn := dialExecBlockedSSH(t, address, hostKey)
	seedPool(t, pool, 1, client)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Discard()
	finished := make(chan error, 1)
	go func() { _, err := lease.Exec(context.Background(), "blocked", time.Minute); finished <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("command did not start")
	}
	if _, err := lease.Exec(context.Background(), "nested", time.Second); err == nil {
		t.Fatal("active lease admitted a nested primitive")
	}
	conn.blockWrites()
	cleanupStarted := time.Now()
	lease.Release() // An early owner release must not publish the active client.
	lease.Cancel("revoked")
	lease.Cancel("duplicate revocation")
	select {
	case <-conn.blockedWrite:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not attempt channel close")
	}
	select {
	case <-lease.Done():
		t.Fatal("lease settled while channel close was blocked")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	other, err := pool.AcquireContext(ctx, 1, testFP(1))
	cancel()
	if other != nil {
		pool.DiscardHandle(other)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy lease returned its quota: %v", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled primitive = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lease did not join within its absolute cleanup budget")
	}
	if elapsed := time.Since(cleanupStarted); elapsed > 500*time.Millisecond {
		t.Fatalf("100ms grace restarted during cleanup: %v", elapsed)
	}
	select {
	case <-lease.Done():
	case <-time.After(time.Second):
		t.Fatal("workers joined but the lease is not done")
	}
	next, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	pool.Release(1, next)
	if next != nil {
		t.Fatal("force-closed client was cached")
	}
}

func TestExecBudgetIncludesQuotaWait(t *testing.T) {
	pool := newTestPool(t)
	held, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Release(1, held)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	start := time.Now()
	result, err := svc.Exec(context.Background(), 1, "must not execute", 40*time.Millisecond)
	if result != nil || !errors.Is(err, server.ErrSSHTimeout) {
		t.Fatalf("quota timeout = (%v, %v)", result, err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("quota wait exceeded the operation budget: %v", elapsed)
	}
}

type waitingServerSource struct{ staticServerSource }

func (waitingServerSource) FindByID(ctx context.Context, _ uint) (*model.Server, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestExecBudgetIncludesServerLookup(t *testing.T) {
	svc := NewSSHService(nil, waitingServerSource{}, nil, time.Second, time.Second)
	start := time.Now()
	_, err := svc.Exec(context.Background(), 1, "must not execute", 30*time.Millisecond)
	if !errors.Is(err, server.ErrSSHTimeout) {
		t.Fatalf("lookup timeout = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("lookup exceeded the operation budget: %v", elapsed)
	}
}

func TestUploadRejectsNonRegularSourcesBeforeSSHSetup(t *testing.T) {
	svc := NewSSHService(nil, nil, nil, time.Second, time.Second)
	// A directory cannot become a blocking local read worker or an empty success.
	path := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	err, closed := svc.copyFileOnClient(context.Background(), nil, path, "/tmp/agent", time.Second)
	if err == nil || closed {
		t.Fatalf("non-regular source = (%v, %v)", err, closed)
	}
}

func TestSSHCleanupSharesAnExistingGraceDeadline(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	client, conn := dialExecBlockedSSH(t, address, hostKey)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan *ssh.Session, 1)
	ready <- session
	joined := make(chan struct{})
	close(joined)
	conn.blockWrites()
	// The caller already spent its grace joining a previous stage. Close must
	// force the transport now instead of starting another 100ms grace.
	started := time.Now()
	closed, err := closeSSHSession(client, ready, joined, newSSHCleanupDeadlines(started.Add(-time.Second), execTeardownGrace))
	if err != nil || !closed {
		t.Fatalf("expired cleanup = (%v, %v)", closed, err)
	}
	if elapsed := time.Since(started); elapsed >= execTeardownGrace {
		t.Fatalf("expired grace was restarted: %v", elapsed)
	}
}

// Even after the response cleanup deadline expires, a slow local worker still
// owns its quota. Delayed teardown consumes the original budget, not a new 3s.
func TestSSHCleanupAbsoluteDeadlineRetainsLeaseUntilJoined(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := make(chan *ssh.Session)
	joined := make(chan struct{})
	deadlines := newSSHCleanupDeadlines(time.Now().Add(-sshCleanupBudget), execTeardownGrace)
	type completion struct {
		closed bool
		err    error
	}
	result := make(chan completion, 1)
	go func() {
		closed, err := closeSSHSession(lease.client(), ready, joined, deadlines)
		result <- completion{closed, err}
	}()
	var cleanupErr error
	select {
	case got := <-result:
		cleanupErr = got.err
		var pending *sshCleanupPending
		if !got.closed || !errors.As(got.err, &pending) {
			close(joined)
			lease.end(true, nil)
			lease.Discard()
			t.Fatalf("expired cleanup = (%v, %v)", got.closed, got.err)
		}
	case <-time.After(500 * time.Millisecond):
		close(joined)
		lease.end(true, nil)
		lease.Discard()
		t.Fatal("entering cleanup restarted the expired 3s total")
	}
	lease.end(true, cleanupErr)
	lease.Release()
	lease.Cancel("duplicate cancellation")
	select {
	case <-lease.Done():
		t.Fatal("deadline exhaustion published Done before workers joined")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	other, err := pool.AcquireContext(ctx, 1, testFP(1))
	cancel()
	if other != nil {
		pool.DiscardHandle(other)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline exhaustion released active quota: %v", err)
	}
	close(joined)
	select {
	case <-lease.Done():
	case <-time.After(time.Second):
		t.Fatal("joined workers did not settle the pending lease")
	}
}

type leaseBlockedCloseConn struct {
	net.Conn
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (c *leaseBlockedCloseConn) Close() error {
	c.once.Do(func() { close(c.entered) })
	<-c.resume
	return c.Conn.Close()
}

func TestLeaseSettlementUsesExistingCleanupDeadline(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn := &leaseBlockedCloseConn{Conn: raw, entered: make(chan struct{}), resume: make(chan struct{})}
	var resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(conn.resume) }) }
	defer unblock()
	transport, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(transport, channels, requests)
	pool := newTestPool(t)
	// The peer connection must unblock before pool cleanup attempts to close it.
	t.Cleanup(unblock)
	seedPool(t, pool, 1, client)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	lease.cleanup = &sshCleanupClock{}
	lease.cleanup.once.Do(func() {
		// Simulate a caller that already spent most of its absolute 3s budget.
		lease.cleanup.value = newSSHCleanupDeadlines(time.Now().Add(-sshCleanupBudget+80*time.Millisecond), execTeardownGrace)
	})
	cancelled := make(chan struct{})
	go func() { lease.Cancel("idle lease revoked"); close(cancelled) }()
	select {
	case <-cancelled:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Cancel blocked on transport close")
	}
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("settlement did not close the discarded transport")
	}
	start := time.Now()
	var pending *sshCleanupPending
	if err := lease.releaseAndWait(); !errors.As(err, &pending) {
		t.Fatalf("blocked settlement result = %v", err)
	}
	var appErr *server.AppError
	if errors.Is(pending, context.Canceled) || errors.Is(pending, context.DeadlineExceeded) ||
		!errors.As(pending, &appErr) || appErr.Code != http.StatusServiceUnavailable || appErr.Params["state"] != "ssh_cleanup_pending" {
		t.Fatalf("cleanup-only error must remain a server lifecycle failure: %v", pending)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("settlement restarted the cleanup budget: %s", elapsed)
	}
	select {
	case <-lease.Done():
		t.Fatal("blocked close reported completed settlement")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	other, err := pool.AcquireContext(ctx, 1, testFP(1))
	cancel()
	if other != nil {
		pool.DiscardHandle(other)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked close released quota: %v", err)
	}
	unblock()
	select {
	case <-lease.Done():
	case <-time.After(time.Second):
		t.Fatal("completed close did not return quota")
	}
}

func TestExecCleanupPendingPreservesOriginalReason(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "server_timeout", cause: server.ErrSSHTimeout},
		{name: "caller_cancelled", cause: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, closed <-chan struct{}) {
				_, _ = io.Copy(io.Discard, channel)
				close(started)
				<-closed
			})
			raw, err := net.Dial("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			writes := &gatedConn{Conn: raw}
			conn := &leaseBlockedCloseConn{Conn: writes, entered: make(chan struct{}), resume: make(chan struct{})}
			var resumeOnce sync.Once
			unblock := func() { resumeOnce.Do(func() { close(conn.resume) }) }
			defer unblock()
			transport, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			client := ssh.NewClient(transport, channels, requests)
			defer func() { unblock(); _ = client.Close() }()
			clock := &sshCleanupClock{}
			parent, cancel := context.WithCancelCause(context.WithValue(context.Background(), sshCleanupClockKey{}, clock))
			defer cancel(context.Canceled)
			done := make(chan error, 1)
			svc := NewSSHService(nil, nil, nil, time.Second, time.Second)
			go func() { _, err, _ := svc.runCommand(parent, client, "wait", time.Minute); done <- err }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("command did not start")
			}
			writes.blockWrites()
			clock.once.Do(func() {
				clock.value = newSSHCleanupDeadlines(time.Now().Add(-sshCleanupBudget+150*time.Millisecond), execTeardownGrace)
			})
			cancel(test.cause)
			var pending *sshCleanupPending
			select {
			case err := <-done:
				if !errors.As(err, &pending) || !errors.Is(err, test.cause) {
					t.Fatalf("cleanup lost the original outcome: %v", err)
				}
				if errors.Is(err, context.DeadlineExceeded) || (test.cause == server.ErrSSHTimeout && errors.Is(err, context.Canceled)) {
					t.Fatalf("server cleanup was classified as caller cancellation: %v", err)
				}
				if test.cause == server.ErrSSHTimeout && server.StatusCode(err) != http.StatusGatewayTimeout {
					t.Fatalf("cleanup replaced the original timeout status: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending cleanup restarted the absolute budget")
			}
			unblock()
			select {
			case <-pending.done:
			case <-time.After(time.Second):
				t.Fatal("cleanup workers did not settle after transport close")
			}
		})
	}
}

// TestRereviewLeaseGaugeFollowsThePoolSlot is the N03 accounting reproduction: the
// gauge is owned by the SSH layer, so it rises when the pool slot is taken and only
// falls when the slot is really back. While a settlement is pending the foreground
// result must not report the resource as reclaimed.
func TestRereviewLeaseGaugeFollowsThePoolSlot(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn := &leaseBlockedCloseConn{Conn: raw, entered: make(chan struct{}), resume: make(chan struct{})}
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(conn.resume) }) }
	defer unblock()
	transport, channels, requests, err := ssh.NewClientConn(conn, address,
		&ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pool := newTestPool(t)
	t.Cleanup(unblock)
	seedPool(t, pool, 1, ssh.NewClient(transport, channels, requests))

	var mu sync.Mutex
	inUse := 0.0
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	svc.SetLeaseObserver(func(_ LeaseConsumer, delta float64) {
		mu.Lock()
		inUse += delta
		mu.Unlock()
	})
	gauge := func() float64 {
		mu.Lock()
		defer mu.Unlock()
		return inUse
	}

	lease, err := svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
	if err != nil {
		t.Fatal(err)
	}
	if got := gauge(); got != 1 {
		t.Fatalf("gauge after acquire = %v, want 1", got)
	}

	// The transport close blocks, so the slot is still held while the foreground
	// gives up on it.
	lease.Cancel("collection abandoned")
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("settlement did not reach the transport close")
	}
	select {
	case <-lease.Done():
		t.Fatal("settlement reported done while the close was still blocked")
	default:
	}
	if got := gauge(); got != 1 {
		t.Fatalf("gauge while settlement is pending = %v, want 1: returning the foreground "+
			"result must not report the slot as reclaimed (N03)", got)
	}

	unblock()
	select {
	case <-lease.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("settlement did not finish after the transport close was unblocked")
	}
	if got := gauge(); got != 0 {
		t.Fatalf("gauge after settlement = %v, want 0", got)
	}
	// Repeated release calls must not book the slot twice.
	lease.Release()
	lease.Cancel("duplicate")
	if got := gauge(); got != 0 {
		t.Fatalf("gauge after repeated release = %v, want 0", got)
	}
}

// TestRereviewSettlementDeadlineIsSharedWithTheOwner is the N03 budget
// reproduction: a lease that already spent its absolute budget reports a deadline
// inside that same budget, and the monitor's wait consumes it instead of starting a
// fresh 3s timer.
func TestRereviewSettlementDeadlineIsSharedWithTheOwner(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn := &leaseBlockedCloseConn{Conn: raw, entered: make(chan struct{}), resume: make(chan struct{})}
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(conn.resume) }) }
	defer unblock()
	transport, channels, requests, err := ssh.NewClientConn(conn, address,
		&ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pool := newTestPool(t)
	t.Cleanup(unblock)
	seedPool(t, pool, 1, ssh.NewClient(transport, channels, requests))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a resource owner that already consumed all but ~80ms of the 3s
	// absolute teardown budget.
	start := time.Now().Add(-sshCleanupBudget + 80*time.Millisecond)
	lease.mu.Lock()
	lease.cleanup = &sshCleanupClock{}
	lease.cleanup.once.Do(func() {
		lease.cleanup.value = newSSHCleanupDeadlines(start, execTeardownGrace)
	})
	lease.mu.Unlock()

	deadline := lease.SettlementDeadline()
	if remaining := time.Until(deadline); remaining > 400*time.Millisecond {
		t.Fatalf("settlement deadline is %v away, want the remainder of the original budget "+
			"(the nested wait must not restart it)", remaining)
	}

	// The monitor's wait consumes that deadline: the transport close stays blocked,
	// so the wait has to give up inside the remaining budget instead of 3 more
	// seconds.
	monitor := NewMonitorService(nil, nil, nil, time.Minute)
	lease.Cancel("collection abandoned")
	waitStart := time.Now()
	if monitor.awaitLeaseSettlement(lease) {
		t.Fatal("a lease whose settlement is blocked must be reported as pending")
	}
	if elapsed := time.Since(waitStart); elapsed > 700*time.Millisecond {
		t.Fatalf("nested wait took %v: the SSH teardown plus the monitor wait must stay "+
			"inside one 3s budget (N03)", elapsed)
	}
	unblock()
	select {
	case <-lease.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("settlement did not finish after unblocking")
	}
}

// blockedWriteConn stops writing after the SSH handshake has completed, so a
// cached connection's keepalive probe can be held inside the write.
type blockedWriteConn struct {
	net.Conn
	mu      sync.Mutex
	blocked bool
	entered chan struct{}
	resume  chan struct{}
	closed  chan struct{}
	once    sync.Once
	closeOn sync.Once
}

func (c *blockedWriteConn) blockWrites() {
	c.mu.Lock()
	c.blocked = true
	c.mu.Unlock()
}

// Write holds writes until the test lets them through. A close releases them too,
// like a real socket: a cancelled borrow closes the transport and must be able to
// join the probe instead of waiting for it forever.
func (c *blockedWriteConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	blocked := c.blocked
	c.mu.Unlock()
	if blocked {
		c.once.Do(func() { close(c.entered) })
		select {
		case <-c.resume:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *blockedWriteConn) Close() error {
	err := c.Conn.Close()
	c.closeOn.Do(func() { close(c.closed) })
	return err
}

// TestRereviewQuotaGaugeCountsTheProbePhase is the P2-02 reproduction: the slot is
// reserved before the cached-connection probe, so a borrow that is stuck in that
// probe already holds quota and must already be counted. The gauge used to start
// only once a lease existed, so the whole probe/dial window was invisible.
func TestRereviewQuotaGaugeCountsTheProbePhase(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn := &blockedWriteConn{Conn: raw, entered: make(chan struct{}),
		resume: make(chan struct{}), closed: make(chan struct{})}
	var resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(conn.resume) }) }
	defer unblock()
	transport, channels, requests, err := ssh.NewClientConn(conn, address,
		&ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pool := sshpool.NewPool(time.Minute, 1, time.Second)
	t.Cleanup(pool.Close)
	t.Cleanup(unblock)
	// The pool holds one cached connection; writing on it is blocked from now on, so
	// the next borrow's keepalive probe cannot complete.
	seedPool(t, pool, 1, ssh.NewClient(transport, channels, requests))
	conn.blockWrites()

	var mu sync.Mutex
	inUse := 0
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	svc.SetLeaseObserver(func(_ LeaseConsumer, delta float64) {
		mu.Lock()
		inUse += int(delta)
		mu.Unlock()
	})
	gauge := func() int {
		mu.Lock()
		defer mu.Unlock()
		return inUse
	}

	type acquired struct {
		lease *sshLease
		err   error
	}
	result := make(chan acquired, 1)
	go func() {
		lease, err := svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
		result <- acquired{lease, err}
	}()

	// The probe is stuck in the write: the slot is already reserved, so the gauge
	// has to be 1 while the borrow is still in flight.
	select {
	case <-conn.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the keepalive probe never reached the blocked write")
	}
	if got := gauge(); got != 1 {
		t.Fatalf("gauge during the cached-connection probe = %d, want 1: the slot is "+
			"already reserved, so the probe/dial phase must be counted (P2-02)", got)
	}

	// The reservation is real: a second borrow cannot get the only slot.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	other, err := pool.AcquireContext(ctx, 1, testFP(1))
	cancel()
	if other != nil {
		pool.DiscardHandle(other)
	}
	if err == nil {
		t.Fatal("a second borrow obtained the slot while the first was still probing")
	}

	unblock()
	var lease *sshLease
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("acquire after unblocking: %v", got.err)
		}
		lease = got.lease
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not finish after the write was unblocked")
	}
	if got := gauge(); got != 1 {
		t.Fatalf("gauge after the lease was handed out = %d, want 1", got)
	}
	lease.Release()
	select {
	case <-lease.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the lease never settled")
	}
	deadline := time.Now().Add(2 * time.Second)
	for gauge() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := gauge(); got != 0 {
		t.Fatalf("gauge after settlement = %d, want 0", got)
	}
}

// TestRereviewQuotaGaugeBalancesFailedAcquire pins the other half of P2-02: every
// failure exit between reserving a slot and handing out a lease must give the slot
// back exactly once, so a failed borrow cannot leak the gauge or the quota.
func TestRereviewQuotaGaugeBalancesFailedAcquire(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, _ ssh.Channel, _ <-chan struct{}) {})
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn := &blockedWriteConn{Conn: raw, entered: make(chan struct{}),
		resume: make(chan struct{}), closed: make(chan struct{})}
	var resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(conn.resume) }) }
	defer unblock()
	transport, channels, requests, err := ssh.NewClientConn(conn, address,
		&ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pool := sshpool.NewPool(time.Minute, 1, time.Second)
	t.Cleanup(pool.Close)
	t.Cleanup(unblock)
	seedPool(t, pool, 1, ssh.NewClient(transport, channels, requests))
	conn.blockWrites()

	var mu sync.Mutex
	inUse := 0
	pool.SetQuotaObserver(func(_ string, delta int) {
		mu.Lock()
		inUse += delta
		mu.Unlock()
	})
	gauge := func() int {
		mu.Lock()
		defer mu.Unlock()
		return inUse
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		handle, err := pool.AcquireContext(sshpool.WithQuotaConsumer(ctx, string(LeaseConsumerExec)), 1, testFP(1))
		if handle != nil {
			pool.DiscardHandle(handle)
		}
		done <- err
	}()
	select {
	case <-conn.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe never reached the blocked write")
	}
	if got := gauge(); got != 1 {
		t.Fatalf("gauge while the slot is reserved = %d, want 1", got)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled acquire reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a cancelled acquire did not return")
	}
	if got := gauge(); got != 0 {
		t.Fatalf("gauge after the failed acquire = %d, want 0: the failure exit must "+
			"release the reservation exactly once", got)
	}
	// The quota is really free again, so the next borrow (once the write flows)
	// succeeds rather than reporting a leaked slot.
	unblock()
	next, err := pool.AcquireContext(context.Background(), 1, testFP(1))
	if err != nil {
		t.Fatalf("the slot was not returned by the failed acquire: %v", err)
	}
	if got := gauge(); got != 1 {
		t.Fatalf("gauge after the following acquire = %d, want 1", got)
	}
	pool.ReleaseHandle(next)
	if got := gauge(); got != 0 {
		t.Fatalf("gauge after release = %d, want 0", got)
	}
}
