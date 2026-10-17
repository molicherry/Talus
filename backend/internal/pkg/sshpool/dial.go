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

var ErrHostKeyUnverified = errors.New("sshpool: host key requires confirmation")

// HostKeyUnverifiedError carries an observed key without trusting it. The
// handshake stops before any authentication method can send credentials.
type HostKeyUnverifiedError struct {
	Host      string
	Presented []byte
	Cause     error
}

func (e *HostKeyUnverifiedError) Error() string {
	return "ssh host key requires confirmation for " + e.Host
}
func (e *HostKeyUnverifiedError) Unwrap() error        { return e.Cause }
func (e *HostKeyUnverifiedError) Is(target error) bool { return target == ErrHostKeyUnverified }

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
// If knownHostKey is empty, the handshake stops and returns the presented key for confirmation.
// If knownHostKey is set, the presented key must match or the connection is rejected with a *HostKeyMismatchError
// that carries the presented key (the client is still returned as nil).
func DialSSH(host string, port int, username string, authMethod ssh.AuthMethod, knownHostKey []byte, timeout time.Duration) (*ssh.Client, []byte, error) {
	return DialSSHContext(context.Background(), host, port, username, authMethod, knownHostKey, timeout)
}

// DialSSHContext bounds TCP connection and SSH authentication together. The
// cancellation callback is stopped or joined before returning ownership.
func DialSSHContext(parent context.Context, host string, port int, username string, authMethod ssh.AuthMethod, knownHostKey []byte, timeout time.Duration) (*ssh.Client, []byte, error) {
	client, key, _, err := DialRouteContext(parent, Route{Mode: "direct", Target: Endpoint{Host: host, Port: port, Username: username, AuthMethod: authMethod, KnownHostKey: knownHostKey}}, timeout)
	return client, key, err
}

// HandshakeContext authenticates an existing transport, taking ownership of
// raw on both success and failure. Cancellation is stopped or joined before
// a successful client is transferred to the caller.
func HandshakeContext(ctx context.Context, raw net.Conn, endpoint Endpoint) (*ssh.Client, []byte, error) {
	var capturedKey []byte
	mismatch := false
	unverified := false
	config := &ssh.ClientConfig{
		User: endpoint.Username,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			capturedKey = key.Marshal()
			if len(endpoint.KnownHostKey) == 0 {
				unverified = true
				return ErrHostKeyUnverified
			}
			if !bytes.Equal(endpoint.KnownHostKey, capturedKey) {
				mismatch = true
				return fmt.Errorf("recorded %s, presented %s", Fingerprint(endpoint.KnownHostKey), Fingerprint(capturedKey))
			}
			return nil
		},
	}
	if endpoint.AuthMethod != nil {
		config.Auth = []ssh.AuthMethod{endpoint.AuthMethod}
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = raw.Close(); close(closed) })
	conn, channels, requests, err := ssh.NewClientConn(raw, endpoint.address(), config)
	if !stop() {
		<-closed
	}
	if contextErr := ctx.Err(); contextErr != nil {
		_ = raw.Close()
		return nil, nil, contextErr
	}
	if err != nil {
		_ = raw.Close()
		if unverified {
			return nil, capturedKey, &HostKeyUnverifiedError{Host: endpoint.Host, Presented: capturedKey, Cause: err}
		}
		if mismatch {
			return nil, capturedKey, &HostKeyMismatchError{Host: endpoint.Host, Presented: capturedKey, Cause: err}
		}
		return nil, nil, err
	}
	return ssh.NewClient(conn, channels, requests), capturedKey, nil
}
