package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func failedSettingsStartup(t *testing.T, dsn string, env map[string]string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestServerProcessHelper$", "-test.count=1")
	cmd.Env = startupTestEnv(startupServerEnv(dsn, freePort(t), env))
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil {
		t.Fatalf("expected prompt startup rejection: %v %s", err, out)
	}
	return string(out)
}

func TestDeploymentInvalidBeforeDatabaseOrArtifacts(t *testing.T) {
	for _, env := range []map[string]string{{"MONITOR_TOLERATED_ROUNDS": "-1"}, {"SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES": "invalid"}} {
		env["AGENT_ARTIFACTS_DIR"] = t.TempDir()
		logs := failedSettingsStartup(t, "postgres://unused:none@127.0.0.1:1/unused", env)
		if !strings.Contains(logs, "invalid deployment configuration") || strings.Contains(logs, "agent artifacts are invalid") || strings.Contains(logs, "failed to connect") {
			t.Fatalf("deployment-only validation was deferred: %s", logs)
		}
	}
}

func TestSettingsInvalidOverrideRecoveryAndFieldError(t *testing.T) {
	dsn := startupTestDatabase(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE TABLE system_settings (id bigint PRIMARY KEY, revision bigint NOT NULL, overrides jsonb NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO system_settings VALUES (1, 7, '{"MONITOR_INTERVAL":"SECRET_BAD_VALUE","LOG_LEVEL":"debug"}')`); err != nil {
		t.Fatal(err)
	}
	logs := failedSettingsStartup(t, dsn, map[string]string{"AGENT_ARTIFACTS_DIR": agentArtifactsDir(t), "SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES": "false"})
	if !strings.Contains(logs, "revision=7") || !strings.Contains(logs, "MONITOR_INTERVAL") || strings.Contains(logs, "SECRET_BAD_VALUE") {
		t.Fatalf("unsafe/missing recovery diagnostics: %s", logs)
	}
	hub := startTestServer(t, dsn, map[string]string{"SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES": "true"})
	var login struct {
		Token string `json:"token"`
	}
	hub.request(t, "POST", "/api/v1/auth/setup", map[string]string{"token": "isolated-startup-bootstrap-secret-32", "username": "recovery-admin", "password": "recovery-test-password"}, "", http.StatusOK, &login)
	var view struct {
		settingsResponse
		Recovery *struct {
			Revision uint64
			Key      string
		} `json:"recovery"`
	}
	hub.request(t, "GET", "/api/v1/settings", nil, login.Token, http.StatusOK, &view)
	if view.Recovery == nil || view.Recovery.Revision != 7 || view.Recovery.Key != "MONITOR_INTERVAL" {
		t.Fatal("recovery was not disclosed")
	}
	// The API must identify a cross-field failure without echoing raw values.
	req, err := http.NewRequest("PUT", hub.baseURL+"/api/v1/settings", strings.NewReader(`{"revision":7,"values":{"EXEC_TIMEOUT_MAX":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+login.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hub.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var failure struct {
		Error struct {
			Reason string
			Params struct{ Key string }
		}
	}
	err = json.NewDecoder(resp.Body).Decode(&failure)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 422 || failure.Error.Reason != "settings_invalid" || failure.Error.Params.Key != "EXEC_TIMEOUT_MAX" {
		t.Fatalf("field error missing: %+v, %v", failure, err)
	}
	hub.request(t, "PUT", "/api/v1/settings", map[string]any{"revision": 7, "values": map[string]any{"MONITOR_INTERVAL": 10}}, login.Token, http.StatusOK, nil)
	hub.stop(t)
	restarted := startTestServer(t, dsn, map[string]string{"SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES": "false"})
	view.Recovery = nil
	restarted.request(t, "GET", "/api/v1/settings", nil, login.Token, http.StatusOK, &view)
	if view.Revision != 8 || view.Recovery != nil {
		t.Fatal("repaired configuration did not survive a normal restart")
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='settings.update'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("repair not audited: %d %v", count, err)
	}
}
