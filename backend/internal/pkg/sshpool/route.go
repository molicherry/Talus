package sshpool

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Endpoint is an SSH identity, independent of the transport used to reach it.
type Endpoint struct {
	Host         string
	Port         int
	Username     string
	AuthMethod   ssh.AuthMethod
	KnownHostKey []byte
}

func (e Endpoint) address() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type SOCKSConfig struct {
	Host     string
	Port     int
	AuthType string
	Username string
	Password string
}

type Route struct {
	Target Endpoint
	Mode   string
	Jump   *Endpoint
	SOCKS  *SOCKSConfig
}

// RouteError identifies a stable failure stage without exposing credentials.
// Cause retains host-key mismatch and context cancellation identities.
type RouteError struct {
	Reason string
	Stage  string
	Role   string
	Cause  error
}

func (e *RouteError) Error() string { return fmt.Sprintf("%s (%s): %v", e.Reason, e.Stage, e.Cause) }
func (e *RouteError) Unwrap() error { return e.Cause }

func routeFailure(role, stage, reason string, err error) error {
	return &RouteError{Reason: reason, Stage: stage, Role: role, Cause: err}
}
func handshakeFailure(role string, err error) error {
	if errors.Is(err, ErrHostKeyMismatch) {
		return routeFailure(role, role+"_host_key", "ssh_host_key_mismatch", err)
	}
	reason := "ssh_connection_failed"
	if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
		reason = "ssh_authentication_failed"
	}
	return routeFailure(role, role+"_auth", reason, err)
}

// transportChain closes the outer TCP socket first. Closing an inner SSH
// channel can otherwise wait forever for the very outer transport it owns.
type transportChain struct {
	mu        sync.Mutex
	closed    bool
	base      net.Conn
	jump      *ssh.Client
	forwarded net.Conn
}

func (c *transportChain) setJump(client *ssh.Client) {
	c.mu.Lock()
	closed := c.closed
	if !closed {
		c.jump = client
	}
	c.mu.Unlock()
	if closed && client != nil {
		_ = client.Close()
	}
}
func (c *transportChain) setForwarded(conn net.Conn) {
	c.mu.Lock()
	closed := c.closed
	if !closed {
		c.forwarded = conn
	}
	c.mu.Unlock()
	if closed && conn != nil {
		_ = conn.Close()
	}
}
func (c *transportChain) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	base, jump, forwarded := c.base, c.jump, c.forwarded
	c.mu.Unlock()
	var err error
	if base != nil {
		err = base.Close()
	}
	if jump != nil {
		_ = jump.Close()
	}
	if forwarded != nil {
		_ = forwarded.Close()
	}
	return err
}

type chainConn struct {
	net.Conn
	owner *transportChain
}

func (c *chainConn) Close() error { return c.owner.Close() }

func watchCancellation(ctx context.Context, closeFn func()) func() {
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(joined); closeFn() })
	return func() {
		if !stop() {
			<-joined
		}
	}
}

// DialRouteContext owns the entire route and one timeout budget, including
// proxy negotiation, both SSH handshakes and jump forwarding. It never falls
// back to a direct connection. Keys belong to Target and Jump respectively.
func DialRouteContext(parent context.Context, route Route, timeout time.Duration) (client *ssh.Client, targetKey, jumpKey []byte, err error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	mode := route.Mode
	if mode == "" {
		mode = "direct"
	}
	if route.Target.Host == "" || route.Target.Port < 1 || route.Target.Port > 65535 {
		return nil, nil, nil, routeFailure("target", "target_connect", "ssh_connection_failed", errors.New("invalid target endpoint"))
	}
	endpoint := route.Target
	role := "target"
	switch mode {
	case "direct":
	case "ssh_jump":
		if route.Jump == nil || route.Jump.Host == "" || route.Jump.Port < 1 || route.Jump.Port > 65535 {
			return nil, nil, nil, routeFailure("jump", "jump_connect", "ssh_connection_failed", errors.New("invalid jump endpoint"))
		}
		endpoint, role = *route.Jump, "jump"
	case "socks5":
		if route.SOCKS == nil || route.SOCKS.Host == "" || route.SOCKS.Port < 1 || route.SOCKS.Port > 65535 {
			return nil, nil, nil, routeFailure("proxy", "proxy_connect", "proxy_connection_failed", errors.New("invalid SOCKS5 endpoint"))
		}
		endpoint = Endpoint{Host: route.SOCKS.Host, Port: route.SOCKS.Port}
		role = "proxy"
	default:
		return nil, nil, nil, routeFailure("target", "target_connect", "ssh_connection_failed", errors.New("unsupported connection mode"))
	}
	raw, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint.address())
	if dialErr != nil {
		reason := "ssh_connection_failed"
		if role == "proxy" {
			reason = "proxy_connection_failed"
		}
		return nil, nil, nil, routeFailure(role, role+"_connect", reason, dialErr)
	}
	chain := &transportChain{base: raw}
	stop := watchCancellation(ctx, func() { _ = chain.Close() })
	defer func() {
		stop()
		if ctxErr := ctx.Err(); ctxErr != nil {
			var stageErr *RouteError
			if errors.As(err, &stageErr) {
				err = routeFailure(stageErr.Role, stageErr.Stage, stageErr.Reason, ctxErr)
			} else {
				err = routeFailure("target", "target_auth", "ssh_connection_failed", ctxErr)
			}
			if client != nil {
				_ = client.Close()
				client = nil
			}
		}
		if err != nil {
			_ = chain.Close()
		}
	}()
	targetRaw := raw
	switch mode {
	case "ssh_jump":
		var jump *ssh.Client
		jump, jumpKey, err = HandshakeContext(ctx, raw, *route.Jump)
		if err != nil {
			return nil, nil, jumpKey, handshakeFailure("jump", err)
		}
		chain.setJump(jump)
		// Joining the open operation after closing the exclusive jump transport
		// prevents a timed-out OpenChannel worker from retaining route ownership.
		type openResult struct {
			conn net.Conn
			err  error
		}
		opened := make(chan openResult, 1)
		go func() { conn, openErr := jump.Dial("tcp", route.Target.address()); opened <- openResult{conn, openErr} }()
		var result openResult
		select {
		case result = <-opened:
		case <-ctx.Done():
			_ = chain.Close()
			result = <-opened
			if result.conn != nil {
				_ = result.conn.Close()
			}
			return nil, nil, jumpKey, routeFailure("jump", "jump_forward", "jump_forward_failed", ctx.Err())
		}
		if result.err != nil {
			return nil, nil, jumpKey, routeFailure("jump", "jump_forward", "jump_forward_failed", result.err)
		}
		chain.setForwarded(result.conn)
		targetRaw = result.conn
	case "socks5":
		if err = socksConnect(raw, route.SOCKS, route.Target); err != nil {
			return nil, nil, nil, err
		}
	}
	client, targetKey, err = HandshakeContext(ctx, &chainConn{Conn: targetRaw, owner: chain}, route.Target)
	if err != nil {
		return nil, targetKey, jumpKey, handshakeFailure("target", err)
	}
	return client, targetKey, jumpKey, nil
}

