package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/vpsmanager/backend/internal/model"
	appcrypto "github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/testutil"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sftpHTTPFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	peer := testutil.NewSFTPPeer(t, root, true)
	master, err := appcrypto.NewMasterKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	salt := []byte("0123456789abcdef")
	encrypted, err := appcrypto.Encrypt([]byte("password"), master.DeriveKey(salt))
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(peer.Address)
	port, _ := strconv.Atoi(portText)
	key := peer.Key.Marshal()
	credentialID := uint(1)
	source := &terminalE2ESource{server: model.Server{BaseModel: model.BaseModel{ID: 1}, Host: host, Port: port, ConnectionRevision: 1, HostKey: &key, CredentialID: &credentialID, Credential: &model.SSHCredential{CryptoVersion: 1, BaseModel: model.BaseModel{ID: 1}, Username: "test", AuthType: "password", EncryptedPassword: &encrypted, Salt: salt}}}
	pool := sshpool.NewPool(time.Minute, 3, time.Second)
	t.Cleanup(pool.Close)
	svc := service.NewSFTPService(service.NewSSHService(pool, source, service.NewCredentialService(nil, nil, master, pool), time.Second, time.Second))
	h := NewSFTPHandler(svc)
	validator := mw.APIKeyValidatorFunc(func(_ context.Context, key string) (uint, string, string, []string, []uint, error) {
		return 1, "file-key", "admin", strings.Split(key, ","), []uint{1}, nil
	})
	router := server.NewRouter(server.RouteConfig{JWTService: token.NewJWTService("test", time.Hour), APIKeyAuth: validator, SFTPListHandler: h.List, SFTPStatHandler: h.Stat, SFTPUploadHandler: h.Upload, SFTPDownloadHandler: h.Download, SFTPMkdirHandler: h.Mkdir, SFTPRenameHandler: h.Rename, SFTPRemoveHandler: h.Remove, SFTPReadTextHandler: h.ReadText, SFTPSaveTextHandler: h.SaveText, SFTPStartUploadHandler: h.StartUpload, SFTPUploadStatusHandler: h.UploadStatus, SFTPUploadChunkHandler: h.UploadChunk, SFTPCompleteUploadHandler: h.CompleteUpload, SFTPCancelUploadHandler: h.CancelUpload, SFTPArchiveHandler: h.Archive})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(router)
	ts.Listener.Close()
	ts.Listener = listener
	ts.Config.ReadTimeout = 15 * time.Millisecond // Upload handler must replace this.
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, root
}
func sftpHTTP(t *testing.T, ts *httptest.Server, method, endpoint, scope string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", scope)
	response, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}
