package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

type serverDeleteMonitor struct {
	events        []string
	disconnected  []uint
	notifications int
	// joined makes the fake a removed-request shell that only waited for
	// another deletion, so the service must not invalidate the pool.
	joined    bool
	onPrepare func(context.Context, uint)
	onRemove  func(context.Context) error
	onClose   func()
}

// Owned reports whether this removal owns the remote cleanup.
func (m *serverDeleteMonitor) Owned() bool { return !m.joined }

func (*serverDeleteMonitor) Statuses(context.Context, []uint) map[uint]*model.MonitoringStatus {
	return nil
}

func (m *serverDeleteMonitor) PrepareAgentRemoval(ctx context.Context, id uint) preparedAgentRemoval {
	m.events = append(m.events, "prepare")
	if m.onPrepare != nil {
		m.onPrepare(ctx, id)
	}
	return m
}

func (m *serverDeleteMonitor) Remove(ctx context.Context) error {
	m.events = append(m.events, "remove")
	if m.onRemove != nil {
		return m.onRemove(ctx)
	}
	return nil
}

func (m *serverDeleteMonitor) Close() {
	m.events = append(m.events, "close")
	if m.onClose != nil {
		m.onClose()
	}
}

func (m *serverDeleteMonitor) Disconnect(id uint) {
	m.disconnected = append(m.disconnected, id)
}

func (m *serverDeleteMonitor) NotifyTopologyChange() { m.notifications++ }

func serverDeleteAgent(t *testing.T, db *gorm.DB, ctx context.Context, srv *model.Server) (*repository.AgentNodeRepo, model.AgentNode) {
	t.Helper()
	if err := db.AutoMigrate(&model.AgentNode{}); err != nil {
		t.Fatal(err)
	}
	agents := repository.NewAgentNodeRepo(db, connectionTestKey(t))
	node, err := agents.Ensure(ctx, *srv)
	if err != nil {
		t.Fatal(err)
	}
	return agents, node
}

