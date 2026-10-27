package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// Use the real install job, SSH leases, SFTP uploads, and repository writes. The
// peer only substitutes successful activation and controlled retirement faults.
type residentMigrationRemote struct {
	files       *fakeRemote
	home        string
	uid         atomic.Int32
	setups      atomic.Int32
	activations atomic.Int32
	retirements atomic.Int32
	pullExit    atomic.Int32
	mu          sync.RWMutex
	onActivate  func(string, ssh.Channel)
	onRetire    func(string, ssh.Channel)
}

func (r *residentMigrationRemote) setActivation(fn func(string, ssh.Channel)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onActivate = fn
}

func (r *residentMigrationRemote) setRetirement(fn func(string, ssh.Channel)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onRetire = fn
}

func (r *residentMigrationRemote) handler(t *testing.T) func(string, ssh.Channel, <-chan struct{}) {
	t.Helper()
	inherited := r.files.handler()
	return func(command string, ch ssh.Channel, closed <-chan struct{}) {
		switch {
		case strings.HasPrefix(command, "set -eu\ncache_dir="):
			if strings.Contains(command, "mode='store'\n") {
				fmt.Fprintln(ch, "TALUS_ARTIFACT_READY")
				exitStatus(ch, 0)
			} else {
				code := r.pullExit.Load()
				marker := "TALUS_PULL_UNAVAILABLE"
				switch code {
				case 76:
					marker = "TALUS_PULL_INCOMPLETE"
				case 79:
					marker = "TALUS_PULL_BUSY"
				default:
					code = 75
				}
				fmt.Fprintln(ch, marker)
				exitStatus(ch, uint32(code))
			}
		case strings.HasPrefix(command, "id -u;"):
			home := r.home
			if r.uid.Load() == 0 {
				home = "/root"
			}
			fmt.Fprintf(ch, "%d\n%s\n", r.uid.Load(), home)
			if strings.Contains(command, "command -v systemctl") {
				fmt.Fprintln(ch, "/usr/bin/systemctl")
			} else if strings.Contains(command, "id -un 0") {
				fmt.Fprintln(ch, "root")
			} else if strings.Contains(command, "id -un ") {
				fmt.Fprintln(ch, "bob")
			}
			exitStatus(ch, 0)
		case strings.Contains(command, "show-environment"):
			exitStatus(ch, 0)
		case strings.HasPrefix(command, "test ! -L"):
			r.setups.Add(1)
			exitStatus(ch, 0)
		case strings.HasPrefix(command, "test ") && strings.Contains(command, " -ef "):
			cmd := exec.Command("sh", "-c", command)
			cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
			status := uint32(0)
			if err := cmd.Run(); err != nil {
				status = 1
				if exit, ok := err.(*exec.ExitError); ok {
					status = uint32(exit.ExitCode())
				}
			}
			exitStatus(ch, status)
		case strings.HasPrefix(command, "if command -v sha256sum"):
			parts := strings.Split(command, "'")
			if len(parts) < 3 {
				t.Error("hash command has no quoted upload path")
				exitStatus(ch, 1)
				return
			}
			data, ok := r.files.file(parts[1])
			if !ok {
				t.Errorf("hash requested before upload: %s", parts[1])
				exitStatus(ch, 1)
				return
			}
			fmt.Fprintf(ch, "%x  %s\n", sha256.Sum256(data), parts[1])
			exitStatus(ch, 0)
		case strings.HasPrefix(command, "set -eu\nhad_previous="):
			r.activations.Add(1)
			r.mu.RLock()
			activate := r.onActivate
			r.mu.RUnlock()
			if activate != nil {
				activate(command, ch)
			} else {
				exitStatus(ch, 0)
			}
		case strings.HasPrefix(command, "set -eu\n") && strings.Contains(command, "rm -rf "):
			r.retirements.Add(1)
			r.mu.RLock()
			retire := r.onRetire
			r.mu.RUnlock()
			if retire != nil {
				retire(command, ch)
			} else {
				exitStatus(ch, 0)
			}
		default:
			inherited(command, ch, closed)
		}
	}
}

type residentMigrationFixture struct {
	ctx     context.Context
	db      *gorm.DB
	agents  *repository.AgentNodeRepo
	node    model.AgentNode
	monitor *ResidentMonitor
	remote  *residentMigrationRemote
}

