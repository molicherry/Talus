package service

import (
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
