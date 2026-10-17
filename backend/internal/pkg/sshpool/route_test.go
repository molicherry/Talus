package sshpool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type routeSSHPeer struct {
	host         string
	port         int
	key          []byte
	closed       chan struct{}
	authAttempts *atomic.Int32
}

// A real SSH server exercises both authentication layers and direct-tcpip.
func newRouteSSHPeer(t *testing.T, username, password string, forward func(string, uint32) (net.Conn, error)) *routeSSHPeer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	authAttempts := &atomic.Int32{}
	config := &ssh.ServerConfig{PasswordCallback: func(conn ssh.ConnMetadata, supplied []byte) (*ssh.Permissions, error) {
		authAttempts.Add(1)
		if conn.User() != username || string(supplied) != password {
			return nil, errors.New("wrong test credentials")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	peer := &routeSSHPeer{host: host, port: port, key: signer.PublicKey().Marshal(), authAttempts: authAttempts, closed: make(chan struct{}, 8)}
	var mu sync.Mutex
	active := make(map[net.Conn]struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for conn := range active {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			active[raw] = struct{}{}
			mu.Unlock()
			go func() {
				defer func() {
					_ = raw.Close()
					mu.Lock()
					delete(active, raw)
					mu.Unlock()
					peer.closed <- struct{}{}
				}()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if channel.ChannelType() != "direct-tcpip" || forward == nil {
						_ = channel.Reject(ssh.Prohibited, "forwarding disabled")
						continue
					}
					var request struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if err := ssh.Unmarshal(channel.ExtraData(), &request); err != nil {
						_ = channel.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					upstream, err := forward(request.Host, request.Port)
					if err != nil {
						_ = channel.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					stream, requests, err := channel.Accept()
					if err != nil {
						_ = upstream.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					go routeTestRelay(stream, upstream)
				}
			}()
		}
	}()
	return peer
}

func routeTestRelay(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func (p *routeSSHPeer) endpoint(username, password string) Endpoint {
	return Endpoint{Host: p.host, Port: p.port, Username: username, AuthMethod: ssh.Password(password), KnownHostKey: p.key}
}

func awaitRouteClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("route retained an owned connection")
	}
}

func TestDialRouteJumpAuthenticatesBothPeersAndOwnsEntireChain(t *testing.T) {
	target := newRouteSSHPeer(t, "target-user", "target-password", nil)
	jump := newRouteSSHPeer(t, "jump-user", "jump-password", func(host string, port uint32) (net.Conn, error) {
		if host != "target-only-known-to-jump.invalid" || int(port) != target.port {
			return nil, fmt.Errorf("wrong forwarded destination %s:%d", host, port)
		}
		return net.Dial("tcp", net.JoinHostPort(target.host, strconv.Itoa(target.port)))
	})
	jumpEndpoint := jump.endpoint("jump-user", "jump-password")
	targetEndpoint := target.endpoint("target-user", "target-password")
	targetEndpoint.Host = "target-only-known-to-jump.invalid"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, targetKey, jumpKey, err := DialRouteContext(ctx, Route{Target: targetEndpoint, Mode: "ssh_jump", Jump: &jumpEndpoint}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !bytes.Equal(targetKey, target.key) || !bytes.Equal(jumpKey, jump.key) {
		t.Fatal("route did not capture the two peers' host keys separately")
	}
	cancel()
	if _, _, err := client.SendRequest("still-alive", true, nil); err != nil {
		t.Fatalf("late dial cancellation closed the returned client: %v", err)
	}
	_ = client.Close()
	awaitRouteClosed(t, target.closed)
	awaitRouteClosed(t, jump.closed)
}

func TestDialRouteJumpHostKeyMismatchIdentifiesFailingPeer(t *testing.T) {
	for _, role := range []string{"jump", "target"} {
		t.Run(role, func(t *testing.T) {
			target := newRouteSSHPeer(t, "target", "secret", nil)
			jump := newRouteSSHPeer(t, "jump", "secret", func(host string, port uint32) (net.Conn, error) {
				return net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
			})
			targetEndpoint := target.endpoint("target", "secret")
			jumpEndpoint := jump.endpoint("jump", "secret")
			if role == "jump" {
				jumpEndpoint.KnownHostKey = []byte("untrusted jump key")
			} else {
				targetEndpoint.KnownHostKey = []byte("untrusted target key")
			}
			client, targetKey, jumpKey, err := DialRouteContext(context.Background(), Route{Target: targetEndpoint, Mode: "ssh_jump", Jump: &jumpEndpoint}, 2*time.Second)
			if client != nil || !errors.Is(err, ErrHostKeyMismatch) {
				t.Fatalf("mismatch dial = %v, %v", client, err)
			}
			var routeErr *RouteError
			if !errors.As(err, &routeErr) || routeErr.Role != role {
				t.Fatalf("mismatch did not identify %s: %v", role, err)
			}
			if !bytes.Equal(jumpKey, jump.key) || (role == "target" && !bytes.Equal(targetKey, target.key)) {
				t.Fatal("mismatch lost the presented host key")
			}
			awaitRouteClosed(t, jump.closed)
			if role == "target" {
				awaitRouteClosed(t, target.closed)
			}
		})
	}
}

func TestDialRouteJumpForwardingDeniedClosesJump(t *testing.T) {
	jump := newRouteSSHPeer(t, "jump", "secret", nil)
	jumpEndpoint := jump.endpoint("jump", "secret")
	client, _, jumpKey, err := DialRouteContext(context.Background(), Route{
		Target: Endpoint{Host: "unreachable-target.invalid", Port: 22, Username: "target", AuthMethod: ssh.Password("secret")},
		Mode:   "ssh_jump", Jump: &jumpEndpoint,
	}, 2*time.Second)
	var routeErr *RouteError
	if client != nil || !errors.As(err, &routeErr) || routeErr.Role != "jump" || routeErr.Stage != "jump_forward" || routeErr.Reason != "jump_forward_failed" {
		t.Fatalf("forwarding rejection = %v, %v", client, err)
	}
	if !bytes.Equal(jumpKey, jump.key) {
		t.Fatal("forwarding rejection lost the authenticated jump key")
	}
	awaitRouteClosed(t, jump.closed)
}

type routeSOCKSPeer struct {
	host   string
	port   int
	closed chan struct{}
	result chan error
}

func newRouteSOCKSPeer(t *testing.T, serve func(net.Conn) error) *routeSOCKSPeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	peer := &routeSOCKSPeer{host: host, port: port, closed: make(chan struct{}), result: make(chan error, 1)}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peer.result <- err
			close(peer.closed)
			return
		}
		defer conn.Close()
		defer close(peer.closed)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		peer.result <- serve(conn)
	}()
	return peer
}

func routeReadExact(conn net.Conn, want []byte) error {
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("received %v, want %v", got, want)
	}
	return nil
}