func newResidentMigrationFixture(t *testing.T, oldUID int, oldHome string, currentUID int, currentHome string) residentMigrationFixture {
	t.Helper()
	svc, servers, db, ctx, _ := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "account-migration", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, node := serverDeleteAgent(t, db, ctx, srv)
	// Seed the stored value directly so historical noncanonical HOME values
	// remain covered even if new repository writes normalize them.
	if err := db.Model(&model.AgentNode{}).Where("server_id = ?", srv.ID).Updates(map[string]any{
		"install_uid": oldUID, "install_home": oldHome,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := agents.Deployment(ctx, node, "ready", "", "old-artifact", "old-config"); err != nil {
		t.Fatal(err)
	}
	node, err = agents.Get(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	remote := &residentMigrationRemote{files: newFakeRemote(), home: currentHome}
	remote.uid.Store(int32(currentUID))
	address, key := startExecSSHServer(t, remote.handler(t))
	pool := newTestPool(t)
	seedPool(t, pool, srv.ID, dialTestSSH(t, address, key))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "migration-v2")
	deployer := NewAgentDeploymentService(sshSvc, dir)
	monitor := NewResidentMonitor(agents, servers, deployer, "https://hub.example", false, 5)
	return residentMigrationFixture{ctx: ctx, db: db, agents: agents, node: node, monitor: monitor, remote: remote}
}

func (f residentMigrationFixture) install(t *testing.T, node model.AgentNode) model.AgentNode {
	t.Helper()
	installErr := f.monitor.installJob(context.Background(), node, "new-artifact", "new-config")
	after, err := f.agents.Get(f.ctx, node.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if (installErr == nil) != (after.Deployment == "ready") {
		t.Fatalf("job result disagrees with persisted state: err=%v deployment=%q reason=%q", installErr, after.Deployment, after.DeployError)
	}
	return after
}

func assertResidentMigrationFailed(t *testing.T, node model.AgentNode) {
	t.Helper()
	if node.Deployment != "failed" || node.DeployError == "" {
		t.Fatalf("failed migration published deployment=%q reason=%q", node.Deployment, node.DeployError)
	}
	if node.InstalledHash != "old-artifact" || node.ConfigHash != "old-config" {
		t.Fatalf("failed migration published new fingerprints: %q/%q", node.InstalledHash, node.ConfigHash)
	}
	if !residentInstallNeeded(node, "new-artifact", "new-config", time.Now(), time.Now()) {
		t.Fatal("failed migration suppresses retry after recent agent contact")
	}
}

func assertResidentMigrationLayoutsRetained(t *testing.T, node model.AgentNode, original, target model.AgentInstallLayout) {
	t.Helper()
	if node.InstallUID == nil || *node.InstallUID != original.UID || node.InstallHome == nil || *node.InstallHome != original.Home {
		t.Fatalf("failed migration lost original current layout: uid=%v home=%v", node.InstallUID, node.InstallHome)
	}
	for _, want := range []model.AgentInstallLayout{original, target} {
		found := false
		for _, got := range node.PendingInstallLayouts {
			if got.UID == want.UID && path.Clean(got.Home) == want.Home {
				found = true
			}
		}
		if !found {
			t.Fatalf("failed migration lost cleanup location %+v: pending=%+v", want, node.PendingInstallLayouts)
		}
	}
}

func assertResidentMigrationReady(t *testing.T, node model.AgentNode, uid int, home string) {
	t.Helper()
	if node.Deployment != "ready" || node.DeployError != "" || node.InstalledHash != "new-artifact" || node.ConfigHash != "new-config" {
		t.Fatalf("successful migration not published: deployment=%q reason=%q hashes=%q/%q", node.Deployment, node.DeployError, node.InstalledHash, node.ConfigHash)
	}
	if node.InstallUID == nil || *node.InstallUID != uid || node.InstallHome == nil || *node.InstallHome != home {
		t.Fatalf("successful migration recorded wrong layout: uid=%v home=%v", node.InstallUID, node.InstallHome)
	}
	if len(node.PendingInstallLayouts) != 0 {
		t.Fatalf("successful migration retained pending locations: %+v", node.PendingInstallLayouts)
	}
}

func TestResidentInstallJobPersistsTransientTransferWaitAndClearsOnSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int32
		err    error
		reason string
	}{
		{"resuming", 76, errArtifactPullProgress, reasonArtifactResuming},
		{"busy", 79, errArtifactPullBusy, reasonArtifactBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResidentMigrationFixture(t, 1001, "/home/bob", 1001, "/home/bob")
			f.remote.pullExit.Store(tc.code)
			if err := f.monitor.installJob(context.Background(), f.node, "new-artifact", "new-config"); !errors.Is(err, tc.err) {
				t.Fatalf("unexpected installation outcome: %v", err)
			}
			after, err := f.agents.Get(f.ctx, f.node.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Deployment != "pending" || after.DeployError != tc.reason || after.InstalledHash != "old-artifact" || after.ConfigHash != "old-config" || f.remote.activations.Load() != 0 {
				t.Fatalf("unfinished transfer was marked failed/installed or activated: state=%q reason=%q hashes=%q/%q activations=%d", after.Deployment, after.DeployError, after.InstalledHash, after.ConfigHash, f.remote.activations.Load())
			}
			if !residentInstallNeeded(after, "new-artifact", "new-config", time.Now(), time.Now()) {
				t.Fatal("pending transfer suppressed its next attempt")
			}
			// Reload the persisted node as the reconciler does after Hub restart.
			restarted := NewResidentMonitor(f.agents, f.monitor.servers, f.monitor.deployer, "https://hub.example", false, 5)
			restarted.mu.Lock()
			restarted.runtimeLocked(after)
			restarted.mu.Unlock()
			for _, m := range []*ResidentMonitor{f.monitor, restarted} {
				status := m.Statuses(f.ctx, []uint{after.ServerID})[after.ServerID]
				if status.Deployment != "pending" || status.DeploymentError != tc.reason {
					t.Fatalf("status lost pending diagnostic: %+v", status)
				}
			}
			if len(f.monitor.transferSlots) != 0 || len(f.monitor.activationSlots) != 0 {
				t.Fatal("waiting transfer retained an active phase slot")
			}
			f.remote.pullExit.Store(0)
			ready := f.install(t, after)
			assertResidentMigrationReady(t, ready, 1001, "/home/bob")
			status := f.monitor.Statuses(f.ctx, []uint{ready.ServerID})[ready.ServerID]
			if status.DeploymentError != "" {
				t.Fatalf("successful retry retained its wait diagnostic: %+v", status)
			}
		})
	}
}

func TestResidentLayoutMigrationEquivalentHomesKeepNewInstallation(t *testing.T) {
	for _, scenario := range []struct{ name, recorded, current string }{
		{"recorded-trailing-slash", "/home/bob/", "/home/bob"},
		{"current-dot-and-double-slash", "/home/bob", "/home//bob/."},
		{"both-aliases", "/home//bob/.", "/home/bob/"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newResidentMigrationFixture(t, 1001, scenario.recorded, 1001, scenario.current)
			root := t.TempDir()
			home := filepath.Join(root, "home", "bob")
			base := filepath.Join(home, ".local/share/talus-agent", f.node.InstallID)
			unitDir := filepath.Join(home, ".config/systemd/user")
			unit := filepath.Join(unitDir, "talus-agent-"+f.node.InstallID+".service")
			ctl := filepath.Join(root, "systemctl")
			if err := os.WriteFile(ctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			f.remote.setActivation(func(_ string, ch ssh.Channel) {
				for _, dir := range []string{base, unitDir} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Error(err)
						exitStatus(ch, 1)
						return
					}
				}
				for _, file := range []string{filepath.Join(base, "agent"), filepath.Join(base, "config.json"), unit} {
					if err := os.WriteFile(file, []byte("healthy new installation"), 0600); err != nil {
						t.Error(err)
						exitStatus(ch, 1)
						return
					}
				}
				exitStatus(ch, 0)
			})
			f.remote.setRetirement(func(command string, ch ssh.Channel) {
				// Executing an accidental retirement against a temporary HOME
				// reproduces its effect on the freshly activated files.
				script := strings.NewReplacer("/home", filepath.Join(root, "home"), "systemctl --user", shellQuote(ctl)).Replace(command)
				cmd := exec.Command("sh", "-c", script)
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				if err := cmd.Run(); err != nil {
					t.Errorf("unexpected equivalent-layout retirement: %v", err)
					exitStatus(ch, 1)
					return
				}
				exitStatus(ch, 0)
			})
			after := f.install(t, f.node)
			if f.remote.activations.Load() != 1 || f.remote.retirements.Load() != 0 {
				t.Fatalf("equivalent HOME retired active layout: activations=%d retirements=%d", f.remote.activations.Load(), f.remote.retirements.Load())
			}
			for _, file := range []string{filepath.Join(base, "agent"), filepath.Join(base, "config.json"), unit} {
				if _, err := os.Stat(file); err != nil {
					t.Fatalf("new installation removed at %s: %v", file, err)
				}
			}
			assertResidentMigrationReady(t, after, 1001, "/home/bob")
		})
	}
}

