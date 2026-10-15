package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/vpsmanager/backend/internal/agentproto"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestServerProcessHelper runs the actual entrypoint in an isolated process, so
// startup failures, signals, and os.Exit are observed by the parent test.
func TestServerProcessHelper(t *testing.T) {
	if os.Getenv("TALUS_STARTUP_TEST_PROCESS") != "1" {
		t.Skip("only executed by the full-process startup regression")
	}
	main()
}

// TestServerEmptyDatabaseBootstrapAndRestart covers the production migration
// order and security wiring, including persistent revocation across restarts.
func TestServerEmptyDatabaseBootstrapAndRestart(t *testing.T) {
	dsn := startupTestDatabase(t)
	first := startTestServer(t, dsn)
	var setup struct {
		Needed bool `json:"needed"`
	}
	first.request(t, http.MethodGet, "/api/v1/auth/setup", nil, "", http.StatusOK, &setup)
	if !setup.Needed {
		t.Fatal("an empty database must require first-user setup")
	}
	var login struct {
		Token string `json:"token"`
	}
	first.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "startup-admin", "password": "startup-old-password",
	}, "", http.StatusOK, &login)
	oldToken := login.Token
	if oldToken == "" {
		t.Fatal("first-user bootstrap returned an empty token")
	}
	first.request(t, http.MethodGet, "/api/v1/servers", nil, oldToken, http.StatusOK, nil)
	first.request(t, http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "startup-old-password", "new_password": "startup-new-password",
	}, oldToken, http.StatusNoContent, nil)
	first.request(t, http.MethodGet, "/api/v1/servers", nil, oldToken, http.StatusUnauthorized, nil)
	first.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "startup-admin", "password": "startup-new-password",
	}, "", http.StatusOK, &login)
	newToken := login.Token
	if newToken == "" || newToken == oldToken {
		t.Fatal("password change must issue a new session token")
	}
	first.request(t, http.MethodGet, "/api/v1/servers", nil, newToken, http.StatusOK, nil)
	first.stop(t)

	second := startTestServer(t, dsn)
	second.request(t, http.MethodGet, "/api/v1/auth/setup", nil, "", http.StatusOK, &setup)
	if setup.Needed {
		t.Fatal("restart must preserve the bootstrapped user")
	}
	second.request(t, http.MethodGet, "/api/v1/servers", nil, oldToken, http.StatusUnauthorized, nil)
	second.request(t, http.MethodGet, "/api/v1/servers", nil, newToken, http.StatusOK, nil)
	second.stop(t)
}

// A separate database is required: main enables TimescaleDB and migrates all
// production tables. Never run that startup sequence in the supplied database.
func startupTestDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for the full-process startup regression")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	name := "talus_startup_" + hex.EncodeToString(suffix[:])
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" TEMPLATE template0"); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create an isolated startup database (CREATEDB privilege is required): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		defer admin.Close(cleanupCtx)
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop the owned startup database %s: %v", name, err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.Path, u.RawPath = "/"+name, ""
		query := u.Query()
		query.Del("database")
		query.Del("dbname")
		query.Set("search_path", "public")
		u.RawQuery = query.Encode()
		return u.String()
	}
	// pgx accepts libpq keyword DSNs; the final occurrence wins. The generated
	// name contains only lowercase letters, digits, and underscores.
	return dsn + " dbname=" + name + " search_path=public"
}

type startupServer struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	stopped bool
	baseURL string
	client  *http.Client
	logPath string
}

// startupServerEnv is the isolated environment every startup regression runs
// with. extra entries override the defaults.
func startupServerEnv(dsn, port string, extra map[string]string) map[string]string {
	env := map[string]string{
		"TALUS_STARTUP_TEST_PROCESS": "1",
		"DATABASE_URL":               dsn, "PORT": port,
		"VPSMANAGER_MASTER_KEY": strings.Repeat("0", 64),
		"JWT_SECRET":            "isolated-startup-regression-secret",
		"LOG_FORMAT":            "json", "LOG_LEVEL": "info", "STATIC_DIR": "",
		"MONITOR_INTERVAL": "60", "SSH_TIMEOUT": "10", "SSH_MAX_IDLE": "300",
		"EXEC_TIMEOUT": "30", "EXEC_TIMEOUT_MAX": "300",
		"EXEC_OUTPUT_LIMIT": "8388608", "EXEC_OUTPUT_STREAM_LIMIT": "4194304",
		"LOGIN_RATE_LIMIT": "100", "TRUST_PROXY": "false",
		"DB_MAX_OPEN_CONNECTIONS": "16", "USAGE_LOG_WRITE_CONCURRENCY": "2",
		"USAGE_LOG_ACTIVE_LIMIT": "4096", "USAGE_LOG_RETENTION_DAYS": "30",
		"USAGE_LOG_SENSITIVE_RETENTION_DAYS": "90", "USAGE_LOG_LEGACY_WRITERS_DRAINED": "false",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func startTestServer(t *testing.T, dsn string) *startupServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestServerProcessHelper$", "-test.count=1", "-test.timeout=2m")
	cmd.Env = startupTestEnv(startupServerEnv(dsn, port, map[string]string{
		"AGENT_ARTIFACTS_DIR": agentArtifactsDir(t),
	}))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	_ = logFile.Close()
	s := &startupServer{
		cmd: cmd, done: make(chan struct{}), logPath: logPath,
		baseURL: "http://127.0.0.1:" + port,
		client:  &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second},
	}
	go func() { s.waitErr = cmd.Wait(); close(s.done) }()
	t.Cleanup(func() {
		s.stop(t)
		s.client.CloseIdleConnections()
		if t.Failed() {
			logs, _ := os.ReadFile(logPath)
			t.Logf("server process log:\n%s", logs)
		}
	})
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			t.Fatalf("server exited during startup: %v", s.waitErr)
		case <-deadline.C:
			t.Fatal("server did not become healthy within 45 seconds")
		case <-ticker.C:
			resp, err := s.client.Get(s.baseURL + "/healthz")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return s
				}
			}
		}
	}
}

