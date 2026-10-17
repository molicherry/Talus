package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

const (
	MaxSFTPFileBytes       int64 = 64 << 20
	SFTPTransferBudget           = 10 * time.Minute
	sftpMetadataBudget           = 25 * time.Second
	sftpDirectoryWireLimit int64 = 8 << 20
	sftpEntryLimit               = 2000
)

// SFTPService uses the same authenticated, host-key-verified route as Exec.
// A per-server reservation leaves the other SSH slots available to terminals
// and monitoring. Its lifetime includes teardown, including pending workers.
type SFTPService struct {
	ssh    *SSHService
	mu     sync.Mutex
	busy   map[uint]bool
	secret []byte
}

func NewSFTPService(ssh *SSHService, signingKey ...[]byte) *SFTPService {
	secret := make([]byte, 32)
	if len(signingKey) > 0 && len(signingKey[0]) > 0 {
		secret = append([]byte(nil), signingKey[0]...)
	} else if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	return &SFTPService{ssh: ssh, busy: make(map[uint]bool), secret: secret}
}

func SFTPError(reason string) error {
	code := http.StatusBadRequest
	switch reason {
	case "sftp_invalid_range":
		code = http.StatusRequestedRangeNotSatisfiable
	case "sftp_not_found":
		code = http.StatusNotFound
	case "sftp_permission_denied":
		code = http.StatusForbidden
	case "sftp_upload_expired":
		code = http.StatusGone
	case "sftp_exists", "sftp_conflict", "sftp_not_empty":
		code = http.StatusConflict
	case "sftp_busy":
		code = http.StatusTooManyRequests
	case "sftp_too_large", "sftp_directory_too_large", "sftp_text_too_large":
		code = http.StatusRequestEntityTooLarge
	case "sftp_unavailable", "sftp_failed":
		code = http.StatusBadGateway
	}
	return server.NewAppError(code, reason)
}
func safeSFTPError(err error) error {
	if err == nil {
		return nil
	}
	var app *server.AppError
	if errors.As(err, &app) {
		return app
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, fs.ErrNotExist) {
		return SFTPError("sftp_not_found")
	}
	if errors.Is(err, fs.ErrPermission) {
		return SFTPError("sftp_permission_denied")
	}
	if errors.Is(err, fs.ErrExist) {
		return SFTPError("sftp_exists")
	}
	return SFTPError("sftp_failed")
}
func remotePath(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !utf8.ValidString(p) || len(p) > 4096 || strings.ContainsRune(p, 0) {
		return "", SFTPError("sftp_invalid_path")
	}
	return path.Clean(p), nil
}
func mutablePath(p string) (string, error) {
	if p == "" {
		return "", SFTPError("sftp_invalid_path")
	}
	p, err := remotePath(p)
	if err != nil {
		return "", err
	}
	if p == "." || p == "/" || p == ".." {
		return "", SFTPError("sftp_invalid_path")
	}
	return p, nil
}

type sftpBoundedReader struct {
	source    io.Reader
	remaining atomic.Int64
}

func (r *sftpBoundedReader) Read(p []byte) (int, error) {
	left := r.remaining.Load()
	if left == 0 {
		return 0, io.EOF
	}
	if left > 0 && int64(len(p)) > left {
		p = p[:int(left)]
	}
	n, err := r.source.Read(p)
	if left > 0 {
		r.remaining.Add(-int64(n))
	}
	return n, err
}

