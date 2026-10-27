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
	for _, scenario := range []string{"resume", "range-ignored", "corrupt", "installed-reuse", "sftp-cache", "busy", "busy-resume", "rate-limited", "redirect", "redirect-resume", "bad-gateway", "interrupted", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			payload := bytes.Repeat([]byte("windows-agent"), 4096)
			sum := sha256.Sum256(payload)
			digest := hex.EncodeToString(sum[:])
			token := strings.Repeat("a", 64)
			layout := Layout{Home: t.TempDir(), InstallID: "install ' path", Attempt: "attempt"}
			base, stage := layout.Base(), layout.Stage()
			cache := filepath.Join(base, "cache")
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(cache, 0700); err != nil {
				t.Fatal(err)
			}
			if scenario == "resume" || scenario == "range-ignored" || strings.HasSuffix(scenario, "-resume") {
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
					http.Error(w, "bad token", http.StatusUnauthorized)
					return
				}
				mu.Lock()
				ranges = append(ranges, r.Header.Get("Range"))
				count := len(ranges)
				mu.Unlock()
				w.Header().Set("ETag", `"`+digest+`"`)
				status := map[string]int{"busy": 503, "busy-resume": 503, "rate-limited": 429, "redirect": 302, "redirect-resume": 302, "bad-gateway": 502}[scenario]
				if status != 0 {
					w.Header().Set("Location", "https://other.example/artifact")
					w.WriteHeader(status)
					_, _ = w.Write(payload)
					return
				}
				if (scenario == "interrupted" || scenario == "deadline") && count == 1 {
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					_, _ = w.Write(payload[:len(payload)/2])
					w.(http.Flusher).Flush()
					if scenario == "deadline" {
						select {
						case <-r.Context().Done():
						case <-time.After(3 * time.Second):
						}
					} else {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
					}
					return
				}
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
				seconds := 10
				if scenario == "deadline" {
					seconds = 1
				}
				stubs := "function Assert-Admin {}\nfunction Protect-Directory($p) { Assert-Path $p; [void][IO.Directory]::CreateDirectory($p) }\nfunction Lock-Install { return [IO.File]::Open((Join-Path $base '.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None) }\n"
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				scriptPath := filepath.Join(stage, "fetch.ps1")
				script := []byte(strings.Replace(string(layout.FetchFile(config, digest, int64(len(payload)), mode, 10)), common, common+stubs, 1))
				if err := os.WriteFile(scriptPath, script, 0600); err != nil {
					t.Fatal(err)
				}
				scriptHash := sha256.Sum256(script)
				cmd := exec.CommandContext(ctx, ps, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", RunFetch(scriptPath, hex.EncodeToString(scriptHash[:]), seconds))
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
			if scenario == "interrupted" || scenario == "deadline" {
				if code, out := run(mode); code != 76 {
					t.Fatalf("interruption lost progress classification: %d %s", code, out)
				}
				if p, err := os.ReadFile(filepath.Join(cache, digest+".part")); err != nil || !bytes.Equal(p, payload[:len(payload)/2]) {
					t.Fatalf("partial bytes not retained: %d %v", len(p), err)
				}
			}
			if scenario == "sftp-cache" {
				mode = "store"
				if err := os.WriteFile(filepath.Join(stage, "agent.exe"), payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out := run(mode)
			wantFailure := map[string]int{"busy": 79, "busy-resume": 79, "rate-limited": 79, "redirect": 75, "redirect-resume": 75, "bad-gateway": 75}[scenario]
			if wantFailure != 0 {
				if code != wantFailure {
					t.Fatalf("incorrect HTTP failure classification: %d want=%d %s", code, wantFailure, out)
				}
				p, err := os.ReadFile(filepath.Join(cache, digest+".part"))
				if strings.HasSuffix(scenario, "-resume") {
					if err != nil || !bytes.Equal(p, payload[:57]) {
						t.Fatalf("HTTP error lost or polluted partial bytes: %d %v", len(p), err)
					}
				} else if len(p) != 0 {
					t.Fatal("HTTP error body became artifact progress")
				}
				return
			}
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