func routeExpectClosed(conn net.Conn) error {
	var b [1]byte
	n, err := conn.Read(b[:])
	if n != 0 || err == nil {
		return errors.New("client proceeded after proxy rejection")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return errors.New("rejected proxy socket was not closed")
	}
	return nil
}

func assertRouteSOCKSServer(t *testing.T, peer *routeSOCKSPeer) {
	t.Helper()
	awaitRouteClosed(t, peer.closed)
	if err := <-peer.result; err != nil {
		t.Fatalf("SOCKS test server: %v", err)
	}
}

func TestDialRouteSOCKSRemoteDNSAndAuthentication(t *testing.T) {
	for _, authType := range []string{"none", "username_password"} {
		t.Run(authType, func(t *testing.T) {
			target := newRouteSSHPeer(t, "target", "secret", nil)
			const remoteHost = "target-only-known-to-proxy.invalid"
			proxy := newRouteSOCKSPeer(t, func(conn net.Conn) error {
				method := byte(0)
				if authType == "username_password" {
					method = 2
				}
				// Exactly one method prevents silent downgrade to no-auth.
				if err := routeReadExact(conn, []byte{5, 1, method}); err != nil {
					return err
				}
				if _, err := conn.Write([]byte{5, method}); err != nil {
					return err
				}
				if method == 2 {
					if err := routeReadExact(conn, []byte{1, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}); err != nil {
						return err
					}
					if _, err := conn.Write([]byte{1, 0}); err != nil {
						return err
					}
				}
				request := append([]byte{5, 1, 0, 3, byte(len(remoteHost))}, []byte(remoteHost)...)
				request = binary.BigEndian.AppendUint16(request, uint16(target.port))
				if err := routeReadExact(conn, request); err != nil {
					return fmt.Errorf("remote DNS request: %w", err)
				}
				upstream, err := net.Dial("tcp", net.JoinHostPort(target.host, strconv.Itoa(target.port)))
				if err != nil {
					return err
				}
				defer upstream.Close()
				if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return err
				}
				routeTestRelay(conn, upstream)
				return nil
			})
			targetEndpoint := target.endpoint("target", "secret")
			targetEndpoint.Host = remoteHost
			proxyConfig := &SOCKSConfig{Host: proxy.host, Port: proxy.port, AuthType: authType}
			if authType == "username_password" {
				proxyConfig.Username, proxyConfig.Password = "user", "pass"
			}
			client, targetKey, jumpKey, err := DialRouteContext(context.Background(), Route{
				Target: targetEndpoint, Mode: "socks5",
				SOCKS: proxyConfig,
			}, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if !bytes.Equal(targetKey, target.key) || len(jumpKey) != 0 {
				t.Fatal("SOCKS route returned incorrect host keys")
			}
			if _, _, err := client.SendRequest("still-alive", true, nil); err != nil {
				t.Fatal(err)
			}
			_ = client.Close()
			assertRouteSOCKSServer(t, proxy)
			awaitRouteClosed(t, target.closed)
		})
	}
}