// withClient owns both the SFTP channel and its SSH lease. The supervisor also
// covers subsystem initialization and protocol Close, which lack context APIs.
func (s *SFTPService) withClient(ctx context.Context, id uint, metadata bool, fn func(context.Context, *sftp.Client) error) error {
	s.mu.Lock()
	if s.busy[id] {
		s.mu.Unlock()
		return SFTPError("sftp_busy")
	}
	s.busy[id] = true
	s.mu.Unlock()
	releaseReservation := func() { s.mu.Lock(); delete(s.busy, id); s.mu.Unlock() }
	budget := SFTPTransferBudget
	if metadata {
		budget = sftpMetadataBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	handle, snapshot, err := s.ssh.getLeaseWithSnapshot(sshpool.WithQuotaConsumer(ctx, string(LeaseConsumerSFTP)), id, false)
	if err != nil {
		releaseReservation()
		return err
	}
	lease := newSSHLease(s.ssh, handle)
	operation, err := lease.begin(ctx)
	if err != nil {
		lease.Discard()
		go func() { <-lease.Done(); releaseReservation() }()
		return safeSFTPError(err)
	}
	operation = context.WithValue(operation, sftpRouteContextKey{}, snapshot.fingerprint())
	ready := make(chan *ssh.Session, 1)
	joined := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(joined)
		session, err := lease.client().NewSession()
		if err != nil {
			result <- err
			return
		}
		ready <- session
		defer session.Close()
		input, err := session.StdinPipe()
		if err != nil {
			result <- err
			return
		}
		output, err := session.StdoutPipe()
		if err != nil {
			result <- err
			return
		}
		stderr, err := session.StderrPipe()
		if err != nil {
			result <- err
			return
		}
		drained := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, stderr); close(drained) }()
		// Join stderr after the subsystem/session has closed.
		defer func() { _ = session.Close(); <-drained }()
		if err = session.RequestSubsystem("sftp"); err != nil {
			result <- SFTPError("sftp_unavailable")
			return
		}

		bounded := &sftpBoundedReader{source: output}
		bounded.remaining.Store(-1)
		if metadata {
			bounded.remaining.Store(sftpDirectoryWireLimit)
		}
		workerContext := context.WithValue(operation, sftpWireContextKey{}, bounded)
		var reader io.Reader = bounded
		client, err := sftp.NewClientPipe(reader, input, sftp.UseConcurrentReads(false))
		if err != nil {
			result <- err
			return
		}
		err = fn(workerContext, client)
		if err != nil && bounded.remaining.Load() == 0 {
			err = SFTPError("sftp_directory_too_large")
		}
		result <- err
		_ = client.Close()
	}()
	var runErr error
	select {
	case runErr = <-result:
	case <-operation.Done():
		runErr = context.Cause(operation)
		// HTTP body cancellation can unblock a transfer, allowing its worker to
		// remove a temporary file before its SFTP channel is forced closed.
		grace := time.NewTimer(execTeardownGrace)
		select {
		case <-result:
		case <-grace.C:
		}
		grace.Stop()
	}
	if operation.Err() != nil {
		runErr = context.Cause(operation)
	}
	forced, cleanupErr := closeSSHSession(lease.client(), ready, joined, sshTeardownDeadlines(operation, execTeardownGrace))
	lease.end(forced || isConnectionError(runErr), errors.Join(runErr, cleanupErr))
	settlementErr := lease.releaseAndWait()
	cleanupErr = errors.Join(cleanupErr, settlementErr)
	// Retain the file reservation until the lease actually settles.
	select {
	case <-lease.Done():
		releaseReservation()
	default:
		go func() { <-lease.Done(); releaseReservation() }()
	}
	return safeSFTPError(errors.Join(runErr, cleanupErr))
}

type SFTPEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	ModifiedAt time.Time `json:"modified_at"`
}
type SFTPDirectory struct {
	Path      string      `json:"path"`
	Parent    string      `json:"parent"`
	Entries   []SFTPEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

func sftpEntry(p string, info os.FileInfo) SFTPEntry {
	kind := "other"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "file"
	}
	return SFTPEntry{Name: info.Name(), Path: p, Kind: kind, Size: info.Size(), Mode: info.Mode().String(), ModifiedAt: info.ModTime().UTC()}
}
func (s *SFTPService) List(ctx context.Context, id uint, p string) (*SFTPDirectory, error) {
	p, err := remotePath(p)
	if err != nil {
		return nil, err
	}
	var directory *SFTPDirectory
	err = s.withClient(ctx, id, true, func(ctx context.Context, c *sftp.Client) error {
		resolved, err := c.RealPath(p)
		if err != nil {
			return err
		}
		entries, err := c.ReadDirContext(ctx, resolved)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name() < entries[j].Name()
		})
		directory = &SFTPDirectory{Path: resolved, Parent: path.Dir(resolved), Entries: make([]SFTPEntry, 0, min(len(entries), sftpEntryLimit))}
		responseBytes := 0
		for _, entry := range entries {
			if entry.Name() == "." || entry.Name() == ".." {
				continue
			}
			if !utf8.ValidString(entry.Name()) || strings.ContainsAny(entry.Name(), "/\x00") {
				return SFTPError("sftp_invalid_path")
			}
			responseBytes += len(entry.Name()) + len(resolved) + 256
			if len(directory.Entries) >= sftpEntryLimit || responseBytes > 2<<20 {
				directory.Truncated = true
				break
			}
			directory.Entries = append(directory.Entries, sftpEntry(path.Join(resolved, entry.Name()), entry))
		}
		return nil
	})
	return directory, err
}
func (s *SFTPService) Stat(ctx context.Context, id uint, p string) (*SFTPEntry, error) {
	p, err := remotePath(p)
	if err != nil {
		return nil, err
	}
	var entry *SFTPEntry
	err = s.withClient(ctx, id, true, func(_ context.Context, c *sftp.Client) error {
		info, err := c.Lstat(p)
		if err != nil {
			return err
		}
		value := sftpEntry(p, info)
		entry = &value
		return nil
	})
	return entry, err
}

