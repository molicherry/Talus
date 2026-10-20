package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func discoveryFixture(t *testing.T, uid int, home, passwd string) (string, []string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"id":     "#!/bin/sh\nprintf '%s\\n' " + shellQuote(strconv.Itoa(uid)) + "\n",
		"getent": "#!/bin/sh\nprintf '%s\\n' " + shellQuote(passwd) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := residentLayoutDiscoveryScript("install1")
	// Only the fixed system-layout paths are virtualized. User homes remain
	// real temporary paths, exercising their quoting and protocol encoding.
	script = strings.NewReplacer("/var/lib/talus-agent/", dir+"/system/state/", "/etc/systemd/system/", dir+"/system/units/").Replace(script)
	return script, append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+home), dir
}

func makeDiscoveryState(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".local/share/talus-agent/install1"), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestResidentLayoutDiscoveryFindsLegacyHomesWithoutGuessingCurrentAccount(t *testing.T) {
	oldHome := filepath.Join(t.TempDir(), "al ice ' home\t")
	makeDiscoveryState(t, oldHome)
	passwd := "root:x:0:0:root:/root:/bin/sh\nalice:x:1001:1001::" + oldHome + ":/bin/sh\nalias:x:1001:1001::" + oldHome + "/:/bin/sh\nother:x:1002:1002::/nonexistent:/bin/sh"
	script, env, _ := discoveryFixture(t, 0, "/root", passwd)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	layouts, err := parseDiscoveredInstallLayouts(model.AgentNode{InstallID: "install1", Deployment: "ready"}, string(out))
	if err != nil || len(layouts) != 1 || layouts[0].UID != 1001 || layouts[0].Home != oldHome {
		t.Fatalf("legacy layout discovery = %+v, %v", layouts, err)
	}
}