func TestResidentLayoutMigrationInsufficientPermissionsRetainsCleanupAndRetries(t *testing.T) {
	f := newResidentMigrationFixture(t, 0, "/root", 1001, "/home/bob")
	after := f.install(t, f.node)
	if f.remote.activations.Load() != 1 || f.remote.retirements.Load() != 0 {
		t.Fatalf("unprivileged migration tried to delete old root installation: activations=%d retirements=%d", f.remote.activations.Load(), f.remote.retirements.Load())
	}
	assertResidentMigrationFailed(t, after)
	assertResidentMigrationLayoutsRetained(t, after, model.AgentInstallLayout{UID: 0, Home: "/root"}, model.AgentInstallLayout{UID: 1001, Home: "/home/bob"})

	// Restoring root access lets reconciliation clean the interrupted bob
	// installation, whose location must survive the failed attempt.
	f.remote.uid.Store(0)
	f.remote.setRetirement(func(command string, ch ssh.Channel) {
		if !strings.Contains(command, "/home/bob/.local/share/talus-agent/"+f.node.InstallID) {
			t.Error("retry forgot the newly activated bob installation")
			exitStatus(ch, 1)
			return
		}
		exitStatus(ch, 0)
	})
	after = f.install(t, after)
	if f.remote.retirements.Load() != 1 {
		t.Fatalf("retry did not retire interrupted installation: retirements=%d", f.remote.retirements.Load())
	}
	assertResidentMigrationReady(t, after, 0, "/root")
}

