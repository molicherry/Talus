package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
