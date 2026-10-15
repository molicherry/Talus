package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestServerIsolatedServiceCatalogAndRelay exercises the deployed Hub entrypoint
// against an owned database and a loopback upstream. It requires no personal
// server, external service, or checked-in deployment evidence.
func TestServerIsolatedServiceCatalogAndRelay(t *testing.T) {
	dsn := startupTestDatabase(t)
	const credential = "isolated-upstream-test-credential"
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/echo" ||
			r.Header.Get("Authorization") != "Bearer "+credential {
			http.Error(w, "unexpected relay request or missing injected credential", http.StatusUnauthorized)
			return
		}
		var body struct {
			Marker     string `json:"marker"`
			Credential string `json:"credential"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
			body.Credential != credential || body.Marker != r.URL.Query().Get("marker") {
			http.Error(w, "relay did not preserve the query and substitute the body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"received": body.Marker})
	}))
	t.Cleanup(upstream.Close)
	hub := startTestServer(t, dsn)

	var setup struct {
		Needed bool `json:"needed"`
	}
	hub.request(t, http.MethodGet, "/api/v1/auth/setup", nil, "", http.StatusOK, &setup)
	if !setup.Needed {
		t.Fatal("the isolated database must require first-user setup")
	}
	var login struct {
		Token string `json:"token"`
	}
	loginBody := map[string]string{"username": "isolated-admin", "password": "isolated-admin-test-password"}
	hub.request(t, http.MethodPost, "/api/v1/auth/login", loginBody, "", http.StatusOK, &login)
	hub.request(t, http.MethodPost, "/api/v1/auth/login", loginBody, "", http.StatusOK, &login)
	if login.Token == "" {
		t.Fatal("bootstrap and login must return a session token")
	}
	jwtHeaders := map[string]string{"Authorization": "Bearer " + login.Token}

	guide := "# Isolated relay guide\nPOST /v1/echo with an Authorization header containing Bearer {{token}}.\n" +
		strings.Repeat("This guide uses synthetic local test data. ", 8)
	const description = "An owned loopback service for isolated acceptance testing."
	created := isolatedHubRequest(t, hub, http.MethodPost, "/api/v1/services", map[string]any{
		"name": "isolated-acceptance", "display_name": "Isolated acceptance",
		"base_url": upstream.URL, "description": description, "usage_guide": guide,
		"credentials":      map[string]string{"token": credential},
		"credential_hints": map[string]string{"token": "Synthetic upstream bearer token"},
	}, jwtHeaders, http.StatusCreated)
	var service struct {
		ID uint `json:"id"`
	}
	decodeIsolatedHubData(t, created, &service)
	if service.ID == 0 {
		t.Fatal("service creation must return a persisted ID")
	}
	assertIsolatedCatalogHasNoSecrets(t, created, credential)

	var apiKey struct {
		Key string `json:"key"`
	}
	keyResponse := isolatedHubRequest(t, hub, http.MethodPost, "/api/v1/api-keys", map[string]any{
		"name": "isolated-service-reader", "scopes": []string{"services:read", "services:relay"},
	}, jwtHeaders, http.StatusCreated)
	decodeIsolatedHubData(t, keyResponse, &apiKey)
	if apiKey.Key == "" {
		t.Fatal("scoped API key creation must return a key")
	}
	keyHeaders := map[string]string{"X-API-Key": apiKey.Key}
	servicePath := fmt.Sprintf("/api/v1/services/%d", service.ID)

	t.Run("scoped key discovers safe catalog and full guide", func(t *testing.T) {
		listResponse := isolatedHubRequest(t, hub, http.MethodGet, "/api/v1/services", nil, keyHeaders, http.StatusOK)
		assertIsolatedCatalogHasNoSecrets(t, listResponse, credential)
		var listed []struct {
			ID                uint   `json:"id"`
			Name              string `json:"name"`
			Description       string `json:"description"`
			UsageGuide        string `json:"usage_guide"`
			UsageGuideExcerpt string `json:"usage_guide_excerpt"`
		}
		decodeIsolatedHubData(t, listResponse, &listed)
		if len(listed) != 1 || listed[0].ID != service.ID || listed[0].Name != "isolated-acceptance" ||
			listed[0].Description != description {
			t.Fatalf("catalog does not identify the created service: %+v", listed)
		}
		if listed[0].UsageGuide != "" || listed[0].UsageGuideExcerpt == "" ||
			listed[0].UsageGuideExcerpt == guide || !strings.HasPrefix(guide, strings.TrimSuffix(listed[0].UsageGuideExcerpt, "…")) {
			t.Fatal("catalog must expose a nonempty abbreviated guide without the full text")
		}
		detailResponse := isolatedHubRequest(t, hub, http.MethodGet, servicePath, nil, keyHeaders, http.StatusOK)
		assertIsolatedCatalogHasNoSecrets(t, detailResponse, credential)
		var detail struct {
			ID         uint   `json:"id"`
			UsageGuide string `json:"usage_guide"`
		}
		decodeIsolatedHubData(t, detailResponse, &detail)
		if detail.ID != service.ID || detail.UsageGuide != guide {
			t.Fatal("service detail must return the exact full usage guide")
		}
		isolatedHubRequest(t, hub, http.MethodGet, servicePath+"/credentials", nil, keyHeaders, http.StatusForbidden)
	})

	for _, caller := range []struct {
		name    string
		headers map[string]string
	}{{"jwt", jwtHeaders}, {"scoped key", keyHeaders}} {
		for _, mode := range []string{"standard", "bounded_stream"} {
			t.Run(caller.name+" relay "+mode, func(t *testing.T) {
				marker := strings.ReplaceAll(caller.name, " ", "-") + "-" + mode
				result := isolatedHubRequest(t, hub, http.MethodPost, servicePath+"/relay", map[string]any{
					"method": http.MethodPost, "path": "/v1/echo?marker=" + marker, "mode": mode,
					"headers": map[string]string{"Authorization": "Bearer {{token}}", "Content-Type": "application/json"},
					"body":    map[string]string{"marker": marker, "credential": "{{token}}"},
				}, caller.headers, http.StatusOK)
				// Relay responses are the upstream payload, without the Hub envelope.
				var response struct {
					Received string `json:"received"`
				}
				if err := json.Unmarshal(result, &response); err != nil || response.Received != marker {
					t.Fatalf("relay did not return the verified upstream result: %q (%v)", result, err)
				}
			})
		}
	}
	if calls := upstreamCalls.Load(); calls != 4 {
		t.Fatalf("expected four verified upstream requests, got %d", calls)
	}
	t.Run("invalid requests never reach the upstream", func(t *testing.T) {
		isolatedHubRequest(t, hub, http.MethodPost, servicePath+"/relay", map[string]string{
			"method": http.MethodPost, "mode": "unlimited",
		}, keyHeaders, http.StatusBadRequest)
		isolatedHubRequest(t, hub, http.MethodPost, servicePath+"/relay", map[string]string{
			"path": "/v1/echo", "mode": "standard",
		}, keyHeaders, http.StatusBadRequest)
		isolatedHubRequest(t, hub, http.MethodGet, "/api/v1/services", nil,
			map[string]string{"X-API-Key": "invalid-isolated-test-key"}, http.StatusUnauthorized)
		isolatedHubRequest(t, hub, http.MethodPost, servicePath+"/relay", map[string]string{
			"method": http.MethodPost, "path": "/v1/echo",
		}, map[string]string{"X-API-Key": "invalid-isolated-test-key"}, http.StatusUnauthorized)
		if calls := upstreamCalls.Load(); calls != 4 {
			t.Fatalf("rejected requests reached the upstream: %d total calls", calls)
		}
	})
}

func isolatedHubRequest(t *testing.T, hub *startupServer, method, path string, body any, headers map[string]string, want int) []byte {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, hub.baseURL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := hub.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, resp.StatusCode, want, result)
	}
	return result
}

func decodeIsolatedHubData(t *testing.T, response []byte, data any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatalf("decode Hub response: %v", err)
	}
	if err := json.Unmarshal(envelope.Data, data); err != nil {
		t.Fatalf("decode Hub response data: %v", err)
	}
}

func assertIsolatedCatalogHasNoSecrets(t *testing.T, response []byte, credential string) {
	t.Helper()
	for _, forbidden := range []string{credential, `"credentials"`, `"encrypted_credentials"`, `"salt"`} {
		if bytes.Contains(response, []byte(forbidden)) {
			t.Fatal("service metadata exposed a secret value or private storage field")
		}
	}
}
