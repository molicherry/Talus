package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

func sftpWriteOnlyKey(id uint) context.Context {
	return mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "api_key", APIKeyID: &id, Scopes: []string{"files:write"}, ServerIDs: []uint{1}})
}

// This is the historical v1 encoder, used only to model an upload started by
// the old Hub. Production must never issue a new signed plaintext ticket.
func legacySFTPUploadToken(t *testing.T, secret []byte, ticket *sftpUploadTicket) string {
	t.Helper()
	old := *ticket
	old.Version = 1
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("talus-sftp-upload-v1\x00"))
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestSFTPUploadTicketEncryptionAndBindings(t *testing.T) {
	secret := []byte("dummy persistent secret for ticket tests")
	svc := NewSFTPService(nil, secret)
	ctx := sftpWriteOnlyKey(7)
	ticket := &sftpUploadTicket{
		Version: sftpUploadTicketVersion, Owner: "key:7", ServerID: 1, Route: "route-one",
		Destination: "/dummy/config", Temporary: "/dummy/.talus-upload-dummy",
		Size: 3, SHA256: fileDigest([]byte("new")), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		Previous: &sftpFileVersion{Size: 9876, Mode: 0640, Modified: 123456789, SHA256: fileDigest([]byte("old private contents"))},
	}
	sealed, err := svc.sealUpload(ticket)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.sealUpload(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == second || !strings.HasPrefix(sealed, "v2.") {
		t.Fatal("ticket did not use the encrypted version and a fresh nonce")
	}
	encrypted, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, "v2."))
	if err != nil || json.Valid(encrypted) || bytes.Contains(encrypted, []byte(ticket.Previous.SHA256)) || bytes.Contains(encrypted, []byte(`"previous"`)) {
		t.Fatal("opaque ticket exposed the old file version")
	}
	restarted := NewSFTPService(nil, secret)
	parsed, err := restarted.readUpload(ctx, 1, sealed)
	if err != nil || !reflect.DeepEqual(parsed, ticket) {
		t.Fatalf("restart lost the authenticated file version: %v", err)
	}
	if err := validateUploadRoute(context.WithValue(ctx, sftpRouteContextKey{}, "route-two"), parsed); err == nil {
		t.Fatal("accepted a ticket after the route changed")
	}
	for _, offset := range []int{0, len(encrypted) / 2, len(encrypted) - 1} {
		tampered := append([]byte(nil), encrypted...)
		tampered[offset] ^= 1
		_, err := svc.readUpload(ctx, 1, "v2."+base64.RawURLEncoding.EncodeToString(tampered))
		fileReason(t, err, "sftp_invalid_upload")
	}
	for _, malformed := range []string{"v2.", "v2.AA", "v3." + strings.TrimPrefix(sealed, "v2."), sealed + ".extra"} {
		_, err := svc.readUpload(ctx, 1, malformed)
		fileReason(t, err, "sftp_invalid_upload")
	}
	_, err = NewSFTPService(nil, []byte("different dummy persistent secret")).readUpload(ctx, 1, sealed)
	fileReason(t, err, "sftp_invalid_upload")
	_, err = svc.readUpload(ctx, 2, sealed)
	fileReason(t, err, "sftp_invalid_upload")
	for _, other := range []context.Context{sftpWriteOnlyKey(8), fileUser(7)} {
		if _, err := svc.readUpload(other, 1, sealed); !errors.Is(err, server.ErrForbidden) {
			t.Fatal("accepted a different principal", err)
		}
	}

	// Legacy tickets keep their signed expiry. The compatibility decoder must
	// not turn a successful read into a fresh ticket with an extended lifetime.
	legacy := legacySFTPUploadToken(t, secret, ticket)
	parsed, err = restarted.readUpload(ctx, 1, legacy)
	if err != nil || parsed.Version != 1 || parsed.ExpiresAt != ticket.ExpiresAt {
		t.Fatal("legacy expiry was changed or the upgrade broke existing tickets", err)
	}
	ticket.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	expired, err := svc.sealUpload(ticket)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{expired, legacySFTPUploadToken(t, secret, ticket)} {
		_, err := restarted.readUpload(ctx, 1, token)
		fileReason(t, err, "sftp_upload_expired")
		if _, err := restarted.readUpload(ctx, 1, token, true); err != nil {
			t.Fatal("expired ticket no longer permits cancellation cleanup", err)
		}
	}
}

