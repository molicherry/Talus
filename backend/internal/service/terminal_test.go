package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"golang.org/x/crypto/ssh"
)

// startTestSSHServer starts an in-process SSH server that accepts a session
// with a PTY and then never writes output or EOF on it. It stands in for a
// remote full-screen program (top, vim) that ignores stdin EOF — the case
// where a terminal session used to stay parked forever.
func startTestSSHServer(t *testing.T) (addr string, hostKey ssh.PublicKey) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveTestSSHConn(conn, cfg)
		}
	}()

	return ln.Addr().String(), signer.PublicKey()
}

// serveTestSSHConn serves one SSH connection. Global requests are rejected,
// which is all the pool keepalive probe needs, and every session channel is
// accepted and kept open without ever being written to.
func serveTestSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		ch, channelReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range channelReqs {
				_ = req.Reply(req.Type == "shell" || req.Type == "pty-req", nil)
			}
		}()
		// Drain stdin but never treat its EOF as the shell exiting, and never
		// write or close: the channel only ends when the client tears it down.
		// This is the remote program (top, vim) that ignores stdin EOF, which
		// is what the incidental stdin-EOF teardown chain relies on.
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := ch.Read(buf); err != nil {
					return
				}
			}
		}()
	}
}

// terminalTestUpgrader upgrades the httptest handler into a WebSocket.
var terminalTestUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// dialTestSSH opens a client to a test SSH server — or to a proxy in front of
// one, for the unresponsive-peer case.
func dialTestSSH(t *testing.T, addr string, hostKey ssh.PublicKey) *ssh.Client {
	t.Helper()

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial test ssh server: %v", err)
	}
	return client
}

// newTestPool returns a pool that allows one session per server.
func newTestPool(t *testing.T) *sshpool.Pool {
	t.Helper()

	pool := sshpool.NewPool(time.Minute, 1, time.Second)
	t.Cleanup(pool.Close)
	return pool
}

// seedPool hands a live client to the pool for serverID. It takes the single
// slot first so Release's slot accounting does not block on an unacquired slot.
func seedPool(t *testing.T, pool *sshpool.Pool, serverID uint, client *ssh.Client) {
	t.Helper()

	if got, err := pool.Get(serverID, testFP(serverID)); err != nil || got != nil {
		t.Fatalf("seed pool Get = (%v, %v), want (nil, nil)", got, err)
	}
	pool.Release(serverID, client)
}

// startSeededTerminalService builds a TerminalService whose pool already holds
// a live SSH client for serverID, so StartSession never dials and the test
// needs no server repository or credential service.
func startSeededTerminalService(t *testing.T, serverID uint) (*TerminalService, *sshpool.Pool) {
	t.Helper()

	addr, hostKey := startTestSSHServer(t)
	pool := newTestPool(t)
	seedPool(t, pool, serverID, dialTestSSH(t, addr, hostKey))

	// nil repository and credential service are safe here: the cached client
	// means GetClient takes the pool hit and never dereferences them.
	return NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)), pool
}

// sessionHarness is one StartSession call in flight over a real WebSocket.
type sessionHarness struct {
	ws     *websocket.Conn
	result chan error
}

// beginSession serves one StartSession call over an httptest WebSocket server
// and connects a client to it, waiting until the SSH shell is up.
func beginSession(t *testing.T, svc *TerminalService, serverID uint) *sessionHarness {
	t.Helper()

	started := make(chan struct{})
	result := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		close(started)
		// r.Context() stays live for the whole handler call, matching the real
		// terminal handler (which calls StartSession and only then returns).
		result <- svc.StartSession(r.Context(), serverID, conn)
	}))
	t.Cleanup(srv.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start the terminal session")
	}

	// The "connected" frame is written once the shell is up; waiting for it
	// proves the session (and both pumps) are running before we disconnect.
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var ready wsMessage
	if err := ws.ReadJSON(&ready); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}
	if ready.Type != "connected" {
		t.Fatalf("first frame = %q, want %q", ready.Type, "connected")
	}

	return &sessionHarness{ws: ws, result: result}
}

// wait asserts StartSession returned on its own within a bounded time. Since it
// only returns after wg.Wait(), a return also proves both pumps joined.
func (h *sessionHarness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("StartSession did not return; a pump is still parked")
		return nil
	}
}