func TestResidentLayoutMigrationRetirementFailureRetainsCleanupAndRetries(t *testing.T) {
	f := newResidentMigrationFixture(t, 1001, "/home/bob", 0, "/root")
	var denyRetirement atomic.Bool
	denyRetirement.Store(true)
	f.remote.setRetirement(func(command string, ch ssh.Channel) {
		if !strings.Contains(command, "/home/bob/.local/share/talus-agent/"+f.node.InstallID) {
			t.Error("retirement targeted a location other than the old bob installation")
			exitStatus(ch, 1)
			return
		}
		if denyRetirement.Load() {
			fmt.Fprintln(ch.Stderr(), "systemctl: failed to stop old user unit")
			exitStatus(ch, 1)
			return
		}
		exitStatus(ch, 0)
	})
	after := f.install(t, f.node)
	if f.remote.activations.Load() != 1 || f.remote.retirements.Load() != 1 {
		t.Fatalf("migration did not reach controlled retirement fault: activations=%d retirements=%d", f.remote.activations.Load(), f.remote.retirements.Load())
	}
	assertResidentMigrationFailed(t, after)
	assertResidentMigrationLayoutsRetained(t, after, model.AgentInstallLayout{UID: 1001, Home: "/home/bob"}, model.AgentInstallLayout{UID: 0, Home: "/root"})

	denyRetirement.Store(false)
	after = f.install(t, after)
	if f.remote.retirements.Load() != 2 {
		t.Fatalf("retry forgot original installation: retirements=%d", f.remote.retirements.Load())
	}
	assertResidentMigrationReady(t, after, 0, "/root")
}