func TestDialRouteSOCKSRejectsAuthenticationDowngrade(t *testing.T) {
	proxy := newRouteSOCKSPeer(t, func(conn net.Conn) error {
		if err := routeReadExact(conn, []byte{5, 1, 2}); err != nil {
			return err
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return err
		}
		return routeExpectClosed(conn)
	})
	client, _, _, err := DialRouteContext(context.Background(), Route{
		Target: Endpoint{Host: "unused.invalid", Port: 22, Username: "user", AuthMethod: ssh.Password("secret")}, Mode: "socks5",
		SOCKS: &SOCKSConfig{Host: proxy.host, Port: proxy.port, AuthType: "username_password", Username: "user", Password: "pass"},
	}, 2*time.Second)
	if client != nil || err == nil {
		t.Fatalf("downgraded dial = %v, %v", client, err)
	}
	assertRouteSOCKSServer(t, proxy)
}

func TestDialRouteSOCKSConnectRejected(t *testing.T) {
	proxy := newRouteSOCKSPeer(t, func(conn net.Conn) error {
		if err := routeReadExact(conn, []byte{5, 1, 0}); err != nil {
			return err
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return err
		}
		if err := routeReadExact(conn, []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 22}); err != nil {
			return err
		}
		if _, err := conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			return err
		}
		return routeExpectClosed(conn)
	})
	client, _, _, err := DialRouteContext(context.Background(), Route{
		Target: Endpoint{Host: "127.0.0.1", Port: 22, Username: "user", AuthMethod: ssh.Password("secret")}, Mode: "socks5",
		SOCKS: &SOCKSConfig{Host: proxy.host, Port: proxy.port, AuthType: "none"},
	}, 2*time.Second)
	var routeErr *RouteError
	if client != nil || !errors.As(err, &routeErr) || routeErr.Role != "proxy" || routeErr.Stage != "proxy_connect_request" || routeErr.Reason != "proxy_connect_rejected" {
		t.Fatalf("CONNECT rejection = %v, %v", client, err)
	}
	assertRouteSOCKSServer(t, proxy)
}

func TestDialRouteCancellationClosesStalledSOCKSNegotiation(t *testing.T) {
	started := make(chan struct{})
	proxy := newRouteSOCKSPeer(t, func(conn net.Conn) error {
		if err := routeReadExact(conn, []byte{5, 1, 0}); err != nil {
			return err
		}
		close(started)
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		client, _, _, err := DialRouteContext(ctx, Route{
			Target: Endpoint{Host: "unused.invalid", Port: 22, Username: "user", AuthMethod: ssh.Password("secret")}, Mode: "socks5",
			SOCKS: &SOCKSConfig{Host: proxy.host, Port: proxy.port, AuthType: "none"},
		}, time.Minute)
		if client != nil {
			_ = client.Close()
		}
		done <- err
	}()
	awaitRouteClosed(t, started)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled route = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS negotiation ignored cancellation")
	}
	assertRouteSOCKSServer(t, proxy)
}