func TestSFTPOverwriteTicketHidesPreviousFromWriteOnlyKey(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := sftpWriteOnlyKey(7)
	destination := filepath.Join(root, "private-config")
	old := []byte("private previous file contents")
	if err := os.WriteFile(destination, old, 0640); err != nil {
		t.Fatal(err)
	}
	session, err := svc.StartUpload(ctx, 1, destination, 3, fileDigest([]byte("new")), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(session.Token, "v2.") {
		t.Fatal("new upload issued a plaintext legacy ticket")
	}
	response, err := json.Marshal(session)
	if err != nil || bytes.Contains(response, []byte(fileDigest(old))) || bytes.Contains(response, []byte(`"previous"`)) {
		t.Fatal("write-only upload response exposed the old version")
	}
	ticket, err := svc.readUpload(ctx, 1, session.Token)
	if err != nil || ticket.Previous == nil || ticket.Previous.SHA256 != fileDigest(old) || ticket.Previous.Size != int64(len(old)) {
		t.Fatal("encryption discarded the overwrite conflict check", err)
	}
	if err := svc.CancelUpload(ctx, 1, session.Token); err != nil {
		t.Fatal(err)
	}
}

func TestSFTPLegacyUploadContinuesAcrossUpgradeWithoutReissue(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := fileUser(1)
	destination := filepath.Join(root, "legacy-resume")
	session, err := svc.StartUpload(ctx, 1, destination, 3, fileDigest([]byte("new")), false)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := svc.readUpload(ctx, 1, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacySFTPUploadToken(t, svc.secret, ticket)
	restarted := NewSFTPService(svc.ssh, svc.secret)
	status, err := restarted.UploadChunk(ctx, 1, legacy, 0, 3, strings.NewReader("new"))
	if err != nil || status.Token != "" || status.Offset != 3 || status.ExpiresAt.Unix() != ticket.ExpiresAt {
		t.Fatal("legacy resume failed or extended its ticket", err)
	}
	for range 2 {
		if err := restarted.CompleteUpload(ctx, 1, legacy); err != nil {
			t.Fatal("legacy completion was not idempotent", err)
		}
	}
	status, err = restarted.UploadStatus(ctx, 1, legacy)
	if err != nil || !status.Completed || status.Token != "" {
		t.Fatal("legacy status failed or reissued a token", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || string(actual) != "new" {
		t.Fatal("upgrade changed the uploaded file", err)
	}
}

func TestSFTPLegacyExpiredTicketOnlyPermitsCleanup(t *testing.T) {
	svc, _, root, _ := fileServiceFixture(t)
	ctx := fileUser(1)
	session, err := svc.StartUpload(ctx, 1, filepath.Join(root, "expired-legacy"), 3, fileDigest([]byte("new")), false)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := svc.readUpload(ctx, 1, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	ticket.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	legacy := legacySFTPUploadToken(t, svc.secret, ticket)
	restarted := NewSFTPService(svc.ssh, svc.secret)
	_, err = restarted.UploadStatus(ctx, 1, legacy)
	fileReason(t, err, "sftp_upload_expired")
	_, err = restarted.UploadChunk(ctx, 1, legacy, 0, 3, strings.NewReader("new"))
	fileReason(t, err, "sftp_upload_expired")
	fileReason(t, restarted.CompleteUpload(ctx, 1, legacy), "sftp_upload_expired")
	if err := restarted.CancelUpload(ctx, 1, legacy); err != nil {
		t.Fatal("upgrade prevented removal of an expired upload", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("expired legacy cancellation left temporary data", err)
	}
}
