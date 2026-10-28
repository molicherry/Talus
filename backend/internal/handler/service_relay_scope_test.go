package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"gorm.io/gorm"
)

func relayScopedKeyValidator(allowedServer uint) mw.APIKeyValidatorFunc {
	return func(_ context.Context, raw string) (uint, string, string, []string, []uint, error) {
		if raw != "dummy-relay-key" {
			return 0, "", "", nil, nil, fmt.Errorf("invalid fixture key")
		}
		return 7, "owner", "admin", []string{"services:relay"}, []uint{allowedServer}, nil
	}
}

// Pause after the handler has read A, replace every authorization-relevant
// field with B, then resume. The outbound request must retain A's authorized
// configuration, including its credential, rather than reloading B by ID.
func TestRelayHandlerUsesAuthorizedSnapshotDuringRebind(t *testing.T) {
	db := usageFlowDB(t)
	if err := db.AutoMigrate(&model.Service{}); err != nil {
		t.Fatal(err)
	}
	var callsA, callsB atomic.Int32
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		if r.Header.Get("X-Upstream-Token") != "dummy-a" {
			http.Error(w, "incorrect fixture credential", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "authorized-a")
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callsB.Add(1)
		_, _ = io.WriteString(w, "forbidden-b")
	}))
	defer upstreamB.Close()
	master, err := crypto.NewMasterKey(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewServiceRelayService(repository.NewServiceRepo(db), master)
	allowedServer, deniedServer := uint(101), uint(202)
	resource, err := svc.Create(context.Background(), service.CreateServiceInput{
		Name: "rebind", BaseURL: upstreamA.URL,
		Credentials: map[string]string{"token": "dummy-a"}, ServerID: &allowedServer,
	})
	if err != nil {
		t.Fatal(err)
	}
	lookupComplete, resume := make(chan struct{}), make(chan struct{})
	var intercepted atomic.Bool
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()
	if err := db.Callback().Query().After("gorm:after_query").Register("test:relay_rebind_after_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table != "services" || !intercepted.CompareAndSwap(false, true) {
			return
		}
		close(lookupComplete)
		select {
		case <-resume:
		case <-tx.Statement.Context.Done():
			_ = tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(mw.Auth(nil, relayScopedKeyValidator(allowedServer), nil))
	router.Post("/api/v1/services/{id}/relay", NewServiceHandler(svc, nil).Relay)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/services/%d/relay", resource.ID), strings.NewReader(`{"method":"GET","headers":{"X-Upstream-Token":"{{token}}"}}`)).WithContext(ctx)
	req.Header.Set("X-API-Key", "dummy-relay-key")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(rec, req)
	}()
	select {
	case <-lookupComplete:
	case <-ctx.Done():
		t.Fatal("relay did not reach the snapshot barrier")
	}
	if _, err := svc.Update(ctx, resource.ID, service.CreateServiceInput{
		Name: "rebind", BaseURL: upstreamB.URL,
		Credentials: map[string]string{"token": "dummy-b"}, ServerID: &deniedServer,
	}); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("relay did not finish after the rebind")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "authorized-a" || callsA.Load() != 1 || callsB.Load() != 0 {
		t.Fatalf("rebind consumed an unauthorized configuration: status=%d body=%q A=%d B=%d", rec.Code, rec.Body.String(), callsA.Load(), callsB.Load())
	}
}

func TestRelayHandlerPreservesServerScopeSemantics(t *testing.T) {
	db := usageFlowDB(t)
	if err := db.AutoMigrate(&model.Service{}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	allowedServer, deniedServer := uint(101), uint(202)
	resources := []model.Service{
		{Name: "allowed", BaseURL: upstream.URL, Salt: []byte{1}, ServerID: &allowedServer},
		{Name: "denied", BaseURL: upstream.URL, Salt: []byte{1}, ServerID: &deniedServer},
		{Name: "global", BaseURL: upstream.URL, Salt: []byte{1}},
	}
	for i := range resources {
		if err := db.Create(&resources[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	jwtSvc := token.NewJWTService("dummy-relay-session-secret", time.Hour)
	bearer, err := jwtSvc.GenerateToken(7, "owner", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(mw.Auth(jwtSvc, relayScopedKeyValidator(allowedServer), nil))
	router.Post("/api/v1/services/{id}/relay", NewServiceHandler(service.NewServiceRelayService(repository.NewServiceRepo(db), nil), nil).Relay)
	for _, tc := range []struct {
		name        string
		resource    int
		admin       bool
		status      int
		upstreamHit bool
	}{
		{name: "allowed key", resource: 0, status: http.StatusOK, upstreamHit: true},
		{name: "forbidden key", resource: 1, status: http.StatusForbidden},
		{name: "unbound service", resource: 2, status: http.StatusOK, upstreamHit: true},
		{name: "administrator", resource: 1, admin: true, status: http.StatusOK, upstreamHit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/services/%d/relay", resources[tc.resource].ID), strings.NewReader(`{"method":"GET"}`))
			if tc.admin {
				req.Header.Set("Authorization", "Bearer "+bearer)
			} else {
				req.Header.Set("X-API-Key", "dummy-relay-key")
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			wantCalls := before
			if tc.upstreamHit {
				wantCalls++
			}
			if rec.Code != tc.status || calls.Load() != wantCalls {
				t.Fatalf("status=%d body=%s upstream calls=%d; want status=%d calls=%d", rec.Code, rec.Body.String(), calls.Load(), tc.status, wantCalls)
			}
		})
	}
}
