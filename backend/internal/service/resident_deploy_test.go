package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

// testUninstallLayout targets the unit file a script test wrote: its directory
// is the unit dir and ctl is the fake service manager to invoke.
func testUninstallLayout(base, unit, ctl string) agentLayout {
	return agentLayout{Base: base, UnitDir: filepath.Dir(unit), ctl: shellQuote(ctl)}
}

// Exercise the exact activation script with a controlled service manager. This
// covers failures before readiness as well as the successful publish path.
func TestResidentActivationRollsBackBinaryConfigurationAndUnit(t *testing.T) {
	for _, scenario := range []string{"success", "health-failure", "manager-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "quoted ' directory")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			binary := filepath.Join(dir, "agent")
			config := filepath.Join(dir, "config.json")
			unit := filepath.Join(dir, "agent.service")
			next := filepath.Join(dir, ".next-agent")
			write := func(path, value string) {
				t.Helper()
				if e := os.WriteFile(path, []byte(value), 0700); e != nil {
					t.Fatal(e)
				}
			}
			write(binary, "#!/bin/sh\nexit 0\n")
			write(config, "old configuration")
			write(unit, "old unit")
			newBin := "#!/bin/sh\nexit 0\n"
			if scenario == "health-failure" {
				newBin = "#!/bin/sh\nexit 1\n"
			}
			write(next, newBin)
			write(next+".config", "new configuration")
			write(next+".unit", "new unit")
			ctl := filepath.Join(dir, "systemctl")
			body := "#!/bin/sh\nexit 0\n"
			if scenario == "manager-failure" {
				body = "#!/bin/sh\nif [ \"$1\" = enable ]; then exit 1; fi\nexit 0\n"
			}
			write(ctl, body)
			script := residentActivationScript(binary, config, unit, "agent.service", next, shellQuote(ctl), 0)
			if scenario == "success" {
				script = residentActivationScript(binary, config, unit, "agent.service", next, shellQuote(ctl), 1)
			}
			cmd := exec.Command("sh", "-c", script)
			out, e := cmd.CombinedOutput()
			if (e == nil) != (scenario == "success") {
				t.Fatalf("activation result: %v %s", e, out)
			}
			want := "old configuration"
			wantUnit := "old unit"
			if scenario == "success" {
				want = "new configuration"
				wantUnit = "new unit"
			}
			b, e := os.ReadFile(config)
			if e != nil || string(b) != want {
				t.Fatalf("configuration rollback: %s %v", b, e)
			}
			b, e = os.ReadFile(unit)
			if e != nil || string(b) != wantUnit {
				t.Fatalf("unit rollback: %s %v", b, e)
			}
			if scenario != "success" {
				b, e = os.ReadFile(binary)
				if e != nil || !strings.Contains(string(b), "exit 0") {
					t.Fatal("binary not restored")
				}
			}
			if _, e = os.Stat(next + ".config"); !os.IsNotExist(e) {
				t.Fatal("staging configuration not cleaned")
			}
		})
	}
}

// The uninstall script must be idempotent and remove both the unit and the
// installation directory, including on hosts where neither exists.
func TestResidentUninstallScriptRemovesUnitAndState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "install ' dir")
	base := filepath.Join(dir, "state")
	unit := filepath.Join(dir, "talus-agent-test.service")
	if e := os.MkdirAll(base, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(base, "config.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(unit, []byte("unit"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(unit+".previous", []byte("old unit"), 0600); e != nil {
		t.Fatal(e)
	}
	log := filepath.Join(dir, "ctl.log")
	ctl := filepath.Join(dir, "systemctl")
	if e := os.WriteFile(ctl, []byte("#!/bin/sh\necho \"$@\" >> "+shellQuote(log)+"\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	script := residentUninstallScript(testUninstallLayout(base, unit, ctl), "talus-agent-test.service", "", true)
	if out, e := exec.Command("sh", "-c", script).CombinedOutput(); e != nil {
		t.Fatalf("uninstall: %v %s", e, out)
	}
	if _, e := os.Stat(base); !os.IsNotExist(e) {
		t.Fatal("installation directory not removed")
	}
	if _, e := os.Stat(unit); !os.IsNotExist(e) {
		t.Fatal("unit file not removed")
	}
	if _, e := os.Stat(unit + ".previous"); !os.IsNotExist(e) {
		t.Fatal("backup unit file not removed")
	}
	if _, e := os.Stat(unit + ".lock"); e != nil {
		t.Fatal("stable lock file must survive removal")
	}
	calls, e := os.ReadFile(log)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"disable --now talus-agent-test.service", "daemon-reload"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("systemctl call %q missing in %q", want, calls)
		}
	}
	// Running it again on a clean host must still succeed.
	if out, e := exec.Command("sh", "-c", script).CombinedOutput(); e != nil {
		t.Fatalf("second uninstall: %v %s", e, out)
	}
}

