package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/gate"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/ssh"
)

// TerminalService manages interactive SSH PTY sessions over WebSocket.
// terminalWriter serializes every frame written to one terminal socket. The output
// pump and the session-teardown notice both write, and gorilla's websocket panics on
// concurrent writes, so each socket has exactly one writer.
type terminalWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *terminalWriter) writeJSON(message wsMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(message)
}

func (w *terminalWriter) closeWith(code int, reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
}

type TerminalService struct {
	sshSvc   *SSHService
	registry *gate.Registry
	userGate mw.UserGate
}

func NewTerminalService(sshSvc *SSHService) *TerminalService { return &TerminalService{sshSvc: sshSvc} }

// SetSessionRegistry wires the JWT terminal registry so a password change can
// revoke in-flight sessions.
func (s *TerminalService) SetSessionRegistry(r *gate.Registry) { s.registry = r }

func (s *TerminalService) SetUserGate(g mw.UserGate) { s.userGate = g }

var errTerminalRevoked = errors.New("terminal session revoked")

// terminalRevokedCloseCode is the application close code for a session ended by
// revocation. 4000-4999 is the private range, so it can never be confused with a
// protocol-level close.
const terminalRevokedCloseCode = 4001

type wsMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	// Reason classifies a terminal end so the client stops retrying a revoked
	// session instead of reconnecting with credentials the server just revoked (F07).
	Reason string `json:"reason,omitempty"`
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
}

// TerminalResult describes the first original event that ended a session.
// Errors caused by closing its other pump cannot replace this event.
type TerminalResult struct {
	Outcome        string
	Reason         string
	CloseReason    string
	ReadyAt        time.Time
	ClosedAt       time.Time
	Forced         bool
	CleanupPending bool
}

// sessionTeardownGrace bounds waiting for a peer that ignores channel close.
var sessionTeardownGrace = 2 * time.Second

// StartSession retains the original service API. A transport close is
// represented by TerminalResult in the new API, rather than a synthetic error.
func (s *TerminalService) StartSession(ctx context.Context, serverID uint, wsConn *websocket.Conn) error {
	_, err := s.StartSessionWithResult(ctx, serverID, wsConn)
	return err
}

var errTerminalCleanupPending = errors.New("terminal cleanup budget exhausted")

type terminalCompletion struct {
	result TerminalResult
	err    error
}

// StartSessionWithResult bounds the HTTP owner while a background owner retains
// the exclusive lease and registry entry until every local worker really joins.
func (s *TerminalService) StartSessionWithResult(ctx context.Context, serverID uint, wsConn *websocket.Conn) (TerminalResult, error) {
	completed := make(chan terminalCompletion, 1)
	expired := make(chan terminalCompletion, 1)
	go func() {
		result, err := s.runSession(ctx, serverID, wsConn, expired)
		completed <- terminalCompletion{result, err}
	}()
	select {
	case done := <-completed:
		return done.result, done.err
	case pending := <-expired:
		return pending.result, pending.err
	}
}

