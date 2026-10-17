package service

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	appcrypto "github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

type routeTestSource struct {
	mu         sync.Mutex
	servers    map[uint]*model.Server
	configs    map[uint]*model.ServerConnectionConfig
	read       chan struct{}
	commitHook func(*model.Server, string)
}

func cloneRouteServer(srv *model.Server) *model.Server {
	if srv == nil {
		return nil
	}
	c := *srv
	if srv.Credential != nil {
		cred := *srv.Credential
		c.Credential = &cred
	}
	return &c
}
func (r *routeTestSource) FindByID(_ context.Context, id uint) (*model.Server, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.read != nil {
		select {
		case r.read <- struct{}{}:
		default:
		}
	}
	srv := r.servers[id]
	if srv == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return cloneRouteServer(srv), nil
}
func (r *routeTestSource) FindConnectionConfig(_ context.Context, id uint) (*model.ServerConnectionConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cfg := r.configs[id]; cfg != nil {
		c := *cfg
		return &c, nil
	}
	return nil, nil
}
func (r *routeTestSource) CommitSSHRouteHostKey(_ context.Context, route *repository.SSHRouteSnapshot, resourceID uint, seen []byte, kind string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	expected := route.Target
	if route.Jump != nil && route.Jump.ID == resourceID {
		expected = route.Jump
	}
	if r.commitHook != nil {
		r.commitHook(r.servers[expected.ID], kind)
	}
	current := &sshRouteSnapshot{target: r.servers[route.Target.ID], config: r.configs[route.Target.ID]}
	if current.config != nil && current.config.JumpServerID != nil {
		current.jump = r.servers[*current.config.JumpServerID]
	}
	if current.target == nil || current.fingerprint() != (&sshRouteSnapshot{target: route.Target, config: route.Config, jump: route.Jump}).fingerprint() {
		return false, nil
	}
	srv := r.servers[expected.ID]
	if srv == nil || serverFingerprint(srv) != serverFingerprint(expected) {
		return false, nil
	}
	srv.ConnectionRevision++
	switch kind {
	case "pin":
		key := append([]byte(nil), seen...)
		srv.HostKey = &key
		srv.HostKeySeen, srv.HostKeyMismatchAt = nil, nil
	case "clear":
		srv.HostKeySeen, srv.HostKeyMismatchAt = nil, nil
	case "mismatch":
		key := append([]byte(nil), seen...)
		now := time.Now()
		srv.HostKeySeen, srv.HostKeyMismatchAt = &key, &now
	}
	return true, nil
}
func routeTestFixture(t *testing.T, address string, key ssh.PublicKey) (*SSHService, *routeTestSource) {
	t.Helper()
	master, err := appcrypto.NewMasterKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	salt := []byte("0123456789abcdef")
	encrypted, err := appcrypto.Encrypt([]byte("password"), master.DeriveKey(salt))
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	credID := uint(1)
	srv := &model.Server{BaseModel: model.BaseModel{ID: 1}, Host: host, Port: port, ConnectionRevision: 1, CredentialID: &credID, Credential: &model.SSHCredential{BaseModel: model.BaseModel{ID: credID}, Username: "test", AuthType: "password", EncryptedPassword: &encrypted, Salt: salt}}
	if key != nil {
		marshaled := key.Marshal()
		srv.HostKey = &marshaled
	}
	source := &routeTestSource{servers: map[uint]*model.Server{1: srv}, configs: make(map[uint]*model.ServerConnectionConfig)}
	svc := NewSSHService(newTestPool(t), source, &CredentialService{masterKey: master}, time.Second, time.Second)
	return svc, source
}

// Simulate the separate, explicit trust action; real persistence is covered by
// TestFirstHostKeyConfirmationLifecycle with the repository and admin service.
func trustRouteFixture(t *testing.T, source *routeTestSource, id uint) {
	t.Helper()
	source.mu.Lock()
	defer source.mu.Unlock()
	srv := source.servers[id]
	if srv.HostKey != nil || srv.HostKeySeen == nil {
		t.Fatal("fixture has no untrusted candidate")
	}
	srv.HostKey, srv.HostKeySeen, srv.HostKeyMismatchAt = srv.HostKeySeen, nil, nil
	srv.ConnectionRevision++
}

