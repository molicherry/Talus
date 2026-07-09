package service

import (
	"context"
	"io"
	"log/slog"
	"sync"

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

// StartSession starts an interactive PTY session over a WebSocket connection.
func (s *TerminalService) StartSession(ctx context.Context, serverID uint, wsConn *websocket.Conn) error {
	client, err := s.sshSvc.GetClient(ctx, serverID)
	if err != nil {
		return err
	}
	// Hold client for the entire session; release when done.
	defer s.sshSvc.pool.Release(serverID, client)

	session, err := client.NewSession()
	if err != nil {
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
		return err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}

	if err := session.Shell(); err != nil {
		return err
	}

	// Notify client that the session is ready.
	if err := wsConn.WriteJSON(wsMessage{Type: "connected"}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// readFromSSH: reads SSH stdout and forwards to WebSocket.
	go func() {
		defer wg.Done()
		defer cancel()
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
		defer cancel()
		defer stdin.Close()
		for {
			var msg wsMessage
			if readErr := wsConn.ReadJSON(&msg); readErr != nil {
				if !websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
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

	wg.Wait()
	return nil
}
