package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

// This server executes a supplied channel handler over a real SSH transport.
func startExecSSHServer(t *testing.T, execute func(string, ssh.Channel, <-chan struct{})) (string, ssh.PublicKey) {
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
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				transport, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer transport.Close()
				transportClosed := make(chan struct{})
				go func() { _ = transport.Wait(); close(transportClosed) }()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						for request := range requests {
							var message struct{ Command string }
							if request.Type != "exec" || ssh.Unmarshal(request.Payload, &message) != nil {
								_ = request.Reply(false, nil)
								continue
							}
							_ = request.Reply(true, nil)
							execute(message.Command, channel, transportClosed)
							return
						}
					}()
				}
			}()
		}
	}()
	return listener.Addr().String(), signer.PublicKey()
}

func TestExecPreservesRealExitCode(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) {
		_, _ = io.WriteString(channel, "output")
		_, _ = io.WriteString(channel.Stderr(), "error output")
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{7}))
		_ = channel.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	result, err := svc.Exec(context.Background(), 1, "anything", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || result.Stdout != "output" || result.Stderr != "error output" {
		t.Fatalf("unexpected result: %#v", result)
	}
	client, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		pool.Release(1, nil)
		t.Fatal("nonzero command exit discarded a healthy transport")
	}
	pool.Release(1, client)
}

func TestExecClientCancellationJoinsAndReusesHealthyTransport(t *testing.T) {
	started := make(chan struct{})
	remoteClosed := make(chan (<-chan struct{}), 1)
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, transportClosed <-chan struct{}) {
		remoteClosed <- transportClosed
		close(started)
		// Keep the channel open independently of Run's empty-stdin EOF.
		// The server mux handles our client's channel cancellation itself.
		<-transportClosed
		_ = channel.Close()
	})
	pool := newTestPool(t)
	original := dialTestSSH(t, address, hostKey)
	seedPool(t, pool, 1, original)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.Exec(ctx, 1, "wait", time.Minute); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, server.ErrSSHTimeout) {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled execution did not join")
	}
	transportClosed := <-remoteClosed
	select {
	case <-transportClosed:
		t.Fatal("channel cancellation closed a healthy SSH transport")
	default:
	}
	client, err := pool.Get(1, testFP(1))
	if err != nil {
		t.Fatal(err)
	}
	if client != original {
		pool.Release(1, client)
		t.Fatal("channel cancellation did not preserve its healthy transport")
	}
	pool.Release(1, client)
}

func TestExecOwnTimeoutIsNotClientCancellation(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, transportClosed <-chan struct{}) {
		<-transportClosed
		_ = channel.Close()
	})
	pool := newTestPool(t)
	original := dialTestSSH(t, address, hostKey)
	seedPool(t, pool, 1, original)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	_, err := svc.Exec(context.Background(), 1, "wait", 30*time.Millisecond)
	if !errors.Is(err, server.ErrSSHTimeout) || errors.Is(err, context.Canceled) {
		t.Fatalf("timeout result = %v", err)
	}
	client, getErr := pool.Get(1, testFP(1))
	if getErr != nil {
		t.Fatal(getErr)
	}
	if client != original {
		pool.Release(1, client)
		t.Fatal("channel timeout did not preserve its healthy transport")
	}
	pool.Release(1, client)
}

func TestExecCancellationUnblocksSessionSetupWrite(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) { _ = channel.Close() })
	client, connection := dialGatedSSH(t, address, hostKey)
	defer client.Close()
	connection.blockWrites()
	svc := NewSSHService(nil, nil, nil, time.Second, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err, _ := svc.runCommand(ctx, client, "blocked", time.Minute); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("result = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session setup write did not unblock")
	}
}