func (s *startupServer) stop(t *testing.T) {
	t.Helper()
	if s.stopped {
		return
	}
	s.stopped = true
	select {
	case <-s.done:
		t.Errorf("server exited before SIGTERM: %v", s.waitErr)
		return
	default:
	}
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("send SIGTERM to the server: %v", err)
	}
	select {
	case <-s.done:
		if s.waitErr != nil {
			t.Errorf("server did not exit cleanly after SIGTERM: %v", s.waitErr)
		}
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
		t.Error("server did not exit within 15 seconds of SIGTERM")
	}
}

func (s *startupServer) request(t *testing.T, method, path string, body any, token string, want int, data any) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, s.baseURL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, resp.StatusCode, want, result)
	}
	if data != nil {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(result, &envelope); err != nil {
			t.Fatalf("decode %s response: %v", path, err)
		}
		if err := json.Unmarshal(envelope.Data, data); err != nil {
			t.Fatalf("decode %s data: %v", path, err)
		}
	}
}

func startupTestEnv(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	return env
}

// agentArtifactsDir builds (once per test binary) a valid agent artifacts
// directory: both architectures plus a matching manifest.
func agentArtifactsDir(t *testing.T) string {
	t.Helper()
	agentArtifactsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "talus-agent-artifacts-")
		if err != nil {
			agentArtifactsErr = err
			return
		}
		agentArtifactsPath = dir
		// Reuse the real build script so the fixtures match what ships. The test
		// binary's working directory varies, so walk up to find it.
		script, err := findUpwards("build", "agents.sh")
		if err != nil {
			agentArtifactsErr = err
			return
		}
		cmd := exec.Command("sh", script, "v0.0.0-startup-test", dir)
		if combined, err := cmd.CombinedOutput(); err != nil {
			agentArtifactsErr = fmt.Errorf("build agent artifacts: %w\n%s", err, combined)
		}
	})
	if agentArtifactsErr != nil {
		t.Fatalf("agent artifacts fixture: %v", agentArtifactsErr)
	}
	return agentArtifactsPath
}

var (
	agentArtifactsOnce sync.Once
	agentArtifactsPath string
	agentArtifactsErr  error
)

// findUpwards locates a file by walking up from the working directory, so the
// test does not depend on where the test binary is executed from.
func findUpwards(parts ...string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(append([]string{dir}, parts...)...)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("%s not found above the working directory", filepath.Join(parts...))
}

// TestServerRejectsInvalidAgentArtifacts pins TC-08-08: a corrupt, tampered or
// single-architecture artifact set must stop the process before it binds a port,
// and the Hub must never fall back to one architecture.
func TestServerRejectsInvalidAgentArtifacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{"corrupt manifest", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, agentproto.ManifestName), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered binary", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "vpsmanager-agent-linux-amd64")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o755)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.Write([]byte("tampered")); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing arm64 artifact", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "vpsmanager-agent-linux-arm64")); err != nil {
				t.Fatal(err)
			}
		}},
		{"single-architecture manifest", func(t *testing.T, dir string) {
			path := filepath.Join(dir, agentproto.ManifestName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest agentproto.Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Agents = manifest.Agents[:1]
			trimmed, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, trimmed, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			copyDir(t, agentArtifactsDir(t), dir)
			tc.mutate(t, dir)

			port := freePort(t)
			logPath := filepath.Join(t.TempDir(), "server.log")
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()

			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestServerProcessHelper$", "-test.count=1", "-test.timeout=1m")
			cmd.Env = startupTestEnv(startupServerEnv("postgres://unused:none@127.0.0.1:1/unused?sslmode=disable", port,
				map[string]string{"AGENT_ARTIFACTS_DIR": dir}))
			cmd.Stdout, cmd.Stderr = logFile, logFile
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case err := <-exited:
				if err == nil {
					t.Fatal("the server exited successfully with invalid agent artifacts")
				}
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 0 {
					t.Fatal("the server exited 0 with invalid agent artifacts")
				}
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("the server did not fail fast on invalid agent artifacts")
			}

			logs, _ := os.ReadFile(logPath)
			if !strings.Contains(string(logs), "agent artifacts are invalid") {
				t.Fatalf("startup log does not report the agent artifact failure:\n%s", logs)
			}
			if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 500*time.Millisecond); err == nil {
				_ = conn.Close()
				t.Fatalf("the server bound port %s despite invalid agent artifacts", port)
			}
		})
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(from, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, entry.Name()), raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
