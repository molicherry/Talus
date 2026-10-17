// Package testutil provides real local protocol peers for integration tests.
package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPPeer struct {
	Address string
	Key     ssh.PublicKey
}

func NewSFTPPeer(t testing.TB, root string, subsystem bool, stall ...bool) *SFTPPeer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var workers sync.WaitGroup
	active := make(map[net.Conn]bool)
	accepted := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		<-accepted
		mu.Lock()
		for conn := range active {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	go func() {
		defer close(accepted)
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			active[raw] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { _ = raw.Close(); mu.Lock(); delete(active, raw); mu.Unlock() }()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				var children sync.WaitGroup
				defer children.Wait()
				for channel := range channels {
					channel := channel
					children.Add(1)
					go func() {
						defer children.Done()
						if channel.ChannelType() == "direct-tcpip" {
							var req struct {
								Host       string
								Port       uint32
								OriginHost string
								OriginPort uint32
							}
							if ssh.Unmarshal(channel.ExtraData(), &req) != nil {
								_ = channel.Reject(ssh.ConnectionFailed, "bad request")
								return
							}
							upstream, err := net.Dial("tcp", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))))
							if err != nil {
								_ = channel.Reject(ssh.ConnectionFailed, "unreachable")
								return
							}
							stream, requests, err := channel.Accept()
							if err != nil {
								upstream.Close()
								return
							}
							go ssh.DiscardRequests(requests)
							done := make(chan struct{})
							go func() { _, _ = io.Copy(stream, upstream); stream.Close(); close(done) }()
							_, _ = io.Copy(upstream, stream)
							upstream.Close()
							stream.Close()
							<-done
							return
						}
						if channel.ChannelType() != "session" {
							_ = channel.Reject(ssh.UnknownChannelType, "session required")
							return
						}
						stream, requests, err := channel.Accept()
						if err != nil {
							return
						}
						defer stream.Close()
						for request := range requests {
							var req struct{ Name string }
							_ = ssh.Unmarshal(request.Payload, &req)
							if request.Type != "subsystem" || req.Name != "sftp" || !subsystem {
								_ = request.Reply(false, nil)
								continue
							}
							_ = request.Reply(true, nil)
							if len(stall) > 0 && stall[0] {
								_, _ = io.Copy(io.Discard, stream)
								return
							}
							server, err := sftp.NewServer(stream, sftp.WithServerWorkingDirectory(root))
							if err != nil {
								return
							}
							_ = server.Serve()
							_ = server.Close()
							return
						}
					}()
				}
			}()
		}
	}()
	return &SFTPPeer{Address: listener.Addr().String(), Key: signer.PublicKey()}
}
