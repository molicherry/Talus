package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

func apiKeyRotationFixture(t *testing.T) (*APIKeyService, *gorm.DB, context.Context, model.APIKey, string) {
	t.Helper()
	svc, db, ctx, key, raw := apiKeyPermissionsFixture(t)
	mk, err := crypto.NewMasterKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.masterKey = mk
	return svc, db, ctx, key, raw
}

func TestAPIKeyRotationPreservesLegacyPolicy(t *testing.T) {
	for _, policy := range []string{`NULL`, `'null'::jsonb`, `'[]'::jsonb`, `'["*","legacy:scope"]'::jsonb`} {
		t.Run(policy, func(t *testing.T) {
			svc, db, ctx, key, oldRaw := apiKeyRotationFixture(t)
			if err := db.Exec("UPDATE api_keys SET scopes="+policy+", user_id=77 WHERE id=?", key.ID).Error; err != nil {
				t.Fatal(err)
			}
			query := `SELECT (to_jsonb(api_keys) - 'key_hash' - 'key_prefix' - 'encrypted_raw_key' - 'salt' - 'crypto_version' - 'permissions_revision')::text FROM api_keys WHERE id=?`
			var before, after string
			if err := db.Raw(query, key.ID).Scan(&before).Error; err != nil {
				t.Fatal(err)
			}
			result, err := svc.Rotate(ctx, key.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := hex.DecodeString(result.Key); err != nil || len(decoded) != 32 || result.Key == oldRaw {
				t.Fatal("replacement is not a new 256-bit key")
			}
			if err := db.Raw(query, key.ID).Scan(&after).Error; err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("rotation rewrote policy, ownership, name or timestamps")
			}
			if _, err := svc.Validate(ctx, oldRaw); !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatal("old key still authenticates")
			}
			current, err := svc.Validate(ctx, result.Key)
			if err != nil || current.ID != key.ID || current.PermissionsRevision != 2 || current.KeyPrefix != result.Key[:8] {
				t.Fatal("new key not usable")
			}
			if current.CryptoVersion != crypto.VersionCurrent || current.EncryptedRawKey == "" || len(current.Salt) != crypto.SaltSize {
				t.Fatal("new material not encrypted")
			}
			if revealed, err := svc.Reveal(ctx, key.ID); err != nil || revealed != result.Key {
				t.Fatal("copy does not return replacement")
			}
			var events []model.AuditEvent
			if err := db.Find(&events).Error; err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(events)
			if len(events) != 1 || events[0].Action != "api_key.rotate" || events[0].OperationID == nil || strings.Contains(string(encoded), result.Key) || strings.Contains(string(encoded), current.KeyHash) || strings.Contains(string(encoded), current.EncryptedRawKey) {
				t.Fatal("missing or unsafe rotation audit")
			}
			// A second deliberate rotation must create fresh key material and salt.
			second, err := svc.Rotate(ctx, key.ID, 2)
			if err != nil || second.Key == result.Key || reflect.DeepEqual(second.APIKey.Salt, current.Salt) {
				t.Fatal("rotation reused material")
			}
		})
	}
}