// Migration must retain the old installation when its manager cannot stop it;
// swallowing this failure would discard the only durable cleanup location.
func TestResidentRetirementRequiresConfirmedServiceStop(t *testing.T) {
	for _, status := range []struct {
		name    string
		code    string
		stopped bool
	}{
		{"still-active", "0", false},
		{"manager-unavailable", "1", false},
		{"already-inactive", "3", true},
		{"unit-already-absent", "4", true},
		{"helper-missing", "127", false},
	} {
		t.Run(status.name, func(t *testing.T) {
			dir := t.TempDir()
			base, unit := filepath.Join(dir, "state"), filepath.Join(dir, "agent.service")
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unit, []byte("old unit"), 0600); err != nil {
				t.Fatal(err)
			}
			ctl := filepath.Join(dir, "systemctl")
			body := "#!/bin/sh\ncase \"$1\" in disable) exit 1 ;; is-active) exit " + status.code + " ;; esac\nexit 0\n"
			if err := os.WriteFile(ctl, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			script := residentRemovalScript(testUninstallLayout(base, unit, ctl), "agent.service", "", true, true)
			if out, err := exec.Command("sh", "-c", script).CombinedOutput(); (err == nil) != status.stopped {
				t.Fatalf("retirement with manager status %s: %v %s", status.code, err, out)
			}
			for _, file := range []string{base, unit} {
				_, err := os.Stat(file)
				if status.stopped && !os.IsNotExist(err) {
					t.Fatalf("stopped installation retained %s: %v", file, err)
				}
				if !status.stopped && err != nil {
					t.Fatalf("failed retirement removed %s: %v", file, err)
				}
			}
		})
	}
}

