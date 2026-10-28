package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vpsmanager/backend/internal/pkg/token"
)

type settingsResponse struct {
	Revision uint64 `json:"revision"`
	Settings []struct {
		Key       string `json:"key"`
		Value     any    `json:"value"`
		Effective any    `json:"effective_value"`
		Source    string `json:"source"`
		Restart   bool   `json:"restart_required"`
	} `json:"settings"`
}

func TestSettingsAPIAdminPersistenceAndRestart(t *testing.T) {
	dsn := startupTestDatabase(t)
	first := startTestServer(t, dsn, map[string]string{"AGENT_HUB_URL": "", "LOG_LEVEL": "warn"})
	first.request(t, "GET", "/api/v1/settings", nil, "", http.StatusUnauthorized, nil)
	var login struct {
		Token string `json:"token"`
	}
	first.request(t, "POST", "/api/v1/auth/setup", map[string]string{"token": "isolated-startup-bootstrap-secret-32", "username": "settings-admin", "password": "settings-test-password"}, "", http.StatusOK, &login)
	var view settingsResponse
	first.request(t, "GET", "/api/v1/settings", nil, login.Token, http.StatusOK, &view)
	if len(view.Settings) != 19 {
		t.Fatalf("want 19 public settings, got %d", len(view.Settings))
	}
	for _, v := range view.Settings {
		if strings.Contains(v.Key, "SECRET") || v.Key == "DATABASE_URL" {
			t.Fatalf("deployment secret exposed: %s", v.Key)
		}
	}
	viewer, err := token.NewJWTService("isolated-startup-regression-secret", time.Hour).GenerateToken(1, "settings-admin", "viewer", 0)
	if err != nil {
		t.Fatal(err)
	}
	first.request(t, "GET", "/api/v1/settings", nil, viewer, http.StatusForbidden, nil)
	first.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": view.Revision, "values": map[string]any{"LOG_LEVEL": "error"}}, viewer, http.StatusForbidden, nil)
	var key struct {
		Key string `json:"key"`
	}
	first.request(t, "POST", "/api/v1/api-keys", map[string]any{"name": "settings-denied", "scopes": []string{"servers:read"}}, login.Token, http.StatusCreated, &key)
	for _, method := range []string{"GET", "PUT"} {
		req, err := http.NewRequest(method, first.baseURL+"/api/v1/settings", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-API-Key", key.Key)
		res, err := first.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("API key %s allowed: %d", method, res.StatusCode)
		}
	}
	first.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": view.Revision, "values": map[string]any{"JWT_SECRET": "forbidden"}}, login.Token, http.StatusUnprocessableEntity, nil)
	first.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": view.Revision, "values": map[string]any{"USAGE_LOG_RETENTION_DAYS": 1}}, login.Token, http.StatusConflict, nil)
	initial := view.Revision
	first.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": view.Revision, "values": map[string]any{"AGENT_HUB_URL": "https://settings.example", "MONITOR_INTERVAL": 10, "LOG_LEVEL": "error", "DB_MAX_OPEN_CONNECTIONS": 48}}, login.Token, http.StatusOK, &view)
	for _, v := range view.Settings {
		if v.Key == "DB_MAX_OPEN_CONNECTIONS" && (!v.Restart || v.Value != float64(48) || v.Effective != float64(16)) {
			t.Fatalf("capacity became live: %+v", v)
		}
	}
	first.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": initial, "values": map[string]any{"LOG_LEVEL": "debug"}}, login.Token, http.StatusConflict, nil)
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var audits int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='settings.update' AND details::jsonb->'changes' ? 'AGENT_HUB_URL'").Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit missing: count=%d err=%v", audits, err)
	}
	first.stop(t)
	second := startTestServer(t, dsn, map[string]string{"AGENT_HUB_URL": "https://deployment.example", "LOG_LEVEL": "debug"})
	second.request(t, "GET", "/api/v1/settings", nil, login.Token, http.StatusOK, &view)
	for _, v := range view.Settings {
		if v.Key == "DB_MAX_OPEN_CONNECTIONS" && (v.Restart || v.Effective != float64(48)) {
			t.Fatalf("restart did not apply capacity: %+v", v)
		}
		if v.Key == "AGENT_HUB_URL" && (v.Value != "https://settings.example" || v.Source != "database") {
			t.Fatalf("environment replaced override: %+v", v)
		}
	}
	second.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": view.Revision, "values": map[string]any{"AGENT_HUB_URL": nil, "LOG_LEVEL": nil}}, login.Token, http.StatusOK, &view)
	for _, v := range view.Settings {
		if v.Key == "AGENT_HUB_URL" && (v.Value != "https://deployment.example" || v.Source != "environment") {
			t.Fatalf("reset failed: %+v", v)
		}
	}
	// Explicit false/empty must survive storage; neither means reset.
	second.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": view.Revision, "values": map[string]any{"AGENT_HUB_URL": "", "AGENT_ALLOW_HTTP": false}}, login.Token, http.StatusOK, &view)
	var raw []byte
	if err := conn.QueryRow(context.Background(), "SELECT overrides FROM system_settings WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["AGENT_HUB_URL"] != "" || saved["AGENT_ALLOW_HTTP"] != false {
		t.Fatalf("explicit values lost: %s", raw)
	}
	second.stop(t)
}