func TestAPIKeyRotationAdminConcurrencyAndRollback(t *testing.T) {
	svc, db, ctx, key, raw := apiKeyRotationFixture(t)
	uid := uint(1)
	for _, p := range []usage.Principal{{AuthType: "api_key", UserID: &uid, Role: "admin", Scopes: []string{"*"}}, {AuthType: "jwt", UserID: &uid, Role: "operator"}} {
		_, err := svc.Rotate(mw.WithPrincipal(context.Background(), p), key.ID, 1)
		requireAPIKeyError(t, err, http.StatusForbidden)
	}
	_, err := svc.Rotate(ctx, key.ID, 0)
	requireAPIKeyError(t, err, http.StatusBadRequest)
	_, err = svc.Rotate(ctx, key.ID+1, 1)
	requireAPIKeyError(t, err, http.StatusNotFound)
	if err := db.Exec(`ALTER TABLE audit_events ADD CONSTRAINT reject_rotation CHECK (action <> 'api_key.rotate')`).Error; err != nil {
		t.Fatal(err)
	}
	session := svc.terminals.Register(key.ID, 1, nil, nil)
	defer svc.terminals.Done(session.ID)
	result, err := svc.Rotate(ctx, key.ID, 1)
	requireAPIKeyError(t, err, http.StatusServiceUnavailable)
	if result != nil {
		t.Fatal("returned uncommitted secret")
	}
	current, err := svc.Validate(ctx, raw)
	if err != nil || current.PermissionsRevision != 1 || session.State() != gate.StatePreparing {
		t.Fatal("audit failure changed key or revoked session")
	}
	if err := db.Exec(`ALTER TABLE audit_events DROP CONSTRAINT reject_rotation`).Error; err != nil {
		t.Fatal(err)
	}
	svc.terminals.Done(session.ID)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := svc.Rotate(ctx, key.ID, 1); errs <- err }()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			requireAPIKeyError(t, err, http.StatusConflict)
		}
	}
	if success != 1 {
		t.Fatalf("successful replacements = %d", success)
	}
	var count int64
	if err := db.Model(&model.AuditEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate rotation/audit")
	}
}

func TestAPIKeyRotationRejectsLookupBeforeCommit(t *testing.T) {
	svc, db, ctx, key, raw := apiKeyRotationFixture(t)
	type pauseLookup struct{}
	lookedUp, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	var once sync.Once
	if err := db.Callback().Query().After("gorm:query").Register("test_pause_key_lookup", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(pauseLookup{}) == true {
			once.Do(func() { close(lookedUp); <-resume })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Callback().Query().Remove("test_pause_key_lookup") }()
	done := make(chan error, 1)
	go func() { _, err := svc.Validate(context.WithValue(ctx, pauseLookup{}, true), raw); done <- err }()
	select {
	case <-lookedUp:
	case <-time.After(3 * time.Second):
		t.Fatal("hash lookup did not run")
	}
	if _, err := svc.Rotate(ctx, key.ID, 1); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("stale hash admitted: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authentication stuck")
	}
}

func TestAPIKeyRotationCommitOutcomes(t *testing.T) {
	for _, mode := range []string{"lost_ack", "rollback", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			svc, db, ctx, key, oldRaw := apiKeyRotationFixture(t)
			faultDB := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
			pool := &authCommitFaultPool{ConnPool: db.Statement.ConnPool, mode: mode, held: make(chan *sql.Tx, 1)}
			faultDB.Statement.ConnPool = pool
			svc.repo = repository.NewAPIKeyRepo(faultDB)
			var session *gate.Session
			session = svc.terminals.Register(key.ID, 1, func() { session.MarkDone() }, nil)
			defer svc.terminals.Done(session.ID)
			result, err := svc.Rotate(ctx, key.ID, 1)
			requireAPIKeyError(t, err, http.StatusServiceUnavailable)
			if result != nil {
				t.Fatal("returned uncertain replacement")
			}
			if mode == "unresolved" {
				tx := <-pool.held
				defer func() { _ = tx.Rollback() }()
				short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				_, err := svc.Validate(short, oldRaw)
				cancel()
				if !errors.Is(err, gate.ErrUnavailable) {
					t.Fatal("old credential admitted during uncertainty")
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			svc.repo = repository.NewAPIKeyRepo(db)
			if err := svc.permissionsGate.Recover(ctx, key.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				if _, err := svc.Validate(ctx, oldRaw); err != nil || session.State() != gate.StatePreparing {
					t.Fatal("rollback replaced key/revoked session")
				}
			} else {
				if _, err := svc.Validate(ctx, oldRaw); !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatal("committed old key accepted")
				}
				raw, err := svc.Reveal(ctx, key.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Validate(ctx, raw); err != nil || session.State() != gate.StateRevoked {
					t.Fatal("committed replacement unavailable or terminal live")
				}
			}
		})
	}
}