func TestStartSessionTearsDownOtherPump(t *testing.T) {
	const serverID = 1

	svc, pool := startSeededTerminalService(t, serverID)
	h := beginSession(t, svc, serverID)

	// Abrupt disconnect: the browser goes away while the SSH shell stays parked
	// in stdout.Read and never emits EOF. Before the fix this hung forever.
	if err := h.ws.Close(); err != nil {
		t.Fatalf("close websocket: %v", err)
	}
	if err := h.wait(t); err != nil {
		t.Fatalf("StartSession = %v, want nil", err)
	}

	// Accounting: the slot was freed and the torn-down connection was
	// discarded, so Get hands out no cached client.
	client, err := pool.Get(serverID, testFP(serverID))
	if err != nil {
		t.Fatalf("Get after torn-down session = %v, want the slot to be free", err)
	}
	if client != nil {
		pool.Release(serverID, client)
		t.Fatal("connection was released back into the pool, want discarded")
	}
	pool.Release(serverID, nil)
}

func TestStartSessionCleanCloseReleasesConnection(t *testing.T) {
	const serverID = 1

	svc, pool := startSeededTerminalService(t, serverID)
	h := beginSession(t, svc, serverID)

	// A close carrying an explicit 1000 must keep a healthy connection
	// available for reuse; the bare ws.close() (1005) case is covered below.
	err := h.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		t.Fatalf("write close frame: %v", err)
	}
	if err := h.wait(t); err != nil {
		t.Fatalf("StartSession = %v, want nil", err)
	}

	client, err := pool.Get(serverID, testFP(serverID))
	if err != nil {
		t.Fatalf("Get after clean close = %v", err)
	}
	if client == nil {
		t.Fatal("healthy connection was discarded after a clean close")
	}
	pool.Release(serverID, client)
}

// TestStartSessionEmptyCloseFrameReleasesConnection covers the frontend's
// actual disconnect: a bare ws.close() sends an empty close frame, which
// gorilla reports as 1005 (CloseNoStatusReceived). That is a clean disconnect,
// so it must not cost a healthy pooled connection.
func TestStartSessionEmptyCloseFrameReleasesConnection(t *testing.T) {
	const serverID = 1

	svc, pool := startSeededTerminalService(t, serverID)
	h := beginSession(t, svc, serverID)

	// FormatCloseMessage(CloseNoStatusReceived, "") is exactly the empty
	// payload a browser sends for ws.close().
	data := websocket.FormatCloseMessage(websocket.CloseNoStatusReceived, "")
	if err := h.ws.WriteMessage(websocket.CloseMessage, data); err != nil {
		t.Fatalf("write close frame: %v", err)
	}
	if err := h.wait(t); err != nil {
		t.Fatalf("StartSession = %v, want nil", err)
	}

	client, err := pool.Get(serverID, testFP(serverID))
	if err != nil {
		t.Fatalf("Get after bare ws.close() = %v", err)
	}
	if client == nil {
		t.Fatal("connection was discarded for a bare ws.close(), want reused")
	}
	pool.Release(serverID, client)
}

// freezeProxy forwards TCP traffic to the test SSH server and can be frozen, so
// the peer never sees — and therefore never answers — the channel close. That is
// the "SSH side is alive but unresponsive" case that session.Close() alone
// cannot recover from.
type freezeProxy struct {
	addr string

	mu     sync.Mutex
	frozen bool
}

func startFreezeProxy(t *testing.T, target string) *freezeProxy {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	p := &freezeProxy{addr: ln.Addr().String()}
	go func() {
		for {
			downstream, err := ln.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				_ = downstream.Close()
				continue
			}
			go p.pipe(downstream, upstream)
			go p.pipe(upstream, downstream)
		}
	}()
	return p
}

func (p *freezeProxy) freeze() {
	p.mu.Lock()
	p.frozen = true
	p.mu.Unlock()
}

func (p *freezeProxy) isFrozen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frozen
}

// pipe copies src to dst, dropping whatever it reads while frozen. The
// connection stays open, so the peer's parked read simply never wakes.
func (p *freezeProxy) pipe(dst, src net.Conn) {
	defer dst.Close()

	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		if p.isFrozen() {
			continue
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

// TestStartSessionClosesTransportWhenPeerNeverAnswers pins the fallback: since
// session.Close() only asks the peer to close the channel, an unresponsive peer
// must not be able to hold the session and its pool slot forever.
func TestStartSessionClosesTransportWhenPeerNeverAnswers(t *testing.T) {
	const serverID = 1

	oldGrace := sessionTeardownGrace
	sessionTeardownGrace = 200 * time.Millisecond
	t.Cleanup(func() { sessionTeardownGrace = oldGrace })

	sshAddr, hostKey := startTestSSHServer(t)
	proxy := startFreezeProxy(t, sshAddr)

	pool := newTestPool(t)
	seedPool(t, pool, serverID, dialTestSSH(t, proxy.addr, hostKey))
	svc := NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second))

	h := beginSession(t, svc, serverID)

	// From here the peer stops answering: the channel close never reaches the
	// SSH server, so only closing the transport underneath can unwind the
	// session. Before the fallback this hung and held the slot.
	proxy.freeze()
	if err := h.ws.Close(); err != nil {
		t.Fatalf("close websocket: %v", err)
	}

	if err := h.wait(t); err != nil {
		t.Fatalf("StartSession = %v, want nil", err)
	}

	// A forced teardown means the client is not reusable: discarded, with the
	// slot released exactly once.
	client, err := pool.Get(serverID, testFP(serverID))
	if err != nil {
		t.Fatalf("Get after forced teardown = %v, want the slot to be free", err)
	}
	if client != nil {
		pool.Release(serverID, client)
		t.Fatal("unresponsive client was released back into the pool, want discarded")
	}
	pool.Release(serverID, nil)
}

