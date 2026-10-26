package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/agentproto/agentprototest"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server/middleware"
)

func TestAgentArtifactHTTPAuthorizationRangesAndVersionPinning(t *testing.T) {
	m, _ := monitorFixture()
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "pull-test")
	m.deployer = NewAgentDeploymentService(nil, dir)
	manifest, err := m.deployer.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	a := manifest.Agents[0]
	data, err := os.ReadFile(filepath.Join(dir, a.File))
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.WriteDeadline)
	router.Get("/api/v1/agent/artifacts/{sha256}", m.AgentArtifact)
	router.Head("/api/v1/agent/artifacts/{sha256}", m.AgentArtifact)
	for _, tc := range []struct {
		name, method, token, digest, byteRange, ifRange string
		status                                          int
	}{
		{"anonymous", http.MethodGet, "", a.SHA256, "", "", 401},
		{"wrong-credential", http.MethodGet, strings.Repeat("b", 64), a.SHA256, "", "", 401},
		{"missing-version", http.MethodGet, strings.Repeat("a", 64), strings.Repeat("0", 64), "", "", 404},
		{"full", http.MethodGet, strings.Repeat("a", 64), a.SHA256, "", "", 200},
		{"head", http.MethodHead, strings.Repeat("a", 64), a.SHA256, "", "", 200},
		{"resume", http.MethodGet, strings.Repeat("a", 64), a.SHA256, "bytes=7-", `"` + a.SHA256 + `"`, 206},
		{"stale-validator", http.MethodGet, strings.Repeat("a", 64), a.SHA256, "bytes=7-", `"old"`, 200},
		{"past-end", http.MethodGet, strings.Repeat("a", 64), a.SHA256, "bytes=" + strconv.Itoa(len(data)) + "-", `"` + a.SHA256 + `"`, 416},
		{"multiple-ranges", http.MethodGet, strings.Repeat("a", 64), a.SHA256, "bytes=0-1,3-4", "", 416},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/v1/agent/artifacts/"+tc.digest, nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			r.Header.Set("Range", tc.byteRange)
			r.Header.Set("If-Range", tc.ifRange)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status == 200 || tc.status == 206 {
				if w.Header().Get("ETag") != `"`+a.SHA256+`"` {
					t.Fatal("missing immutable artifact validator")
				}
				want := data
				if tc.status == 206 {
					want = data[7:]
					if w.Header().Get("Content-Range") != "bytes 7-"+strconv.Itoa(len(data)-1)+"/"+strconv.Itoa(len(data)) {
						t.Fatal("wrong resumed byte interval")
					}
				}
				if tc.method == http.MethodHead {
					want = nil
				}
				if !bytes.Equal(w.Body.Bytes(), want) {
					t.Fatal("artifact bytes changed")
				}
			}
		})
	}
	if len(m.nodes) != 0 {
		t.Fatal("artifact downloads fabricated agent presence")
	}
	if err := os.WriteFile(filepath.Join(dir, a.File), bytes.Repeat([]byte{0}, len(data)), 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agent/artifacts/"+a.SHA256, nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("served bytes which no longer match the manifest digest")
	}
}

type artifactDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *artifactDeadlineRecorder) SetWriteDeadline(at time.Time) error {
	w.deadlines = append(w.deadlines, at)
	return nil
}

func TestAgentArtifactHTTPDeadlinesAndCapacity(t *testing.T) {
	m, _ := monitorFixture()
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "deadline-test")
	m.deployer = NewAgentDeploymentService(nil, dir)
	manifest, err := m.deployer.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/agent/artifacts/" + manifest.Agents[0].SHA256
	router := chi.NewRouter()
	router.Use(middleware.WriteDeadline)
	router.Get("/api/v1/agent/artifacts/{sha256}", m.AgentArtifact)
	router.Head("/api/v1/agent/artifacts/{sha256}", m.AgentArtifact)
	for _, tc := range []struct {
		name, method, path string
		auth, busy         bool
		status             int
	}{
		{"anonymous", "GET", path, false, false, 401},
		{"unknown-digest", "GET", "/api/v1/agent/artifacts/unknown", true, false, 404},
		{"unmatched-route", "GET", path + "/extra", true, false, 404},
		{"wrong-method", "POST", path, true, false, 405},
		{"busy", "GET", path, true, true, 503},
		{"stream", "GET", path, true, false, 200},
		{"head", "HEAD", path, true, false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.busy {
				for i := 0; i < cap(m.artifactSlots); i++ {
					m.artifactSlots <- struct{}{}
				}
				defer func() {
					for len(m.artifactSlots) != 0 {
						<-m.artifactSlots
					}
				}()
			}
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.auth {
				r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
			}
			w := &artifactDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			router.ServeHTTP(w, r)
			if w.Code != tc.status || len(w.deadlines) == 0 {
				t.Fatalf("unprotected request or wrong response: status=%d deadlines=%v", w.Code, w.deadlines)
			}
			if d := time.Until(w.deadlines[0]); d < 14*time.Second || d > 15*time.Second {
				t.Fatalf("ordinary request lost its default write budget: %v", d)
			}
			if tc.status == 200 {
				if d := time.Until(w.deadlines[len(w.deadlines)-1]); len(w.deadlines) < 2 || d < 29*time.Second || d > 30*time.Second {
					t.Fatalf("artifact stream did not replace the short deadline: %v", w.deadlines)
				}
			} else if len(w.deadlines) != 1 {
				t.Fatal("failed request extended the default write budget")
			}
			if tc.busy && w.Header().Get("Retry-After") != "30" {
				t.Fatal("capacity response omitted its retry interval")
			}
		})
	}
}

func TestAgentArtifactHTTPRevokesReplacedAndDeletedNodes(t *testing.T) {
	svc, servers, db, ctx, _ := connectionServiceFixture(t)
	srv, err := svc.Create(ctx, &model.Server{Name: "artifact-node", Host: "old.example", Port: 22, OwnerID: middleware.GetUserClaims(ctx).UserID})
	if err != nil {
		t.Fatal(err)
	}
	agents, node := serverDeleteAgent(t, db, ctx, srv)
	oldToken, err := agents.Secret(node)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	agentprototest.WriteArtifacts(t, dir, "node-auth")
	m := NewResidentMonitor(agents, servers, NewAgentDeploymentService(nil, dir), "https://hub.example", false, 5)
	manifest, err := m.deployer.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Get("/api/v1/agent/artifacts/{sha256}", m.AgentArtifact)
	request := func(token string) int {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/agent/artifacts/"+manifest.Agents[0].SHA256, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(oldToken); code != 200 {
		t.Fatalf("live node denied: %d", code)
	}
	if err := db.Model(srv).Update("host", "replacement.example").Error; err != nil {
		t.Fatal(err)
	}
	newNode, err := agents.Ensure(context.Background(), *srv)
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := agents.Secret(newNode)
	if err != nil {
		t.Fatal(err)
	}
	if code := request(oldToken); code != 401 {
		t.Fatalf("replaced node retained download access: %d", code)
	}
	if code := request(newToken); code != 200 {
		t.Fatalf("replacement node denied: %d", code)
	}
	if err := db.Delete(srv).Error; err != nil {
		t.Fatal(err)
	}
	if code := request(newToken); code != 401 {
		t.Fatalf("deleted node retained download access: %d", code)
	}
}