func TestResidentLayoutDiscoveryUnprivilegedChecksSelfAndSystemOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bob")
	makeDiscoveryState(t, home)
	script, env, dir := discoveryFixture(t, 1002, home, "must not enumerate another account")
	if err := os.MkdirAll(filepath.Join(dir, "system/units"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "system/units/talus-agent-install1.service"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	getent := strings.Split(strings.TrimPrefix(env[len(env)-2], "PATH="), ":")[0] + "/getent"
	if err := os.WriteFile(getent, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	layouts, err := parseDiscoveredInstallLayouts(model.AgentNode{InstallID: "install1", Deployment: "ready"}, string(out))
	if err != nil || len(layouts) != 2 || layouts[0].UID != 0 || layouts[1].UID != 1002 {
		t.Fatalf("limited discovery = %+v, %v", layouts, err)
	}
}

func TestResidentLayoutDiscoveryRejectsIncompleteEnumeration(t *testing.T) {
	for _, failure := range []string{"source", "account-limit"} {
		t.Run(failure, func(t *testing.T) {
			script, env, _ := discoveryFixture(t, 0, "/root", "")
			getent := strings.Split(strings.TrimPrefix(env[len(env)-2], "PATH="), ":")[0] + "/getent"
			body := "#!/bin/sh\nexit 1\n"
			if failure == "account-limit" {
				body = fmt.Sprintf("#!/bin/sh\ni=0\nwhile test \"$i\" -le %d; do printf 'user:x:1001:1001::/nonexistent:/bin/sh\\n'; i=$((i+1)); done\n", residentLayoutDiscoveryLimit)
			}
			if err := os.WriteFile(getent, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("incomplete discovery reported success: %q", out)
			}
		})
	}
}

func TestResidentLayoutDiscoveryValidatesResponseAndMissingInstall(t *testing.T) {
	n := model.AgentNode{InstallID: "install1", Deployment: "ready"}
	for _, output := range []string{
		"", "TALUS_LAYOUTS_V1\x00", "TALUS_LAYOUTS_V1\x001001\x00/home/alice\x00",
		"TALUS_LAYOUTS_V1\x00-1\x00/home/alice\x00USER_DONE\x00",
		"TALUS_LAYOUTS_V1\x001001\x00/home/../alice\x00USER_DONE\x00",
		"TALUS_LAYOUTS_V1\x00USER_DONE\x00",
		"TALUS_LAYOUTS_V1\x00DONE\x00",
	} {
		if layouts, err := parseDiscoveredInstallLayouts(n, output); err == nil {
			t.Fatalf("unsafe/empty discovery accepted: %q (%+v)", output, layouts)
		}
	}
	if layouts, err := parseDiscoveredInstallLayouts(model.AgentNode{InstallID: "install1"}, "TALUS_LAYOUTS_V1\x00USER_DONE\x00"); err != nil || len(layouts) != 0 {
		t.Fatalf("uninstalled node must allow an empty completed scan: %+v %v", layouts, err)
	}
	for _, id := range []string{"", "../escape", "name/service", "unsafe' id", strings.Repeat("a", 65)} {
		if validDiscoveryInstallID(id) {
			t.Fatalf("unsafe install id accepted: %q", id)
		}
	}
}

func TestResidentLayoutDiscoveryCompleteRootScanAllowsRemovedInstallation(t *testing.T) {
	script, env, _ := discoveryFixture(t, 0, "/root", "root:x:0:0:root:/root:/bin/sh")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []model.AgentNode{
		{InstallID: "install1", Deployment: "ready"},
		{InstallID: "install1", Deployment: "failed", InstalledHash: "old-artifact"},
	} {
		if layouts, err := parseDiscoveredInstallLayouts(n, string(out)); err != nil || len(layouts) != 0 {
			t.Fatalf("complete root scan blocked repair after manual removal: %+v %v", layouts, err)
		}
		if _, err := parseDiscoveredInstallLayouts(n, "TALUS_LAYOUTS_V1\x00USER_DONE\x00"); err == nil {
			t.Fatal("limited user scan claimed a previous installation was absent")
		}
	}
}

// Exercise deletion from a real legacy SQL row through its pinned SSH lease.
// The peer executes discovery and removal in an isolated filesystem, including
// resolving Alice's owner name and calling her user service manager as root.
func TestResidentLayoutDiscoveryLegacyAliceToRootServerDelete(t *testing.T) {
	svc, servers, db, ctx, _ := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "legacy-account", Host: "target.example", Port: 22, OwnerID: mw.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, node := serverDeleteAgent(t, db, ctx, srv)
	if err := db.Model(&model.AgentNode{}).Where("server_id = ?", srv.ID).Updates(map[string]any{
		"install_uid": nil, "install_home": nil, "deployment": "ready", "installed_hash": "old-artifact",
	}).Error; err != nil {
		t.Fatal(err)
	}
	legacy, err := agents.Get(ctx, srv.ID)
	if err != nil || legacy.InstallUID != nil || legacy.InstallHome != nil {
		t.Fatalf("legacy row was not persisted: %+v %v", legacy, err)
	}
	home := filepath.Join(t.TempDir(), "al ice ' home")
	base := filepath.Join(home, ".local/share/talus-agent", node.InstallID)
	unit := filepath.Join(home, ".config/systemd/user", "talus-agent-"+node.InstallID+".service")
	for _, dir := range []string{base, filepath.Dir(unit)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(unit, []byte("legacy unit"), 0600); err != nil {
		t.Fatal(err)
	}
	_, env, remoteRoot := discoveryFixture(t, 0, "/root", "root:x:0:0:root:/root:/bin/sh\nalice:x:1001:1001::"+home+":/bin/sh")
	bin := filepath.Join(remoteRoot, "bin")
	controlLog := filepath.Join(remoteRoot, "control.log")
	for name, body := range map[string]string{
		"id":        "case \"$1\" in -u) printf '0\\n';; -un) printf 'alice\\n';; *) exit 1;; esac\n",
		"runuser":   "printf 'runuser %s\\n' \"$*\" >> " + shellQuote(controlLog) + "\nwhile test \"$1\" != --; do shift; done\nshift\nexec \"$@\"\n",
		"systemctl": "printf 'systemctl %s\\n' \"$*\" >> " + shellQuote(controlLog) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var discoveryCalls, removalCalls atomic.Int32
	address, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
		if strings.Contains(command, "TALUS_LAYOUTS_V1") {
			discoveryCalls.Add(1)
		}
		if strings.Contains(command, "rm -rf ") {
			removalCalls.Add(1)
		}
		// Keep every system path isolated even if an incorrect implementation
		// tries to remove the current root account instead of Alice's layout.
		command = strings.NewReplacer("/var/lib/talus-agent/", remoteRoot+"/system/state/", "/etc/systemd/system/", remoteRoot+"/system/units/").Replace(command)
		cmd := exec.Command("sh", "-c", command)
		cmd.Env, cmd.Stdout, cmd.Stderr = env, ch, ch.Stderr()
		code := uint32(0)
		if err := cmd.Run(); err != nil {
			code = 1
		}
		exitStatus(ch, code)
	})
	pool := newTestPool(t)
	seedPool(t, pool, srv.ID, dialTestSSH(t, address, key))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	monitor := NewResidentMonitor(agents, servers, NewAgentDeploymentService(sshSvc, ""), "", false, 5)
	svc.SetMonitoringProvider(monitor)
	if err := svc.Delete(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if discoveryCalls.Load() != 1 || removalCalls.Load() != 1 {
		t.Fatalf("legacy deletion skipped discovery/removal: %d/%d", discoveryCalls.Load(), removalCalls.Load())
	}
	for _, name := range []string{base, unit} {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy Alice installation remained at %s: %v", name, err)
		}
	}
	control, err := os.ReadFile(controlLog)
	if err != nil || !strings.Contains(string(control), "runuser -u alice -- env XDG_RUNTIME_DIR=/run/user/1001 systemctl --user disable --now talus-agent-"+node.InstallID+".service") {
		t.Fatalf("legacy user service was not stopped as Alice: %s %v", control, err)
	}
	if _, err := agents.Get(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted server retained agent row: %v", err)
	}
	if _, err := servers.FindByID(ctx, srv.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("server was not deleted: %v", err)
	}
}

func TestResidentLayoutDiscoveryPropagatesRemoteFailure(t *testing.T) {
	address, key := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		exitStatus(ch, 1)
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := svc.AcquireLease(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if layouts, err := discoverInstallLayouts(context.Background(), model.AgentNode{InstallID: "install1"}, lease); err == nil {
		t.Fatalf("remote discovery failure accepted: %+v", layouts)
	}
}