func TestDialRouteCancellationClosesJumpAndStalledTargetHandshake(t *testing.T) {
	host, port, targetClosed := stalledHandshakeServer(t)
	forwarded := make(chan struct{})
	jump := newRouteSSHPeer(t, "jump", "secret", func(destination string, destinationPort uint32) (net.Conn, error) {
		conn, err := net.Dial("tcp", net.JoinHostPort(destination, strconv.Itoa(int(destinationPort))))
		if err == nil {
			close(forwarded)
		}
		return conn, err
	})
	jumpEndpoint := jump.endpoint("jump", "secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		client, _, _, err := DialRouteContext(ctx, Route{
			Target: Endpoint{Host: host, Port: port, Username: "target", AuthMethod: ssh.Password("secret")},
			Mode:   "ssh_jump", Jump: &jumpEndpoint,
		}, time.Minute)
		if client != nil {
			_ = client.Close()
		}
		done <- err
	}()
	awaitRouteClosed(t, forwarded)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled route = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("target handshake ignored cancellation")
	}
	awaitRouteClosed(t, targetClosed)
	awaitRouteClosed(t, jump.closed)
}

func TestLeasePoolCloseCancelsPendingRouteDial(t *testing.T) {
	for _, mode := range []string{"direct", "socks5"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			peer := newRouteSOCKSPeer(t, func(conn net.Conn) error {
				if mode == "socks5" {
					if err := routeReadExact(conn, []byte{5, 1, 0}); err != nil {
						return err
					}
				} else {
					var firstVersionByte [1]byte
					if _, err := io.ReadFull(conn, firstVersionByte[:]); err != nil {
						return err
					}
				}
				close(started)
				_, err := io.Copy(io.Discard, conn)
				return err
			})
			route := Route{Mode: mode, Target: Endpoint{Host: peer.host, Port: peer.port, Username: "target", AuthMethod: ssh.Password("secret")}}
			if mode == "socks5" {
				route.Target.Host, route.Target.Port = "unreachable-target.invalid", 22
				route.SOCKS = &SOCKSConfig{Host: peer.host, Port: peer.port, AuthType: "none"}
			}
			pool := leaseTestPool(t, 1)
			lease, err := pool.AcquireContext(context.Background(), 1, "route")
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Discard()
			dialCtx, cleanup := lease.DialContext(context.Background())
			defer cleanup()
			done := make(chan error, 1)
			go func() {
				client, _, _, err := DialRouteContext(dialCtx, route, time.Minute)
				if client != nil {
					_ = client.Close()
				}
				done <- err
			}()
			awaitRouteClosed(t, started)
			pool.Close()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown dial = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pool shutdown waited for the pending dial timeout")
			}
			assertRouteSOCKSServer(t, peer)
			lease.Discard()
			if occupied := len(lease.entry.sem); occupied != 0 {
				t.Fatalf("discarded shutdown dial retained %d slots in its original entry", occupied)
			}
		})
	}
}

func TestLeaseDialContextCleanupPreservesEstablishedRoute(t *testing.T) {
	target := newRouteSSHPeer(t, "target", "secret", nil)
	pool := leaseTestPool(t, 1)
	lease, err := pool.AcquireContext(context.Background(), 1, "route")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Discard()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialCtx, cleanup := lease.DialContext(parent)
	defer cleanup()
	client, _, _, err := DialRouteContext(dialCtx, Route{Mode: "direct", Target: target.endpoint("target", "secret")}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	lease.SetClient(client)
	cleanup()
	cancel()
	if _, _, err := client.SendRequest("still-alive", true, nil); err != nil {
		t.Fatalf("dial context cleanup closed the established route: %v", err)
	}
	lease.Discard()
	awaitRouteClosed(t, target.closed)
	if occupied := len(lease.entry.sem); occupied != 0 {
		t.Fatalf("discard retained %d slots in its original entry", occupied)
	}
}