func (s *TerminalService) runSession(ctx context.Context, serverID uint, wsConn *websocket.Conn, expired chan<- terminalCompletion) (result TerminalResult, err error) {
	runtimeDone := make(chan struct{})
	defer close(runtimeDone)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var reg *gate.Session
	var lease *sshLease
	var client *ssh.Client
	var session *ssh.Session
	var resourceMu sync.Mutex
	forced, sealed := false, false
	var readyAt time.Time
	var transportDone chan struct{}
	// Call under resourceMu; the actual Close always runs outside the lock.
	// Tracking it prevents Done or pool settlement from overtaking a slow Close.
	closeTransport := func() {
		if client != nil && transportDone == nil {
			transportDone = make(chan struct{})
			current, done := client, transportDone
			go func() { _ = current.Close(); close(done) }()
		}
	}
	force := func() {
		resourceMu.Lock()
		defer resourceMu.Unlock()
		if sealed {
			return
		}
		forced = true
		closeTransport()
	}
	claims := mw.GetUserClaims(ctx)
	if claims != nil && claims.TokenVersion != nil {
		if s.userGate == nil || s.registry == nil {
			result.Outcome, result.Reason, result.CloseReason = "rejected", "terminal_auth_failed", "revoked"
			result.ClosedAt = time.Now().UTC()
			return result, gate.ErrUnavailable
		}
		err = s.userGate.Admit(sessionCtx, claims.UserID, func(version int64) error {
			if *claims.TokenVersion != version {
				return errTerminalRevoked
			}
			reg = s.registry.Register(claims.UserID, version, cancel, force)
			return nil
		})
		if err != nil {
			result.Outcome, result.Reason, result.CloseReason = "rejected", "terminal_auth_failed", "revoked"
			result.ClosedAt = time.Now().UTC()
			return result, err
		}
	}

	ended, workflowDone, teardownDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// One writer per socket: the pumps and the teardown notice all write to it, and
	// gorilla panics on concurrent writes.
	writer := &terminalWriter{conn: wsConn}

	var endOnce sync.Once
	var event TerminalResult
	end := func(outcome, reason, closeReason string) {
		endOnce.Do(func() {
			event.Outcome, event.Reason, event.CloseReason = outcome, reason, closeReason
			close(ended)
			cancel()
		})
	}
	go func() {
		defer close(teardownDone)
		select {
		case <-ended:
		case <-sessionCtx.Done():
			if reg != nil && reg.State() == gate.StateRevoked {
				end("cancelled", "session_revoked", "revoked")
			} else {
				end("cancelled", "client_cancelled", "client_cancelled")
			}
		}
		started := time.Now()
		graceAt, deadline := started.Add(sessionTeardownGrace), started.Add(gate.CleanupBudget)
		if reg != nil {
			graceAt, deadline = reg.BeginCleanup(started, sessionTeardownGrace, gate.CleanupBudget)
		}
		// The absolute budget also includes joining force-close and returning the
		// lease. Reporting pending never fabricates resource completion.
		go func() {
			timer := time.NewTimer(max(time.Until(deadline), 0))
			defer timer.Stop()
			select {
			case <-runtimeDone:
				return
			case <-timer.C:
				select {
				case <-runtimeDone:
					return
				default:
				}
				pending := event
				resourceMu.Lock()
				pending.Forced, pending.ReadyAt = forced, readyAt
				resourceMu.Unlock()
				pending.ClosedAt, pending.CleanupPending = time.Now().UTC(), true
				expired <- terminalCompletion{pending, errTerminalCleanupPending}
			}
		}()
		// Closing a channel is itself a potentially blocked SSH write. Run it
		// under the supervisor, never on the path that starts its grace timer.
		closeDone := make(chan struct{})
		go func() {
			// Say why the session is ending *before* the socket goes away: an abnormal
			// close (1006, no reason) is indistinguishable from a dropped network, and
			// retrying a revoked session can only fail (F07).
			switch event.Reason {
			case "session_revoked":
				_ = writer.writeJSON(wsMessage{Type: "error", Reason: "revoked", Data: "session revoked"})
				writer.closeWith(terminalRevokedCloseCode, "revoked")
			case "client_cancelled":
				_ = writer.writeJSON(wsMessage{Type: "disconnected"})
				writer.closeWith(websocket.CloseNormalClosure, "")
			}
			_ = wsConn.Close()
			resourceMu.Lock()
			current := session
			resourceMu.Unlock()
			if current != nil {
				_ = current.Close()
			}
			close(closeDone)
		}()
		joined := make(chan struct{})
		go func() {
			<-workflowDone
			<-closeDone
			resourceMu.Lock()
			sealed = true
			transport := transportDone
			resourceMu.Unlock()
			if transport != nil {
				<-transport
			}
			close(joined)
		}()
		timer := time.NewTimer(max(time.Until(graceAt), 0))
		defer timer.Stop()
		select {
		case <-joined:
			return
		case <-timer.C:
			if reg != nil {
				reg.Force()
			} else {
				force()
			}
		}
		// The deadline notifier returns pending independently if a transport
		// implementation fails to unblock. This owner still must join workers.
		<-joined
	}()
	defer func() {
		close(workflowDone)
		<-teardownDone
		result.Outcome, result.Reason, result.CloseReason = event.Outcome, event.Reason, event.CloseReason
		resourceMu.Lock()
		result.Forced = forced
		resourceMu.Unlock()
		if lease != nil {
			if result.Forced || result.Outcome == "failed" || result.Outcome == "cancelled" {
				lease.Discard()
			} else {
				lease.Release()
			}
			<-lease.Done()
		}
		result.ClosedAt = time.Now().UTC()
		if reg != nil {
			s.registry.Done(reg.ID)
		}
	}()
	startFailed := func(reason string) {
		if reg != nil && reg.State() == gate.StateRevoked {
			end("cancelled", "session_revoked", "revoked")
		} else if ctx.Err() != nil {
			end("cancelled", "client_cancelled", "client_cancelled")
		} else {
			end("failed", reason, "ssh_error")
		}
	}
	lease, err = s.sshSvc.AcquireLease(sessionCtx, serverID, LeaseConsumerTerminal)
	if err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	resourceMu.Lock()
	client = lease.client()
	if forced {
		closeTransport()
	}
	resourceMu.Unlock()
	newSession, err := client.NewSession()
	if err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	resourceMu.Lock()
	session = newSession
	resourceMu.Unlock()
	// Cancellation may have won just as NewSession succeeded, after the close
	// worker took its snapshot. Force the transport instead of leaving a late
	// channel alive; this occurs only on the already-cancelled setup path.
	if sessionCtx.Err() != nil {
		force()
		startFailed("terminal_ssh_failed")
		return result, sessionCtx.Err()
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	if err := session.Shell(); err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	if reg != nil {
		err := s.userGate.Admit(sessionCtx, claims.UserID, func(version int64) error {
			if version != reg.Version || !s.registry.Admit(reg.ID) {
				return errTerminalRevoked
			}
			return nil
		})
		if err != nil {
			end("cancelled", "session_revoked", "revoked")
			return result, err
		}
	}
	if sessionCtx.Err() != nil {
		startFailed("terminal_ssh_failed")
		return result, sessionCtx.Err()
	}
	if err := writer.writeJSON(wsMessage{Type: "connected"}); err != nil {
		startFailed("terminal_output_failed")
		return result, err
	}
	result.ReadyAt = time.Now().UTC()
	resourceMu.Lock()
	readyAt = result.ReadyAt
	resourceMu.Unlock()
	usage.FromContext(ctx).SetPhase("ready")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				if err := writer.writeJSON(wsMessage{Type: "output", Data: string(buf[:n])}); err != nil {
					end("failed", "terminal_output_failed", "transport_error")
					return
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					end("succeeded", "", "remote_closed")
				} else {
					end("failed", "terminal_ssh_failed", "ssh_error")
				}
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer stdin.Close()
		for {
			var msg wsMessage
			if readErr := wsConn.ReadJSON(&msg); readErr != nil {
				if websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
					end("succeeded", "", "normal_close")
				} else {
					end("failed", "terminal_transport_failed", "transport_error")
				}
				return
			}
			if msg.Type != "input" && msg.Type != "resize" {
				continue
			}
			if reg != nil {
				// Permission is serialized with password COMMIT; the potentially
				// blocked write is performed after releasing the user gate.
				err := s.userGate.Admit(sessionCtx, claims.UserID, func(version int64) error {
					if version != reg.Version || !s.registry.InputPermit(reg.ID) {
						return errTerminalRevoked
					}
					return nil
				})
				if err != nil {
					end("cancelled", "session_revoked", "revoked")
					return
				}
			}
			switch msg.Type {
			case "input":
				if _, err := stdin.Write([]byte(msg.Data)); err != nil {
					end("failed", "terminal_input_failed", "ssh_error")
					return
				}
			case "resize":
				if err := session.WindowChange(msg.Rows, msg.Cols); err != nil {
					end("failed", "terminal_resize_failed", "ssh_error")
					return
				}
			}
		}
	}()
	wg.Wait()
	return result, nil
}
