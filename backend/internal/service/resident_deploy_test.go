package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
)

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
	script := residentUninstallScript(base, unit, "talus-agent-test.service", shellQuote(ctl))
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
	uninstall := exec.CommandContext(ctx, "sh", "-c", residentUninstallScript(base, unit, "agent.service", shellQuote(ctl)))
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
			script := residentUninstallScript(base, unit, "agent.service", shellQuote(ctl))
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
	base := model.AgentNode{Deployment: "ready", InstalledHash: "h", ConfigHash: "c"}
	for _, tc := range []struct {
		name string
		node model.AgentNode
		last time.Time
		want bool
	}{
		{"ready and fresh", base, fresh, false},
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
