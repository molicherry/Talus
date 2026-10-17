package service

import (
	"bytes"
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/testutil"
)

func TestCopyFileUsesRealSFTPWithLiteralPathAndPrivateArtifact(t *testing.T) {
	files, _, root, _ := fileServiceFixture(t)
	content := bytes.Repeat([]byte{0, 255, 13, 10, 1, 128}, 1<<17)
	local := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(local, content, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "agent's 中文 ; $(literal)")
	if err := files.ssh.CopyFile(context.Background(), 1, local, target); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatal("SFTP artifact bytes differ", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unverified artifact must remain private", info, err)
	}
}
func TestCopyFileReportsMissingSFTPSubsystem(t *testing.T) {
	peer := testutil.NewSFTPPeer(t, t.TempDir(), false)
	svc, _ := routeTestFixture(t, peer.Address, peer.Key)
	fileReason(t, svc.CopyFile(context.Background(), 1, writeLocalArtifact(t), "/tmp/agent"), "sftp_unavailable")
}

func TestCopyFileCancellationDuringSFTPWriteReturnsQuota(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	address, key := startExecSSHServer(t, routeUploads(func(ch ssh.Channel, _ <-chan struct{}) {
		serveUploadPeer(ch, &uploadPeerFS{write: func() { once.Do(func() { close(started) }); <-release }})
	}))
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, key))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- svc.CopyFile(ctx, 1, writeLocalArtifact(t), "/tmp/agent") }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("SFTP write did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lost cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled SFTP write did not settle")
	}
	unblock()
	handle, err := pool.AcquireContext(context.Background(), 1, testFP(1))
	if err != nil {
		t.Fatal("SFTP write leaked quota", err)
	}
	pool.DiscardHandle(handle)
}