func TestSSHRouteRequiresExplicitFirstHostKeyConfirmation(t *testing.T) {
	addr, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, addr, nil)
	for attempt := 0; attempt < 2; attempt++ {
		lease, err := svc.GetLease(context.Background(), 1)
		if lease != nil || !errors.Is(err, server.ErrSSHHostKeyUnverified) {
			t.Fatalf("untrusted connection = %v, %v", lease, err)
		}
		current, _ := source.FindByID(context.Background(), 1)
		if current.ConnectionRevision != 2 || current.HostKey != nil || current.HostKeySeen == nil || string(*current.HostKeySeen) != string(key.Marshal()) {
			t.Fatalf("pending state = %#v", current)
		}
	}
	trustRouteFixture(t, source, 1)
	lease, err := svc.GetLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lease.Client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	current, _ := source.FindByID(context.Background(), 1)
	cached, err := svc.pool.Get(1, serverFingerprint(current))
	if err != nil {
		t.Fatal(err)
	}
	svc.pool.Release(1, cached)
	if cached == nil {
		t.Fatal("verified connection was not cached")
	}
}

func TestSSHRouteRechecksConfigurationAfterWaitingForSlot(t *testing.T) {
	firstAddr, firstKey := startTestSSHServer(t)
	secondAddr, secondKey := startTestSSHServer(t)
	svc, source := routeTestFixture(t, firstAddr, firstKey)
	source.mu.Lock()
	initial := cloneRouteServer(source.servers[1])
	source.mu.Unlock()
	held, err := svc.pool.AcquireContext(context.Background(), 1, serverFingerprint(initial))
	if err != nil {
		t.Fatal(err)
	}
	held.SetClient(dialTestSSH(t, firstAddr, firstKey))
	source.read = make(chan struct{}, 1)
	got := make(chan *sshpool.Lease, 1)
	failures := make(chan error, 1)
	go func() {
		lease, err := svc.GetLease(context.Background(), 1)
		if err != nil {
			failures <- err
		} else {
			got <- lease
		}
	}()
	<-source.read
	host, portText, _ := net.SplitHostPort(secondAddr)
	port, _ := strconv.Atoi(portText)
	key := secondKey.Marshal()
	source.mu.Lock()
	source.servers[1].Host, source.servers[1].Port, source.servers[1].HostKey = host, port, &key
	source.servers[1].ConnectionRevision++
	source.mu.Unlock()
	held.Release()
	select {
	case lease := <-got:
		defer lease.Release()
		if lease.Client.RemoteAddr().String() != secondAddr {
			t.Fatalf("stale target returned: %s", lease.Client.RemoteAddr())
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("configuration retry did not finish")
	}
}

func TestSSHConnectionTestReplacesCacheAndReturnsItsSlot(t *testing.T) {
	addr, key := startTestSSHServer(t)
	svc, source := routeTestFixture(t, addr, key)
	current, _ := source.FindByID(context.Background(), 1)
	lease, err := svc.pool.AcquireContext(context.Background(), 1, serverFingerprint(current))
	if err != nil {
		t.Fatal(err)
	}
	original := dialTestSSH(t, addr, key)
	lease.SetClient(original)
	lease.Release()
	result, err := svc.TestConnection(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Mode != "direct" {
		t.Fatalf("test result = %#v", result)
	}
	closed := make(chan error, 1)
	go func() { closed <- original.Wait() }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("test reused cached connection")
	}
	cached, err := svc.pool.Get(1, serverFingerprint(current))
	if err != nil {
		t.Fatal(err)
	}
	svc.pool.Release(1, cached)
	if cached != nil {
		t.Fatal("test left its fresh connection cached")
	}
}

func TestSSHJumpMismatchUpdatesOnlyJumpIdentity(t *testing.T) {
	svc, source := routeTestFixture(t, "127.0.0.1:22", nil)
	source.mu.Lock()
	jump := cloneRouteServer(source.servers[1])
	jump.ID = 2
	pinned := []byte("pinned-jump-key")
	jump.HostKey = &pinned
	source.servers[2] = jump
	source.mu.Unlock()
	target, _ := source.FindByID(context.Background(), 1)
	presented := []byte("new-jump-key")
	svc.recordMismatch(context.Background(), &sshRouteSnapshot{target: target, jump: cloneRouteServer(jump)}, &sshpool.RouteError{Role: "jump", Stage: "jump_host_key", Reason: server.ReasonSSHHostKeyMismatch, Cause: &sshpool.HostKeyMismatchError{Presented: presented}})
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.servers[1].HostKeySeen != nil || source.servers[1].ConnectionRevision != 1 {
		t.Fatal("jump key recorded against target")
	}
	if source.servers[2].HostKeySeen == nil || string(*source.servers[2].HostKeySeen) != string(presented) || source.servers[2].ConnectionRevision != 2 {
		t.Fatal("jump warning not recorded")
	}
}

func TestSSHRouteRejectsConcurrentIdentityEditDuringDiscovery(t *testing.T) {
	addr, _ := startTestSSHServer(t)
	svc, source := routeTestFixture(t, addr, nil)
	source.commitHook = func(srv *model.Server, kind string) {
		if kind == "mismatch" {
			srv.ConnectionRevision++
			srv.Port = 1
		}
	}
	_, err := svc.GetLease(context.Background(), 1)
	if err == nil {
		t.Fatal("concurrent endpoint change accepted")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.servers[1].HostKey != nil || source.servers[1].HostKeySeen != nil {
		t.Fatal("old endpoint key recorded after concurrent edit")
	}
}

func TestSafeSSHRouteErrorOmitsUpstreamSecrets(t *testing.T) {
	err := safeRouteError(context.Background(), &sshpool.RouteError{Role: "proxy", Stage: "proxy_auth", Reason: "proxy_auth_failed", Cause: errors.New("secret-user:secret-password@proxy")})
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("secret exposed: %v", err)
	}
	var app *server.AppError
	if !errors.As(err, &app) || app.Params["stage"] != "proxy_auth" || app.Params["role"] != "proxy" {
		t.Fatalf("missing route diagnostics: %v", err)
	}
}

func TestSSHServicePoolCloseCancelsPendingProxyHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started, closed := make(chan struct{}), make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(closed)
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			return
		}
		methods := make([]byte, int(greeting[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}
		close(started)
		// Never answer the SOCKS greeting. Only pool shutdown should interrupt it.
		_, _ = io.Copy(io.Discard, conn)
	}()
	svc, source := routeTestFixture(t, "127.0.0.1:22", nil)
	configureServiceSOCKS(t, source, listener.Addr().String())
	svc.sshDialTimeout = time.Minute
	done := make(chan error, 1)
	go func() {
		lease, err := svc.GetLease(context.Background(), 1)
		if lease != nil {
			lease.Discard()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy handshake did not start")
	}
	svc.pool.Close()
	select {
	case err := <-done:
		if !errors.Is(err, sshpool.ErrClosed) {
			t.Fatalf("shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending SSH dial ignored pool shutdown")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("pending proxy socket remained open")
	}
}

func TestExplicitTrustSupportsSequentialLeasePrimitives(t *testing.T) {
	address, _ := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
		_, _ = io.WriteString(ch, command)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	svc, source := routeTestFixture(t, address, nil)
	lease, err := svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
	if lease != nil || !errors.Is(err, server.ErrSSHHostKeyUnverified) {
		t.Fatalf("untrusted agent lease = %v, %v", lease, err)
	}
	trustRouteFixture(t, source, 1)
	lease, err = svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, command := range []string{"probe", "verify"} {
		result, err := lease.Exec(context.Background(), command, time.Second)
		if err != nil {
			t.Fatalf("verified lease %s = %v", command, err)
		}
		if result.Stdout != command {
			t.Fatalf("primitive output = %q", result.Stdout)
		}
	}
	if err := lease.releaseAndWait(); err != nil {
		t.Fatal(err)
	}
	current, err := source.FindByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := svc.pool.Get(1, serverFingerprint(current))
	if err != nil {
		t.Fatal(err)
	}
	svc.pool.Release(1, cached)
	if cached == nil {
		t.Fatal("verified transport was not cached")
	}
}
