package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vpsmanager/backend/internal/model"
)

func TestServerAPIKeyPermissionsUpgrade(t *testing.T) {
	dsn := startupTestDatabase(t)
	first := startTestServer(t, dsn)
	var login struct {
		Token string `json:"token"`
	}
	first.request(t, http.MethodPost, "/api/v1/auth/setup", map[string]string{"token": "isolated-startup-bootstrap-secret-32", "username": "key-admin", "password": "synthetic-test-password"}, "", http.StatusOK, &login)
	var created struct {
		Key    string       `json:"key"`
		APIKey model.APIKey `json:"api_key"`
	}
	first.request(t, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "legacy", "scopes": []string{"servers:read", "services:read"}}, login.Token, http.StatusCreated, &created)
	first.stop(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// Simulate a pre-edit, hash-only key and lost scope-backfill bookkeeping.
	for _, sql := range []string{
		`UPDATE api_keys SET encrypted_raw_key='', salt=NULL`,
		`ALTER TABLE api_keys DROP COLUMN permissions_revision, DROP COLUMN permissions_updated_at`,
		`DELETE FROM schema_migrations WHERE id LIKE '%services-read%' OR id LIKE '%api-key-permissions%'`,
		`INSERT INTO users (username,password_hash,role,token_version,created_at,updated_at) SELECT 'key-operator',password_hash,'operator',0,now(),now() FROM users WHERE username='key-admin'`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	second := startTestServer(t, dsn)
	var keys []model.APIKey
	second.request(t, http.MethodGet, "/api/v1/api-keys", nil, login.Token, http.StatusOK, &keys)
	if len(keys) != 1 || keys[0].PermissionsRevision != 1 || !reflect.DeepEqual(keys[0].Scopes, created.APIKey.Scopes) {
		t.Fatal("legacy metadata changed")
	}
	path := fmt.Sprintf("/api/v1/api-keys/%d", created.APIKey.ID)
	second.request(t, http.MethodGet, path+"/reveal", nil, login.Token, http.StatusNotFound, nil)
	var updated model.APIKey
	second.request(t, http.MethodPatch, path, map[string]any{"expected_revision": "1", "scopes": []string{"servers:read"}}, login.Token, http.StatusOK, &updated)
	if updated.PermissionsRevision != 2 || updated.KeyPrefix != created.APIKey.KeyPrefix {
		t.Fatal("revision/prefix contract failed")
	}
	second.request(t, http.MethodPatch, path, map[string]any{"expected_revision": "1", "scopes": []string{"services:read"}}, login.Token, http.StatusConflict, nil)
	second.request(t, http.MethodPatch, path, map[string]any{"expected_revision": "2", "scopes": []string{}}, login.Token, http.StatusBadRequest, nil)
	var operator struct {
		Token string `json:"token"`
	}
	second.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "key-operator", "password": "synthetic-test-password"}, "", http.StatusOK, &operator)
	second.request(t, http.MethodGet, "/api/v1/api-keys", nil, operator.Token, http.StatusForbidden, nil)
	second.request(t, http.MethodPatch, path, map[string]any{"expected_revision": "2", "scopes": []string{"services:read"}}, operator.Token, http.StatusForbidden, nil)
	checkKey := func(base string, client *http.Client, method, endpoint string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"expected_revision": "2", "scopes": []string{"services:read"}})
		req, _ := http.NewRequest(method, base+endpoint, bytes.NewReader(body))
		req.Header.Set("X-API-Key", created.Key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status %d want %d", method, endpoint, resp.StatusCode, want)
		}
	}
	checkKey(second.baseURL, second.client, http.MethodGet, "/api/v1/servers", http.StatusOK)
	checkKey(second.baseURL, second.client, http.MethodGet, "/api/v1/services", http.StatusForbidden)
	checkKey(second.baseURL, second.client, http.MethodPatch, path, http.StatusForbidden)
	second.stop(t)
	third := startTestServer(t, dsn)
	checkKey(third.baseURL, third.client, http.MethodGet, "/api/v1/servers", http.StatusOK)
	checkKey(third.baseURL, third.client, http.MethodGet, "/api/v1/services", http.StatusForbidden)
	third.request(t, http.MethodGet, "/api/v1/api-keys", nil, login.Token, http.StatusOK, &keys)
	if keys[0].PermissionsRevision != 2 || !reflect.DeepEqual(keys[0].Scopes, []string{"servers:read"}) {
		t.Fatal("restart restored revoked scope")
	}
	third.request(t, http.MethodPost, path+"/rotate", map[string]string{"expected_revision": "2"}, operator.Token, http.StatusForbidden, nil)
	checkKey(third.baseURL, third.client, http.MethodPost, path+"/rotate", http.StatusForbidden)
	var rotated struct {
		Key    string       `json:"key"`
		APIKey model.APIKey `json:"api_key"`
	}
	third.request(t, http.MethodPost, path+"/rotate", map[string]string{"expected_revision": "2"}, login.Token, http.StatusOK, &rotated)
	if rotated.Key == created.Key || rotated.APIKey.ID != created.APIKey.ID || rotated.APIKey.PermissionsRevision != 3 || !reflect.DeepEqual(rotated.APIKey.Scopes, keys[0].Scopes) {
		t.Fatal("rotation identity/policy contract failed")
	}
	checkKey(third.baseURL, third.client, http.MethodGet, "/api/v1/servers", http.StatusUnauthorized)
	created.Key = rotated.Key
	checkKey(third.baseURL, third.client, http.MethodGet, "/api/v1/servers", http.StatusOK)
	checkKey(third.baseURL, third.client, http.MethodGet, "/api/v1/services", http.StatusForbidden)
	third.request(t, http.MethodPost, path+"/rotate", map[string]string{"expected_revision": "2"}, login.Token, http.StatusConflict, nil)
	var revealed string
	third.request(t, http.MethodGet, path+"/reveal", nil, login.Token, http.StatusOK, &revealed)
	if revealed != rotated.Key {
		t.Fatal("copy returned an old key")
	}
	third.stop(t)
	fourth := startTestServer(t, dsn)
	checkKey(fourth.baseURL, fourth.client, http.MethodGet, "/api/v1/servers", http.StatusOK)
	fourth.stop(t)
	var audits int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='api_key.permissions.update'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit count %d: %v", audits, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='api_key.rotate'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("rotation audit count %d: %v", audits, err)
	}
}
