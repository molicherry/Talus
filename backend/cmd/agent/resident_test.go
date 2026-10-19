package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/agentclient"
	"github.com/vpsmanager/backend/internal/agentwire"
)

// Build and execute the shipped command, including its isolated collector
// children, TLS verification, fallback, signal handling and persisted restart.
func TestResidentAgentCommandTLSFallbackAndRestartReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("command integration test")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "agent")
	build := exec.Command("go", "build", "-o", binary, ".")
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build resident command: %v %s", e, b)
	}
	token := strings.Repeat("a", 64)
	var outage, websockets, wsConnected atomic.Bool
	var mu sync.Mutex
	seen := map[string]int{}
	var connections []*websocket.Conn
	dropACK := true
	handle := func(req agentwire.Request) agentwire.Response {
		mu.Lock()
		defer mu.Unlock()
		reply := agentwire.Response{ServerTime: time.Now().UTC()}
		for _, s := range req.Samples {
			seen[s.ID]++
			reply.Ack = append(reply.Ack, s.ID)
		}
		return reply
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.RawQuery != "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if outage.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/connect") {
			if !websockets.Load() {
				http.NotFound(w, r)
				return
			}
			u := websocket.Upgrader{}
			c, e := u.Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer c.Close()
			mu.Lock()
			connections = append(connections, c)
			mu.Unlock()
			wsConnected.Store(true)
			for {
				var req agentwire.Request
				if c.ReadJSON(&req) != nil {
					return
				}
				if req.Validate(time.Now()) != nil {
					t.Error("invalid resident WebSocket payload")
					return
				}
				if c.WriteJSON(handle(req)) != nil {
					return
				}
			}
		}
		var req agentwire.Request
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Validate(time.Now()) != nil {
			http.Error(w, "bad report", http.StatusBadRequest)
			return
		}
		reply := handle(req)
		mu.Lock()
		lose := len(req.Samples) > 0 && dropACK
		if lose {
			dropACK = false
		}
		mu.Unlock()
		if lose {
			http.Error(w, "lost acknowledgement", http.StatusServiceUnavailable)
			return
		}
		if len(req.Samples) > 0 {
			websockets.Store(true)
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	ca := filepath.Join(dir, "ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	state := filepath.Join(dir, "outbox")
	q, e := agentclient.OpenSpool(state)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := agentwire.NewID()
	p := 5.0
	old := agentwire.Sample{ID: id, BootID: "previous-boot", Time: time.Now().Add(-time.Minute).UTC(), CPU: &p}
	if e = q.Add(old); e != nil {
		t.Fatal(e)
	}
	if e = q.Close(); e != nil {
		t.Fatal(e)
	}
	cfg := agentwire.Config{HubURL: server.URL, Token: token, StateDir: state, IntervalSeconds: 1}
	b, _ := json.Marshal(cfg)
	config := filepath.Join(dir, "config.json")
	if e = os.WriteFile(config, b, 0600); e != nil {
		t.Fatal(e)
	}
	start := func() (*exec.Cmd, context.CancelFunc) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		c := exec.CommandContext(ctx, binary, "--daemon", "--config", config)
		c.Env = append(os.Environ(), "SSL_CERT_FILE="+ca)
		c.Stdout = nil
		c.Stderr = nil
		if e := c.Start(); e != nil {
			cancel()
			t.Fatal(e)
		}
		return c, cancel
	}
	stop := func(c *exec.Cmd, cancel context.CancelFunc) {
		t.Helper()
		_ = c.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case e := <-done:
			cancel()
			if e != nil {
				t.Fatalf("resident shutdown: %v", e)
			}
		case <-time.After(5 * time.Second):
			cancel()
			<-done
			t.Fatal("resident shutdown did not join workers")
		}
	}
	until := func(check func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("timed out: " + what)
	}
	process, cancel := start()
	running := true
	defer func() {
		if running {
			cancel()
			_ = process.Wait()
		}
	}()
	until(func() bool {
		mu.Lock()
		n := seen[id]
		mu.Unlock()
		_, e := os.Stat(filepath.Join(state, id+".sample"))
		return n >= 2 && os.IsNotExist(e) && wsConnected.Load()
	}, "TLS HTTP fallback, lost ACK replay and WSS recovery")
	outage.Store(true)
	mu.Lock()
	for _, c := range connections {
		_ = c.Close()
	}
	mu.Unlock()
	until(func() bool { files, _ := filepath.Glob(filepath.Join(state, "*.sample")); return len(files) >= 2 }, "outage persisted samples")
	stop(process, cancel)
	running = false
	files, e := filepath.Glob(filepath.Join(state, "*.sample"))
	if e != nil || len(files) == 0 {
		t.Fatal("restart test has no pending observations")
	}
	outage.Store(false)
	process, cancel = start()
	running = true
	until(func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, f := range files {
			id := strings.TrimSuffix(filepath.Base(f), ".sample")
			if seen[id] == 0 {
				return false
			}
			if _, e := os.Stat(f); !os.IsNotExist(e) {
				return false
			}
		}
		return true
	}, "pending observations replayed after process restart")
	stop(process, cancel)
	running = false
}
