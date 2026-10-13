package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestUploadCancellationDuringChannelOpen(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	opened := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		transport, channels, requests, err := ssh.NewServerConn(raw, cfg)
		if err != nil {
			raw.Close()
			return
		}
		defer transport.Close()
		go ssh.DiscardRequests(requests)
		for range channels {
			// Deliberately neither Accept nor Reject the channel-open request.
			close(opened)
			_ = transport.Wait()
			return
		}
	}()
	pool := newTestPool(t)
	client := dialTestSSH(t, listener.Addr().String(), signer.PublicKey())
	defer client.Close()
	seedPool(t, pool, 1, client)
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	local := writeLocalArtifact(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.CopyFile(ctx, 1, local, "/tmp/agent") }()
	select {
	case <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("upload never reached channel open")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled upload = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("upload remained blocked 500ms after parent cancellation during NewSession")
		// Explicit test cleanup: unblock the channel-open worker and permit quota settlement.
		client.Close()
		select {
		case err := <-done:
			t.Logf("upload returned only after explicit transport close: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("upload remained blocked even after explicit cleanup")
		}
	}
	client.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Error("SSH peer did not exit")
	}
}
