package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/usage"
)

type captureTestStore struct {
	mu              sync.Mutex
	rows            []model.UsageLog
	canceledWrite   bool
	missingDeadline bool
}

func (s *captureTestStore) Upsert(ctx context.Context, row *model.UsageLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceledWrite = s.canceledWrite || ctx.Err() != nil
	_, deadline := ctx.Deadline()
	s.missingDeadline = s.missingDeadline || !deadline
	s.rows = append(s.rows, *row)
	return nil
}
func (*captureTestStore) RegisterInstance(context.Context, string) error    { return nil }
func (*captureTestStore) Heartbeat(context.Context, string) error           { return nil }
func (*captureTestStore) Reconcile(context.Context, string, []string) error { return nil }
func (*captureTestStore) Cleanup(context.Context) error                     { return nil }

func TestUsageCaptureScopeDenialHasVerifiedCaller(t *testing.T) {
	store := &captureTestStore{}
	recorder := usage.NewRecorder(store, nil, usage.Options{})
	keyID := uint(91)
	validator := APIKeyIdentityValidatorFunc(func(context.Context, string) (usage.Principal, error) {
		return usage.Principal{APIKeyID: &keyID, APIKeyName: "automation", Role: "admin"}, nil
	})
	handler := RequestID(UsageCapture(recorder)(Auth(nil, validator, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("denied request reached business") }))))
	req := httptest.NewRequest("POST", "/api/v1/servers/12/exec", nil)
	req.Header.Set("X-API-Key", "raw-secret")
	req.Header.Set("X-Request-ID", "same-client-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if len(store.rows) != 1 {
		t.Fatalf("rows = %d", len(store.rows))
	}
	row := store.rows[0]
	if response.Code != 403 || row.Outcome != "rejected" || row.ErrorReason != "forbidden" || row.AuthType != "api_key" || row.APIKeyID == nil || *row.APIKeyID != keyID || row.UserID != nil {
		t.Fatalf("incorrect denial row: %#v", row)
	}
	if row.RequestID != "same-client-id" || row.OperationID == row.RequestID || row.FinishedAt == nil || row.HTTPStatus == nil || *row.HTTPStatus != 403 {
		t.Fatalf("incorrect lifecycle: %#v", row)
	}
}

func TestUsageCaptureRejectBudgetPrecedesDatabase(t *testing.T) {
	store := &captureTestStore{}
	recorder := usage.NewRecorder(store, nil, usage.Options{})
	handler := UsageCapture(recorder)(Auth(nil, nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unauthenticated operation admitted") })))
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest("POST", "/api/v1/servers", nil)
		req.RemoteAddr = "192.0.2.8:3456"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != 401 {
			t.Fatal("capture budget changed authentication result")
		}
	}
	if len(store.rows) != 5 || recorder.Stats().CaptureSkipped != 7 {
		t.Fatalf("unbounded denial writes: %d, stats %#v", len(store.rows), recorder.Stats())
	}
	for _, row := range store.rows {
		if row.AuthType != "unauthenticated" || row.ResourceNameSnapshot != "" || row.ClientAddress != "192.0.2.8" {
			t.Fatal("untrusted identity/resource data recorded")
		}
	}
}

func TestUsageCaptureDoesNotRecordReads(t *testing.T) {
	store := &captureTestStore{}
	recorder := usage.NewRecorder(store, nil, usage.Options{})
	handler := UsageCapture(recorder)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if usage.FromContext(r.Context()) != nil {
			t.Error("read route received operation context")
		}
		w.WriteHeader(200)
	}))
	for _, path := range []string{"/api/v1/servers", "/api/v1/servers/1/metrics", "/api/v1/usage-logs", "/api/v1/usage-logs/1", "/healthz"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if len(store.rows) != 0 {
		t.Fatal("read operations wrote history")
	}
}

func TestUsageCaptureFinishesPanicBeforeOuterRecovery(t *testing.T) {
	store := &captureTestStore{}
	recorder := usage.NewRecorder(store, nil, usage.Options{})
	handler := UsageCapture(recorder)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("do not capture this raw value") }))
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				w.WriteHeader(500)
			}
		}()
		handler.ServeHTTP(w, r)
	})
	response := httptest.NewRecorder()
	outer.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/servers", nil))
	if response.Code != 500 || len(store.rows) != 1 || store.rows[0].Outcome != "failed" || store.rows[0].ErrorReason != "internal_error" || store.rows[0].HTTPStatus == nil || *store.rows[0].HTTPStatus != 500 {
		t.Fatalf("panic captured as success: %#v", store.rows)
	}
}

func TestUsageCapturePersistsAfterClientCancellation(t *testing.T) {
	store := &captureTestStore{}
	recorder := usage.NewRecorder(store, nil, usage.Options{})
	jwtService := token.NewJWTService("secret", time.Hour)
	bearer, err := jwtService.GenerateToken(7, "admin", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	handler := UsageCapture(recorder)(Auth(jwtService, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := usage.FromContext(r.Context())
		op.SetResult("succeeded", "")
		cancel()
		w.WriteHeader(http.StatusCreated)
	})))
	req := httptest.NewRequest("POST", "/api/v1/servers", nil).WithContext(requestCtx)
	req.Header.Set("Authorization", "Bearer "+bearer)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if store.canceledWrite || store.missingDeadline || len(store.rows) != 1 || store.rows[0].Outcome != "succeeded" {
		t.Fatal("cancellation prevented independent bounded completion write")
	}
}
