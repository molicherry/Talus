package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

func apiKeyPermissionsFixture(t *testing.T) (*APIKeyService, *gorm.DB, context.Context, model.APIKey, string) {
	t.Helper()
	db := authOwnerTestDB(t)
	if err := db.AutoMigrate(&model.AuditEvent{}, &model.Server{}); err != nil {
		t.Fatal(err)
	}
	raw := "synthetic-original-api-key"
	hash := sha256.Sum256([]byte(raw))
	key := model.APIKey{Name: "legacy hash only", KeyHash: hex.EncodeToString(hash[:]), KeyPrefix: "synthetic", Scopes: []string{"servers:read", "servers:terminal", "services:read"}, ServerIDs: []uint{9999}}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	uid := uint(1)
	ctx := mw.WithPrincipal(context.Background(), usage.Principal{AuthType: "jwt", UserID: &uid, Role: "admin", Username: "admin"})
	return NewAPIKeyService(repository.NewAPIKeyRepo(db), repository.NewServerRepo(db), nil), db, ctx, key, raw
}

func requireAPIKeyError(t *testing.T, err error, code int) {
	t.Helper()
	var app *server.AppError
	if !errors.As(err, &app) || app.Code != code {
		t.Fatalf("error = %v; want status %d", err, code)
	}
}

func TestAPIKeyPermissionsPreserveKeyAndAudit(t *testing.T) {
	svc, db, ctx, key, raw := apiKeyPermissionsFixture(t)
	scopes := []string{"servers:read", "servers:read"}
	updated, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
	if err != nil {
		t.Fatal(err)
	}
	if updated.PermissionsRevision != 2 || updated.PermissionsUpdatedAt == nil || !reflect.DeepEqual(updated.Scopes, []string{"servers:read"}) || !reflect.DeepEqual(updated.ServerIDs, key.ServerIDs) {
		t.Fatal("partial update changed wrong fields")
	}
	validated, err := svc.Validate(context.Background(), raw)
	if err != nil || validated.KeyHash != key.KeyHash || validated.KeyPrefix != key.KeyPrefix || validated.EncryptedRawKey != "" || validated.UserID != key.UserID || !reflect.DeepEqual(validated.Scopes, scopes[:1]) {
		t.Fatal("original hash-only key or identity changed")
	}
	var events []model.AuditEvent
	if err := db.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "api_key.permissions.update" || events[0].OperationID == nil || !strings.Contains(events[0].Details, `"revision":"2"`) || strings.Contains(events[0].Details, raw) || strings.Contains(events[0].Details, key.KeyHash) {
		t.Fatal("missing or unsafe audit")
	}
	// A semantic no-op neither revokes sessions nor changes revision/audit.
	session := svc.terminals.Register(key.ID, 2, nil, nil)
	defer svc.terminals.Done(session.ID)
	updated, err = svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 2, Scopes: &scopes})
	if err != nil || updated.PermissionsRevision != 2 || session.State() != gate.StatePreparing {
		t.Fatal("no-op changed state")
	}
	var count int64
	db.Model(&model.AuditEvent{}).Count(&count)
	if count != 1 {
		t.Fatal("no-op created a change audit")
	}
}

func TestAPIKeyPermissionsValidationAndServerScope(t *testing.T) {
	svc, db, ctx, key, _ := apiKeyPermissionsFixture(t)
	for _, scopes := range [][]string{{}, {"*"}, {"unknown"}} {
		_, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
		requireAPIKeyError(t, err, http.StatusBadRequest)
	}
	for _, ids := range [][]uint{{0}, {9998}} {
		_, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, ServerIDs: &ids})
		requireAPIKeyError(t, err, http.StatusBadRequest)
	}
	serverRow := model.Server{Name: "one", Host: "127.0.0.1", OwnerID: 1}
	if err := db.Create(&serverRow).Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint{9999, serverRow.ID, serverRow.ID}
	updated, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, ServerIDs: &ids})
	if err != nil || len(updated.ServerIDs) != 2 || !reflect.DeepEqual(updated.Scopes, key.Scopes) {
		t.Fatalf("preserve stale id and scopes: %v", err)
	}
	all := []uint{}
	updated, err = svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 2, ServerIDs: &all})
	if err != nil || len(updated.ServerIDs) != 0 {
		t.Fatalf("explicit all servers: %v", err)
	}
	var stored model.APIKey
	db.First(&stored, key.ID)
	if len(stored.ServerIDs) != 0 {
		t.Fatal("GORM skipped explicit empty server array")
	}
}

