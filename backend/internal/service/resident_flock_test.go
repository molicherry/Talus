package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/repository"
)

// Exercise both real lock consumers with only the flock interface supported by
// BusyBox. Delegating to the real nonblocking syscall preserves actual locking.
func TestResidentAndArtifactLocksSupportMinimalFlock(t *testing.T) {
	realFlock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("flock is required for resident installations")
	}
	for _, operation := range []string{"activation-lock", "optional-removal-lock", "artifact-store"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			toolDir := filepath.Join(dir, "tools")
			if err := os.Mkdir(toolDir, 0700); err != nil {
				t.Fatal(err)
			}
			flock := "#!/bin/sh\n" +
				"test \"$#\" -eq 2 && test \"$1\" = -n && test \"$2\" = 9 || exit 2\n" +
				"exec " + shellQuote(realFlock) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(toolDir, "flock"), []byte(flock), 0700); err != nil {
				t.Fatal(err)
			}
			unit := filepath.Join(dir, "agent.service")
			script := "set -eu\n" + residentLockScript(unit, operation == "optional-removal-lock") + "printf '%s\\n' TALUS_LOCK_OWNED\n"
			var artifact []byte
			next := filepath.Join(dir, "next")
			if operation == "artifact-store" {
				artifact = []byte("verified artifact on a minimal flock host")
				if err := os.WriteFile(next, artifact, 0600); err != nil {
					t.Fatal(err)
				}
				config := filepath.Join(dir, "fetch-config")
				if err := os.WriteFile(config, []byte("private dummy config"), 0600); err != nil {
					t.Fatal(err)
				}
				a := agentproto.Artifact{SHA256: repository.TokenHash(string(artifact)), Size: int64(len(artifact))}
				script = linuxArtifactCommand(dir, filepath.Join(dir, "current"), next, config, "store", a, 5)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("minimal flock %s: %v %s", operation, err, out)
			}
			if operation == "artifact-store" {
				got, err := os.ReadFile(next)
				if err != nil || !bytes.Equal(got, artifact) || !strings.Contains(string(out), "TALUS_ARTIFACT_READY") {
					t.Fatalf("artifact was not verified and published: %q %v %s", got, err, out)
				}
			} else if !strings.Contains(string(out), "TALUS_LOCK_OWNED") {
				t.Fatalf("minimal flock did not acquire %s: %s", operation, out)
			}
		})
	}
}

// Replace sleep with a bounded clock fixture: a permanently held lock may
// consume ten one-second waits, while an unsupported tool must fail at once.
func TestResidentFlockWaitIsBoundedAndFailsClosed(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status string
		waits  int
		marker string
	}{
		{"held", "1", 10, "TALUS_LOCK_TIMEOUT"},
		{"unsupported-tool", "2", 0, "TALUS_LOCK_UNAVAILABLE"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			waitLog, action := filepath.Join(dir, "waits"), filepath.Join(dir, "action")
			for name, contents := range map[string]string{
				"flock": "#!/bin/sh\n[ \"$#\" -eq 2 ] && [ \"$1\" = -n ] && [ \"$2\" = 9 ] || exit 2\nexit " + scenario.status + "\n",
				"sleep": "#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = 1 ] || exit 2\nprintf '%s\\n' waited >> " + shellQuote(waitLog) + "\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := "set -eu\n" + residentLockScript(filepath.Join(dir, "agent.service"), false) + "printf unsafe > " + shellQuote(action) + "\n"
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil || !strings.Contains(string(out), scenario.marker) {
				t.Fatalf("lock failure did not terminate with its bounded reason: err=%v ctx=%v output=%s", err, ctx.Err(), out)
			}
			if _, err := os.Stat(action); !os.IsNotExist(err) {
				t.Fatal("script continued without holding its lock")
			}
			waits, err := os.ReadFile(waitLog)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if got := strings.Count(string(waits), "waited\n"); got != scenario.waits {
				t.Fatalf("one-second waits = %d, want %d", got, scenario.waits)
			}
		})
	}
}
