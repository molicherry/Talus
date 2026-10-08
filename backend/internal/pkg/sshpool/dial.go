package sshpool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch reports that a server presented a host key different from
// the one recorded for it. The handshake is refused (fail-closed); the caller
// can record the presented key and let an operator decide whether to trust it.
var ErrHostKeyMismatch = errors.New("sshpool: host key mismatch")

// HostKeyMismatchError carries the key the server presented so the caller can
// surface it (a fingerprint) for out-of-band verification before re-trusting.
type HostKeyMismatchError struct {
	Host      string
	Presented []byte
	Cause     error
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("ssh host key mismatch for %s: %v", e.Host, e.Cause)
}

func (e *HostKeyMismatchError) Unwrap() error { return e.Cause }

func (e *HostKeyMismatchError) Is(target error) bool { return target == ErrHostKeyMismatch }

// Fingerprint renders the OpenSSH-style SHA256 fingerprint of a marshaled key.
func Fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// DialSSH creates an SSH client connection and performs host key verification.
// If knownHostKey is nil, the key presented by the server is accepted and returned (TOFU — Trust On First Use).
// If knownHostKey is set, the presented key must match or the connection is rejected with a *HostKeyMismatchError
// that carries the presented key (the client is still returned as nil).
func DialSSH(host string, port int, username string, authMethod ssh.AuthMethod, knownHostKey []byte, timeout time.Duration) (*ssh.Client, []byte, error) {
	return DialSSHContext(context.Background(), host, port, username, authMethod, knownHostKey, timeout)
}

// DialSSHContext bounds TCP connection and SSH authentication together. The
// cancellation callback is stopped or joined before returning ownership.
func DialSSHContext(parent context.Context, host string, port int, username string, authMethod ssh.AuthMethod, knownHostKey []byte, timeout time.Duration) (*ssh.Client, []byte, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	var capturedKey []byte
	mismatch := false

	config := &ssh.ClientConfig{
		User:    username,
		Auth:    []ssh.AuthMethod{authMethod},
		Timeout: timeout,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			presented := key.Marshal()
			capturedKey = presented

			if len(knownHostKey) == 0 {
				return nil // TOFU: trust on first use
			}

			if !bytes.Equal(knownHostKey, presented) {
				mismatch = true
				return fmt.Errorf("ssh host key mismatch for %s: recorded %s, presented %s",
					host, Fingerprint(knownHostKey), Fingerprint(presented))
			}
			return nil
		},
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	raw, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = raw.Close(); close(closed) })
	conn, channels, requests, err := ssh.NewClientConn(raw, addr, config)
	if !stop() {
		<-closed
	}
	if contextErr := ctx.Err(); contextErr != nil {
		_ = raw.Close()
		return nil, nil, contextErr
	}
	if err != nil {
		_ = raw.Close()
		if mismatch {
			return nil, capturedKey, &HostKeyMismatchError{Host: host, Presented: capturedKey, Cause: err}
		}
		return nil, nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	return ssh.NewClient(conn, channels, requests), capturedKey, nil
}
