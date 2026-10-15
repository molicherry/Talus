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
		{"server-list", http.MethodGet, "/api/v1/servers", DefaultWriteBudget},
		{"unrelated-post", http.MethodPost, "/api/v1/credentials", DefaultWriteBudget},
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
