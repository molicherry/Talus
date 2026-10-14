// Command e2efixture is the fixture side of tests/e2e/run.sh (M4): a controlled SSH
// peer that records exactly what the Hub asks of it, and the lifecycle of the
// scratch database that browser end-to-end run uses.
//
// It is a test tool, like cmd/agentverify: it is not part of a production image.
//
//	e2efixture -mode serve -addr 127.0.0.1:0 -log peer.jsonl -host-key peer_key -authorized-key peer_key.pub
//	e2efixture -mode genkey -name /tmp/e2e_client      # writes the key pair the peer accepts
//	e2efixture -mode db-create -database "$TEST_DATABASE_URL" -name talus_e2e_1234
//	e2efixture -mode db-drop   -database "$TEST_DATABASE_URL" -name talus_e2e_1234
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/ssh"
)

// event is one JSONL line of the peer's log. The browser test reads it, so the field
// names are the contract between the two.
type event struct {
	At   time.Time `json:"at"`
	Type string    `json:"type"`
	Size [2]int    `json:"size,omitempty"` // cols, rows
	Term string    `json:"term,omitempty"`
	Data string    `json:"data,omitempty"`
	// Session identifies the SSH *connection*; Channel identifies one session channel
	// inside it. A test that wants to prove a terminal was reclaimed must name the
	// channel it opened, not just count connection events (F13).
	Session string `json:"session,omitempty"`
	Channel string `json:"channel,omitempty"`
	Error   string `json:"error,omitempty"`
}

type recorder struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

func newRecorder(path string) (*recorder, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(file)
	return &recorder{file: file, enc: enc}, nil
}

func (r *recorder) record(e event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e.At = time.Now().UTC()
	_ = r.enc.Encode(e)
	_ = r.file.Sync()
}

func (r *recorder) close() {
	if r == nil {
		return
	}
	_ = r.file.Close()
}