// socksConnect sends a domain directly to the proxy instead of resolving it
// locally. Explicit password authentication offers only RFC 1929 (0x02), so
// a server cannot silently select unauthenticated access.
func socksConnect(conn net.Conn, cfg *SOCKSConfig, target Endpoint) error {
	authType := cfg.AuthType
	if authType == "" {
		authType = "none"
	}
	method := byte(0)
	switch authType {
	case "none":
	case "username_password":
		if len(cfg.Username) < 1 || len(cfg.Username) > 255 || len(cfg.Password) < 1 || len(cfg.Password) > 255 {
			return routeFailure("proxy", "proxy_auth", "proxy_auth_failed", errors.New("SOCKS5 credentials must be 1–255 bytes"))
		}
		method = 2
	default:
		return routeFailure("proxy", "proxy_auth", "proxy_auth_failed", errors.New("unsupported SOCKS5 authentication"))
	}
	if err := writeAll(conn, []byte{5, 1, method}); err != nil {
		return routeFailure("proxy", "proxy_auth", "proxy_connection_failed", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return routeFailure("proxy", "proxy_auth", "proxy_connection_failed", err)
	}
	if reply[0] != 5 || reply[1] != method {
		return routeFailure("proxy", "proxy_auth", "proxy_auth_failed", errors.New("SOCKS5 server rejected the configured authentication method"))
	}
	if method == 2 {
		request := []byte{1, byte(len(cfg.Username))}
		request = append(request, cfg.Username...)
		request = append(request, byte(len(cfg.Password)))
		request = append(request, cfg.Password...)
		if err := writeAll(conn, request); err != nil {
			return routeFailure("proxy", "proxy_auth", "proxy_connection_failed", err)
		}
		if _, err := io.ReadFull(conn, reply); err != nil {
			return routeFailure("proxy", "proxy_auth", "proxy_connection_failed", err)
		}
		if reply[0] != 1 || reply[1] != 0 {
			return routeFailure("proxy", "proxy_auth", "proxy_auth_failed", errors.New("SOCKS5 authentication rejected"))
		}
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(target.Host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 1)
			request = append(request, ip4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(target.Host) < 1 || len(target.Host) > 255 {
			return routeFailure("proxy", "proxy_connect_request", "proxy_connect_rejected", errors.New("invalid SOCKS5 destination hostname"))
		}
		request = append(request, 3, byte(len(target.Host)))
		request = append(request, target.Host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(target.Port))
	if err := writeAll(conn, request); err != nil {
		return routeFailure("proxy", "proxy_connect_request", "proxy_connection_failed", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return routeFailure("proxy", "proxy_connect_request", "proxy_connection_failed", err)
	}
	if header[0] != 5 || header[2] != 0 {
		return routeFailure("proxy", "proxy_connect_request", "proxy_connect_rejected", errors.New("invalid SOCKS5 CONNECT response"))
	}
	if header[1] != 0 {
		return routeFailure("proxy", "proxy_connect_request", "proxy_connect_rejected", fmt.Errorf("SOCKS5 CONNECT rejected (code %d)", header[1]))
	}
	addressBytes := 0
	switch header[3] {
	case 1:
		addressBytes = 4
	case 4:
		addressBytes = 16
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return routeFailure("proxy", "proxy_connect_request", "proxy_connection_failed", err)
		}
		addressBytes = int(size[0])
	default:
		return routeFailure("proxy", "proxy_connect_request", "proxy_connect_rejected", errors.New("invalid SOCKS5 address type"))
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addressBytes+2)); err != nil {
		return routeFailure("proxy", "proxy_connect_request", "proxy_connection_failed", err)
	}
	return nil
}
func writeAll(conn net.Conn, data []byte) error {
	for len(data) != 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
