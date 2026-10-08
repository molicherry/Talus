package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func usageFlowDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL required in CI")
		}
		t.Skip("TEST_DATABASE_URL required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminSQL := stdlib.OpenDB(*cfg)
	schema := fmt.Sprintf("talus_usage_flow_%d", time.Now().UnixNano())
	if _, err = adminSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminSQL.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = adminSQL.Close() })
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["search_path"] = schema
	sqlDB := stdlib.OpenDB(*cfg)
	sqlDB.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&model.User{}, &model.Server{}, &model.SSHCredential{}, &model.APIKey{}, &model.AuditEvent{}, &model.UsageLogInstance{}, &model.UsageLog{}, &model.UsageLogBackfillState{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// Exercise the actual handlers, auth, capture, repositories and encryption.
// A usage INSERT constraint failure must never roll back a revealed audit.
func TestUsageFlowRevealAuditSurvivesSummaryFailure(t *testing.T) {
	db := usageFlowDB(t)
	master, err := crypto.NewMasterKey(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	credRepo := repository.NewCredentialRepo(db)
	serverRepo := repository.NewServerRepo(db)
	audit := repository.NewAuditEventRepo(db)
	store := repository.NewUsageLogRepo(db)
	recorder := usage.NewRecorder(store, audit, usage.Options{})
	recorder.Start(context.Background())
	defer recorder.Close()
	creds := service.NewCredentialService(credRepo, serverRepo, master, nil)
	h := NewCredentialHandler(creds, audit)
	jwtSvc := token.NewJWTService("usage-flow-secret", time.Hour)
	jwt, err := jwtSvc.GenerateToken(77, "operator", "admin")
	if err != nil {
		t.Fatal(err)
	}
	route := func(method, path string, body []byte, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+jwt)
		r.Header.Set("X-Request-ID", "client-request")
		ctx := chi.NewRouteContext()
		parts := strings.Split(path, "/")
		if len(parts) > 4 {
			ctx.URLParams.Add("id", parts[4])
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
		w := httptest.NewRecorder()
		mw.RequestID(mw.UsageCapture(recorder)(mw.Auth(jwtSvc, nil)(handler))).ServeHTTP(w, r)
		return w
	}
	secret := "secret-value-never-log"
	w := route("POST", "/api/v1/credentials", []byte(`{"name":"production ssh","auth_type":"password","username":"root","password":"`+secret+`"}`), h.Create)
	if w.Code != 201 {
		t.Fatalf("create failed %s", w.Body)
	}
	var created struct {
		Data model.SSHCredential `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("ALTER TABLE usage_logs ADD CONSTRAINT fail_reveal_summary CHECK (action <> 'credential.reveal')").Error; err != nil {
		t.Fatal(err)
	}
	w = route("GET", "/api/v1/credentials/"+strconv.Itoa(int(created.Data.ID))+"/reveal", nil, h.Reveal)
	if w.Code != 200 || !strings.Contains(w.Body.String(), secret) {
		t.Fatalf("reveal business response changed: %s", w.Body)
	}
	var auditRows []model.AuditEvent
	if err = db.Find(&auditRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(auditRows) != 1 || auditRows[0].OperationID == nil || auditRows[0].UserID != 77 {
		t.Fatal("independent security event missing or incorrect identity")
	}
	if st := recorder.Stats(); st.WriteFailed != 1 || st.FinalizationLost != 1 || st.Pending != 0 {
		t.Fatalf("permanent summary failure was not counted or kept retrying: %+v", st)
	}
	if err = db.Exec("ALTER TABLE usage_logs DROP CONSTRAINT fail_reveal_summary").Error; err != nil {
		t.Fatal(err)
	}
	var logs []model.UsageLog
	if err = db.Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatal("permanently rejected summary was retried after the constraint was removed")
	}
	// A later request succeeds after repair and keeps its own audit identity;
	// the rejected earlier summary is counted as lost rather than resurrected.
	w = route("GET", "/api/v1/credentials/"+strconv.Itoa(int(created.Data.ID))+"/reveal", nil, h.Reveal)
	if w.Code != 200 || !strings.Contains(w.Body.String(), secret) {
		t.Fatalf("new reveal after storage repair failed: %s", w.Body)
	}
	if err = db.Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Order("id").Find(&auditRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || len(auditRows) != 2 || auditRows[1].OperationID == nil {
		t.Fatal("new successful request did not persist its independent summary and audit")
	}
	last := logs[1]
	if last.OperationID != *auditRows[1].OperationID || last.OperationID == *auditRows[0].OperationID || last.Outcome != "succeeded" || last.ResourceNameSnapshot != "production ssh" || last.RequestID != "client-request" {
		t.Fatalf("incorrect new summary after repair: %+v", last)
	}
	serialized, _ := json.Marshal(struct {
		Logs  []model.UsageLog
		Audit []model.AuditEvent
	}{logs, auditRows})
	if bytes.Contains(serialized, []byte(secret)) || bytes.Contains(serialized, []byte("encrypted_password")) {
		t.Fatal("secret entered persisted log fields")
	}
}

func TestNewAPIKeyOwnerComesFromVerifiedJWT(t *testing.T) {
	db := usageFlowDB(t)
	master, err := crypto.NewMasterKey(strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewAPIKeyService(repository.NewAPIKeyRepo(db), repository.NewServerRepo(db), master)
	ctx := mw.WithUserClaims(context.Background(), &token.Claims{UserID: 123, Username: "owner", Role: "admin"})
	result, err := svc.Create(ctx, "deploy", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.APIKey.UserID != 123 {
		t.Fatal("API key owner was not saved")
	}
	verified, err := svc.Validate(context.Background(), result.Key)
	if err != nil || verified.UserID != 123 {
		t.Fatal("validated API key lost owner")
	}
}
