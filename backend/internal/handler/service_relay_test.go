package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

func TestRelayHandlerAbortsTruncatedHTTPResponse(t *testing.T) {
	for _, failure := range []string{"upstream truncation", "total budget"} {
		t.Run(failure, func(t *testing.T) {
			db := usageFlowDB(t)
			if err := db.AutoMigrate(&model.Service{}); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "partial")
				w.(http.Flusher).Flush()
				if failure == "total budget" {
					<-r.Context().Done()
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}))
			defer upstream.Close()
			resource := model.Service{Name: "relay-regression", BaseURL: upstream.URL, Salt: []byte{1}}
			if err := db.Create(&resource).Error; err != nil {
				t.Fatal(err)
			}
			svc := service.NewServiceRelayService(repository.NewServiceRepo(db), nil)
			h := NewServiceHandler(svc, nil)
			recorder := usage.NewRecorder(repository.NewUsageLogRepo(db), nil, usage.Options{})
			recorder.Start(context.Background())
			defer recorder.Close()
			jwtService := token.NewJWTService("relay-test-secret", time.Hour)
			bearer, err := jwtService.GenerateToken(7, "relay", "admin", 0)
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			router.Use(mw.WriteDeadline, mw.RequestID, mw.Logger, chimw.Recoverer, mw.UsageCapture(recorder), mw.Auth(jwtService, nil, nil))
			if failure == "total budget" {
				router.Use(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
						defer cancel()
						next.ServeHTTP(w, r.WithContext(ctx))
					})
				})
			}
			router.Post("/api/v1/services/{id}/relay", h.Relay)
			relay := httptest.NewServer(router)
			defer relay.Close()
			req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/services/%d/relay", relay.URL, resource.ID), strings.NewReader(`{"method":"GET","mode":"bounded_stream"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+bearer)
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if readErr == nil || response.StatusCode != 200 || string(body) != "partial" {
				t.Fatalf("truncated stream looked complete or received a second response: status=%d body=%q error=%v", response.StatusCode, body, readErr)
			}
			var rows []model.UsageLog
			if err := db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			wantOutcome, wantReason := "failed", "relay_copy_failed"
			if failure == "total budget" {
				wantReason = "relay_budget_exceeded"
			}
			if len(rows) != 1 || rows[0].Outcome != wantOutcome || rows[0].ErrorReason != wantReason || rows[0].HTTPStatus == nil || *rows[0].HTTPStatus != 200 || rows[0].FinishedAt == nil {
				t.Fatalf("abort lost its original usage outcome/status: %+v", rows)
			}
		})
	}
}

func TestRelayHandlerFreezesBudgetBeforeResourceLookup(t *testing.T) {
	db := usageFlowDB(t)
	if err := db.AutoMigrate(&model.Service{}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer upstream.Close()
	resource := model.Service{Name: "lookup-budget", BaseURL: upstream.URL, Salt: []byte{1}}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	h := NewServiceHandler(service.NewServiceRelayService(repository.NewServiceRepo(db), nil), nil)
	var deadlines []time.Time
	if err := db.Callback().Query().Before("gorm:query").Register("test:relay_handler_budget", func(tx *gorm.DB) {
		deadline, ok := tx.Statement.Context.Deadline()
		if !ok {
			t.Error("resource lookup ran without a relay deadline")
		}
		deadlines = append(deadlines, deadline)
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode   string
		budget time.Duration
	}{{"standard", 30 * time.Second}, {"bounded_stream", 300 * time.Second}} {
		t.Run(tc.mode, func(t *testing.T) {
			deadlines = nil
			router := chi.NewRouter()
			router.Post("/api/v1/services/{id}/relay", h.Relay)
			rec := httptest.NewRecorder()
			started := time.Now()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/services/%d/relay", resource.ID), strings.NewReader(fmt.Sprintf(`{"method":"GET","mode":%q}`, tc.mode))))
			if rec.Code != 200 || rec.Body.String() != "ok" || len(deadlines) != 2 {
				t.Fatalf("relay response=%d %q lookup deadlines=%v", rec.Code, rec.Body.String(), deadlines)
			}
			if !deadlines[0].Equal(deadlines[1]) {
				t.Fatalf("lookup stages restarted deadline: %v", deadlines)
			}
			if elapsed := deadlines[0].Sub(started); elapsed < tc.budget || elapsed > tc.budget+time.Second {
				t.Fatalf("mode %s froze unexpected budget %s", tc.mode, elapsed)
			}
		})
	}
}

func TestRelayHandlerRejectsInvalidModeBeforeLookup(t *testing.T) {
	h := NewServiceHandler(service.NewServiceRelayService(nil, nil), nil)
	router := chi.NewRouter()
	router.Post("/api/v1/services/{id}/relay", h.Relay)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/services/1/relay", strings.NewReader(`{"method":"GET","mode":"unlimited"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status = %d", rec.Code)
	}
}

func TestRelayHandlerLookupTimeoutReturnsGatewayTimeout(t *testing.T) {
	db := usageFlowDB(t)
	if err := db.Callback().Query().Before("gorm:query").Register("test:relay_lookup_timeout", func(tx *gorm.DB) {
		if tx.Statement.Table != "services" {
			return
		}
		<-tx.Statement.Context.Done()
		tx.AddError(tx.Statement.Context.Err())
	}); err != nil {
		t.Fatal(err)
	}
	h := NewServiceHandler(service.NewServiceRelayService(repository.NewServiceRepo(db), nil), nil)
	recorder := usage.NewRecorder(repository.NewUsageLogRepo(db), nil, usage.Options{})
	recorder.Start(context.Background())
	defer recorder.Close()
	router := chi.NewRouter()
	router.Use(mw.UsageCapture(recorder))
	router.Post("/api/v1/services/{id}/relay", h.Relay)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services/1/relay", strings.NewReader(`{"method":"GET"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("resource lookup timeout status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var rows []model.UsageLog
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Outcome != "failed" || rows[0].ErrorReason != "relay_budget_exceeded" || rows[0].FinishedAt == nil {
		t.Fatalf("lookup timeout lost its persisted failure reason: %+v", rows)
	}
}
