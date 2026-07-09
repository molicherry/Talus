package sshpool

import (
	"bytes"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// DialSSH creates an SSH client connection and performs host key verification.
// If knownHostKey is nil, the key presented by the server is accepted and returned (TOFU — Trust On First Use).
// If knownHostKey is set, the presented key must match or the connection is rejected.
func DialSSH(host string, port int, username string, authMethod ssh.AuthMethod, knownHostKey []byte, timeout time.Duration) (*ssh.Client, []byte, error) {
	var capturedKey []byte

	config := &ssh.ClientConfig{
		User:  username,
		Auth:  []ssh.AuthMethod{authMethod},
		Timeout: timeout,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			presented := key.Marshal()
			capturedKey = presented

			if len(knownHostKey) == 0 {
				return nil // TOFU: trust on first use
			}

			if !bytes.Equal(knownHostKey, presented) {
				return fmt.Errorf("ssh host key mismatch for %s: the server presented a different key than previously recorded (possible MITM attack)", host)
			}
			return nil
		},
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	return client, capturedKey, nil
}
