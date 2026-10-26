package service

import (
	"bytes"
	"context"
	"fmt"
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

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/repository"
)

func TestAgentArtifactPullResumesCachesAndRejectsCorruption(t *testing.T) {
	for _, scenario := range []string{"interrupted", "range-ignored", "corrupt", "installed-reuse", "sftp-cache", "redirect", "redirect-resume", "busy", "busy-resume", "rate-limited", "bad-gateway", "missing-hash"} {
		t.Run(scenario, func(t *testing.T) {
			payload := bytes.Repeat([]byte("verified-agent-bytes"), 16<<10)
			a := agentproto.Artifact{SHA256: repository.TokenHash(string(payload)), Size: int64(len(payload))}
			base := filepath.Join(t.TempDir(), "quoted ' node")
			if err := os.MkdirAll(filepath.Join(base, "cache"), 0700); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var ranges []string
			token := strings.Repeat("a", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("If-Range") != `"`+a.SHA256+`"` {
					t.Error("download authentication/validator missing")
					http.Error(w, "unauthorized", 401)
					return
				}
				mu.Lock()
				ranges = append(ranges, r.Header.Get("Range"))
				count := len(ranges)
				mu.Unlock()
				w.Header().Set("ETag", `"`+a.SHA256+`"`)
				status := map[string]int{"redirect": 302, "redirect-resume": 302, "busy": 503, "busy-resume": 503, "rate-limited": 429, "bad-gateway": 502}[scenario]
				if status != 0 {
					w.Header().Set("Location", "https://other.example/artifact")
					w.WriteHeader(status)
					_, _ = w.Write(payload)
					return
				}
				if scenario == "interrupted" && count == 1 {
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					w.WriteHeader(200)
					_, _ = w.Write(payload[:len(payload)/2])
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				data := payload
				if scenario == "range-ignored" {
					r.Header.Del("Range")
				}
				if scenario == "corrupt" {
					data = bytes.Repeat([]byte("x"), len(payload))
				}
				http.ServeContent(w, r, "agent", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()
			current := filepath.Join(base, "agent")
			if scenario == "installed-reuse" {
				if err := os.WriteFile(current, payload, 0700); err != nil {
					t.Fatal(err)
				}
			}
			part := filepath.Join(base, "cache", a.SHA256+".part")
			if scenario == "range-ignored" || strings.HasSuffix(scenario, "-resume") {
				if err := os.WriteFile(part, payload[:91], 0600); err != nil {
					t.Fatal(err)
				}
			}
			run := func(attempt, mode string) (string, int) {
				t.Helper()
				next := filepath.Join(base, ".next-"+attempt)
				config := next + ".fetch"
				if err := os.WriteFile(config, artifactCurlConfig(artifactDownload{URL: server.URL, Token: token, AllowHTTP: true}, a.SHA256), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "store" {
					if err := os.WriteFile(next, payload, 0600); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", linuxArtifactCommand(base, current, next, config, mode, a, 8))
				cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
				if scenario == "missing-hash" {
					bin := t.TempDir()
					rm, err := exec.LookPath("rm")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(rm, filepath.Join(bin, "rm")); err != nil {
						t.Fatal(err)
					}
					cmd.Env = append(cmd.Env, "PATH="+bin)
				}
				out, err := cmd.CombinedOutput()
				code := 0
				if err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatalf("fetch command: %v %s", err, out)
					}
				}
				if strings.Contains(string(out), token) {
					t.Fatal("download exposed credential in output")
				}
				if _, err := os.Stat(config); !os.IsNotExist(err) {
					t.Fatal("download retained its credential file")
				}
				return string(out), code
			}
			if scenario == "interrupted" {
				if out, code := run("first", "pull"); code != 76 {
					t.Fatalf("interruption did not retain progress: %d %s", code, out)
				}
				p, err := os.ReadFile(part)
				if err != nil || len(p) != len(payload)/2 {
					t.Fatalf("partial progress lost: %d %v", len(p), err)
				}
			}
			mode := "pull"
			if scenario == "sftp-cache" {
				mode = "store"
			}
			out, code := run("second", mode)
			wantFailure := map[string]int{"redirect": 75, "redirect-resume": 75, "busy": 79, "busy-resume": 79, "rate-limited": 79, "bad-gateway": 75, "missing-hash": 78}[scenario]
			if wantFailure != 0 {
				if code != wantFailure {
					t.Fatalf("incorrect failure classification: code=%d want=%d output=%s", code, wantFailure, out)
				}
				if strings.HasSuffix(scenario, "-resume") {
					if p, err := os.ReadFile(part); err != nil || !bytes.Equal(p, payload[:91]) {
						t.Fatalf("HTTP error lost or polluted existing progress: %d %v", len(p), err)
					}
				} else if p, _ := os.ReadFile(part); len(p) != 0 {
					t.Fatal("HTTP error body became artifact progress")
				}
				return
			}
			if scenario == "corrupt" {
				if code != 77 {
					t.Fatalf("corrupt artifact accepted: %d %s", code, out)
				}
				if _, err := os.Stat(filepath.Join(base, ".next-second")); !os.IsNotExist(err) {
					t.Fatal("published corrupt artifact")
				}
				return
			}
			if code != 0 || !strings.HasSuffix(strings.TrimSpace(out), "TALUS_ARTIFACT_READY") {
				t.Fatalf("pull failed: %d %s", code, out)
			}
			got, err := os.ReadFile(filepath.Join(base, ".next-second"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("artifact changed during resume/cache publication")
			}
			mu.Lock()
			count := len(ranges)
			firstRanges := append([]string(nil), ranges...)
			mu.Unlock()
			if scenario == "interrupted" && (count != 2 || firstRanges[1] != fmt.Sprintf("bytes=%d-", len(payload)/2)) {
				t.Fatalf("did not resume exact retained bytes: %v", firstRanges)
			}
			if scenario == "range-ignored" && (count != 2 || firstRanges[0] != "bytes=91-" || firstRanges[1] != "") {
				t.Fatalf("did not restart safely when Range was ignored: %v", firstRanges)
			}
			if (scenario == "installed-reuse" || scenario == "sftp-cache") && count != 0 {
				t.Fatal("downloaded an already verified local artifact")
			}
			if out, code := run("third", "pull"); code != 0 {
				t.Fatalf("cache reuse: %d %s", code, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(ranges) != count {
				t.Fatal("cached configuration update downloaded the binary again")
			}
		})
	}
}
