package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

type terminalE2ESource struct {
	entered chan struct{}
	resume  chan struct{}
	calls   atomic.Int32
}

func (s *terminalE2ESource) FindByID(ctx context.Context, id uint) (*model.Server, error) {
	s.calls.Add(1)
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &model.Server{BaseModel: model.BaseModel{ID: id}, Host: "terminal-test", Port: 22}, nil
}
func (*terminalE2ESource) SetHostKeyIfUnchanged(context.Context, uint, string, int, []byte) (bool, error) {
	return false, nil
}
func (*terminalE2ESource) RecordHostKeyMismatch(context.Context, uint, string, int, []byte) (bool, error) {
	return false, nil
}
func (*terminalE2ESource) ClearHostKeyMismatch(context.Context, uint) error { return nil }

type terminalE2EFixture struct {
	registry     *gate.Registry
	userGate     *gate.Gate
	version      atomic.Int64
	source       *terminalE2ESource
	pool         *sshpool.Pool
	svc          *service.TerminalService
	jwt          *token.JWTService
	shellEntered chan struct{}
	shellResume  chan struct{}
	input        chan string
	// resizes records the PTY window size the Hub sent (cols, rows), so a resize
	// frame from the browser can be observed at the SSH boundary (TC-09-02).
	resizes chan [2]int
}