// Download calls begin only after validating and opening the remote file. It
// never routes file bytes through JSON or buffers the entire file in the Hub.
func (s *SFTPService) Download(ctx context.Context, id uint, p string, begin func(SFTPEntry) (io.Writer, error)) error {
	p, err := remotePath(p)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		info, err := c.Lstat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return SFTPError("sftp_not_regular")
		}
		if info.Size() > MaxSFTPFileBytes {
			return SFTPError("sftp_too_large")
		}
		f, err := c.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() || opened.Size() != info.Size() || !opened.ModTime().Equal(info.ModTime()) {
			return SFTPError("sftp_conflict")
		}
		out, err := begin(sftpEntry(p, opened))
		if err != nil {
			return err
		}
		n, err := io.CopyBuffer(out, io.LimitReader(f, opened.Size()), make([]byte, 32<<10))
		if err != nil {
			return err
		}
		if n != opened.Size() {
			return SFTPError("sftp_conflict")
		}
		return ctx.Err()
	})
}
func randomFileSuffix() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func sameRemoteFile(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}
func pendingDestination(c *sftp.Client, p string, overwrite bool) (os.FileInfo, error) {
	info, err := c.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !overwrite {
		return nil, SFTPError("sftp_exists")
	}
	if !info.Mode().IsRegular() {
		return nil, SFTPError("sftp_not_regular")
	}
	if _, ok := c.HasExtension("posix-rename@openssh.com"); !ok {
		return nil, SFTPError("sftp_atomic_unsupported")
	}
	return info, nil
}
func commitRemoteFile(c *sftp.Client, tmp, p string, previous os.FileInfo) error {
	current, err := c.Lstat(p)
	if previous == nil {
		if err == nil {
			return SFTPError("sftp_exists")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		// Hard-link publication cannot replace a concurrently created destination.
		if _, ok := c.HasExtension("hardlink@openssh.com"); ok {
			if err := c.Link(tmp, p); err != nil {
				return err
			}
			_ = c.Remove(tmp)
			return nil
		}
		// Standard SFTP v3 rename must refuse an existing destination.
		return c.Rename(tmp, p)
	}
	if err != nil || !sameRemoteFile(previous, current) {
		return SFTPError("sftp_conflict")
	}
	if err = c.Chmod(tmp, previous.Mode().Perm()); err != nil {
		return err
	}
	return c.PosixRename(tmp, p)
}
func (s *SFTPService) Upload(ctx context.Context, id uint, p string, body io.Reader, size int64, overwrite bool) error {
	p, err := mutablePath(p)
	if err != nil {
		return err
	}
	if size < 0 || size > MaxSFTPFileBytes {
		return SFTPError("sftp_too_large")
	}
	return s.withClient(ctx, id, false, func(ctx context.Context, c *sftp.Client) error {
		previous, err := pendingDestination(c, p, overwrite)
		if err != nil {
			return err
		}
		tmp := path.Join(path.Dir(p), ".talus-upload-"+randomFileSuffix())
		f, err := c.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			_ = f.Close()
			if !committed {
				_ = c.Remove(tmp)
			}
		}()
		if err = c.Chmod(tmp, 0600); err != nil {
			return err
		}
		n, err := io.CopyBuffer(f, io.LimitReader(body, size+1), make([]byte, 32<<10))
		if err != nil {
			return err
		}
		if n != size {
			return SFTPError("sftp_size_mismatch")
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = commitRemoteFile(c, tmp, p, previous); err != nil {
			return err
		}
		committed = true
		return nil
	})
}
func (s *SFTPService) Mkdir(ctx context.Context, id uint, p string, parents ...bool) error {
	p, err := mutablePath(p)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, true, func(_ context.Context, c *sftp.Client) error {
		if len(parents) > 0 && parents[0] {
			return c.MkdirAll(p)
		}
		return c.Mkdir(p)
	})
}
func (s *SFTPService) Rename(ctx context.Context, id uint, from, to string) error {
	from, err := mutablePath(from)
	if err != nil {
		return err
	}
	to, err = mutablePath(to)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, true, func(_ context.Context, c *sftp.Client) error {
		if _, err := c.Lstat(to); err == nil {
			return SFTPError("sftp_exists")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return c.Rename(from, to)
	})
}
func (s *SFTPService) Remove(ctx context.Context, id uint, p string) error {
	p, err := mutablePath(p)
	if err != nil {
		return err
	}
	return s.withClient(ctx, id, true, func(_ context.Context, c *sftp.Client) error {
		info, err := c.Lstat(p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return c.RemoveDirectory(p)
		}
		return c.Remove(p)
	})
}
