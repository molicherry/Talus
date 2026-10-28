package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// A healthy HTTP listener must not leave an owner guessing why an empty
// installation rejects setup. Removing the proof after setup stays supported.
func TestServerDiagnosesDisabledBootstrap(t *testing.T) {
	dsn := startupTestDatabase(t)
	disabled := startTestServer(t, dsn, map[string]string{"BOOTSTRAP_TOKEN": ""})
	var status struct {
		Needed  bool `json:"needed"`
		Enabled bool `json:"bootstrap_enabled"`
	}
	disabled.request(t, http.MethodGet, "/api/v1/auth/setup", nil, "", http.StatusOK, &status)
	if !status.Needed || status.Enabled {
		t.Fatalf("empty installation must disclose disabled setup: %+v", status)
	}
	disabled.request(t, http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"token":    "unconfigured-bootstrap-proof-0123456789",
		"username": "owner", "password": "dummy-owner-password",
	}, "", http.StatusForbidden, nil)
	disabled.stop(t)
	logs, err := os.ReadFile(disabled.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), "bootstrap_token_missing") || !strings.Contains(string(logs), "BOOTSTRAP_TOKEN is not configured") {
		t.Fatalf("missing actionable setup warning: %s", logs)
	}
	if strings.Contains(string(logs), "unconfigured-bootstrap-proof-0123456789") {
		t.Fatal("setup proof was included in startup/request logs")
	}

	enabled := startTestServer(t, dsn)
	enabled.request(t, http.MethodGet, "/api/v1/auth/setup", nil, "", http.StatusOK, &status)
	if !status.Needed || !status.Enabled {
		t.Fatalf("configured setup must be available: %+v", status)
	}
	enabled.request(t, http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"token":    "isolated-startup-bootstrap-secret-32",
		"username": "owner", "password": "dummy-owner-password",
	}, "", http.StatusOK, nil)
	enabled.stop(t)

	initialized := startTestServer(t, dsn, map[string]string{"BOOTSTRAP_TOKEN": ""})
	initialized.request(t, http.MethodGet, "/api/v1/auth/setup", nil, "", http.StatusOK, &status)
	if status.Needed || status.Enabled {
		t.Fatalf("initialized installation must not require a proof: %+v", status)
	}
	initialized.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "owner", "password": "dummy-owner-password",
	}, "", http.StatusOK, nil)
	initialized.stop(t)
	logs, err = os.ReadFile(initialized.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logs), "bootstrap_token_missing") {
		t.Fatalf("initialized installation emitted an irrelevant setup warning: %s", logs)
	}
}
