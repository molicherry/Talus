package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func TestResidentRemovalWaitsForInstallerAndFencesStaleSnapshots(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	installCanceled := make(chan struct{})
	installDone := make(chan struct{})
	m.installJobs[7] = residentJob{cancel: func() { close(installCanceled) }, done: installDone}
	epoch := m.installEpoch
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepared := make(chan preparedAgentRemoval, 1)
	go func() { prepared <- m.PrepareAgentRemoval(ctx, 7) }()
	select {
	case <-installCanceled:
	case <-time.After(time.Second):
		t.Fatal("removal did not cancel the in-flight installer")
	}
	if m.installAllowed(7, epoch) {
		t.Fatal("removal admitted an installation from an old snapshot")
	}
	select {
	case r := <-prepared:
		r.Close()
		t.Fatal("preparation returned before the installer finished")
	default:
	}
	// Exhausting the preparation budget must return without racing a still
	// active installer with remote cleanup, while retaining the fence until
	// the caller has accepted or rejected the database deletion.
	cancel()
	var removal preparedAgentRemoval
	select {
	case removal = <-prepared:
	case <-time.After(time.Second):
		t.Fatal("preparation ignored cancellation while joining the installer")
	}
	if !errors.Is(removal.(*residentRemoval).err, context.Canceled) {
		t.Fatalf("preparation error = %v", removal.(*residentRemoval).err)
	}
	m.installJobsMu.Lock()
	fencedEpoch := m.installEpoch
	m.installJobsMu.Unlock()
	if m.installAllowed(7, fencedEpoch) {
		t.Fatal("timed-out preparation released its fence too soon")
	}
	removal.Close()
	removal.Close()
	m.installJobsMu.Lock()
	currentEpoch := m.installEpoch
	m.installJobsMu.Unlock()
	if m.installAllowed(7, epoch) || m.installAllowed(7, fencedEpoch) {
		t.Fatal("closing a removal revived a stale reconciliation snapshot")
	}
	if !m.installAllowed(7, currentEpoch) {
		t.Fatal("rejected deletion did not allow a fresh reconciliation")
	}
}

func TestResidentRemovalSerializesConcurrentPreparation(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	finish, _, err := m.fenceRemoval(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.fenceRemoval(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("concurrent preparation did not respect its wait budget: %v", err)
	}
	m.installJobsMu.Lock()
	epoch := m.installEpoch
	m.installJobsMu.Unlock()
	if m.installAllowed(7, epoch) {
		t.Fatal("a canceled concurrent preparation lifted the original fence")
	}
	finish()
	nextFinish, _, err := m.fenceRemoval(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	nextFinish()
}

// A deletion that cannot take the fence is a join, not an owner: it reports the
// in-flight removal so the caller leaves the pool and credential alone.
func TestResidentRemovalReportsJoinWhenAnotherRemovalOwnsTheFence(t *testing.T) {
	m := NewResidentMonitor(nil, nil, nil, "", false, 1)
	finish, _, err := m.fenceRemoval(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	removal := m.PrepareAgentRemoval(ctx, 7)
	if removal.Owned() {
		t.Fatal("joined removal claimed the owner's fence")
	}
	if !errors.Is(removal.(*residentRemoval).err, errRemovalInProgress) {
		t.Fatalf("join error = %v, want errRemovalInProgress", removal.(*residentRemoval).err)
	}
	removal.Close()
	m.installJobsMu.Lock()
	fenced := m.removing[7] != nil
	m.installJobsMu.Unlock()
	if !fenced {
		t.Fatal("joined removal released the owner's fence")
	}
}

// Removal must not depend on the callback URL: an operator can clear
// AGENT_HUB_URL and still expect deletion to uninstall the agent. The layout is
// also taken from the recorded install account, not from the current credential.
func TestResidentRemovalCleansRecordedLayoutWithoutHubURL(t *testing.T) {
	svc, _, db, ctx, _ := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "switched", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, node := serverDeleteAgent(t, db, ctx, srv)
	uid, home := 1000, "/home/alice"
	if err := agents.SetInstallLayout(ctx, node, uid, home); err != nil {
		t.Fatal(err)
	}
	commands := make(chan string, 2)
	address, hostKey := startExecSSHServer(t, func(command string, channel ssh.Channel, _ <-chan struct{}) {
		commands <- command
		if strings.HasPrefix(command, "id -u; ") {
			_, _ = io.WriteString(channel, "0\n/root\nalice\n")
		} else {
			_, _ = io.WriteString(channel, "ok\n")
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = channel.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, srv.ID, dialTestSSH(t, address, hostKey))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	monitor := NewResidentMonitor(agents, nil, NewAgentDeploymentService(sshSvc, t.TempDir()), "", false, 5)

	removal := monitor.PrepareAgentRemoval(ctx, srv.ID)
	if !removal.Owned() {
		t.Fatal("preparation did not own the fence")
	}
	if err := removal.Remove(ctx); err != nil {
		t.Fatalf("remove: %v", err)
	}
	removal.Close()

	next := func() string {
		select {
		case command := <-commands:
			return command
		case <-time.After(10 * time.Second):
			t.Fatal("uninstall did not run")
			return ""
		}
	}
	next() // probe
	script := next()
	if want := "/home/alice/.local/share/talus-agent/" + node.InstallID; !strings.Contains(script, want) {
		t.Fatalf("uninstall did not target the recorded layout %q:\n%s", want, script)
	}
	if strings.Contains(script, "/var/lib/talus-agent/"+node.InstallID) {
		t.Fatalf("uninstall targeted the logged-in account's layout:\n%s", script)
	}
	if _, err := agents.Get(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("removal left the resident credential behind: %v", err)
	}
}
