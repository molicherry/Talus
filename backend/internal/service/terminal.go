package service

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/ssh"
)

// TerminalService manages interactive SSH PTY sessions over WebSocket.
type TerminalService struct{ sshSvc *SSHService }

func NewTerminalService(sshSvc *SSHService) *TerminalService { return &TerminalService{sshSvc: sshSvc} }

type wsMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// TerminalResult describes the first original event that ended a session.
// Errors caused by closing its other pump cannot replace this event.
type TerminalResult struct {
	Outcome     string
	Reason      string
	CloseReason string
	ReadyAt     time.Time
	ClosedAt    time.Time
	Forced      bool
}

// sessionTeardownGrace bounds waiting for a peer that ignores channel close.
var sessionTeardownGrace = 2 * time.Second

// StartSession retains the original service API. A transport close is
// represented by TerminalResult in the new API, rather than a synthetic error.
func (s *TerminalService) StartSession(ctx context.Context, serverID uint, wsConn *websocket.Conn) error {
	_, err := s.StartSessionWithResult(ctx, serverID, wsConn)
	return err
}

func (s *TerminalService) StartSessionWithResult(ctx context.Context, serverID uint, wsConn *websocket.Conn) (result TerminalResult, err error) {
	defer func() { result.ClosedAt = time.Now().UTC() }()
	startFailed := func(reason string) {
		result.Outcome, result.Reason, result.CloseReason = "failed", reason, "ssh_error"
		if ctx.Err() != nil {
			result.Outcome, result.Reason, result.CloseReason = "cancelled", "client_cancelled", "client_cancelled"
		}
	}
	lease, err := s.sshSvc.AcquireLease(ctx, serverID)
	if err != nil {
		startFailed("terminal_ssh_failed")
		return result, err
	}
	client := lease.client()

	discard := false
	defer func() {
		if discard || result.Forced || result.Outcome == "failed" || result.Outcome == "cancelled" {
			lease.Discard()
		} else {
			lease.Release()
		}
	}()

	// Cancellation also bounds setup RPCs before the pumps are ready. Closing
	// this exclusively owned client unblocks a stalled NewSession/PTY/Shell.
	stopPreparation := make(chan struct{})
	preparationDone := make(chan struct{})
	go func() {
		defer close(preparationDone)
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-stopPreparation:
		}
	}()
	var stopOnce sync.Once
	stopPreparing := func() { stopOnce.Do(func() { close(stopPreparation); <-preparationDone }) }
	defer stopPreparing()

	session, err := client.NewSession()
	if err != nil {
		discard = true
		startFailed("terminal_ssh_failed")
		return result, err
	}
	defer session.Close()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		discard = true
		startFailed("terminal_ssh_failed")
		return result, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		discard = true
		startFailed("terminal_ssh_failed")
		return result, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		discard = true
		startFailed("terminal_ssh_failed")
		return result, err
	}
	if err := session.Shell(); err != nil {
		discard = true
		startFailed("terminal_ssh_failed")
		return result, err
	}
	if err := wsConn.WriteJSON(wsMessage{Type: "connected"}); err != nil {
		discard = true
		startFailed("terminal_output_failed")
		return result, err
	}
	result.ReadyAt = time.Now().UTC()
	usage.FromContext(ctx).SetPhase("ready")
	stopPreparing()

	teardownStarted := make(chan struct{})
	var closeOnce sync.Once
	end := func(outcome, reason, closeReason string) {
		closeOnce.Do(func() {
			result.Outcome, result.Reason, result.CloseReason = outcome, reason, closeReason
			// Publish the original event before teardown makes other reads fail.
			close(teardownStarted)
			_ = wsConn.Close()
			_ = session.Close()
		})
	}
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			end("cancelled", "client_cancelled", "client_cancelled")
		case <-stopWatch:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				if err := wsConn.WriteJSON(wsMessage{Type: "output", Data: string(buf[:n])}); err != nil {
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
		// end starts the grace clock before stdin.Close can block on a write.
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

	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-teardownStarted:
		timer := time.NewTimer(sessionTeardownGrace)
		defer timer.Stop()
		select {
		case <-joined:
		case <-timer.C:
			result.Forced = true
			_ = client.Close()
			<-joined
		}
	}
	close(stopWatch)
	<-watchDone
	return result, nil
}