func TestServerDeletePreservesAgentWhenJumpBoundDuringPreparation(t *testing.T) {
	svc, repo, db, ctx, pool := connectionServiceFixture(t)
	encrypted := "test-ciphertext"
	credential := &model.SSHCredential{Name: "jump-credential", AuthType: "password", Username: "jump", EncryptedPassword: &encrypted}
	if err := db.Create(credential).Error; err != nil {
		t.Fatal(err)
	}
	ownerID := mw.GetUserClaims(ctx).UserID
	jump, err := svc.Create(ctx, &model.Server{Name: "jump", Host: "jump.example", Port: 22, OwnerID: ownerID, CredentialID: &credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	agents, before := serverDeleteAgent(t, db, ctx, jump)
	monitor := &serverDeleteMonitor{}
	monitor.onPrepare = func(_ context.Context, id uint) {
		if id != jump.ID {
			t.Fatalf("prepared server %d, want %d", id, jump.ID)
		}
		// Interleave another request after deletion has begun but before its
		// transaction takes the jump-server lock. Its own notifications must
		// not be confused with notifications from the rejected delete.
		other := NewServerService(repo, nil, nil)
		other.SetMasterKey(connectionTestKey(t))
		_, err := other.Create(ctx, &model.Server{Name: "new-target", Host: "target.example", Port: 22, OwnerID: ownerID}, ConnectionMutation{
			Set: true, Config: &ConnectionInput{Mode: model.ConnectionSSHJump, JumpServerID: &jump.ID},
		})
		if err != nil {
			t.Fatalf("bind target during removal preparation: %v", err)
		}
	}
	monitor.onRemove = func(removeCtx context.Context) error { return agents.Delete(removeCtx, jump.ID) }
	svc.SetMonitoringProvider(monitor)
	svc.SetTopologyNotifier(monitor)

	expectConnectionReason(t, svc.Delete(ctx, jump.ID), server.ReasonConnectionInUse)
	if got := strings.Join(monitor.events, ","); got != "prepare,close" {
		t.Fatalf("rejected deletion removed the agent or leaked its preparation: %s", got)
	}
	if _, err := repo.FindByID(ctx, jump.ID); err != nil {
		t.Fatalf("rejected deletion removed the jump server: %v", err)
	}
	after, err := agents.Get(ctx, jump.ID)
	if err != nil || after.TokenHash != before.TokenHash || after.InstallID != before.InstallID {
		t.Fatalf("rejected deletion changed the resident credential: %v", err)
	}
	if len(pool.ids) != 0 || len(monitor.disconnected) != 0 || monitor.notifications != 0 {
		t.Fatalf("rejected deletion changed connection lifecycle: invalidations=%v disconnects=%v notifications=%d", pool.ids, monitor.disconnected, monitor.notifications)
	}
}

func TestServerDeleteCommitsBeforeRemovingAgentAndInvalidatesAfterward(t *testing.T) {
	svc, repo, db, ctx, pool := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "delete-me", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, _ := serverDeleteAgent(t, db, ctx, srv)
	monitor := &serverDeleteMonitor{}
	monitor.onPrepare = func(_ context.Context, id uint) {
		if _, err := repo.FindByID(ctx, id); err != nil {
			t.Fatalf("server disappeared before preparing its SSH removal: %v", err)
		}
	}
	monitor.onRemove = func(removeCtx context.Context) error {
		if _, err := repo.FindByID(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("agent removal preceded the server deletion commit: %v", err)
		}
		if len(pool.ids) != 0 {
			t.Fatalf("prepared SSH connection invalidated before uninstall: %v", pool.ids)
		}
		return agents.Delete(removeCtx, srv.ID)
	}
	monitor.onClose = func() {
		if len(pool.ids) != 0 {
			t.Fatalf("prepared SSH connection invalidated before it was released: %v", pool.ids)
		}
	}
	svc.SetMonitoringProvider(monitor)
	svc.SetTopologyNotifier(monitor)

	if err := svc.Delete(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(monitor.events, ","); got != "prepare,remove,close" {
		t.Fatalf("removal lifecycle = %s, want prepare,remove,close", got)
	}
	if _, err := agents.Get(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("resident credential survived removal: %v", err)
	}
	if len(pool.ids) != 1 || pool.ids[0] != srv.ID || len(monitor.disconnected) != 1 || monitor.disconnected[0] != srv.ID || monitor.notifications != 1 {
		t.Fatalf("successful deletion did not finish connection lifecycle: invalidations=%v disconnects=%v notifications=%d", pool.ids, monitor.disconnected, monitor.notifications)
	}
}

func TestServerDeleteCancellationDuringPreparationPreservesAgent(t *testing.T) {
	svc, repo, db, ctx, pool := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "keep-me", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, before := serverDeleteAgent(t, db, ctx, srv)
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	monitor := &serverDeleteMonitor{
		onPrepare: func(context.Context, uint) { cancel() },
		onRemove:  func(removeCtx context.Context) error { return agents.Delete(removeCtx, srv.ID) },
	}
	svc.SetMonitoringProvider(monitor)
	svc.SetTopologyNotifier(monitor)

	if err := svc.Delete(requestCtx, srv.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled deletion = %v, want context.Canceled", err)
	}
	if got := strings.Join(monitor.events, ","); got != "prepare,close" {
		t.Fatalf("canceled deletion removed the agent or leaked its preparation: %s", got)
	}
	if _, err := repo.FindByID(ctx, srv.ID); err != nil {
		t.Fatalf("canceled deletion removed the server: %v", err)
	}
	after, err := agents.Get(ctx, srv.ID)
	if err != nil || after.TokenHash != before.TokenHash || after.InstallID != before.InstallID {
		t.Fatalf("canceled deletion changed the resident credential: %v", err)
	}
	if len(pool.ids) != 0 || len(monitor.disconnected) != 0 || monitor.notifications != 0 {
		t.Fatalf("canceled deletion changed connection lifecycle: invalidations=%v disconnects=%v notifications=%d", pool.ids, monitor.disconnected, monitor.notifications)
	}
}

// A deletion that only joined an in-flight removal commits the row but must not
// invalidate the pool or repeat the cleanup: the owner's uninstall is still on
// that pinned connection, and dropping it would leave the agent installed.
func TestServerDeleteJoinedRemovalKeepsOwnerConnectionAlive(t *testing.T) {
	svc, repo, db, ctx, pool := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "joined", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, _ := serverDeleteAgent(t, db, ctx, srv)
	monitor := &serverDeleteMonitor{joined: true}
	monitor.onRemove = func(context.Context) error {
		t.Fatal("joined deletion repeated the remote cleanup")
		return nil
	}
	svc.SetMonitoringProvider(monitor)
	svc.SetTopologyNotifier(monitor)

	if err := svc.Delete(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("joined deletion did not commit the row: %v", err)
	}
	if len(pool.ids) != 0 {
		t.Fatalf("joined deletion invalidated the owner's connection: %v", pool.ids)
	}
	if got := strings.Join(monitor.events, ","); got != "prepare,close" {
		t.Fatalf("joined deletion lifecycle = %s, want prepare,close", got)
	}
	if _, err := agents.Get(ctx, srv.ID); err != nil {
		t.Fatalf("joined deletion removed the credential the owner still needs: %v", err)
	}
}

// Reproduces the concurrent-delete race: the second request joins the first's
// fence, commits the row, and leaves the pool alone while the owner's uninstall
// runs. Before the fix the second invalidation killed the owner's pinned
// connection, so the uninstall script never ran even though both calls returned
// success.
func TestConcurrentDeletesRunUninstallOnceAndKeepOwnerConnection(t *testing.T) {
	svc, repo, db, ctx, invalidations := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "race", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, _ := serverDeleteAgent(t, db, ctx, srv)

	uninstallStarted := make(chan struct{})
	release := make(chan struct{})
	var started sync.Once
	var uninstalls atomic.Int32
	address, hostKey := startExecSSHServer(t, func(command string, channel ssh.Channel, _ <-chan struct{}) {
		if strings.HasPrefix(command, "id -u; ") {
			_, _ = io.WriteString(channel, "0\n/root\n")
		} else {
			uninstalls.Add(1)
			started.Do(func() { close(uninstallStarted) })
			<-release
			_, _ = io.WriteString(channel, "ok\n")
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = channel.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, srv.ID, dialTestSSH(t, address, hostKey))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	monitor := NewResidentMonitor(agents, nil, NewAgentDeploymentService(sshSvc, t.TempDir()), "", false, 5)
	svc.SetMonitoringProvider(monitor)
	svc.SetDeletionBudgets(500*time.Millisecond, 2*time.Second, 5*time.Second)

	first := make(chan error, 1)
	go func() { first <- svc.Delete(ctx, srv.ID) }()
	select {
	case <-uninstallStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first deletion never reached the uninstall")
	}

	second := make(chan error, 1)
	go func() { second <- svc.Delete(ctx, srv.ID) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("joined deletion: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("joined deletion did not return")
	}
	if got := invalidations.ids; len(got) != 0 {
		t.Fatalf("joined deletion invalidated the owner's connection: %v", got)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("owner deletion: %v", err)
	}
	if n := uninstalls.Load(); n != 1 {
		t.Fatalf("uninstall ran %d times, want exactly 1", n)
	}
	if got := invalidations.ids; len(got) != 1 || got[0] != srv.ID {
		t.Fatalf("pool invalidations = %v, want exactly [%d]", got, srv.ID)
	}
	if _, err := repo.FindByID(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted server still present: %v", err)
	}
}
