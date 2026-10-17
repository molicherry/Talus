package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/testutil"
)

func fileServiceFixture(t *testing.T) (*SFTPService, *routeTestSource, string, *testutil.SFTPPeer) {
	t.Helper()
	root := t.TempDir()
	peer := testutil.NewSFTPPeer(t, root, true)
	ssh, source := routeTestFixture(t, peer.Address, peer.Key)
	return NewSFTPService(ssh), source, root, peer
}
func fileReason(t *testing.T, err error, reason string) {
	t.Helper()
	var app *server.AppError
	if !errors.As(err, &app) || app.Reason != reason {
		t.Fatalf("error = %v; want %s", err, reason)
	}
}
func TestSFTPCRUDAndBinaryTransfer(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := context.Background()
	folder := filepath.Join(root, "中文 space ; ' $(ignored)")
	if err := svc.Mkdir(ctx, 1, folder); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(folder, "binary.dat")
	data := bytes.Repeat([]byte{0, 255, 1, 2, 0xf0, 0x9f, 0x98, 0x80}, 128<<10)
	if err := svc.Upload(ctx, 1, destination, bytes.NewReader(data), int64(len(data)), false); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || sha256.Sum256(actual) != sha256.Sum256(data) {
		t.Fatal("binary upload mismatch", err)
	}
	info, _ := os.Stat(destination)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("new upload mode = %o", info.Mode().Perm())
	}
	var out bytes.Buffer
	err = svc.Download(ctx, 1, destination, func(entry SFTPEntry) (io.Writer, error) {
		if entry.Size != int64(len(data)) {
			t.Fatal("wrong size")
		}
		return &out, nil
	})
	if err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatal("download mismatch", err)
	}
	directory, err := svc.List(ctx, 1, folder)
	if err != nil || len(directory.Entries) != 1 || directory.Entries[0].Name != "binary.dat" {
		t.Fatalf("list = %+v, %v", directory, err)
	}
	stat, err := svc.Stat(ctx, 1, destination)
	if err != nil || stat.Kind != "file" {
		t.Fatalf("stat = %+v,%v", stat, err)
	}
	fileReason(t, svc.Upload(ctx, 1, destination, strings.NewReader("new"), 3, false), "sftp_exists")
	if err := os.Chmod(destination, 0640); err != nil {
		t.Fatal(err)
	}
	if err := svc.Upload(ctx, 1, destination, strings.NewReader("new"), 3, true); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(destination)
	actual, _ = os.ReadFile(destination)
	if string(actual) != "new" || info.Mode().Perm() != 0640 {
		t.Fatal("overwrite failed to preserve permissions")
	}
	renamed := filepath.Join(folder, "renamed")
	if err := svc.Rename(ctx, 1, destination, renamed); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(ctx, 1, folder); err == nil {
		t.Fatal("removed nonempty directory")
	}
	if err := svc.Remove(ctx, 1, renamed); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(ctx, 1, folder); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty")
	if err := svc.Upload(ctx, 1, empty, strings.NewReader(""), 0, false); err != nil {
		t.Fatal(err)
	}
}
func TestSFTPPartialUploadPreservesOriginalAndCleansTemporaryFile(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	destination := filepath.Join(root, "original")
	if err := os.WriteFile(destination, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	fileReason(t, svc.Upload(context.Background(), 1, destination, strings.NewReader("partial"), 999, true), "sftp_size_mismatch")
	actual, _ := os.ReadFile(destination)
	if string(actual) != "original" {
		t.Fatal("incomplete upload changed original")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %+v", entries)
	}
}

type fileCancelReader struct {
	ctx     context.Context
	entered chan struct{}
}

func (r *fileCancelReader) Read([]byte) (int, error) {
	close(r.entered)
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func TestSFTPCancellationAndSlotRecovery(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- svc.Upload(ctx, 1, filepath.Join(root, "cancelled"), &fileCancelReader{ctx, entered}, 99, false)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	_, err := svc.List(context.Background(), 1, root)
	fileReason(t, err, "sftp_busy")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel retained a worker")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("cancelled upload left partial file: %+v", entries)
	}
	if _, err := svc.List(context.Background(), 1, root); err != nil {
		t.Fatal("slot did not recover", err)
	}
}
func TestSFTPHostKeysRoutesAndSubsystem(t *testing.T) {
	t.Run("unverified", func(t *testing.T) {
		svc, source, root, _ := fileServiceFixture(t)
		source.servers[1].HostKey = nil
		_, err := svc.List(context.Background(), 1, root)
		if !errors.Is(err, server.ErrSSHHostKeyUnverified) {
			t.Fatal("bypassed host key", err)
		}
	})
	t.Run("socks5", func(t *testing.T) {
		svc, source, root, peer := fileServiceFixture(t)
		proxy, requested := startServiceSOCKSProxy(t, peer.Address)
		expected := configureServiceSOCKS(t, source, proxy)
		if _, err := svc.List(context.Background(), 1, root); err != nil {
			t.Fatal(err)
		}
		if <-requested != expected {
			t.Fatal("bypassed proxy")
		}
	})
	t.Run("jump", func(t *testing.T) {
		svc, source, root, _ := fileServiceFixture(t)
		jump := testutil.NewSFTPPeer(t, t.TempDir(), true)
		host, portText, _ := net.SplitHostPort(jump.Address)
		port, _ := strconv.Atoi(portText)
		source.mu.Lock()
		server := cloneRouteServer(source.servers[1])
		server.ID = 2
		server.Host = host
		server.Port = port
		key := jump.Key.Marshal()
		server.HostKey = &key
		source.servers[2] = server
		source.configs[1] = &model.ServerConnectionConfig{ServerID: 1, Mode: "ssh_jump", JumpServerID: &server.ID}
		source.mu.Unlock()
		if _, err := svc.List(context.Background(), 1, root); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		peer := testutil.NewSFTPPeer(t, t.TempDir(), false)
		ssh, _ := routeTestFixture(t, peer.Address, peer.Key)
		_, err := NewSFTPService(ssh).List(context.Background(), 1, ".")
		fileReason(t, err, "sftp_unavailable")
	})
}
func TestSFTPDoesNotFollowSymlinksOrShellPaths(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	target := filepath.Join(root, "original")
	link := filepath.Join(root, "link")
	os.WriteFile(target, []byte("keep"), 0600)
	os.Symlink(target, link)
	fileReason(t, svc.Download(context.Background(), 1, link, func(SFTPEntry) (io.Writer, error) { t.Fatal("followed symlink"); return io.Discard, nil }), "sftp_not_regular")
	fileReason(t, svc.Upload(context.Background(), 1, link, strings.NewReader("bad"), 3, true), "sftp_not_regular")
	if err := svc.Remove(context.Background(), 1, link); err != nil {
		t.Fatal(err)
	}
	if actual, _ := os.ReadFile(target); string(actual) != "keep" {
		t.Fatal("deleting link removed target")
	}
	for _, p := range []string{"", "/", ".", "..", "bad\x00path"} {
		fileReason(t, svc.Remove(context.Background(), 1, p), "sftp_invalid_path")
	}
}

func TestSFTPStalledSubsystemCancellationDoesNotLeakSlots(t *testing.T) {
	peer := testutil.NewSFTPPeer(t, t.TempDir(), true, true)
	ssh, _ := routeTestFixture(t, peer.Address, peer.Key)
	svc := NewSFTPService(ssh)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		started := time.Now()
		_, err := svc.List(ctx, 1, ".")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
			t.Fatalf("stalled subsystem cancellation = %v (%s)", err, time.Since(started))
		}
	}
}

func TestSFTPDirectoryListingIsBounded(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	for i := 0; i < sftpEntryLimit+5; i++ {
		if err := os.WriteFile(filepath.Join(root, strconv.Itoa(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := svc.List(context.Background(), 1, root)
	if err != nil || !result.Truncated || len(result.Entries) != sftpEntryLimit {
		t.Fatalf("bounded directory = %v, %v", result, err)
	}
}