// A cancelled SSH command can keep rolling back remotely after its local
// installer has returned. The remote lock must cover that entire rollback,
// otherwise a concurrent uninstall can be followed by a late service restart.
func TestResidentUninstallWaitsForActivationRollback(t *testing.T) {
	realFlock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("flock is required for resident installations")
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "state")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	binary, config := filepath.Join(base, "agent"), filepath.Join(base, "config.json")
	unit, next := filepath.Join(dir, "agent.service"), filepath.Join(base, ".next-agent")
	log, state := filepath.Join(dir, "ctl.log"), filepath.Join(dir, "service-state")
	rollback, resume := filepath.Join(dir, "rollback"), filepath.Join(dir, "resume")
	waiting := filepath.Join(dir, "uninstall-waiting")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(binary, "#!/bin/sh\nexit 0\n")
	write(config, "old configuration")
	write(unit, "old unit")
	write(next, "#!/bin/sh\nexit 1\n")
	write(next+".config", "new configuration")
	write(next+".unit", "new unit")
	ctl := filepath.Join(dir, "systemctl")
	write(ctl, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(log)+"\n"+
		"case \"$1\" in\n"+
		"stop) touch "+shellQuote(rollback)+"; while test ! -e "+shellQuote(resume)+"; do sleep 0.01; done ;;\n"+
		"restart) printf running > "+shellQuote(state)+" ;;\n"+
		"disable) printf stopped > "+shellQuote(state)+" ;;\nesac\n")
	// Before waiting normally, prove uninstall reached the lock while rollback
	// still owns it. This makes the conflicting execution order deterministic.
	write(filepath.Join(dir, "flock"), "#!/bin/sh\n"+
		"if test \"${UNINSTALL:-}\" = 1; then\n"+
		"  if "+shellQuote(realFlock)+" -n 9; then exit 42; fi\n"+
		"  touch "+shellQuote(waiting)+"\nfi\nexec "+shellQuote(realFlock)+" \"$@\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env := append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	activation := exec.CommandContext(ctx, "sh", "-c", residentActivationScript(binary, config, unit, "agent.service", next, shellQuote(ctl), 0))
	activation.Env = env
	var activationOutput bytes.Buffer
	activation.Stdout, activation.Stderr = &activationOutput, &activationOutput
	if err := activation.Start(); err != nil {
		t.Fatal(err)
	}
	activationDone := make(chan struct{})
	var activationErr error
	go func() { activationErr = activation.Wait(); close(activationDone) }()
	t.Cleanup(func() {
		_ = os.WriteFile(resume, nil, 0600)
		cancel()
		<-activationDone
	})
	waitFor := func(path string) {
		t.Helper()
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for script checkpoint %s: %v", filepath.Base(path), ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	waitFor(rollback)
	uninstall := exec.CommandContext(ctx, "sh", "-c", residentUninstallScript(testUninstallLayout(base, unit, ctl), "agent.service", "", true))
	uninstall.Env = append(env, "UNINSTALL=1")
	var uninstallOutput bytes.Buffer
	uninstall.Stdout, uninstall.Stderr = &uninstallOutput, &uninstallOutput
	if err := uninstall.Start(); err != nil {
		t.Fatal(err)
	}
	uninstallDone := make(chan struct{})
	var uninstallErr error
	go func() { uninstallErr = uninstall.Wait(); close(uninstallDone) }()
	t.Cleanup(func() {
		_ = os.WriteFile(resume, nil, 0600)
		cancel()
		<-uninstallDone
	})
	waitFor(waiting)
	write(resume, "")
	<-activationDone
	<-uninstallDone
	if activationErr == nil {
		t.Fatal("test activation unexpectedly passed its health check")
	}
	if uninstallErr != nil {
		t.Fatalf("uninstall failed: %v\n%s", uninstallErr, uninstallOutput.String())
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	disabled := strings.Index(string(calls), "disable --now agent.service")
	if disabled < 0 || strings.LastIndex(string(calls), "restart agent.service") > disabled {
		t.Fatalf("service restarted after removal: %s", calls)
	}
	if value, err := os.ReadFile(state); err != nil || string(value) != "stopped" {
		t.Fatalf("service was left running: %q %v", value, err)
	}
	for _, path := range []string{base, unit, unit + ".previous"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("removed installation path was restored: %s (%v)", path, err)
		}
	}
}

func TestResidentScriptsFailClosedWithoutInstallationLock(t *testing.T) {
	for _, operation := range []string{"activation", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "state")
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
			binary, config := filepath.Join(base, "agent"), filepath.Join(base, "config.json")
			unit, next := filepath.Join(dir, "agent.service"), filepath.Join(base, ".next-agent")
			ctl, called := filepath.Join(dir, "systemctl"), filepath.Join(dir, "called")
			for path, body := range map[string]string{
				binary: "old binary", config: "old config", unit: "old unit",
				next: "new binary", next + ".config": "new config", next + ".unit": "new unit",
				filepath.Join(dir, "flock"): "#!/bin/sh\nexit 1\n",
				ctl:                         "#!/bin/sh\ntouch " + shellQuote(called) + "\n",
			} {
				if err := os.WriteFile(path, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := residentUninstallScript(testUninstallLayout(base, unit, ctl), "agent.service", "", true)
			if operation == "activation" {
				script = residentActivationScript(binary, config, unit, "agent.service", next, shellQuote(ctl), 1)
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("script continued after lock failure: %s", out)
			}
			for path, want := range map[string]string{binary: "old binary", config: "old config", unit: "old unit"} {
				if got, err := os.ReadFile(path); err != nil || string(got) != want {
					t.Fatalf("lock failure changed %s: %q %v", path, got, err)
				}
			}
			if _, err := os.Stat(called); !os.IsNotExist(err) {
				t.Fatal("service manager called without owning installation lock")
			}
		})
	}
}