// gatedConn is a net.Conn whose writes can be parked on demand, which makes the
// SSH transport's send path block deterministically — no need to fill kernel
// socket buffers. Closing it releases a parked write, like a real connection.
type gatedConn struct {
	net.Conn

	mu    sync.Mutex
	gate  chan struct{}
	block bool
}

func (c *gatedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	block, gate := c.block, c.gate
	c.mu.Unlock()

	if block {
		<-gate
	}
	return c.Conn.Write(p)
}

func (c *gatedConn) Close() error {
	c.mu.Lock()
	if c.gate != nil {
		close(c.gate)
		c.gate = nil
	}
	c.block = false
	c.mu.Unlock()

	return c.Conn.Close()
}

// blockWrites parks every subsequent Write until the connection is closed.
func (c *gatedConn) blockWrites() {
	c.mu.Lock()
	if c.gate == nil {
		c.gate = make(chan struct{})
	}
	c.block = true
	c.mu.Unlock()
}

// dialGatedSSH opens a client over a connection whose writes can be parked.
func dialGatedSSH(t *testing.T, addr string, hostKey ssh.PublicKey) (*ssh.Client, *gatedConn) {
	t.Helper()

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial test ssh server: %v", err)
	}
	conn := &gatedConn{Conn: raw}
	s, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("ssh handshake: %v", err)
	}
	return ssh.NewClient(s, chans, reqs), conn
}

// TestStartSessionUnwindsWhenTheSSHWritePathIsWedged pins the order of the
// deferred calls in readFromWS. stdin.Close() is itself a write to the SSH
// transport, so on a wedged connection it blocks; teardown() must run first or
// teardownStarted never fires, the grace timer never starts, and the pool slot
// stays held.
func TestStartSessionUnwindsWhenTheSSHWritePathIsWedged(t *testing.T) {
	const serverID = 1

	oldGrace := sessionTeardownGrace
	sessionTeardownGrace = 200 * time.Millisecond
	t.Cleanup(func() { sessionTeardownGrace = oldGrace })

	addr, hostKey := startTestSSHServer(t)
	client, conn := dialGatedSSH(t, addr, hostKey)

	pool := newTestPool(t)
	seedPool(t, pool, serverID, client)
	svc := NewTerminalService(NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second))

	h := beginSession(t, svc, serverID)

	// Every SSH write from here on parks, so the channel close that would
	// normally unblock the other pump never leaves the process.
	conn.blockWrites()
	if err := h.ws.Close(); err != nil {
		t.Fatalf("close websocket: %v", err)
	}

	if err := h.wait(t); err != nil {
		t.Fatalf("StartSession = %v, want nil", err)
	}

	got, err := pool.Get(serverID, testFP(serverID))
	if err != nil {
		t.Fatalf("Get after wedged transport = %v, want the slot to be free", err)
	}
	if got != nil {
		pool.Release(serverID, got)
		t.Fatal("client on a wedged transport was released, want discarded")
	}
	pool.Release(serverID, nil)
}

// staticServerSource satisfies serverSource for tests that seed the pool: it
// reports a server whose fingerprint matches testFP, so GetClient can load
// "current" state without a database, and it never persists a host key.
type staticServerSource struct{}

func (staticServerSource) FindByID(_ context.Context, id uint) (*model.Server, error) {
	return testServerFor(id), nil
}

func (staticServerSource) SetHostKeyIfUnchanged(context.Context, uint, string, int, []byte) (bool, error) {
	return false, nil
}

func (staticServerSource) RecordHostKeyMismatch(context.Context, uint, string, int, []byte) (bool, error) {
	return true, nil
}

func (staticServerSource) ClearHostKeyMismatch(context.Context, uint) error {
	return nil
}

// testServerFor is the server the fake source reports; testFP derives the
// fingerprint seedPool must seed the pool with so GetClient hits the cache.
func testServerFor(id uint) *model.Server {
	return &model.Server{BaseModel: model.BaseModel{ID: id}, Host: "test-host", Port: 22}
}

func testFP(id uint) string { return serverFingerprint(testServerFor(id)) }
