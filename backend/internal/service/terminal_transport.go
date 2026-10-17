package service

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// TerminalBinaryProtocol opts into raw output frames. Unversioned clients retain
// the JSON protocol, including control frames on both versions.
const TerminalBinaryProtocol = "talus.terminal.v2"

const (
	terminalKeepaliveInterval = 25 * time.Second
	terminalKeepaliveTimeout  = 10 * time.Second
)

func (w *terminalWriter) writeBinary(data []byte) error {
	w.sem <- struct{}{}
	defer func() { <-w.sem }()
	return w.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (w *terminalWriter) ping(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case w.sem <- struct{}{}:
		defer func() { <-w.sem }()
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("terminal ping writer timeout")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.conn.WriteControl(websocket.PingMessage, nil, deadline)
}

type terminalOutputWriter struct {
	writer  *terminalWriter
	binary  bool
	pending []byte
}

func (w *terminalOutputWriter) write(data []byte, final bool) error {
	if w.binary {
		if len(data) == 0 {
			return nil
		}
		return w.writer.writeBinary(data)
	}
	// A legacy JSON frame must contain complete UTF-8 characters. Keep only an
	// incomplete trailing rune between SSH reads, and flush it at EOF.
	data = append(w.pending, data...)
	n := 0
	for n < len(data) && (final || utf8.FullRune(data[n:])) {
		_, size := utf8.DecodeRune(data[n:])
		n += size
	}
	w.pending = append([]byte(nil), data[n:]...)
	if n == 0 {
		return nil
	}
	return w.writer.writeJSON(wsMessage{Type: "output", Data: string(data[:n])})
}

func (s *TerminalService) keepaliveSettings() (time.Duration, time.Duration) {
	interval, timeout := s.keepaliveInterval, s.keepaliveTimeout
	if interval <= 0 {
		interval = terminalKeepaliveInterval
	}
	if timeout <= 0 {
		timeout = terminalKeepaliveTimeout
	}
	return interval, timeout
}

// keepTerminalAlive owns at most one outstanding SSH request. When cancellation
// or a timeout wins, it joins that request before returning; the session's
// existing cleanup supervisor closes a wedged transport and retains its lease
// until this worker and both pumps have actually joined.
func keepTerminalAlive(ctx context.Context, client *ssh.Client, writer *terminalWriter, interval, timeout time.Duration, end func(string, string, string), force func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := writer.ping(ctx, timeout); err != nil {
			if ctx.Err() == nil {
				end("failed", "terminal_transport_timeout", "transport_error")
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		done := make(chan error, 1)
		go func() {
			// A REQUEST_FAILURE response still proves that the peer is alive.
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		timer := time.NewTimer(timeout)
		select {
		case err := <-done:
			timer.Stop()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				end("failed", "terminal_keepalive_failed", "ssh_error")
				return
			}
		case <-timer.C:
			if ctx.Err() == nil {
				end("failed", "terminal_keepalive_timeout", "ssh_error")
				force()
			}
			<-done
			return
		case <-ctx.Done():
			timer.Stop()
			<-done
			return
		}
	}
}
