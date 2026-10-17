package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vpsmanager/backend/internal/model"
	appcrypto "github.com/vpsmanager/backend/internal/pkg/crypto"
	"golang.org/x/crypto/ssh"
)

// This proxy is the only component resolving the deliberately unresolvable
// target name, so these tests fail if a business path bypasses the route.
func startServiceSOCKSProxy(t *testing.T, target string, auth ...string) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requested := make(chan string, 10)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				head := make([]byte, 2)
				if _, err := io.ReadFull(conn, head); err != nil {
					return
				}
				methods := make([]byte, int(head[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				method := byte(0)
				if len(auth) == 2 {
					method = 2
				}
				if !bytes.Contains(methods, []byte{method}) {
					return
				}
				if _, err := conn.Write([]byte{5, method}); err != nil {
					return
				}
				if method == 2 {
					authHead := make([]byte, 2)
					if _, err := io.ReadFull(conn, authHead); err != nil {
						return
					}
					username := make([]byte, int(authHead[1]))
					if _, err := io.ReadFull(conn, username); err != nil {
						return
					}
					size := make([]byte, 1)
					if _, err := io.ReadFull(conn, size); err != nil {
						return
					}
					password := make([]byte, int(size[0]))
					if _, err := io.ReadFull(conn, password); err != nil {
						return
					}
					if string(username) != auth[0] || string(password) != auth[1] {
						_, _ = conn.Write([]byte{1, 1})
						return
					}
					if _, err := conn.Write([]byte{1, 0}); err != nil {
						return
					}
				}
				request := make([]byte, 4)
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				var host string
				switch request[3] {
				case 3:
					length := make([]byte, 1)
					if _, err := io.ReadFull(conn, length); err != nil {
						return
					}
					name := make([]byte, int(length[0]))
					if _, err := io.ReadFull(conn, name); err != nil {
						return
					}
					host = string(name)
				case 1:
					addr := make([]byte, 4)
					if _, err := io.ReadFull(conn, addr); err != nil {
						return
					}
					host = net.IP(addr).String()
				case 4:
					addr := make([]byte, 16)
					if _, err := io.ReadFull(conn, addr); err != nil {
						return
					}
					host = net.IP(addr).String()
				default:
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(conn, port); err != nil {
					return
				}
				requested <- net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer upstream.Close()
				if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				joined := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close(); close(joined) }()
				_, _ = io.Copy(conn, upstream)
				_ = conn.Close()
				<-joined
			}()
		}
	}()
	return listener.Addr().String(), requested
}
func configureServiceSOCKS(t *testing.T, source *routeTestSource, proxy string) string {
	t.Helper()
	host, portText, _ := net.SplitHostPort(proxy)
	port, _ := strconv.Atoi(portText)
	source.mu.Lock()
	defer source.mu.Unlock()
	source.servers[1].Host = "target-only-via-proxy.invalid"
	source.configs[1] = &model.ServerConnectionConfig{ServerID: 1, Mode: "socks5", SocksHost: host, SocksPort: port, SocksAuthType: "none"}
	return net.JoinHostPort(source.servers[1].Host, strconv.Itoa(source.servers[1].Port))
}