func TestDeployFailureReasonClassifiesSSHAndAgentFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"agent reason", deployError(ReasonAgentHashMismatch, errors.New("hash")), ReasonAgentHashMismatch},
		{"ssh host key unverified", server.ErrSSHHostKeyUnverified, server.ReasonSSHHostKeyUnverified},
		{"ssh auth", server.NewAppError(502, server.ReasonSSHAuth), server.ReasonSSHAuth},
		{"deadline", context.DeadlineExceeded, "deployment_timeout"},
		{"canceled", context.Canceled, "deployment_canceled"},
		{"resuming", fmt.Errorf("transfer: %w", errArtifactPullProgress), reasonArtifactResuming},
		{"busy", fmt.Errorf("transfer: %w", errArtifactPullBusy), reasonArtifactBusy},
		{"unknown", errors.New("boom"), "deployment_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deployFailureReason(tc.err); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// A failed or interrupted deployment must be retried; only a ready deployment
// with matching fingerprints and a fresh contact may be skipped.
func TestResidentInstallNeededRetriesUnreadyDeployments(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-5 * time.Second)
	stale := now.Add(-5 * time.Minute)
	uid, home := 1000, "/home/agent"
	base := model.AgentNode{Deployment: "ready", InstalledHash: "h", ConfigHash: "c", InstallUID: &uid, InstallHome: &home}
	for _, tc := range []struct {
		name string
		node model.AgentNode
		last time.Time
		want bool
	}{
		{"ready and fresh", base, fresh, false},
		{"legacy ready without layout", model.AgentNode{Deployment: "ready", InstalledHash: "h", ConfigHash: "c"}, fresh, true},
		{"unfinished migration", model.AgentNode{Deployment: "ready", InstalledHash: "h", ConfigHash: "c", InstallUID: &uid, InstallHome: &home, PendingInstallLayouts: []model.AgentInstallLayout{{UID: 0, Home: "/root"}}}, fresh, true},
		{"ready but stale", base, stale, true},
		{"failed with matching hashes", model.AgentNode{Deployment: "failed", InstalledHash: "h", ConfigHash: "c"}, fresh, true},
		{"installing with matching hashes", model.AgentNode{Deployment: "installing", InstalledHash: "h", ConfigHash: "c"}, fresh, true},
		{"ready but old fingerprint", model.AgentNode{Deployment: "ready", InstalledHash: "old", ConfigHash: "c"}, fresh, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := residentInstallNeeded(tc.node, "h", "c", tc.last, now); got != tc.want {
				t.Fatalf("install needed = %v, want %v", got, tc.want)
			}
		})
	}
}

// Deletion must still clean up hosts that predate the flock requirement.
func TestResidentUninstallScriptDegradesWithoutFlock(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	rm, err := exec.LookPath("rm")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rm, filepath.Join(binDir, "rm")); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "state")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, "agent.service")
	if err := os.WriteFile(unit, []byte("unit"), 0600); err != nil {
		t.Fatal(err)
	}
	ctl := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(ctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", residentUninstallScript(testUninstallLayout(base, unit, ctl), "agent.service", "", true))
	// Only rm is reachable: `command -v flock` must fail and the script must
	// still stop the unit and remove the installation.
	cmd.Env = []string{"PATH=" + binDir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall without flock: %v %s", err, out)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("installation directory survived a host without flock")
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatal("unit survived a host without flock")
	}
}

// A half-cleaned host often has no unit directory left; uninstall must still
// remove the installation instead of failing while opening the lock file.
func TestResidentUninstallScriptToleratesMissingUnitDirectory(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "state")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, "missing", "agent.service")
	ctl := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(ctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", residentUninstallScript(testUninstallLayout(base, unit, ctl), "agent.service", "", true)).CombinedOutput(); err != nil {
		t.Fatalf("uninstall with missing unit directory: %v %s", err, out)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("installation directory survived a missing unit directory")
	}
}

// The layout must follow the account that installed the agent, so deletion can
// clean it after the credential logs in as somebody else.
func TestAgentLayoutFollowsInstallAccount(t *testing.T) {
	root := agentLayoutFor("install1", 0, "/root")
	if root.Base != "/var/lib/talus-agent/install1" || root.UnitDir != "/etc/systemd/system" || root.Control() != "systemctl" {
		t.Fatalf("root layout = %+v", root)
	}
	user := agentLayoutFor("install1", 1000, "/home/alice")
	if user.Base != "/home/alice/.local/share/talus-agent/install1" ||
		user.UnitDir != "/home/alice/.config/systemd/user" || user.Control() != "systemctl --user" {
		t.Fatalf("user layout = %+v", user)
	}
	uid, home := 1000, "/home/alice"
	node := model.AgentNode{InstallID: "install1", InstallUID: &uid, InstallHome: &home}
	recorded, ok := recordedLayout(node)
	if !ok || recorded != user {
		t.Fatalf("recorded layout = %+v, ok=%t", recorded, ok)
	}
	node.InstallHome = nil
	if _, ok := recordedLayout(node); ok {
		t.Fatal("layout without a recorded home was accepted")
	}
	node = nodeForLayout("install1", user)
	if node.InstallUID == nil || *node.InstallUID != 1000 || node.InstallHome == nil || *node.InstallHome != "/home/alice" {
		t.Fatalf("layout node = %+v", node)
	}
	for _, invalid := range []string{"", "relative/path", "/home/../etc", "/home/alice\nrm -rf /"} {
		if validInstallHome(invalid) {
			t.Fatalf("invalid home %q accepted", invalid)
		}
	}
}

