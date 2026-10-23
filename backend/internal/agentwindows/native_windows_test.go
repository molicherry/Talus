package agentwindows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/agentwire"
)

// This uses the real Windows SCM, Task Scheduler and shipped agent. Only the
// Hub's HTTP endpoint is a fixture. The installer runs detached from its caller.
func TestWindowsNativeDeploymentUpgradeRollbackAndRemoval(t *testing.T) {
	if os.Getenv("TALUS_WINDOWS_INTEGRATION") != "1" {
		t.Skip("set TALUS_WINDOWS_INTEGRATION=1 on an administrator Windows runner")
	}
	run := func(script string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		// Exercise cmd.exe's real command-length and quoting rules, as used by
		// the default Windows OpenSSH shell, before invoking PowerShell 5.1.
		return exec.CommandContext(ctx, "cmd.exe", "/d", "/c", Command(script)).CombinedOutput()
	}
	mustRun := func(script string) {
		t.Helper()
		if out, err := run(script); err != nil {
			t.Fatalf("PowerShell: %v\n%s", err, out)
		}
	}
	artifact := filepath.Join(t.TempDir(), "agent.exe")
	build := exec.Command("go", "build", "-o", artifact, "../../cmd/agent")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v %s", err, out)
	}
	bin, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	hash := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	var mu sync.Mutex
	var latest agentwire.Sample
	token := strings.Repeat("a", 64)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/connect") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req agentwire.Request
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Validate(time.Now()) != nil {
			http.Error(w, "bad report", 400)
			return
		}
		reply := agentwire.Response{ServerTime: time.Now().UTC()}
		mu.Lock()
		if req.Latest != nil {
			latest = *req.Latest
		}
		for _, s := range req.Samples {
			reply.Ack = append(reply.Ack, s.ID)
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer hub.Close()
	id, err := agentwire.NewID()
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{Home: strings.ReplaceAll(os.Getenv("ProgramData"), "\\", "/"), InstallID: id}
	if !ValidHome(l.Home) {
		t.Fatalf("invalid ProgramData: %q", l.Home)
	}
	t.Cleanup(func() {
		if out, err := run(l.Remove()); err != nil {
			t.Errorf("cleanup: %v %s", err, out)
		}
	})
	stage := func(url string, interval int) (Layout, []byte) {
		t.Helper()
		a, err := agentwire.NewID()
		if err != nil {
			t.Fatal(err)
		}
		v := l
		v.Attempt = a
		mustRun(v.Prepare())
		cfg := agentwire.Config{HubURL: url, Token: token, StateDir: l.Base() + "/outbox", IntervalSeconds: interval, AllowHTTP: true}
		b, _ := json.Marshal(cfg)
		activation := v.ActivationFile(hash(bin), hash(b))
		for name, data := range map[string][]byte{"agent.exe": bin, "config.json": b, "activate.ps1": activation} {
			if err := os.WriteFile(filepath.Join(v.Stage(), name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		// Launch returns before activation completes; SCM and Task Scheduler
		// must finish installation independently of this PowerShell process.
		mustRun(v.Launch(hash(activation)))
		return v, b
	}
	first, _ := stage(hub.URL, 1)
	mustRun(first.Wait(70))
	deadline := time.Now().Add(30 * time.Second)
	for {
		mu.Lock()
		s := latest
		mu.Unlock()
		if s.CPU != nil && s.MemoryTotal != nil && s.DiskTotal != nil && s.OS != nil {
			if s.Load1 != nil || s.Load5 != nil || s.Load15 != nil || s.Errors["load"] != "" {
				t.Fatalf("fabricated Windows load: %+v", s)
			}
			if s.BootID == "" || s.Uptime == nil || !strings.HasPrefix(*s.OS, "windows") {
				t.Fatalf("Windows metadata: %+v", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Windows collection incomplete: %+v", s)
		}
		time.Sleep(time.Second)
	}
	// A valid configuration update replaces the running service and authenticates
	// with the new configuration hash before it is declared successful.
	updated, good := stage(hub.URL, 2)
	mustRun(updated.Wait(70))
	failed, _ := stage("http://127.0.0.1:1", 3)
	if out, err := run(failed.Wait(70)); err == nil {
		t.Fatalf("unreachable Hub accepted: %s", out)
	}
	actual, err := os.ReadFile(filepath.Join(l.Base(), "config.json"))
	if err != nil || string(actual) != string(good) {
		t.Fatalf("previous configuration not restored: %v", err)
	}
	mustRun("$ErrorActionPreference='Stop'; if ((Get-Service -Name " + Quote(l.Name()) + ").Status -ne 'Running') { exit 1 }")
	// ACLs must protect the token and outbox from non-administrator users.
	mustRun("$a=Get-Acl -LiteralPath " + Quote(l.Base()) + "; if (!$a.AreAccessRulesProtected) { exit 1 }; foreach($r in $a.Access){$s=$r.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value;if($s -ne 'S-1-5-18' -and $s -ne 'S-1-5-32-544'){exit 1}}")
	mustRun(l.RemoveDetached())
	deadline = time.Now().Add(40 * time.Second)
	for {
		if _, err := os.Stat(l.Base()); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached removal did not finish")
		}
		time.Sleep(time.Second)
	}
	mustRun(l.Remove()) // idempotent, including a missing service and directory
	if _, err := os.Stat(l.Base()); !os.IsNotExist(err) {
		t.Fatalf("installation remains: %v", err)
	}
	mustRun("if(Get-Service -Name " + Quote(l.Name()) + " -ErrorAction SilentlyContinue){exit 1}")
}
