package agentwindows

import (
	"bytes"
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
	"sync/atomic"
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
		ctx, cancel := context.WithTimeout(context.Background(), (ActivationWaitSeconds+30)*time.Second)
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
	var downloads atomic.Int32
	var resumed atomic.Bool
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
		if r.URL.Path == "/artifact" {
			downloads.Add(1)
			if r.Header.Get("Range") == "bytes=12345-" {
				resumed.Store(true)
			}
			w.Header().Set("ETag", `"`+hash(bin)+`"`)
			http.ServeContent(w, r, "agent.exe", time.Time{}, bytes.NewReader(bin))
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
		for name, data := range map[string][]byte{"config.json": b, "activate.ps1": activation} {
			if err := os.WriteFile(filepath.Join(v.Stage(), name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if interval == 1 {
			if err := os.WriteFile(filepath.Join(v.Base(), "cache", hash(bin)+".part"), bin[:12345], 0600); err != nil {
				t.Fatal(err)
			}
		}
		downloadConfig := filepath.Join(v.Stage(), "download.json")
		downloadJSON, _ := json.Marshal(map[string]any{"url": hub.URL + "/artifact", "token": token, "allow_http": true})
		if err := os.WriteFile(downloadConfig, downloadJSON, 0600); err != nil {
			t.Fatal(err)
		}
		fetchScript := v.FetchFile(downloadConfig, hash(bin), int64(len(bin)), "pull", 300)
		fetchPath := filepath.Join(v.Stage(), "fetch.ps1")
		if err := os.WriteFile(fetchPath, fetchScript, 0600); err != nil {
			t.Fatal(err)
		}
		mustRun(RunFetch(fetchPath, hash(fetchScript), 300))
		// Launch returns before activation completes; SCM and Task Scheduler
		// must finish installation independently of this PowerShell process.
		mustRun(v.Launch(hash(activation)))
		return v, b
	}
	first, _ := stage(hub.URL, 1)
	mustRun(first.Wait(ActivationWaitSeconds))
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
	mustRun(updated.Wait(ActivationWaitSeconds))
	failed, _ := stage("http://127.0.0.1:1", 3)
	if out, err := run(failed.Wait(ActivationWaitSeconds)); err == nil {
		t.Fatalf("unreachable Hub accepted: %s", out)
	}
	if downloads.Load() != 1 || !resumed.Load() {
		t.Fatal("initial HTTP resume or cached configuration-only upgrade failed")
	}
	actual, err := os.ReadFile(filepath.Join(l.Base(), "config.json"))
	if err != nil || string(actual) != string(good) {
		t.Fatalf("previous configuration not restored: %v", err)
	}
	mustRun("$ErrorActionPreference='Stop'; if ((Get-Service -Name " + Quote(l.Name()) + ").Status -ne 'Running') { exit 1 }")
	// ACLs must protect the token and outbox from non-administrator users.
	mustRun("$a=Get-Acl -LiteralPath " + Quote(l.Base()) + "; if (!$a.AreAccessRulesProtected) { exit 1 }; foreach($r in $a.Access){$s=$r.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value;if($s -ne 'S-1-5-18' -and $s -ne 'S-1-5-32-544'){exit 1}}")
	// A real open Windows file denies deletion. The first task must report
	// failure and keep its retry entry even though the Hub record is gone.
	blocked, err := os.OpenFile(filepath.Join(l.Base(), "blocked-cleanup"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	mustRun(l.PrepareRemoval())
	if err = os.WriteFile(l.RemovalPath(), []byte("throw 'tampered cleanup'"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(l.RemoveDetached()); err == nil {
		t.Fatalf("unverified cleanup launched: %s", out)
	}
	if err = os.WriteFile(l.RemovalPath(), l.RemovalFile(), 0600); err != nil {
		t.Fatal(err)
	}
	mustRun(l.RemoveDetached())
	waitReceipt := func(status string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			b, _ := os.ReadFile(filepath.Join(l.Root(), l.Name()+".remove.json"))
			var receipt struct {
				Status  string
				Attempt int
			}
			_ = json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\xef\xbb\xbf")), &receipt)
			if receipt.Status == status {
				if status == "succeeded" && receipt.Attempt < 2 {
					t.Fatalf("automatic retry was not exercised: %s", b)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("removal receipt: want %s, got %s", status, b)
			}
			time.Sleep(time.Second)
		}
	}
	waitReceipt("failed", 40*time.Second)
	mustRun("$t=Get-ScheduledTask -TaskName " + Quote(l.Name()+"-Remove") + "; if($t.State -ne 'Running'){exit 1}")
	if _, err = os.Stat(l.RemovalPath()); err != nil {
		t.Fatalf("retry script lost: %v", err)
	}
	if err = blocked.Close(); err != nil {
		t.Fatal(err)
	}
	// The detached task retries after one minute, with no Hub request or
	// manual Start-ScheduledTask to conceal a broken retry policy.
	waitReceipt("succeeded", 100*time.Second)
	mustRun(l.WaitRemoval(0))
	mustRun(l.Remove()) // idempotent, including a missing service and directory
	if _, err := os.Stat(l.Base()); !os.IsNotExist(err) {
		t.Fatalf("installation remains: %v", err)
	}
	mustRun("if(Get-Service -Name " + Quote(l.Name()) + " -ErrorAction SilentlyContinue){exit 1}")
}
