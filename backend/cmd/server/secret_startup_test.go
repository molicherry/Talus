package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
)

func TestServerAutomaticallyUpgradesEncryptedData(t *testing.T) {
	dsn := startupTestDatabase(t)
	first := startTestServer(t, dsn)
	var login struct {
		Token string `json:"token"`
	}
	first.request(t, http.MethodPost, "/api/v1/auth/setup", map[string]string{"token": "isolated-startup-bootstrap-secret-32", "username": "upgrade-admin", "password": "upgrade-password"}, "", http.StatusOK, &login)
	var credential, service struct {
		ID uint `json:"id"`
	}
	first.request(t, http.MethodPost, "/api/v1/credentials", map[string]string{"name": "original-credential", "auth_type": "password", "username": "root", "password": "original-ssh-password"}, login.Token, http.StatusCreated, &credential)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer original-service-token" {
			http.Error(w, "bad credential", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("relay-after-upgrade"))
	}))
	defer upstream.Close()
	first.request(t, http.MethodPost, "/api/v1/services", map[string]any{"name": "original-service", "base_url": upstream.URL, "credentials": map[string]string{"token": "original-service-token"}}, login.Token, http.StatusCreated, &service)
	var api struct {
		Key    string `json:"key"`
		APIKey struct {
			ID uint `json:"id"`
		} `json:"api_key"`
	}
	first.request(t, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "original-api-key"}, login.Token, http.StatusCreated, &api)
	first.stop(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	master, _ := crypto.NewMasterKey(strings.Repeat("0", 64))
	// Seed the original, pre-version-column formats in an owned database.
	sshCipher, _ := crypto.Encrypt([]byte("original-ssh-password"), master.LegacyDeriveKey())
	if _, err := conn.Exec(ctx, "UPDATE ssh_credentials SET encrypted_password=$1,salt=NULL WHERE id=$2", sshCipher, credential.ID); err != nil {
		t.Fatal(err)
	}
	salt := []byte("0123456789abcdef")
	oldKey := master.DeriveKey(salt)
	tokenCipher, _ := crypto.Encrypt([]byte("original-service-token"), oldKey)
	secrets, _ := json.Marshal(map[string]string{"token": tokenCipher})
	if _, err := conn.Exec(ctx, "UPDATE services SET encrypted_credentials=$1::jsonb,salt=$2 WHERE id=$3", string(secrets), salt, service.ID); err != nil {
		t.Fatal(err)
	}
	apiCipher, _ := crypto.Encrypt([]byte(api.Key), oldKey)
	if _, err := conn.Exec(ctx, "UPDATE api_keys SET encrypted_raw_key=$1,salt=$2 WHERE id=$3", apiCipher, salt, api.APIKey.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"ssh_credentials", "services", "server_connection_configs", "api_keys"} {
		if _, err := conn.Exec(ctx, "ALTER TABLE "+table+" DROP COLUMN crypto_version"); err != nil {
			t.Fatal(err)
		}
	}
	second := startTestServer(t, dsn)
	var revealed struct {
		Password string `json:"password"`
	}
	second.request(t, http.MethodGet, fmt.Sprintf("/api/v1/credentials/%d/reveal", credential.ID), nil, login.Token, http.StatusOK, &revealed)
	if revealed.Password != "original-ssh-password" {
		t.Fatal("SSH credential changed during upgrade")
	}
	var raw string
	second.request(t, http.MethodGet, fmt.Sprintf("/api/v1/api-keys/%d/reveal", api.APIKey.ID), nil, login.Token, http.StatusOK, &raw)
	if raw != api.Key {
		t.Fatal("API key raw value changed during upgrade")
	}
	req, _ := http.NewRequest(http.MethodGet, second.baseURL+"/api/v1/servers", nil)
	req.Header.Set("X-API-Key", api.Key)
	resp, err := second.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("existing API key no longer authenticates")
	}
	second.request(t, http.MethodPost, fmt.Sprintf("/api/v1/services/%d/relay", service.ID), map[string]any{"method": "GET", "path": "/", "headers": map[string]string{"Authorization": "Bearer {{token}}"}}, login.Token, http.StatusOK, nil)
	second.stop(t)
	var upgraded string
	var version int
	if err := conn.QueryRow(ctx, "SELECT encrypted_password,crypto_version FROM ssh_credentials WHERE id=$1", credential.ID).Scan(&upgraded, &version); err != nil {
		t.Fatal(err)
	}
	if version != crypto.VersionCurrent || upgraded == sshCipher {
		t.Fatal("startup did not upgrade old ciphertext")
	}
	// A bad v2 row must stop the actual process before any HTTP port opens.
	if _, err := conn.Exec(ctx, "UPDATE ssh_credentials SET encrypted_password=$1 WHERE id=$2", "private-bad-ciphertext-marker", credential.ID); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	logPath := filepath.Join(t.TempDir(), "failed-upgrade.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	cmd := exec.Command(executable, "-test.run=^TestServerProcessHelper$", "-test.count=1", "-test.timeout=1m")
	cmd.Env = startupTestEnv(startupServerEnv(dsn, port, map[string]string{"AGENT_ARTIFACTS_DIR": agentArtifactsDir(t)}))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	logFile.Close()
	defer func() { _ = cmd.Process.Kill() }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			t.Fatal("corrupt encrypted data did not fail startup")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("failed migration did not stop startup")
	}
	logs, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logs), "credential migration failed; startup stopped") || strings.Contains(string(logs), "private-bad-ciphertext-marker") {
		t.Fatal("migration failure was not reported safely")
	}
	if socket, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 300*time.Millisecond); err == nil {
		socket.Close()
		t.Fatal("HTTP started despite a failed migration")
	}
	if _, err := conn.Exec(ctx, "UPDATE ssh_credentials SET encrypted_password=$1 WHERE id=$2", upgraded, credential.ID); err != nil {
		t.Fatal(err)
	}
	third := startTestServer(t, dsn)
	third.request(t, http.MethodGet, fmt.Sprintf("/api/v1/credentials/%d/reveal", credential.ID), nil, login.Token, http.StatusOK, &revealed)
	third.stop(t)
}
