package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

func fileUser(id uint) context.Context {
	return mw.WithUserClaims(context.Background(), &token.Claims{UserID: id, Role: "admin"})
}
func fileDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func TestSFTPResumableUploadSurvivesClientAndServiceRestart(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := fileUser(1)
	destination := filepath.Join(root, "resumable")
	data := bytes.Repeat([]byte{0, 255, 17, 4}, 600<<10)
	session, err := svc.StartUpload(ctx, 1, destination, int64(len(data)), fileDigest(data), false)
	if err != nil {
		t.Fatal(err)
	}
	first := int64(1 << 20)
	status, err := svc.UploadChunk(ctx, 1, session.Token, 0, first, bytes.NewReader(data[:first]))
	if err != nil || status.Offset != first {
		t.Fatal(status, err)
	}
	_, err = svc.UploadChunk(ctx, 1, session.Token, 0, 1, bytes.NewReader(data[:1]))
	fileReason(t, err, "sftp_conflict")
	svc = NewSFTPService(svc.ssh, svc.secret)
	status, err = svc.UploadStatus(ctx, 1, session.Token)
	if err != nil || status.Offset != first {
		t.Fatal("resume state lost", status, err)
	}
	for offset := first; offset < int64(len(data)); {
		size := min(MaxSFTPChunkBytes, int64(len(data))-offset)
		status, err = svc.UploadChunk(ctx, 1, session.Token, offset, size, bytes.NewReader(data[offset:offset+size]))
		if err != nil {
			t.Fatal(err)
		}
		offset = status.Offset
	}
	if err = svc.CompleteUpload(ctx, 1, session.Token); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(destination)
	if !bytes.Equal(actual, data) {
		t.Fatal("resumed upload changed bytes")
	}
	if err = svc.CompleteUpload(ctx, 1, session.Token); err != nil {
		t.Fatal("completion was not idempotent", err)
	}
	status, err = svc.UploadStatus(ctx, 1, session.Token)
	if err != nil || !status.Completed {
		t.Fatal("lost final acknowledgement cannot recover", status, err)
	}
}
func TestSFTPResumeRejectsWrongIdentityRouteHashAndExpiry(t *testing.T) {
	svc, source, root, _ := fileServiceFixture(t)
	ctx := fileUser(1)
	p := filepath.Join(root, "original")
	if err := os.WriteFile(p, []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	session, err := svc.StartUpload(ctx, 1, p, 3, fileDigest([]byte("new")), true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UploadStatus(fileUser(2), 1, session.Token)
	if !errors.Is(err, server.ErrForbidden) {
		t.Fatal("cross-user token accepted", err)
	}
	_, err = svc.UploadStatus(ctx, 2, session.Token)
	fileReason(t, err, "sftp_invalid_upload")
	_, err = svc.UploadStatus(ctx, 1, session.Token+"tampered")
	fileReason(t, err, "sftp_invalid_upload")
	ticket, err := svc.readUpload(ctx, 1, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	ticket.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	expiredToken, err := svc.sealUpload(ticket)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UploadStatus(ctx, 1, expiredToken)
	fileReason(t, err, "sftp_upload_expired")
	_, err = svc.UploadChunk(ctx, 1, session.Token, 0, 3, strings.NewReader("bad"))
	if err != nil {
		t.Fatal(err)
	}
	fileReason(t, svc.CompleteUpload(ctx, 1, session.Token), "sftp_checksum_mismatch")
	actual, _ := os.ReadFile(p)
	if string(actual) != "original" {
		t.Fatal("checksum failure replaced original")
	}
	if err = svc.CancelUpload(ctx, 1, session.Token); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("cancel left upload temporary file")
	}
	session, err = svc.StartUpload(ctx, 1, filepath.Join(root, "route"), 0, fileDigest(nil), false)
	if err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.servers[1].ConnectionRevision++
	source.mu.Unlock()
	_, err = svc.UploadStatus(ctx, 1, session.Token)
	fileReason(t, err, "sftp_conflict")
}
func TestSFTPResumableOverwriteDetectsSameSecondEdit(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := fileUser(1)
	p := filepath.Join(root, "config")
	if err := os.WriteFile(p, []byte("aaaa"), 0640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	session, err := svc.StartUpload(ctx, 1, p, 4, fileDigest([]byte("new!")), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.UploadChunk(ctx, 1, session.Token, 0, 4, strings.NewReader("new!")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("bbbb"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	fileReason(t, svc.CompleteUpload(ctx, 1, session.Token), "sftp_conflict")
	data, _ := os.ReadFile(p)
	if string(data) != "bbbb" {
		t.Fatal("overwrote concurrent edit")
	}
}
func TestSFTPTextVersionConflictAndEncoding(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := context.Background()
	p := filepath.Join(root, "config.txt")
	if err := os.WriteFile(p, []byte("alpha\r\n中文\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0640); err != nil {
		t.Fatal(err)
	}
	initial, err := svc.ReadText(ctx, 1, p)
	if err != nil || initial.Content != "alpha\r\n中文\n" {
		t.Fatal(initial, err)
	}
	saved, err := svc.SaveText(ctx, 1, p, "beta\r\n中文\n", initial.Version)
	if err != nil || saved.Version == initial.Version {
		t.Fatal(saved, err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0640 {
		t.Fatal("editor changed permissions")
	}
	if err := os.WriteFile(p, []byte("xxxx\r\n中文\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SaveText(ctx, 1, p, "my draft", saved.Version)
	fileReason(t, err, "sftp_conflict")
	actual, _ := os.ReadFile(p)
	if string(actual) != "xxxx\r\n中文\n" {
		t.Fatal("stale save replaced external edit")
	}
	for _, data := range [][]byte{{0, 1}, {255, 254}} {
		if err := os.WriteFile(p, data, 0640); err != nil {
			t.Fatal(err)
		}
		_, err = svc.ReadText(ctx, 1, p)
		fileReason(t, err, "sftp_not_text")
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte{'x'}, int(MaxSFTPTextBytes+1)), 0640); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ReadText(ctx, 1, p)
	fileReason(t, err, "sftp_text_too_large")
}
func TestSFTPRecursiveZIPIncludesEmptyDirectoriesAndSkipsLinks(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := context.Background()
	folder := filepath.Join(root, "folder")
	nested := filepath.Join(folder, "nested", "empty")
	if err := svc.Mkdir(ctx, 1, nested, true); err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 255, 7, 8}
	if err := os.WriteFile(filepath.Join(folder, "nested", "中文.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("excluded"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := svc.Archive(ctx, 1, folder, func(name string) (io.Writer, error) {
		if name != "folder.zip" {
			t.Fatal(name)
		}
		return &output, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range archive.File {
		seen[file.Name] = true
		if strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "../") || strings.Contains(file.Name, "link") {
			t.Fatal("unsafe archive entry", file.Name)
		}
		if strings.HasSuffix(file.Name, "中文.bin") {
			reader, _ := file.Open()
			actual, _ := io.ReadAll(reader)
			reader.Close()
			if !bytes.Equal(actual, data) {
				t.Fatal("archive changed bytes")
			}
		}
	}
	if !seen["folder/nested/empty/"] || !seen["folder/nested/中文.bin"] {
		t.Fatal("recursive archive incomplete", seen)
	}
	large := filepath.Join(folder, "large")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxSFTPFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	fileReason(t, svc.Archive(ctx, 1, folder, func(string) (io.Writer, error) { t.Fatal("started oversized archive"); return io.Discard, nil }), "sftp_too_large")
}
func TestSFTPDownloadRangesAndIfRange(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	p := filepath.Join(root, "range")
	if err := os.WriteFile(p, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var first bytes.Buffer
	var etag string
	err := svc.DownloadRange(ctx, 1, p, SFTPDownloadRequest{Range: "bytes=0-3"}, func(response SFTPDownload) (io.Writer, error) {
		etag = response.ETag
		if response.Start != 0 || response.Length != 4 || !response.Partial {
			t.Fatal(response)
		}
		return &first, nil
	})
	if err != nil || first.String() != "0123" {
		t.Fatal(first.String(), err)
	}
	var rest bytes.Buffer
	err = svc.DownloadRange(ctx, 1, p, SFTPDownloadRequest{Range: "bytes=4-", IfRange: etag}, func(response SFTPDownload) (io.Writer, error) { return &rest, nil })
	if err != nil || rest.String() != "456789" {
		t.Fatal(rest.String(), err)
	}
	if err := os.WriteFile(p, []byte("abcdefghij"), 0600); err != nil {
		t.Fatal(err)
	}
	var changed bytes.Buffer
	err = svc.DownloadRange(ctx, 1, p, SFTPDownloadRequest{Range: "bytes=4-", IfRange: etag}, func(response SFTPDownload) (io.Writer, error) {
		if response.Partial || response.Start != 0 {
			t.Fatal("appended a new file version")
		}
		return &changed, nil
	})
	if err != nil || changed.String() != "abcdefghij" {
		t.Fatal(changed.String(), err)
	}
	for _, value := range []string{"bytes=99-", "bytes=0-1,4-5", "bytes=-0", "garbage"} {
		fileReason(t, svc.DownloadRange(ctx, 1, p, SFTPDownloadRequest{Range: value}, func(SFTPDownload) (io.Writer, error) { t.Fatal("accepted invalid range"); return io.Discard, nil }), "sftp_invalid_range")
	}
}
