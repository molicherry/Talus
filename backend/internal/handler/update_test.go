package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/update"
)

type updateSnapshotFunc func() update.Status

func (f updateSnapshotFunc) Snapshot() update.Status { return f() }

func TestUpdateHandlerPrivateRevalidation(t *testing.T) {
	checked := time.Date(2026, 10, 22, 12, 0, 0, 123456789, time.UTC)
	status := update.Status{
		CurrentVersion: "v0.12.0", UpdateAvailable: true, CheckedAt: &checked,
		LatestRelease: &update.Release{Version: "v0.13.0", PublishedAt: checked, Notes: "Release notes", URL: "https://github.com/molicherry/Talus/releases/tag/v0.13.0"},
	}
	handler := NewUpdateHandler(updateSnapshotFunc(func() update.Status { return status }))
	get := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/updates", nil)
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}
	first := get("")
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "private, no-cache" || first.Header().Get("Vary") != "Authorization" {
		t.Fatalf("unexpected private cache response: %d %v", first.Code, first.Header())
	}
	var decoded struct {
		Data update.Status `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &decoded); err != nil || !reflect.DeepEqual(decoded.Data, status) {
		t.Fatalf("JSON envelope changed: %s; error=%v", first.Body.String(), err)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	for _, conditional := range []string{etag, `"old", W/` + etag, "*"} {
		cached := get(conditional)
		if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
			t.Fatalf("unchanged data downloaded again for %s: %d", conditional, cached.Code)
		}
	}
	status.LatestRelease.Notes = "Revised upgrade instructions"
	changed := get(etag)
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Fatal("changed notes must invalidate the cache")
	}
	status.UpdateAvailable = false
	status.LatestRelease = nil
	cleared := get(changed.Header().Get("ETag"))
	if cleared.Code != http.StatusOK || cleared.Header().Get("ETag") == changed.Header().Get("ETag") {
		t.Fatal("disappearing update must invalidate the cache")
	}
}
