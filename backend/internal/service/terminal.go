package service

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// TerminalService manages interactive SSH PTY sessions over WebSocket.
type TerminalService struct {
	sshSvc *SSHService
}

// NewTerminalService creates a TerminalService with the given SSH service.
func NewTerminalService(sshSvc *SSHService) *TerminalService {
	return &TerminalService{sshSvc: sshSvc}
}

// wsMessage is the JSON message format used over WebSocket.
type wsMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// sessionTeardownGrace bounds how long a closing session waits for the SSH
// peer to acknowledge the channel close before the transport is closed
// underneath it. Variable so tests can shorten it.
var sessionTeardownGrace = 2 * time.Second

// StartSession starts an interactive PTY session over a WebSocket connection.
func (s *TerminalService) StartSession(ctx context.Context, serverID uint, wsConn *websocket.Conn) error {
	client, err := s.sshSvc.GetClient(ctx, serverID)
	if err != nil {
		return err
	}
	// Hold client for the entire session; release when done. If the session
	// fails to start (or the transport breaks), discard the connection so a
	// dead client is never cached and reused.
	// sessionErr and forced both mean "do not reuse this client": the former
	// for a failed start or a broken transport, the latter for a transport that
	// had to be closed because the peer never answered the channel close.
	var sessionErr error
	var forced bool
	defer func() {
		if sessionErr != nil || forced {
			s.sshSvc.pool.Discard(serverID, client)
		} else {
			s.sshSvc.pool.Release(serverID, client)
		}
	}()

	session, err := client.NewSession()
	if err != nil {
		sessionErr = err
		return err
	}
	defer session.Close()

	// Request PTY with terminal modes.
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		sessionErr = err
		return err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		sessionErr = err
		return err
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		sessionErr = err
		return err
	}

	if err := session.Shell(); err != nil {
		sessionErr = err
		return err
	}

	// Notify client that the session is ready.
	if err := wsConn.WriteJSON(wsMessage{Type: "connected"}); err != nil {
		sessionErr = err
		return err
	}
	// Derive a cancellable context so an aborted request, or either pump
	// finishing, can tear the whole session down.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// teardown ends the session exactly once, from whichever side finishes
	// first: closing the SSH session unblocks a parked stdout.Read and closing
	// the WebSocket unblocks a parked ReadJSON. Without it, a pump that exits
	// leaves the other parked in a read, so wg.Wait() never returns and the
	// goroutine, SSH session and pool slot stay held (issue #12).
	//
	// session.Close() only *asks* the peer to close the channel (RFC 4254), so
	// it cannot guarantee the local read wakes: a peer that never answers leaves
	// stdout.Read parked. teardownStarted lets the wait below bound that.
	teardownStarted := make(chan struct{})
	var teardownOnce sync.Once
	teardown := func() {
		teardownOnce.Do(func() {
			close(teardownStarted)
			cancel()
			_ = session.Close()
			_ = wsConn.Close()
		})
	}

	// Unwind the session when the caller's context is cancelled (aborted
	// request, shutdown): neither pump can select on ctx.Done() while it is
	// blocked in a read, so this watcher closes their primitives instead.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			teardown()
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// readFromSSH: reads SSH stdout and forwards to WebSocket.
	go func() {
		defer wg.Done()
		defer teardown()
		buf := make([]byte, 4096)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				if writeErr := wsConn.WriteJSON(wsMessage{Type: "output", Data: string(buf[:n])}); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					slog.Warn("terminal ssh read closed", "error", readErr)
				}
				return
			}
		}
	}()

	// readFromWS: reads WebSocket messages and forwards to SSH stdin.
	go func() {
		defer wg.Done()
		// stdin.Close() sends a channel close over the SSH transport, so it can
		// block on a wedged connection. teardown must therefore run *first*, so
		// the grace timer that force-closes the transport is already armed by the
		// time anything can block here. Deferred calls run LIFO: last registered,
		// first executed.
		defer stdin.Close()
		defer teardown()
		for {
			var msg wsMessage
			if readErr := wsConn.ReadJSON(&msg); readErr != nil {
				// 1005 (no status received) is what a browser's bare ws.close()
				// produces, so it is a clean disconnect too, not a broken one.
				if !websocket.IsCloseError(readErr,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway,
					websocket.CloseNoStatusReceived,
				) {
					// The browser vanished or the transport broke instead of
					// closing the terminal cleanly: never cache this connection.
					sessionErr = readErr
					slog.Warn("terminal ws read closed", "error", readErr)
				}
				return
			}

			switch msg.Type {
			case "input":
				if _, err := stdin.Write([]byte(msg.Data)); err != nil {
					slog.Warn("terminal stdin write failed", "error", err)
					return
				}
			case "resize":
				if err := session.WindowChange(msg.Rows, msg.Cols); err != nil {
					slog.Warn("terminal window resize failed", "error", err)
				}
			}
		}
	}()

	// Wait for both pumps to join — that is what guarantees this function
	// returns. Once a teardown is in flight the wait is bounded: closing the SSH
	// session only asks the peer to close the channel, and a peer that never
	// answers would leave the other pump parked forever. Past the grace period
	// the transport itself is closed, which unblocks the read locally. Such a
	// client is not reused (forced), and the deferred accounting above still
	// releases the pool slot exactly once.
	joined := make(chan struct{})
	go func() {
		wg.Wait()
		close(joined)
	}()

	select {
	case <-joined:
	case <-teardownStarted:
		timer := time.NewTimer(sessionTeardownGrace)
		defer timer.Stop()
		select {
		case <-joined:
		case <-timer.C:
			forced = true
			_ = client.Close()
			<-joined
		}
	}

	return nil
}
