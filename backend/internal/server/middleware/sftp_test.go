package middleware

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSFTPScopeMatrixAndBudgets(t *testing.T) {
	for _, tc := range []struct{ method, path, scope string }{{"GET", "text", "files:read"}, {"PUT", "text", "files:write"}, {"GET", "archive", "files:read"}, {"POST", "uploads/start", "files:write"}, {"POST", "uploads/status", "files:write"}, {"PUT", "uploads/chunk", "files:write"}, {"POST", "uploads/complete", "files:write"}, {"DELETE", "uploads", "files:write"}, {"GET", "entries", "files:read"}, {"GET", "stat", "files:read"}, {"GET", "download", "files:read"}, {"POST", "upload", "files:write"}, {"POST", "mkdir", "files:write"}, {"POST", "rename", "files:write"}, {"DELETE", "entry", "files:delete"}} {
		for _, id := range []string{"1", "invalid"} {
			p := "/api/v1/servers/" + id + "/sftp/" + tc.path
			for _, scope := range []string{"servers:read", "servers:write", "servers:exec", "servers:terminal", "files:read", "files:write", "files:delete"} {
				allowed, _ := hasScope(tc.method, p, []string{scope})
				if allowed != (scope == tc.scope) {
					t.Fatalf("%s %s with %s allowed=%t", tc.method, p, scope, allowed)
				}
			}
			if !IsRouteRegistered(normalizePath(tc.method, p)) {
				t.Fatal("unregistered SFTP route")
			}
			budget := writeBudgetFor(httptest.NewRequest(tc.method, p, nil))
			if (tc.path == "upload" || tc.path == "download") && budget < 10*time.Minute {
				t.Fatal("file transfer still has CRUD timeout")
			}
		}
	}
	if bad := ValidateScopes([]string{"files:read", "files:write", "files:delete"}); len(bad) > 0 {
		t.Fatal(bad)
	}
}