// A user unit is stopped through its owner's manager, never through the manager
// of whichever account the credential happens to use now.
func TestResidentUninstallScriptStopsRecordedUserUnit(t *testing.T) {
	layout := agentLayoutFor("install1", 1000, "/home/alice")
	script := residentUninstallScript(layout, "talus-agent-install1.service", "alice", true)
	for _, want := range []string{
		"runuser -u 'alice' -- env XDG_RUNTIME_DIR='/run/user/1000' sh -c ",
		"systemctl --user disable --now ",
		"/home/alice/.config/systemd/user/talus-agent-install1.service",
		"/home/alice/.local/share/talus-agent/install1",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("uninstall script missing %q:\n%s", want, script)
		}
	}
	// Without a reachable owner no unrelated service manager may be touched.
	blind := residentUninstallScript(layout, "talus-agent-install1.service", "", false)
	if strings.Contains(blind, "systemctl") {
		t.Fatalf("unreachable account touched a service manager:\n%s", blind)
	}
	if !strings.Contains(blind, "rm -rf '/home/alice/.local/share/talus-agent/install1'") {
		t.Fatalf("blind cleanup did not remove the recorded state:\n%s", blind)
	}
}

// A credential switch must not make deletion clean the layout of the account
// that is logged in now: the recorded install layout owns the unit and state.
func TestResidentUninstallUsesRecordedLayoutAfterCredentialSwitch(t *testing.T) {
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
	seedPool(t, pool, 9, dialTestSSH(t, address, hostKey))
	sshSvc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	lease, err := sshSvc.AcquireLease(context.Background(), 9, LeaseConsumerAgent)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	uid, home := 1000, "/home/alice"
	node := model.AgentNode{ServerID: 9, InstallID: "install1", InstallUID: &uid, InstallHome: &home}
	monitor := NewResidentMonitor(nil, nil, nil, "", false, 5)
	if err := monitor.uninstall(context.Background(), node, lease); err != nil {
		t.Fatal(err)
	}
	next := func() string {
		select {
		case command := <-commands:
			return command
		case <-time.After(10 * time.Second):
			t.Fatal("uninstall did not run")
			return ""
		}
	}
	if probe := next(); !strings.Contains(probe, "id -un 1000") {
		t.Fatalf("probe did not resolve the recorded owner: %q", probe)
	}
	script := next()
	for _, want := range []string{
		"runuser -u 'alice' -- env XDG_RUNTIME_DIR='/run/user/1000' sh -c ",
		"systemctl --user disable --now ",
		"/home/alice/.config/systemd/user/talus-agent-install1.service",
		"/home/alice/.local/share/talus-agent/install1",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("uninstall script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "/var/lib/talus-agent/install1") {
		t.Fatalf("uninstall cleaned the current login's layout instead of the recorded one:\n%s", script)
	}
}

// The cross-account script must survive quoting when the owning home contains
// spaces, both for the manager switch and for the state it removes.
func TestResidentUninstallScriptCrossAccountExecutesWithQuotedPaths(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	home := filepath.Join(dir, "al ice")
	base := filepath.Join(home, ".local/share/talus-agent/install1")
	unitDir := filepath.Join(home, ".config/systemd/user")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(unitDir, 0700); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(unitDir, "talus-agent-install1.service")
	if err := os.WriteFile(unit, []byte("unit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	runuser := "#!/bin/sh\nprintf 'runuser %s\\n' \"$*\" >> " + shellQuote(log) + "\nshift 2\nshift\nexec \"$@\"\n"
	systemctl := "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> " + shellQuote(log) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "runuser"), []byte(runuser), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(systemctl), 0700); err != nil {
		t.Fatal(err)
	}
	layout := agentLayoutFor("install1", 1000, home)
	cmd := exec.Command("sh", "-c", residentUninstallScript(layout, "talus-agent-install1.service", "alice", true))
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-account uninstall: %v %s", err, out)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("recorded state survived the uninstall: %v", err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("recorded unit survived the uninstall: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"runuser -u alice -- env XDG_RUNTIME_DIR=/run/user/1000 sh -c ",
		"systemctl --user daemon-reload",
	} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("call %q missing in %q", want, calls)
		}
	}
}
