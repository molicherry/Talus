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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Execute the actual download body on PowerShell, with only Windows account/ACL
// operations substituted on Linux. Native Windows acceptance retains real ACLs.
func TestWindowsArtifactPullResumesAndReusesVerifiedCache(t *testing.T) {
	ps, err := exec.LookPath("pwsh")
	if err != nil {
		ps, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell unavailable")
	}
	for _, scenario := range []string{"resume", "range-ignored", "corrupt", "installed-reuse", "sftp-cache"} {
		t.Run(scenario, func(t *testing.T) {
			payload := bytes.Repeat([]byte("windows-agent"), 4096)
			sum := sha256.Sum256(payload)
			digest := hex.EncodeToString(sum[:])
			token := strings.Repeat("a", 64)
			base := filepath.Join(t.TempDir(), "install ' path")
			stage := filepath.Join(base, "staging", "attempt")
			cache := filepath.Join(base, "cache")
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(cache, 0700); err != nil {
				t.Fatal(err)
			}
			if scenario == "resume" || scenario == "range-ignored" {
				if err := os.WriteFile(filepath.Join(cache, digest+".part"), payload[:57], 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "installed-reuse" {
				if err := os.WriteFile(filepath.Join(base, "agent.exe"), payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			var ranges []string
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					http.Error(w, "bad token", 401)
					return
				}
				mu.Lock()
				ranges = append(ranges, r.Header.Get("Range"))
				mu.Unlock()
				w.Header().Set("ETag", `"`+digest+`"`)
				if scenario == "range-ignored" {
					r.Header.Del("Range")
				}
				data := payload
				if scenario == "corrupt" {
					data = bytes.Repeat([]byte("x"), len(payload))
				}
				http.ServeContent(w, r, "agent.exe", time.Time{}, bytes.NewReader(data))
			}))
			defer hub.Close()
			run := func(mode string) (int, string) {
				t.Helper()
				config := filepath.Join(stage, "download.json")
				raw, _ := json.Marshal(map[string]any{"url": hub.URL, "token": token, "allow_http": true})
				if err := os.WriteFile(config, raw, 0600); err != nil {
					t.Fatal(err)
				}
				vars := "$ErrorActionPreference='Stop'\n$base=" + Quote(base) + "\n$root=" + Quote(base) + "\n$stage=" + Quote(stage) + "\n$fetchConfig=" + Quote(config) + "\n$digest=" + Quote(digest) + "\n$mode=" + Quote(mode) + "\n$size=" + strconv.Itoa(len(payload)) + "L\n$seconds=10\n$name='test'\n"
				stubs := "function Assert-Admin {}\nfunction Protect-Directory($p) { Assert-Path $p; [void][IO.Directory]::CreateDirectory($p) }\nfunction Lock-Install { return [IO.File]::Open((Join-Path $base '.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None) }\n"
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				scriptPath := filepath.Join(stage, "fetch.ps1")
				if err := os.WriteFile(scriptPath, []byte("\xef\xbb\xbf"+vars+common+stubs+fetch), 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", scriptPath)
				cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
				out, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatalf("PowerShell: %v %s", err, out)
					}
				}
				if strings.Contains(string(out), token) {
					t.Fatal("download exposed credential")
				}
				if _, err := os.Stat(config); !os.IsNotExist(err) {
					t.Fatalf("download retained credential file: %d %s", code, out)
				}
				return code, string(out)
			}
			mode := "pull"
			if scenario == "sftp-cache" {
				mode = "store"
				if err := os.WriteFile(filepath.Join(stage, "agent.exe"), payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out := run(mode)
			if scenario == "corrupt" {
				if code != 77 {
					t.Fatalf("corrupt bytes accepted: %d %s", code, out)
				}
				return
			}
			if code != 0 || !strings.Contains(out, "TALUS_ARTIFACT_READY") {
				t.Fatalf("download failed: %d %s", code, out)
			}
			got, err := os.ReadFile(filepath.Join(stage, "agent.exe"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("resumed bytes differ from artifact")
			}
			mu.Lock()
			count := len(ranges)
			requests := append([]string(nil), ranges...)
			mu.Unlock()
			if scenario == "resume" && (count != 1 || requests[0] != "bytes=57-") {
				t.Fatalf("resume offset lost: %v", requests)
			}
			if scenario == "range-ignored" && (count != 2 || requests[0] != "bytes=57-" || requests[1] != "") {
				t.Fatalf("invalid Range fallback: %v", requests)
			}
			if (scenario == "installed-reuse" || scenario == "sftp-cache") && count != 0 {
				t.Fatal("redownloaded installed artifact")
			}
			if code, out := run("pull"); code != 0 {
				t.Fatalf("cache reuse: %d %s", code, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(ranges) != count {
				t.Fatal("configuration-only retry downloaded binary again")
			}
		})
	}
}