func TestAPIKeyPermissionsErrorsRenderParameters(t *testing.T) {
	svc, db, ctx, key, _ := apiKeyPermissionsFixture(t)
	existing := model.Server{Name: "existing", Host: "127.0.0.1", OwnerID: 1}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	scopes := []string{"servers:read", "unknown:scope"}
	ids := []uint{key.ServerIDs[0], existing.ID, 9998, 9998}
	for _, tc := range []struct {
		name, reason, message, param string
		input                        UpdateAPIKeyPermissionsInput
		want                         any
	}{
		{"scopes", server.ReasonInvalidScopes, "invalid scopes: [unknown:scope]", "scopes", UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes}, []any{"unknown:scope"}},
		{"servers", server.ReasonServersNotFound, "unknown server ids: [9998]", "servers", UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, ServerIDs: &ids}, []any{float64(9998)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdatePermissions(ctx, key.ID, tc.input)
			recorder := httptest.NewRecorder()
			server.WriteError(recorder, httptest.NewRequest("PATCH", "/", nil).WithContext(ctx), err)
			var body struct {
				Error struct {
					Reason  string         `json:"reason"`
					Message string         `json:"message"`
					Params  map[string]any `json:"params"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusBadRequest || body.Error.Reason != tc.reason || body.Error.Message != tc.message || !reflect.DeepEqual(body.Error.Params[tc.param], tc.want) {
				t.Fatalf("unexpected error envelope: %s", recorder.Body.String())
			}
		})
	}
}

func TestAPIKeyPermissionsSlowTransactionAndCancellation(t *testing.T) {
	svc, db, ctx, key, raw := apiKeyPermissionsFixture(t)
	if err := db.Exec(`CREATE FUNCTION slow_permission_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(2.2); RETURN NEW; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER slow_permission_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION slow_permission_audit()`).Error; err != nil {
		t.Fatal(err)
	}
	scopes := []string{"servers:read"}
	updated, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
	if err != nil || updated.PermissionsRevision != 2 {
		t.Fatalf("write exceeding admission budget failed: %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = svc.UpdatePermissions(short, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 2, Scopes: &key.Scopes})
	requireAPIKeyError(t, err, http.StatusServiceUnavailable)
	current, err := svc.Validate(ctx, raw)
	if err != nil || current.PermissionsRevision != 2 || !reflect.DeepEqual(current.Scopes, scopes) {
		t.Fatal("cancelled transaction changed policy")
	}
	var count int64
	if err := db.Model(&model.AuditEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("cancelled transaction committed audit")
	}
}

func TestAPIKeyPermissionsEditPreservesLegacyColumns(t *testing.T) {
	for _, scopes := range []string{`NULL`, `'null'::jsonb`, `'[]'::jsonb`, `'["*","legacy:scope"]'::jsonb`} {
		t.Run(scopes, func(t *testing.T) {
			svc, db, ctx, key, raw := apiKeyPermissionsFixture(t)
			if err := db.Exec("UPDATE api_keys SET scopes="+scopes+",user_id=77 WHERE id=?", key.ID).Error; err != nil {
				t.Fatal(err)
			}
			var before, after string
			query := `SELECT (to_jsonb(api_keys) - 'server_ids' - 'permissions_revision' - 'permissions_updated_at')::text FROM api_keys WHERE id=?`
			if err := db.Raw(query, key.ID).Scan(&before).Error; err != nil {
				t.Fatal(err)
			}
			all := []uint{}
			if _, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, ServerIDs: &all}); err != nil {
				t.Fatal(err)
			}
			if err := db.Raw(query, key.ID).Scan(&after).Error; err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("server-only patch rewrote legacy scopes, ownership or secret columns")
			}
			if _, err := svc.Validate(ctx, raw); err != nil {
				t.Fatal("old hash-only key no longer authenticates")
			}
		})
	}
}

func TestAPIKeyPermissionsAdminAndConcurrentRevision(t *testing.T) {
	svc, _, ctx, key, _ := apiKeyPermissionsFixture(t)
	scopes := []string{"servers:read"}
	uid := uint(1)
	for _, p := range []usage.Principal{{AuthType: "api_key", UserID: &uid, Role: "admin", Scopes: []string{"*"}}, {AuthType: "jwt", UserID: &uid, Role: "operator"}} {
		_, err := svc.UpdatePermissions(mw.WithPrincipal(context.Background(), p), key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
		requireAPIKeyError(t, err, http.StatusForbidden)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			requireAPIKeyError(t, err, http.StatusConflict)
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	if err := svc.Delete(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	_, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 2, Scopes: &scopes})
	requireAPIKeyError(t, err, http.StatusNotFound)
}

func TestAPIKeyPermissionsAuditFailureRollsBack(t *testing.T) {
	svc, db, ctx, key, raw := apiKeyPermissionsFixture(t)
	if err := db.Exec(`ALTER TABLE audit_events ADD CONSTRAINT reject_permission_audit CHECK (action <> 'api_key.permissions.update')`).Error; err != nil {
		t.Fatal(err)
	}
	scopes := []string{"servers:read"}
	session := svc.terminals.Register(key.ID, 1, nil, nil)
	defer svc.terminals.Done(session.ID)
	_, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
	requireAPIKeyError(t, err, http.StatusServiceUnavailable)
	current, err := svc.Validate(ctx, raw)
	if err != nil || current.PermissionsRevision != 1 || !reflect.DeepEqual(current.Scopes, key.Scopes) || session.State() != gate.StatePreparing {
		t.Fatal("audit failure partially committed/revoked")
	}
}

func TestAPIKeyPermissionsCommitOutcomes(t *testing.T) {
	for _, mode := range []string{"lost_ack", "rollback", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			svc, db, ctx, key, raw := apiKeyPermissionsFixture(t)
			faultDB := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
			pool := &authCommitFaultPool{ConnPool: db.Statement.ConnPool, mode: mode, held: make(chan *sql.Tx, 1)}
			faultDB.Statement.ConnPool = pool
			svc.repo = repository.NewAPIKeyRepo(faultDB)
			var session *gate.Session
			session = svc.terminals.Register(key.ID, 1, func() { session.MarkDone() }, nil)
			defer svc.terminals.Done(session.ID)
			scopes := []string{"servers:read"}
			_, err := svc.UpdatePermissions(ctx, key.ID, UpdateAPIKeyPermissionsInput{ExpectedRevision: 1, Scopes: &scopes})
			requireAPIKeyError(t, err, http.StatusServiceUnavailable)
			if mode == "unresolved" {
				tx := <-pool.held
				defer func() { _ = tx.Rollback() }()
				short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				err := svc.permissionsGate.Admit(short, key.ID, func(int64) error { t.Error("admitted while commit unresolved"); return nil })
				cancel()
				if err == nil {
					t.Fatal("uncertainty fence missing")
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			// Authentication uses a normal pool; the fault only affects writes.
			svc.repo = repository.NewAPIKeyRepo(db)
			current, err := svc.Validate(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				if current.PermissionsRevision != 1 || session.State() != gate.StatePreparing {
					t.Fatal("definite rollback revoked/changed key")
				}
			} else if current.PermissionsRevision != 2 || session.State() != gate.StateRevoked {
				t.Fatal("committed or uncertain update did not revoke")
			}
		})
	}
}