func TestResidentLayoutMigrationStorageFailureCanRetry(t *testing.T) {
	for _, phase := range []string{"tracking", "finalization"} {
		t.Run(phase, func(t *testing.T) {
			f := newResidentMigrationFixture(t, 1001, "/home/bob", 0, "/root")
			column := "pending_install_layouts"
			if phase == "finalization" {
				column = "install_uid"
			}
			// Fail only the layout write; installJob's deployment/error writes
			// still succeed, as they do during a transient metadata failure.
			function := `CREATE FUNCTION reject_migration_layout_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.` + column + ` IS DISTINCT FROM OLD.` + column + ` THEN RAISE EXCEPTION 'temporary layout storage failure'; END IF; RETURN NEW; END $$`
			if err := f.db.Exec(function).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Exec(`CREATE TRIGGER reject_migration_layout_write BEFORE UPDATE ON agent_nodes FOR EACH ROW EXECUTE FUNCTION reject_migration_layout_write()`).Error; err != nil {
				t.Fatal(err)
			}
			after := f.install(t, f.node)
			assertResidentMigrationFailed(t, after)
			if phase == "tracking" {
				if f.remote.setups.Load() != 0 || f.remote.activations.Load() != 0 || f.remote.retirements.Load() != 0 || len(f.remote.files.fileKeys()) != 0 {
					t.Fatalf("tracking failure allowed remote writes: setups=%d activations=%d retirements=%d uploads=%d", f.remote.setups.Load(), f.remote.activations.Load(), f.remote.retirements.Load(), len(f.remote.files.fileKeys()))
				}
			} else {
				if f.remote.activations.Load() != 1 || f.remote.retirements.Load() != 1 {
					t.Fatalf("migration did not reach final layout write: activations=%d retirements=%d", f.remote.activations.Load(), f.remote.retirements.Load())
				}
				assertResidentMigrationLayoutsRetained(t, after, model.AgentInstallLayout{UID: 1001, Home: "/home/bob"}, model.AgentInstallLayout{UID: 0, Home: "/root"})
			}
			if err := f.db.Exec(`DROP TRIGGER reject_migration_layout_write ON agent_nodes`).Error; err != nil {
				t.Fatal(err)
			}
			after = f.install(t, after)
			assertResidentMigrationReady(t, after, 0, "/root")
		})
	}
}

func TestResidentLayoutMigrationDeletionCleansOriginalAndPendingInstallations(t *testing.T) {
	f := newResidentMigrationFixture(t, 0, "/root", 1001, "/home/bob")
	after := f.install(t, f.node)
	assertResidentMigrationFailed(t, after)
	assertResidentMigrationLayoutsRetained(t, after, model.AgentInstallLayout{UID: 0, Home: "/root"}, model.AgentInstallLayout{UID: 1001, Home: "/home/bob"})

	f.remote.uid.Store(0)
	var rootRemovals, bobRemovals atomic.Int32
	f.remote.setRetirement(func(command string, ch ssh.Channel) {
		switch {
		case strings.Contains(command, "/var/lib/talus-agent/"+f.node.InstallID):
			rootRemovals.Add(1)
		case strings.Contains(command, "/home/bob/.local/share/talus-agent/"+f.node.InstallID):
			bobRemovals.Add(1)
		default:
			t.Error("deletion targeted an unknown installation layout")
			exitStatus(ch, 1)
			return
		}
		exitStatus(ch, 0)
	})
	removal := f.monitor.PrepareAgentRemoval(f.ctx, f.node.ServerID)
	defer removal.Close()
	if err := removal.Remove(context.Background()); err != nil {
		t.Fatalf("delete interrupted migration: %v", err)
	}
	if rootRemovals.Load() != 1 || bobRemovals.Load() != 1 {
		t.Fatalf("deletion forgot or duplicated an installation: root=%d bob=%d", rootRemovals.Load(), bobRemovals.Load())
	}
	if _, err := f.agents.Get(f.ctx, f.node.ServerID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deletion retained resident credential: %v", err)
	}
}

func TestResidentLayoutMigrationRejectsDifferentAccountsSharingHome(t *testing.T) {
	f := newResidentMigrationFixture(t, 1002, "/home/bob", 1001, "/home/bob")
	after := f.install(t, f.node)
	assertResidentMigrationFailed(t, after)
	if f.remote.setups.Load() != 0 || f.remote.activations.Load() != 0 || f.remote.retirements.Load() != 0 {
		t.Fatal("shared-home migration changed the existing installation")
	}
	if after.InstallUID == nil || *after.InstallUID != 1002 || after.InstallHome == nil || *after.InstallHome != "/home/bob" || len(after.PendingInstallLayouts) != 0 {
		t.Fatalf("rejected shared-home migration changed cleanup records: %+v", after)
	}
}
