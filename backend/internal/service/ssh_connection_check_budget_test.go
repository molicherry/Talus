package service

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

type connectionCheckGatedSource struct {
	*routeTestSource
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (r *connectionCheckGatedSource) FindByID(ctx context.Context, id uint) (*model.Server, error) {
	var blocked bool
	r.once.Do(func() { blocked = true; close(r.entered) })
	if blocked {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	return r.routeTestSource.FindByID(ctx, id)
}

func TestConnectionTestBudgetIncludesInitialSnapshotRead(t *testing.T) {
	address, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, address, key)
	svc.sshDialTimeout = 30 * time.Millisecond
	gated := &connectionCheckGatedSource{routeTestSource: source, entered: make(chan struct{}), gate: make(chan struct{})}
	svc.serverRepo = gated
	result := make(chan error, 1)
	go func() { _, err := svc.TestConnection(context.Background(), 1); result <- err }()
	select {
	case <-gated.entered:
	case <-time.After(time.Second):
		t.Fatal("initial snapshot read did not start")
	}
	select {
	case err := <-result:
		if !errors.Is(err, server.ErrSSHTimeout) {
			t.Fatalf("test budget error=%v, want SSH timeout", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(gated.gate)
		waitErr := <-result
		t.Fatalf("connection test ignored its 30ms budget while reading the initial snapshot: %v", waitErr)
	}
}

type connectionCheckChangingSource struct {
	*routeTestSource
	reads int
}

func (r *connectionCheckChangingSource) FindConnectionConfig(ctx context.Context, id uint) (*model.ServerConnectionConfig, error) {
	cfg, err := r.routeTestSource.FindConnectionConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if id == 1 {
		r.reads++
		if r.reads == 3 {
			// The final route snapshot is already copied; another Hub commits a new
			// route immediately before the verified old lease is handed to its owner.
			r.mu.Lock()
			r.servers[1].ConnectionRevision++
			r.configs[1] = &model.ServerConnectionConfig{ServerID: 1, Mode: "socks5", SocksHost: "127.0.0.1", SocksPort: 1, SocksAuthType: "none"}
			r.mu.Unlock()
		}
	}
	return cfg, nil
}
func TestConnectionTestReportsTheRouteActuallyHandshaken(t *testing.T) {
	address, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, address, key)
	changed := &connectionCheckChangingSource{routeTestSource: source}
	svc.serverRepo = changed
	result, err := svc.TestConnection(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "direct" {
		t.Fatalf("successful test reports untested %q route; only direct SSH was handshaken", result.Mode)
	}
	source.mu.Lock()
	mode := source.configs[1].Mode
	source.mu.Unlock()
	if mode != "socks5" {
		t.Fatal("concurrent route change did not occur")
	}
}

func TestConnectionTestBudgetIncludesWaitingForQuota(t *testing.T) {
	address, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, address, key)
	current, err := source.FindByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	held, err := svc.pool.AcquireContext(context.Background(), 1, serverFingerprint(current))
	if err != nil {
		t.Fatal(err)
	}
	original := dialTestSSH(t, address, key)
	held.SetClient(original)
	defer held.Release()
	svc.sshDialTimeout = 30 * time.Millisecond
	_, err = svc.TestConnection(context.Background(), 1)
	if !errors.Is(err, server.ErrSSHTimeout) || server.StatusCode(err) != 504 {
		t.Fatalf("quota budget error=%v status=%d", err, server.StatusCode(err))
	}
	// A test timing out in the queue must not close another operation's client.
	if _, _, err := original.SendRequest("still-alive", true, nil); err != nil {
		t.Fatalf("quota waiter interrupted the active lease: %v", err)
	}
	held.Release()
	next, err := svc.pool.AcquireContext(context.Background(), 1, serverFingerprint(current))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Discard()
	if next.Client != original {
		t.Fatal("quota timeout lost the original cached transport")
	}
}

func TestConnectionTestPreservesParentCancellationCause(t *testing.T) {
	address, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, address, key)
	gated := &connectionCheckGatedSource{routeTestSource: source, entered: make(chan struct{}), gate: make(chan struct{})}
	svc.serverRepo = gated
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cause := errors.New("test owner revoked")
	result := make(chan error, 1)
	go func() { _, err := svc.TestConnection(ctx, 1); result <- err }()
	select {
	case <-gated.entered:
	case <-time.After(time.Second):
		t.Fatal("initial snapshot read did not start")
	}
	cancel(cause)
	select {
	case err := <-result:
		if !errors.Is(err, cause) {
			t.Fatalf("test error=%v, want original cause", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not interrupt the test")
	}
}

func TestConnectionTestFreshHandshakeSkipsBlockedCachedProbe(t *testing.T) {
	address, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, address, key)
	raw, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &blockedWriteConn{Conn: raw, entered: make(chan struct{}), resume: make(chan struct{}), closed: make(chan struct{})}
	transport, channels, requests, err := ssh.NewClientConn(wrapped, address, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(key)})
	if err != nil {
		_ = wrapped.Close()
		t.Fatal(err)
	}
	original := ssh.NewClient(transport, channels, requests)
	current, err := source.FindByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := svc.pool.AcquireContext(context.Background(), 1, serverFingerprint(current))
	if err != nil {
		_ = original.Close()
		t.Fatal(err)
	}
	seed.SetClient(original)
	seed.Release()
	wrapped.blockWrites()
	result, err := svc.TestConnection(context.Background(), 1)
	if err != nil {
		t.Fatalf("fresh handshake depended on the blocked old probe: %v", err)
	}
	if !result.Success || result.Mode != "direct" {
		t.Fatalf("fresh test result=%#v", result)
	}
	select {
	case <-wrapped.entered:
		t.Fatal("fresh test sent a keepalive on the obsolete connection")
	default:
	}
	select {
	case <-wrapped.closed:
	default:
		t.Fatal("fresh test retained the obsolete cached connection")
	}
}
