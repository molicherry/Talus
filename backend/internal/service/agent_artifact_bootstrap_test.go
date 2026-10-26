package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/agentwire"
	"github.com/vpsmanager/backend/internal/repository"
	"golang.org/x/crypto/ssh"
)

type artifactCountingChannel struct {
	ssh.Channel
	bytes *atomic.Int64
}

func (c artifactCountingChannel) Read(p []byte) (int, error) {
	n, err := c.Channel.Read(p)
	c.bytes.Add(int64(n))
	return n, err
}

// Exercise the production coordinator over real SSH and SFTP, with the remote
// shell confined by its test-generated paths. HTTP success avoids binary SFTP;
// an unavailable pull seeds the same verified cache through the fallback.
func TestAgentArtifactBootstrapUsesPullAndFallsBackToSFTP(t *testing.T) {
	for _, name := range []string{"http-pull", "sftp-fallback", "redirect-fallback", "busy-retry", "progress-retry", "deadline-retry", "missing-hash"} {
		t.Run(name, func(t *testing.T) {
			unavailable := name == "sftp-fallback" || name == "redirect-fallback"
			base := filepath.Join(t.TempDir(), "remote ' stage")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("bootstrap-artifact"), 32<<10)
			a := agentproto.Artifact{File: "agent", SHA256: repository.TokenHash(string(payload)), Size: int64(len(payload))}
			local := t.TempDir()
			if err := os.WriteFile(filepath.Join(local, a.File), payload, 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			var retryReady atomic.Bool
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := requests.Add(1)
				if r.URL.Path != "/api/v1/agent/artifacts/"+a.SHA256 {
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
					http.Error(w, "bad token", 401)
					return
				}
				if unavailable {
					status := http.StatusNotFound
					if name == "redirect-fallback" {
						status = http.StatusFound
					}
					http.Error(w, "download unavailable", status)
					return
				}
				if name == "busy-retry" && !retryReady.Load() {
					http.Error(w, "transfers busy", http.StatusServiceUnavailable)
					return
				}
				if (name == "progress-retry" || name == "deadline-retry") && count == 1 {
					w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
					_, _ = w.Write(payload[:len(payload)/2])
					w.(http.Flusher).Flush()
					if name == "deadline-retry" {
						select {
						case <-r.Context().Done():
						case <-time.After(5 * time.Second):
						}
					} else {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
					}
					return
				}
				w.Header().Set("ETag", `"`+a.SHA256+`"`)
				http.ServeContent(w, r, "agent", time.Time{}, bytes.NewReader(payload))
			}))
			defer hub.Close()
			var sftpBytes atomic.Int64
			address, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
				defer ch.Close()
				if command == "subsystem:sftp" {
					server, err := sftp.NewServer(artifactCountingChannel{Channel: ch, bytes: &sftpBytes})
					if err != nil {
						return
					}
					defer server.Close()
					_ = server.Serve()
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", command)
				cmd.Stdout = ch
				cmd.Stderr = ch.Stderr()
				cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
				if name == "missing-hash" {
					cmd.Env = append(cmd.Env, "PATH="+filepath.Join(base, "tools"))
				}
				code := uint32(0)
				if err := cmd.Run(); err != nil {
					code = 1
					if e, ok := err.(*exec.ExitError); ok {
						code = uint32(e.ExitCode())
					}
				}
				exitStatus(ch, code)
			})
			pool := newTestPool(t)
			seedPool(t, pool, 1, dialTestSSH(t, address, key))
			svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
			lease, err := svc.AcquireLease(context.Background(), 1, LeaseConsumerAgent)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			m := NewResidentMonitor(nil, nil, NewAgentDeploymentService(svc, local), hub.URL, true, 5)
			cfg := agentwire.Config{HubURL: hub.URL, Token: strings.Repeat("a", 64), StateDir: filepath.Join(base, "outbox"), IntervalSeconds: 5, AllowHTTP: true}
			next := filepath.Join(base, ".next-one")
			if name == "missing-hash" {
				if err := os.Mkdir(filepath.Join(base, "tools"), 0700); err != nil {
					t.Fatal(err)
				}
				rm, err := exec.LookPath("rm")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(rm, filepath.Join(base, "tools", "rm")); err != nil {
					t.Fatal(err)
				}
				err = m.stageArtifact(context.Background(), lease, a, cfg, base, filepath.Join(base, "agent"), next, nil)
				if ReasonFromError(err) != ReasonAgentToolMissing || requests.Load() != 0 || sftpBytes.Load() > a.Size/2 {
					t.Fatalf("missing hash tool misclassified or transferred binary: %v", err)
				}
				if err := lease.releaseAndWait(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if name == "busy-retry" || name == "progress-retry" || name == "deadline-retry" {
				ctx := context.Background()
				if name == "deadline-retry" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 4*time.Second)
					defer cancel()
				}
				err := m.stageArtifact(ctx, lease, a, cfg, base, filepath.Join(base, "agent"), next, nil)
				want := errArtifactPullBusy
				if name == "progress-retry" || name == "deadline-retry" {
					want = errArtifactPullProgress
				}
				if !errors.Is(err, want) || sftpBytes.Load() > a.Size/2 {
					t.Fatalf("transient HTTP result triggered binary SFTP or lost classification: %v", err)
				}
				retryReady.Store(true)
			}
			if err := m.stageArtifact(context.Background(), lease, a, cfg, base, filepath.Join(base, "agent"), next, nil); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(next)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatal("bootstrap published wrong artifact")
			}
			if unavailable && sftpBytes.Load() < a.Size {
				t.Fatal("unavailable HTTP path did not use SFTP")
			}
			if !unavailable && sftpBytes.Load() > a.Size/2 {
				t.Fatal("successful HTTP pull still uploaded the binary")
			}
			beforeHTTP, beforeSFTP := requests.Load(), sftpBytes.Load()
			if err := m.stageArtifact(context.Background(), lease, a, cfg, base, filepath.Join(base, "agent"), filepath.Join(base, ".next-two"), nil); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != beforeHTTP || sftpBytes.Load()-beforeSFTP > a.Size/2 {
				t.Fatal("configuration-only retry retransferred the binary")
			}
			if err := lease.releaseAndWait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