func newTerminalE2EFixture(t *testing.T) *terminalE2EFixture {
	t.Helper()
	f := &terminalE2EFixture{registry: gate.NewRegistry(), source: &terminalE2ESource{}, shellEntered: make(chan struct{}), shellResume: make(chan struct{}), input: make(chan string, 8), resizes: make(chan [2]int, 8)}
	f.userGate = gate.New(func(context.Context, uint) (int64, error) { return f.version.Load(), nil })
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var shellOnce sync.Once
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		server, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			_ = conn.Close()
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(reqs)
		for next := range chans {
			ch, requests, err := next.Accept()
			if err != nil {
				continue
			}
			go func() {
				for req := range requests {
					if req.Type == "shell" {
						shellOnce.Do(func() { close(f.shellEntered) })
						<-f.shellResume
					}
					if req.Type == "window-change" {
						// The SSH window-change payload is four uint32s: cols, rows,
						// width and height in pixels.
						var size struct{ Cols, Rows, Width, Height uint32 }
						if err := ssh.Unmarshal(req.Payload, &size); err == nil {
							select {
							case f.resizes <- [2]int{int(size.Cols), int(size.Rows)}:
							default:
							}
						}
					}
					_ = req.Reply(req.Type == "shell" || req.Type == "pty-req" || req.Type == "window-change", nil)
				}
			}()
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := ch.Read(buf)
					if n > 0 {
						f.input <- string(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.pool = sshpool.NewPool(time.Minute, 1, time.Second)
	t.Cleanup(f.pool.Close)
	if _, err := f.pool.Get(1, "terminal-test:22:none"); err != nil {
		t.Fatal(err)
	}
	f.pool.Release(1, client)
	f.svc = service.NewTerminalService(service.NewSSHService(f.pool, f.source, nil, time.Second, time.Second))
	f.svc.SetSessionRegistry(f.registry)
	f.jwt = token.NewJWTService("terminal-e2e", time.Hour)
	return f
}

func (f *terminalE2EFixture) connect(t *testing.T, g mw.UserGate, raw string) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	h := NewTerminalHandler(f.svc, f.jwt)
	h.SetUserGate(g)
	done := make(chan struct{})
	router := chi.NewRouter()
	router.Get("/api/v1/servers/{id}/terminal", func(w http.ResponseWriter, r *http.Request) { defer close(done); h.Handle(w, r) })
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/servers/1/terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	if raw == "" {
		raw, err = f.jwt.GenerateToken(7, "test", "admin", 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ws.WriteJSON(wsMessage{Type: "auth", Data: raw}); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	return ws, done
}
func terminalE2EWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatal("terminal did not complete within cleanup budget")
	}
}
func terminalE2ENoReady(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	for {
		var msg wsMessage
		err := ws.ReadJSON(&msg)
		if err != nil {
			return
		}
		if msg.Type == "connected" {
			t.Fatal("revoked token obtained a connected frame")
		}
	}
}
func terminalE2EReady(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	var msg wsMessage
	if err := ws.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "connected" {
		t.Fatalf("first frame = %q", msg.Type)
	}
}

type observingTerminalGate struct {
	base           *gate.Gate
	afterAdmission func()
	calls          atomic.Int32
}

func (g *observingTerminalGate) Admit(ctx context.Context, id uint, fn func(int64) error) error {
	return g.base.Admit(ctx, id, func(v int64) error {
		err := fn(v)
		if g.calls.Add(1) == 1 {
			g.afterAdmission()
		}
		return err
	})
}

func TestTerminalE2EValidationAndRegistrationShareGate(t *testing.T) {
	f := newTerminalE2EFixture(t)
	close(f.shellResume)
	f.source.entered, f.source.resume = make(chan struct{}), make(chan struct{})
	registered := make(chan bool, 1)
	g := &observingTerminalGate{base: f.userGate, afterAdmission: func() { registered <- f.registry.LiveCount() == 1 }}
	ws, done := f.connect(t, g, "")
	if !<-registered {
		t.Fatal("version admission released its gate before registering the preparing session")
	}
	terminalE2EWait(t, f.source.entered)
	var sessions []*gate.Session
	if err := f.userGate.Admit(context.Background(), 7, func(int64) error { f.version.Add(1); sessions = f.registry.MarkRevoked(7); return nil }); err != nil {
		t.Fatal(err)
	}
	close(f.source.resume)
	terminalE2ENoReady(t, ws)
	terminalE2EWait(t, done)
	if result := f.registry.Await(context.Background(), sessions); !result.Complete {
		t.Fatalf("cleanup = %+v", result)
	}
}

func TestTerminalE2ERevocationBeforeReady(t *testing.T) {
	f := newTerminalE2EFixture(t)
	ws, done := f.connect(t, f.userGate, "")
	terminalE2EWait(t, f.shellEntered)
	var sessions []*gate.Session
	if err := f.userGate.Admit(context.Background(), 7, func(int64) error { f.version.Add(1); sessions = f.registry.MarkRevoked(7); return nil }); err != nil {
		t.Fatal(err)
	}
	close(f.shellResume)
	terminalE2ENoReady(t, ws)
	terminalE2EWait(t, done)
	if result := f.registry.Await(context.Background(), sessions); !result.Complete || f.registry.LiveCount() != 0 {
		t.Fatalf("cleanup = %+v", result)
	}
}

func TestTerminalE2ERejectsStaleAndLegacyJWT(t *testing.T) {
	for _, name := range []string{"stale", "legacy"} {
		t.Run(name, func(t *testing.T) {
			f := newTerminalE2EFixture(t)
			close(f.shellResume)
			f.version.Store(1)
			raw, err := f.jwt.GenerateToken(7, "test", "admin", 0)
			if err != nil {
				t.Fatal(err)
			}
			if name == "legacy" {
				raw, err = jwt.NewWithClaims(jwt.SigningMethodHS256, token.Claims{UserID: 7, Role: "admin", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte("terminal-e2e"))
				if err != nil {
					t.Fatal(err)
				}
			}
			ws, done := f.connect(t, f.userGate, raw)
			terminalE2ENoReady(t, ws)
			terminalE2EWait(t, done)
			if f.registry.LiveCount() != 0 || f.source.calls.Load() != 0 {
				t.Fatal("invalid JWT reached SSH setup")
			}
		})
	}
}

func TestTerminalE2ERejectsMissingRevocationDependencies(t *testing.T) {
	for _, missing := range []string{"registry", "gate"} {
		t.Run(missing, func(t *testing.T) {
			f := newTerminalE2EFixture(t)
			close(f.shellResume)
			var admission mw.UserGate = f.userGate
			if missing == "registry" {
				f.svc.SetSessionRegistry(nil)
			} else {
				admission = nil
			}
			ws, done := f.connect(t, admission, "")
			terminalE2ENoReady(t, ws)
			terminalE2EWait(t, done)
			if f.registry.LiveCount() != 0 || f.source.calls.Load() != 0 {
				t.Fatal("JWT terminal bypassed unavailable revocation dependencies")
			}
		})
	}
}

func TestTerminalE2EInputCannotCrossPasswordCommit(t *testing.T) {
	f := newTerminalE2EFixture(t)
	close(f.shellResume)
	ws, done := f.connect(t, f.userGate, "")
	terminalE2EReady(t, ws)
	committed, mark := make(chan struct{}), make(chan struct{})
	revoked := make(chan []*gate.Session, 1)
	go func() {
		_ = f.userGate.Admit(context.Background(), 7, func(int64) error {
			f.version.Add(1)
			close(committed)
			<-mark
			revoked <- f.registry.MarkRevoked(7)
			return nil
		})
	}()
	terminalE2EWait(t, committed)
	if err := ws.WriteJSON(wsMessage{Type: "input", Data: "must-not-be-forwarded"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-f.input:
		t.Fatalf("input crossed password COMMIT: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(mark)
	sessions := <-revoked
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := f.registry.Await(ctx, sessions); !result.Complete {
		t.Fatalf("cleanup = %+v", result)
	}
	terminalE2EWait(t, done)
	if f.registry.LiveCount() != 0 {
		t.Fatal("revoked terminal leaked its registry entry")
	}
	client, err := f.pool.Get(1, "terminal-test:22:none")
	if err != nil {
		t.Fatal(err)
	}
	if client != nil {
		f.pool.Release(1, client)
		t.Fatal("revoked transport returned to pool")
	}
	f.pool.Release(1, nil)
	select {
	case got := <-f.input:
		t.Fatalf("revoked input was forwarded: %q", got)
	default:
	}
}

// Exercise the production password handler, primary-database version gate and
// transaction together with a live HTTP/WebSocket/SSH terminal connection.
func TestTerminalE2EPasswordChangeThroughHTTP(t *testing.T) {
	db := usageFlowDB(t)
	f := newTerminalE2EFixture(t)
	close(f.shellResume)
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "terminal-http", PasswordHash: string(hash), Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	repo := repository.NewUserRepo(db)
	f.userGate = gate.New(repo.TokenVersion, repo.LockedTokenVersion)
	auth := service.NewAuthService(repo, f.jwt, db)
	auth.SetUserGate(f.userGate)
	auth.SetTerminalRegistry(f.registry)
	handler := NewAuthHandler(auth)
	router := chi.NewRouter()
	router.Post("/api/v1/auth/login", handler.Login)
	router.Group(func(r chi.Router) {
		r.Use(mw.Auth(f.jwt, nil, f.userGate))
		r.Put("/api/v1/auth/password", handler.ChangePassword)
		r.Get("/api/v1/auth/profile", handler.Profile)
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path, raw, body string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if raw != "" {
			req.Header.Set("Authorization", "Bearer "+raw)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d body=%s err=%v, want %d", method, path, resp.StatusCode, data, err, want)
		}
		return data
	}
	login := func(password string) string {
		t.Helper()
		body, err := json.Marshal(LoginRequest{Username: user.Username, Password: password})
		if err != nil {
			t.Fatal(err)
		}
		data := request(http.MethodPost, "/api/v1/auth/login", "", string(body), http.StatusOK)
		var response struct {
			Data LoginResponse `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil || response.Data.Token == "" {
			t.Fatalf("login response = %s, err = %v", data, err)
		}
		return response.Data.Token
	}
	oldJWT := login("old-password")
	request(http.MethodGet, "/api/v1/auth/profile", oldJWT, "", http.StatusOK)
	ws, done := f.connect(t, f.userGate, oldJWT)
	terminalE2EReady(t, ws)
	if err := ws.WriteJSON(wsMessage{Type: "input", Data: "allowed-before-password-change"}); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-f.input:
		if input != "allowed-before-password-change" {
			t.Fatalf("SSH input = %q", input)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated terminal did not forward input to SSH")
	}
	request(http.MethodPut, "/api/v1/auth/password", oldJWT, `{"current_password":"old-password","new_password":"new-password"}`, http.StatusNoContent)
	terminalE2ENoReady(t, ws)
	terminalE2EWait(t, done)
	if f.registry.LiveCount() != 0 {
		t.Fatal("HTTP password change completed before terminal resource reclamation")
	}
	cached, err := f.pool.Get(1, "terminal-test:22:none")
	if err != nil {
		t.Fatalf("HTTP password change leaked SSH quota: %v", err)
	}
	f.pool.Release(1, cached)
	if cached != nil {
		t.Fatal("revoked SSH transport was cached")
	}
	request(http.MethodGet, "/api/v1/auth/profile", oldJWT, "", http.StatusUnauthorized)
	before := f.source.calls.Load()
	stale, staleDone := f.connect(t, f.userGate, oldJWT)
	terminalE2ENoReady(t, stale)
	terminalE2EWait(t, staleDone)
	if f.source.calls.Load() != before || f.registry.LiveCount() != 0 {
		t.Fatal("old JWT reached SSH setup after the password transaction committed")
	}
	version, err := repo.TokenVersion(context.Background(), user.ID)
	if err != nil || version != 1 {
		t.Fatalf("committed token version = %d, err = %v", version, err)
	}
	newJWT := login("new-password")
	request(http.MethodGet, "/api/v1/auth/profile", newJWT, "", http.StatusOK)
}

// TestTerminalE2EWindowChangeReachesThePTY pins the protocol half of TC-09-02: a
// resize frame from the client is forwarded to the SSH PTY as a window-change
// request, and the session keeps working afterwards.
func TestTerminalE2EWindowChangeReachesThePTY(t *testing.T) {
	f := newTerminalE2EFixture(t)
	close(f.shellResume)
	ws, done := f.connect(t, f.userGate, "")
	terminalE2EReady(t, ws)

	// The service owns the post-handshake wire format, which carries cols/rows; the
	// handler's wsMessage is only the auth frame.
	type resizeFrame struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		if err := ws.WriteJSON(resizeFrame{Type: "resize", Cols: size[0], Rows: size[1]}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-f.resizes:
			if got != size {
				t.Fatalf("PTY window = %v, want %v", got, size)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("resize %v never reached the PTY", size)
		}
	}

	// A resize must not disturb the session: input still flows.
	if err := ws.WriteJSON(wsMessage{Type: "input", Data: "after-resize"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-f.input:
		if got != "after-resize" {
			t.Fatalf("input after resize = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input after resize was not forwarded")
	}

	// An unsupported frame type is ignored without killing the session.
	if err := ws.WriteJSON(wsMessage{Type: "unsupported"}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(wsMessage{Type: "input", Data: "still-alive"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-f.input:
		if got != "still-alive" {
			t.Fatalf("input after an unknown frame = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unknown frame type ended the session")
	}

	_ = ws.Close()
	terminalE2EWait(t, done)
}

// TestTerminalE2ERevocationAnnouncesItself pins the F07 protocol contract: a revoked
// session sends a classified frame and a policy close code, so the client can tell
// "sign in again" apart from a dropped connection and stop retrying.
func TestTerminalE2ERevocationAnnouncesItself(t *testing.T) {
	f := newTerminalE2EFixture(t)
	close(f.shellResume)
	ws, done := f.connect(t, f.userGate, "")
	terminalE2EReady(t, ws)

	// Register the session, then revoke the user's token version.
	revoked := make(chan []*gate.Session, 1)
	admitted := make(chan struct{})
	go func() {
		_ = f.userGate.Admit(context.Background(), 7, func(int64) error {
			close(admitted)
			return nil
		})
	}()
	terminalE2EWait(t, admitted)
	if sessions := f.registry.MarkRevoked(7); len(sessions) > 0 {
		revoked <- sessions
	}
	select {
	case <-revoked:
	case <-time.After(2 * time.Second):
		// The fixture's session may not be registered under this user id; the frame
		// contract is still observable when the service ends the session.
	}

	var classified bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var frame struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
			Data   string `json:"data"`
		}
		if err := ws.ReadJSON(&frame); err != nil {
			break
		}
		if frame.Type == "error" && frame.Reason == "revoked" {
			classified = true
			if frame.Data == "" {
				t.Fatal("a revocation frame must carry a readable reason")
			}
			break
		}
	}
	terminalE2EWait(t, done)
	if !classified {
		t.Fatal("a revoked terminal must announce the revocation before closing")
	}
	if f.registry.LiveCount() != 0 {
		t.Fatal("a revoked terminal leaked its registry entry")
	}
}