func TestSOCKSRouteExecAndCopyFileUseSameConnectionPath(t *testing.T) {
	uploaded := make(chan string, 1)
	target, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
		if command == "echo routed" {
			_, _ = io.WriteString(ch, "routed")
		} else if command == "subsystem:sftp" {
			serveUploadPeer(ch, &uploadPeerFS{finish: func(_ string, data []byte) error { uploaded <- string(data); return nil }})
			return
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	proxy, requested := startServiceSOCKSProxy(t, target)
	svc, source := routeTestFixture(t, target, key)
	expected := configureServiceSOCKS(t, source, proxy)
	result, err := svc.Exec(context.Background(), 1, "echo routed", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "routed" {
		t.Fatalf("exec output = %q", result.Stdout)
	}
	select {
	case address := <-requested:
		if address != expected {
			t.Fatalf("proxy resolved %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy was bypassed")
	}
	local := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(local, []byte("agent-via-proxy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyFile(context.Background(), 1, local, "agent"); err != nil {
		t.Fatal(err)
	}
	if got := <-uploaded; got != "agent-via-proxy" {
		t.Fatalf("upload = %q", got)
	}
	// A new test is required to handshake again, even though Exec and upload
	// have left a healthy connection in the shared pool.
	if result, err := svc.TestConnection(context.Background(), 1); err != nil || result.Mode != "socks5" {
		t.Fatalf("fresh proxy test = %#v, %v", result, err)
	}
	select {
	case address := <-requested:
		if address != expected {
			t.Fatalf("fresh proxy target = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("connection test reused cache")
	}
}

func TestSOCKSRouteInteractiveTerminalUsesProxy(t *testing.T) {
	target, key := startTestSSHServer(t)
	proxy, requested := startServiceSOCKSProxy(t, target)
	svc, source := routeTestFixture(t, target, key)
	expected := configureServiceSOCKS(t, source, proxy)
	h := beginSession(t, NewTerminalService(svc), 1)
	select {
	case address := <-requested:
		if address != expected {
			t.Fatalf("terminal target = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal bypassed proxy")
	}
	if err := h.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
}

func TestSOCKSRouteDecryptsSavedProxyPassword(t *testing.T) {
	target, key := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		_, _ = io.WriteString(ch, "authenticated-proxy")
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	proxy, requested := startServiceSOCKSProxy(t, target, "proxy-user", "proxy-password")
	svc, source := routeTestFixture(t, target, key)
	expected := configureServiceSOCKS(t, source, proxy)
	salt := []byte("proxy-salt-12345")
	encrypted, err := appcrypto.Encrypt([]byte("proxy-password"), svc.credSvc.masterKey.DeriveKey(salt))
	if err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	cfg := source.configs[1]
	cfg.SocksAuthType, cfg.SocksUsername, cfg.EncryptedPassword, cfg.Salt = "username_password", "proxy-user", &encrypted, salt
	source.mu.Unlock()
	result, err := svc.Exec(context.Background(), 1, "routed", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "authenticated-proxy" {
		t.Fatalf("proxy output = %q", result.Stdout)
	}
	select {
	case address := <-requested:
		if address != expected {
			t.Fatalf("proxy target = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated proxy bypassed")
	}
}

func startServiceSSHJump(t *testing.T, target string) (string, ssh.PublicKey, <-chan string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if c.User() != "jump-user" || string(password) != "jump-password" {
			return nil, fmt.Errorf("jump authentication rejected")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requested := make(chan string, 10)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				transport, chans, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer transport.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range chans {
					if incoming.ChannelType() != "direct-tcpip" {
						_ = incoming.Reject(ssh.UnknownChannelType, "forward only")
						continue
					}
					var request struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if ssh.Unmarshal(incoming.ExtraData(), &request) != nil {
						_ = incoming.Reject(ssh.ConnectionFailed, "invalid request")
						continue
					}
					requested <- net.JoinHostPort(request.Host, strconv.Itoa(int(request.Port)))
					upstream, err := net.Dial("tcp", target)
					if err != nil {
						_ = incoming.Reject(ssh.ConnectionFailed, "upstream unavailable")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						_ = upstream.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					go func() {
						defer channel.Close()
						defer upstream.Close()
						joined := make(chan struct{})
						go func() { _, _ = io.Copy(upstream, channel); _ = upstream.Close(); close(joined) }()
						_, _ = io.Copy(channel, upstream)
						_ = channel.Close()
						<-joined
					}()
				}
			}()
		}
	}()
	return listener.Addr().String(), signer.PublicKey(), requested
}

func TestSSHJumpRouteExecAndCopyFileUseIndependentSavedCredentials(t *testing.T) {
	uploaded := make(chan string, 1)
	target, key := startExecSSHServer(t, func(command string, ch ssh.Channel, _ <-chan struct{}) {
		if command == "echo jump" {
			_, _ = io.WriteString(ch, "via-jump")
		} else if command == "subsystem:sftp" {
			serveUploadPeer(ch, &uploadPeerFS{finish: func(_ string, data []byte) error { uploaded <- string(data); return nil }})
			return
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	jumpAddr, jumpKey, requested := startServiceSSHJump(t, target)
	svc, source := routeTestFixture(t, target, key)
	host, portText, _ := net.SplitHostPort(jumpAddr)
	port, _ := strconv.Atoi(portText)
	salt := []byte("jump-salt-123456")
	encrypted, err := appcrypto.Encrypt([]byte("jump-password"), svc.credSvc.masterKey.DeriveKey(salt))
	if err != nil {
		t.Fatal(err)
	}
	jumpID, credID := uint(2), uint(2)
	jumpHostKey := jumpKey.Marshal()
	source.mu.Lock()
	source.servers[1].Host = "target-only-via-jump.invalid"
	expected := net.JoinHostPort(source.servers[1].Host, strconv.Itoa(source.servers[1].Port))
	source.servers[2] = &model.Server{BaseModel: model.BaseModel{ID: 2}, Host: host, Port: port, ConnectionRevision: 1, HostKey: &jumpHostKey, CredentialID: &credID, Credential: &model.SSHCredential{BaseModel: model.BaseModel{ID: credID}, AuthType: "password", Username: "jump-user", EncryptedPassword: &encrypted, Salt: salt}}
	source.configs[1] = &model.ServerConnectionConfig{ServerID: 1, Mode: "ssh_jump", JumpServerID: &jumpID}
	source.mu.Unlock()
	result, err := svc.Exec(context.Background(), 1, "echo jump", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "via-jump" {
		t.Fatalf("jump output = %q", result.Stdout)
	}
	select {
	case address := <-requested:
		if address != expected {
			t.Fatalf("jump target = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("jump bypassed")
	}
	local := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(local, []byte("agent-via-jump"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyFile(context.Background(), 1, local, "agent"); err != nil {
		t.Fatal(err)
	}
	if got := <-uploaded; got != "agent-via-jump" {
		t.Fatalf("jump upload = %q", got)
	}
}