func TestSFTPHTTPAuthScopesAndServerRestrictions(t *testing.T) {
	ts, _ := sftpHTTPFixture(t)
	for _, tc := range []struct {
		method, endpoint, scope string
		status                  int
	}{
		{"GET", "/api/v1/servers/1/sftp/entries", "", 401},
		{"GET", "/api/v1/servers/1/sftp/entries", "servers:read", 403},
		{"POST", "/api/v1/servers/1/sftp/upload?size=0&path=/tmp/nope", "files:read", 403},
		{"POST", "/api/v1/servers/1/sftp/mkdir", "files:read", 403},
		{"DELETE", "/api/v1/servers/1/sftp/entry?path=/tmp/nope", "files:write", 403},
		{"GET", "/api/v1/servers/2/sftp/entries", "files:read", 403},
		{"GET", "/api/v1/servers/malformed/sftp/entries", "servers:read", 403},
		{"GET", "/api/v1/servers/malformed/sftp/entries", "files:read", 400},
	} {
		response := sftpHTTP(t, ts, tc.method, tc.endpoint, tc.scope, nil)
		if response.StatusCode != tc.status {
			t.Fatalf("%s %s -> %d want %d", tc.method, tc.endpoint, response.StatusCode, tc.status)
		}
	}
}
func TestSFTPHTTPBinaryTransferAndReadDeadline(t *testing.T) {
	ts, root := sftpHTTPFixture(t)
	destination := filepath.Join(root, "中文 file.bin")
	data := bytes.Repeat([]byte{0, 255, 3, 17}, 1<<15)
	pr, pw := io.Pipe()
	done := make(chan *http.Response, 1)
	go func() {
		done <- sftpHTTP(t, ts, "POST", "/api/v1/servers/1/sftp/upload?size="+strconv.Itoa(len(data))+"&path="+url.QueryEscape(destination), "files:write", pr)
	}()
	time.Sleep(60 * time.Millisecond) // Exceeds the server's global ReadTimeout.
	if _, err := pw.Write(data); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	response := <-done
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("upload %d: %s", response.StatusCode, body)
	}
	actual, _ := os.ReadFile(destination)
	if !bytes.Equal(actual, data) {
		t.Fatal("HTTP upload changed bytes")
	}
	response = sftpHTTP(t, ts, "GET", "/api/v1/servers/1/sftp/download?path="+url.QueryEscape(destination), "files:read", nil)
	actual, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !bytes.Equal(actual, data) {
		t.Fatal("HTTP download mismatch", err, response.StatusCode)
	}
	if response.Header.Get("Content-Disposition") == "" || response.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatal("missing download headers")
	}
	response = sftpHTTP(t, ts, "GET", "/api/v1/servers/1/sftp/entries?path="+url.QueryEscape(root), "files:read", nil)
	var payload struct {
		Data service.SFTPDirectory `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil || len(payload.Data.Entries) != 1 {
		t.Fatalf("directory response = %+v,%v", payload, err)
	}
}

func TestSFTPHTTPPhase2ScopesAndRangeDownload(t *testing.T) {
	ts, root := sftpHTTPFixture(t)
	p := filepath.Join(root, "range.txt")
	if err := os.WriteFile(p, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{{"GET", "text"}, {"PUT", "text"}, {"GET", "archive"}, {"POST", "uploads/start"}, {"POST", "uploads/status"}, {"PUT", "uploads/chunk"}, {"POST", "uploads/complete"}, {"DELETE", "uploads"}} {
		response := sftpHTTP(t, ts, tc.method, "/api/v1/servers/1/sftp/"+tc.path, "servers:exec", nil)
		if response.StatusCode != 403 {
			t.Fatalf("ungated %s %s: %d", tc.method, tc.path, response.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/servers/1/sftp/download?path="+url.QueryEscape(p), nil)
	req.Header.Set("X-API-Key", "files:read")
	req.Header.Set("Range", "bytes=3-6")
	response, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 206 || string(data) != "3456" || response.Header.Get("Content-Range") != "bytes 3-6/10" || response.Header.Get("ETag") == "" {
		t.Fatalf("HTTP range = %d %q %v", response.StatusCode, data, err)
	}
}

func TestSFTPHTTPPrivateResponsesAreNotCached(t *testing.T) {
	ts, root := sftpHTTPFixture(t)
	p := filepath.Join(root, "private.txt")
	if err := os.WriteFile(p, []byte("private file contents"), 0600); err != nil {
		t.Fatal(err)
	}
	upload, err := json.Marshal(map[string]any{"path": filepath.Join(root, "pending.txt"), "size": 4, "sha256": strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, endpoint, scope, body string
		status                        int
	}{
		{"GET", "entries?path=" + url.QueryEscape(root), "files:read", "", 200},
		{"GET", "stat?path=" + url.QueryEscape(p), "files:read", "", 200},
		{"GET", "text?path=" + url.QueryEscape(p), "files:read", "", 200},
		{"GET", "download?path=" + url.QueryEscape(p), "files:read", "", 200},
		{"POST", "uploads/start", "files:write", string(upload), 200},
		{"GET", "text?path=" + url.QueryEscape(filepath.Join(root, "missing.txt")), "files:read", "", 404},
	} {
		response := sftpHTTP(t, ts, tc.method, "/api/v1/servers/1/sftp/"+tc.endpoint, tc.scope, strings.NewReader(tc.body))
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != tc.status {
			t.Fatalf("%s: status %d, error %v", tc.endpoint, response.StatusCode, err)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s can cache private file data", tc.endpoint)
		}
		if tc.status != 200 && strings.Contains(string(body), root) {
			t.Fatal("remote path leaked through an error response")
		}
	}
}
