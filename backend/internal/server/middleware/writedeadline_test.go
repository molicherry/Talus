package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type deadlineRecorder struct {
	http.ResponseWriter
	deadline time.Time
	set      bool
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	d.set = true
	return nil
}

// TestWriteDeadlinePerRoute pins the REQ-07 rule: long routes get a large write
// budget, ordinary routes keep the short protection.
func TestWriteDeadlinePerRoute(t *testing.T) {
	cases := []struct {
		name         string
		method, path string
		want         time.Duration
	}{
		{"exec", http.MethodPost, "/api/v1/servers/8/exec", ExecWriteBudget},
		{"relay", http.MethodPost, "/api/v1/services/5/relay", RelayWriteBudget},
		{"server-delete", http.MethodDelete, "/api/v1/servers/8", ServerDeleteWriteBudget},
		{"server-delete-trailing-slash", http.MethodDelete, "/api/v1/servers/8/", ServerDeleteWriteBudget},
		{"server-delete-query", http.MethodDelete, "/api/v1/servers/8?source=list", ServerDeleteWriteBudget},
		{"server-get", http.MethodGet, "/api/v1/servers/8", DefaultWriteBudget},
		{"server-delete-nested", http.MethodDelete, "/api/v1/servers/8/exec", DefaultWriteBudget},
		{"sftp-delete", http.MethodDelete, "/api/v1/servers/8/sftp/entry", 35 * time.Second},
		{"server-delete-missing-id", http.MethodDelete, "/api/v1/servers/", DefaultWriteBudget},
		{"unrelated-delete", http.MethodDelete, "/api/v1/credentials/8", DefaultWriteBudget},
		{"server-list", http.MethodGet, "/api/v1/servers", DefaultWriteBudget},
		{"unrelated-post", http.MethodPost, "/api/v1/credentials", DefaultWriteBudget},
		{"artifact-get", http.MethodGet, "/api/v1/agent/artifacts/digest", DefaultWriteBudget},
		{"artifact-head", http.MethodHead, "/api/v1/agent/artifacts/digest", DefaultWriteBudget},
		{"artifact-unknown-route", http.MethodGet, "/api/v1/agent/artifacts/extra/path", DefaultWriteBudget},
		{"artifact-wrong-method", http.MethodPost, "/api/v1/agent/artifacts/digest", DefaultWriteBudget},
	}
	handler := WriteDeadline(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, tc := range cases {
		rec := &deadlineRecorder{}
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if !rec.set {
			t.Fatalf("%s: no write deadline was set", tc.name)
		}
		got := time.Until(rec.deadline)
		if got > tc.want || got < tc.want-2*time.Second {
			t.Fatalf("%s: budget ~%s, want ~%s", tc.name, got, tc.want)
		}
	}
}

// A slow prepare (20s), commit (10s) and uninstall (20s) can run back to back,
// followed by a separate 5s credential cleanup. The response deadline must still
// leave time for the 204 response after those bounded operations complete.
func TestServerDeleteDeadlineCoversResidentCleanup(t *testing.T) {
	rec := &deadlineRecorder{ResponseWriter: httptest.NewRecorder()}
	handler := WriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 20s prepare + 10s commit + 20s uninstall, leaving room for the 5s
		// credential cleanup that runs inside Remove.
		cleanupFinishedAt := time.Now().Add(50 * time.Second)
		if !rec.set || rec.deadline.Sub(cleanupFinishedAt) < 5*time.Second {
			t.Fatalf("server deletion has insufficient response budget after cleanup: %s", rec.deadline.Sub(cleanupFinishedAt))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/servers/8", nil))
}

// TestWriteDeadlineToleratesUnsupportedWriter ensures a ResponseWriter without
// deadline support does not break the chain.
func TestWriteDeadlineToleratesUnsupportedWriter(t *testing.T) {
	called := false
	handler := WriteDeadline(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/servers/8/exec", nil))
	if !called {
		t.Fatal("handler did not run when the writer does not support deadlines")
	}
}