func main() {
	mode := flag.String("mode", "serve", "serve | genkey | db-create | db-drop")
	addr := flag.String("addr", "127.0.0.1:0", "listen address for the SSH peer")
	logPath := flag.String("log", "", "JSONL event log written by the peer")
	hostKeyPath := flag.String("host-key", "", "private host key (generated when missing)")
	authorizedPath := flag.String("authorized-key", "", "client public key the peer accepts")
	database := flag.String("database", "", "database URL for the db-* modes")
	name := flag.String("name", "", "database name for the db-* modes")
	echo := flag.Bool("echo", true, "echo terminal input back to the client")
	flag.Parse()

	switch *mode {
	case "genkey":
		if err := generateKeyPair(*name); err != nil {
			fail(err)
		}
		fmt.Printf("CLIENT_KEY=%s\n", *name)
	case "db-create":
		if err := createDatabase(*database, *name); err != nil {
			fail(err)
		}
		// The Hub needs a database it can migrate; handing back the URL keeps the
		// harness free of connection-string editing.
		scratch, err := replaceDatabasePath(*database, *name)
		if err != nil {
			fail(err)
		}
		fmt.Printf("DATABASE_URL=%s\n", scratch)
	case "db-drop":
		if err := dropDatabase(*database, *name); err != nil {
			fail(err)
		}
	case "serve":
		if err := serve(*addr, *logPath, *hostKeyPath, *authorizedPath, *echo); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown mode %q", *mode))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "e2efixture:", err)
	os.Exit(2)
}

// ---------------------------------------------------------------- SSH peer ----

func serve(addr, logPath, hostKeyPath, authorizedPath string, echo bool) error {
	signer, err := loadOrCreateHostKey(hostKeyPath)
	if err != nil {
		return err
	}
	authorized, err := os.ReadFile(authorizedPath)
	if err != nil {
		return fmt.Errorf("read authorized key: %w", err)
	}
	want, _, _, _, err := ssh.ParseAuthorizedKey(authorized)
	if err != nil {
		return fmt.Errorf("parse authorized key: %w", err)
	}

	rec, err := newRecorder(logPath)
	if err != nil {
		return err
	}
	defer rec.close()

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(want.Marshal()) {
				return nil, errors.New("unknown public key")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// The harness learns the port from stdout rather than guessing one.
	fmt.Printf("ADDR=%s\n", listener.Addr().String())
	if err := os.Stdout.Sync(); err != nil {
		return err
	}
	rec.record(event{Type: "listening"})

	sessions := &sessionCounter{}
	for {
		conn, err := listener.Accept()
		if err != nil {
			return nil // listener closed: the harness is shutting the fixture down
		}
		go handleConn(conn, cfg, rec, echo, sessions)
	}
}

type sessionCounter struct {
	mu sync.Mutex
	n  int
}

func (s *sessionCounter) next(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("%s-%d", prefix, s.n)
}

func handleConn(conn net.Conn, cfg *ssh.ServerConfig, rec *recorder, echo bool, sessions *sessionCounter) {
	server, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		rec.record(event{Type: "handshake-failed", Error: err.Error()})
		_ = conn.Close()
		return
	}
	id := sessions.next("session")
	rec.record(event{Type: "connected", Session: id})
	go func() {
		// The Hub's pool probes cached connections with a keepalive that expects a
		// reply: answering "false" would make every pooled connection look dead.
		for req := range reqs {
			switch req.Type {
			case "keepalive@talus.local", "keepalive@openssh.com":
				_ = req.Reply(true, nil)
			case "tcpip-forward", "cancel-tcpip-forward":
				_ = req.Reply(false, nil)
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()
	go func() {
		_ = server.Wait()
		rec.record(event{Type: "disconnected", Session: id})
		_ = conn.Close()
	}()

	for next := range chans {
		if next.ChannelType() != "session" {
			_ = next.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, requests, err := next.Accept()
		if err != nil {
			continue
		}
		go handleSession(channel, requests, rec, echo, id, sessions.next("channel"))
	}
}

func handleSession(channel ssh.Channel, requests <-chan *ssh.Request, rec *recorder, echo bool, session, channelID string) {
	// only a channel that really started a shell is a terminal: a rejected exec
	// channel (the monitor probing the target) must not be reported as one, or a test
	// could "prove" a terminal was reused by closing something else (F13).
	shellStarted := false
	defer func() {
		kind := "channel-closed"
		if shellStarted {
			kind = "shell-closed"
		}
		rec.record(event{Type: kind, Session: session, Channel: channelID})
		_ = channel.Close()
	}()
	for req := range requests {
		switch req.Type {
		case "pty-req":
			// The request is term, cols, rows, width, height, then the mandatory
			// terminal modes string. Omitting the last field leaves unparsed bytes and
			// ssh.Unmarshal fails; accepting it anyway would let the browser flow claim
			// a PTY that was never requested (F14).
			var payload struct {
				Term   string
				Cols   uint32
				Rows   uint32
				Width  uint32
				Height uint32
				Modes  string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				rec.record(event{Type: "pty-req-invalid", Session: session, Channel: channelID, Error: err.Error()})
				_ = req.Reply(false, nil)
				continue
			}
			if payload.Term == "" || payload.Cols == 0 || payload.Rows == 0 {
				rec.record(event{Type: "pty-req-invalid", Session: session, Channel: channelID,
					Error: "missing terminal type or size"})
				_ = req.Reply(false, nil)
				continue
			}
			rec.record(event{Type: "pty-req", Session: session, Channel: channelID, Term: payload.Term,
				Size: [2]int{int(payload.Cols), int(payload.Rows)}})
			_ = req.Reply(true, nil)
		case "window-change":
			var payload struct{ Cols, Rows, Width, Height uint32 }
			if err := ssh.Unmarshal(req.Payload, &payload); err == nil {
				rec.record(event{Type: "window-change", Session: session, Channel: channelID,
					Size: [2]int{int(payload.Cols), int(payload.Rows)}})
			}
			_ = req.Reply(true, nil)
		case "shell":
			shellStarted = true
			rec.record(event{Type: "shell", Session: session, Channel: channelID})
			_ = req.Reply(true, nil)
			go pump(channel, rec, echo, session, channelID)
		case "exec":
			// The monitor probes targets with exec; the fixture is a terminal peer, so
			// it declines instead of pretending to run commands.
			rec.record(event{Type: "exec-declined", Session: session, Channel: channelID})
			_ = req.Reply(false, nil)
		case "subsystem":
			_ = req.Reply(false, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// pump echoes what the terminal sends back to it, so the browser can assert that a
// round trip really happened, and records the input for the test to inspect.
func pump(channel ssh.Channel, rec *recorder, echo bool, session, channelID string) {
	reader := bufio.NewReader(channel)
	buf := make([]byte, 1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			text := string(buf[:n])
			rec.record(event{Type: "input", Session: session, Channel: channelID, Data: text})
			if echo {
				if _, writeErr := io.WriteString(channel, text); writeErr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if path == "" {
		return nil, errors.New("host key path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if genErr := generateKeyPair(path); genErr != nil {
			return nil, genErr
		}
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse host key %s: %w", path, err)
	}
	return signer, nil
}

// --------------------------------------------------------- scratch database ----

// maintenanceURL points at the server's default database, which is where CREATE
// DATABASE and DROP DATABASE must run.
func maintenanceURL(raw string) (string, error) {
	return replaceDatabasePath(raw, "postgres")
}

func createDatabase(url, name string) error {
	if url == "" || name == "" {
		return errors.New("-database and -name are required")
	}
	if !strings.HasPrefix(name, "talus_e2e") {
		return fmt.Errorf("refusing to create %q: scratch databases are named talus_e2e*", name)
	}
	maintenance, err := maintenanceURL(url)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", maintenance)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// The name is validated above and quoted, so it cannot carry SQL.
	if _, err := db.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}

func dropDatabase(url, name string) error {
	if url == "" || name == "" {
		return errors.New("-database and -name are required")
	}
	if !strings.HasPrefix(name, "talus_e2e") {
		return fmt.Errorf("refusing to drop %q: only talus_e2e* databases are dropped", name)
	}
	maintenance, err := maintenanceURL(url)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", maintenance)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// TimescaleDB keeps background workers attached to a database it has seen, so a
	// plain DROP can be refused while they are still connected.
	if _, err := db.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

// generateKeyPair writes an ed25519 key pair in the formats both sides use: PKCS#8
// PEM for the private key (what the Hub's credential stores) and an authorized-key
// line for the public half (what the peer accepts).
func generateKeyPair(path string) error {
	if path == "" {
		return errors.New("key path is required")
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(der), 0o600); err != nil {
		return err
	}
	public, err := ssh.NewPublicKey(privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		return err
	}
	return os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(public), 0o644)
}

// replaceDatabasePath swaps the database component of a PostgreSQL URL while keeping
// the scheme, credentials, host, port, encoding and every connection option (sslmode,
// connect_timeout, application_name, ...). Truncating the query silently downgraded
// the connection policy the caller asked for (F15).
//
// PostgreSQL and pgx also accept the database name as a `dbname`/`database` query
// parameter, and those override the path. Rewriting only the path would return a URL
// that still connects to the caller's original database - which for a scratch run
// means migrating the wrong one (R06).
func replaceDatabasePath(raw, name string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse database url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("database url %q has no scheme or host", raw)
	}
	parsed.Path = "/" + name
	query := parsed.Query()
	rewritten := false
	for _, key := range []string{"dbname", "database"} {
		if _, ok := query[key]; ok {
			query.Set(key, name)
			rewritten = true
		}
	}
	if rewritten {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}
