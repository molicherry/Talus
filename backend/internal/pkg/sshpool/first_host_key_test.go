package sshpool

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestFirstHostKeyStopsBeforePasswordAuthentication(t *testing.T) {
	for _, mode := range []string{"direct", "socks5"} {
		t.Run(mode, func(t *testing.T) {
			target := newRouteSSHPeer(t, "target", "secret", nil)
			endpoint := target.endpoint("target", "secret")
			endpoint.KnownHostKey = nil
			var clientAuth atomic.Int32
			endpoint.AuthMethod = ssh.PasswordCallback(func() (string, error) {
				clientAuth.Add(1)
				return "secret", nil
			})
			route := Route{Target: endpoint, Mode: mode}
			var proxy *routeSOCKSPeer
			if mode == "socks5" {
				proxy = newRouteSOCKSPeer(t, func(conn net.Conn) error {
					if err := routeReadExact(conn, []byte{5, 1, 0}); err != nil {
						return err
					}
					if _, err := conn.Write([]byte{5, 0}); err != nil {
						return err
					}
					request := []byte{5, 1, 0, 1, 127, 0, 0, 1}
					request = binary.BigEndian.AppendUint16(request, uint16(target.port))
					if err := routeReadExact(conn, request); err != nil {
						return err
					}
					upstream, err := net.Dial("tcp", net.JoinHostPort(target.host, strconv.Itoa(target.port)))
					if err != nil {
						return err
					}
					if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
						upstream.Close()
						return err
					}
					routeTestRelay(conn, upstream)
					return nil
				})
				route.SOCKS = &SOCKSConfig{Host: proxy.host, Port: proxy.port, AuthType: "none"}
			}
			client, seen, _, err := DialRouteContext(context.Background(), route, 2*time.Second)
			var unverified *HostKeyUnverifiedError
			var routeErr *RouteError
			if client != nil || !errors.Is(err, ErrHostKeyUnverified) || !errors.As(err, &unverified) || !bytes.Equal(seen, target.key) || !bytes.Equal(unverified.Presented, target.key) {
				t.Fatalf("unverified dial = %v, %v, %x", client, err, seen)
			}
			if !errors.As(err, &routeErr) || routeErr.Role != "target" || routeErr.Stage != "target_host_key" || routeErr.Reason != "ssh_host_key_unverified" {
				t.Fatalf("missing failure identity: %v", err)
			}
			awaitRouteClosed(t, target.closed)
			if proxy != nil {
				assertRouteSOCKSServer(t, proxy)
			}
			if clientAuth.Load() != 0 || target.authAttempts.Load() != 0 {
				t.Fatal("credentials sent before host-key confirmation")
			}
			// A direct retry after explicit pinning can authenticate normally.
			route.Target.KnownHostKey = target.key
			route.Mode, route.SOCKS = "direct", nil
			client, _, _, err = DialRouteContext(context.Background(), route, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			client.Close()
			awaitRouteClosed(t, target.closed)
			if clientAuth.Load() != 1 || target.authAttempts.Load() != 1 {
				t.Fatal("trusted peer did not authenticate")
			}
		})
	}
}

func TestFirstHostKeyRequiresJumpThenTargetConfirmation(t *testing.T) {
	target := newRouteSSHPeer(t, "target", "secret", nil)
	jump := newRouteSSHPeer(t, "jump", "secret", func(host string, port uint32) (net.Conn, error) {
		return net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	})
	jumpEndpoint := jump.endpoint("jump", "secret")
	targetEndpoint := target.endpoint("target", "secret")
	jumpEndpoint.KnownHostKey, targetEndpoint.KnownHostKey = nil, nil
	route := Route{Target: targetEndpoint, Jump: &jumpEndpoint, Mode: "ssh_jump"}
	for _, role := range []string{"jump", "target"} {
		client, targetKey, jumpKey, err := DialRouteContext(context.Background(), route, 2*time.Second)
		var routeErr *RouteError
		if client != nil || !errors.Is(err, ErrHostKeyUnverified) || !errors.As(err, &routeErr) || routeErr.Role != role {
			t.Fatalf("%s discovery = %v, %v", role, client, err)
		}
		awaitRouteClosed(t, jump.closed)
		if !bytes.Equal(jumpKey, jump.key) {
			t.Fatal("missing jump candidate")
		}
		if role == "jump" {
			if jump.authAttempts.Load() != 0 || target.authAttempts.Load() != 0 || len(targetKey) != 0 {
				t.Fatal("untrusted jump authenticated or reached target")
			}
			jumpEndpoint.KnownHostKey = jump.key
		} else {
			awaitRouteClosed(t, target.closed)
			if jump.authAttempts.Load() != 1 || target.authAttempts.Load() != 0 || !bytes.Equal(targetKey, target.key) {
				t.Fatal("target authenticated before confirmation")
			}
			route.Target.KnownHostKey = target.key
		}
	}
	client, _, _, err := DialRouteContext(context.Background(), route, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	awaitRouteClosed(t, jump.closed)
	awaitRouteClosed(t, target.closed)
	if jump.authAttempts.Load() != 2 || target.authAttempts.Load() != 1 {
		t.Fatal("both trusted hops did not authenticate")
	}
}

func TestFirstHostKeyStopsBeforePrivateKeySigner(t *testing.T) {
	target := newRouteSSHPeer(t, "target", "secret", nil)
	endpoint := target.endpoint("target", "secret")
	endpoint.KnownHostKey = nil
	var signing atomic.Bool
	endpoint.AuthMethod = ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		signing.Store(true)
		return nil, io.EOF
	})
	client, _, _, err := DialRouteContext(context.Background(), Route{Mode: "direct", Target: endpoint}, time.Second)
	if client != nil || !errors.Is(err, ErrHostKeyUnverified) || signing.Load() {
		t.Fatalf("private key used before trust: %v", err)
	}
	awaitRouteClosed(t, target.closed)
}
