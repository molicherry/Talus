package service

import (
	"bytes"
	"context"
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
	for _, unavailable := range []bool{false, true} {
		name := "http-pull"
		if unavailable {
			name = "sftp-fallback"
		}
		t.Run(name, func(t *testing.T) {
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
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/api/v1/agent/artifacts/"+a.SHA256 {
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
					http.Error(w, "bad token", 401)
					return
				}
				if unavailable {
					http.Error(w, "temporarily unavailable", 503)
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
