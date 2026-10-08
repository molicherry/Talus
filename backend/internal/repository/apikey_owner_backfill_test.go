package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func apiKeyOwnerBackfillDB(t *testing.T) (*gorm.DB, *APIKeyRepo) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	// Older installations may have an owner column that permits NULL.
	if err := db.Exec("ALTER TABLE api_keys ALTER COLUMN user_id DROP NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	return db, NewAPIKeyRepo(db)
}

func insertOwnerBackfillUser(t *testing.T, db *gorm.DB, id uint, role string, deleted bool) {
	t.Helper()
	u := model.User{BaseModel: model.BaseModel{ID: id}, Username: fmt.Sprintf("owner-%d", id), PasswordHash: "fixture-password-hash", Role: role}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if deleted {
		if err := db.Delete(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func insertOwnerBackfillKey(t *testing.T, db *gorm.DB, id, owner uint, nullOwner bool) {
	t.Helper()
	k := model.APIKey{
		ID: id, UserID: owner, Name: fmt.Sprintf("legacy-key-%d", id),
		KeyHash: fmt.Sprintf("fixture-key-hash-%d", id), KeyPrefix: "prefix",
		EncryptedRawKey: "fixture-ciphertext", Salt: []byte{1, 2, 3, 255},
		Scopes: []string{"servers:exec"}, ServerIDs: []uint{13, 17},
		CreatedAt: time.Date(2025, 4, 3, 2, 1, 0, 0, time.UTC),
	}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	if owner == 0 || nullOwner {
		// This fixture represents a key present at the upgrade boundary.
		if err := db.Model(&model.APIKey{}).Where("id = ?", id).Update("owner_binding_version", 0).Error; err != nil {
			t.Fatal(err)
		}
	}
	if nullOwner {
		if err := db.Model(&model.APIKey{}).Where("id = ?", id).Update("user_id", nil).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func ownerBackfillSnapshots(t *testing.T, db *gorm.DB, query string) []string {
	t.Helper()
	var rows []struct{ Snapshot string }
	if err := db.Raw(query).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	result := make([]string, len(rows))
	for i, row := range rows {
		result[i] = row.Snapshot
	}
	return result
}

func requireOwnerBackfillOwners(t *testing.T, db *gorm.DB, expected map[uint]string) {
	t.Helper()
	var rows []struct {
		ID    uint
		Owner string
	}
	if err := db.Raw("SELECT id, COALESCE(user_id::text, 'NULL') AS owner FROM api_keys ORDER BY id").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	actual := make(map[uint]string, len(rows))
	for _, row := range rows {
		actual[row.ID] = row.Owner
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unexpected key owners: got %v, want %v", actual, expected)
	}
}

func TestAPIKeyOwnerBackfillSelectsActiveDefaultAndPreservesData(t *testing.T) {
	db, repo := apiKeyOwnerBackfillDB(t)
	insertOwnerBackfillUser(t, db, 1, "operator", false)
	insertOwnerBackfillUser(t, db, 2, "admin", true)
	insertOwnerBackfillUser(t, db, 9, "admin", false)
	insertOwnerBackfillUser(t, db, 5, "admin", false)
	insertOwnerBackfillKey(t, db, 11, 0, false)
	insertOwnerBackfillKey(t, db, 12, 0, true)
	insertOwnerBackfillKey(t, db, 13, 9, false)
	insertOwnerBackfillKey(t, db, 14, 2, false)
	insertOwnerBackfillKey(t, db, 15, 999, false)
	if err := db.AutoMigrate(&model.UsageLog{}, &model.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	keyID := uint(11)
	zero := uint(0)
	log := model.UsageLog{
		OperationID: "00000000-0000-4000-8000-000000000011", StartedAt: time.Now().UTC(),
		Outcome: "succeeded", Phase: "closed", StateSeq: 1, Action: "server.exec",
		AuthType: "api_key", UserID: &zero, APIKeyID: &keyID,
		APIKeyNameSnapshot: "legacy key", Source: "operation", Metadata: json.RawMessage(`{"version":1}`),
	}
	if err := db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	// Unknown historical owners include both legacy zero and SQL NULL.
	nullKeyID := uint(12)
	nullOwnerLog := log
	nullOwnerLog.ID = 0
	nullOwnerLog.OperationID = "00000000-0000-4000-8000-000000000012"
	nullOwnerLog.UserID = nil
	nullOwnerLog.APIKeyID = &nullKeyID
	if err := db.Create(&nullOwnerLog).Error; err != nil {
		t.Fatal(err)
	}
	audit := model.AuditEvent{UserID: 0, Action: "api_key.reveal", ResourceType: "api_key", ResourceID: keyID, Details: "historical owner unknown"}
	if err := db.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	queries := []string{
		"SELECT (to_jsonb(api_keys) - 'user_id' - 'owner_binding_version')::text AS snapshot FROM api_keys ORDER BY id",
		"SELECT to_jsonb(usage_logs)::text AS snapshot FROM usage_logs ORDER BY id",
		"SELECT to_jsonb(audit_events)::text AS snapshot FROM audit_events ORDER BY id",
	}
	before := make([][]string, len(queries))
	for i, query := range queries {
		before[i] = ownerBackfillSnapshots(t, db, query)
	}
	changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
	if err != nil || changed != 2 {
		t.Fatalf("backfill: changed=%d err=%v", changed, err)
	}
	requireOwnerBackfillOwners(t, db, map[uint]string{11: "5", 12: "5", 13: "9", 14: "2", 15: "999"})
	for i, query := range queries {
		if after := ownerBackfillSnapshots(t, db, query); !reflect.DeepEqual(after, before[i]) {
			t.Fatalf("backfill changed key material or history: query=%s", query)
		}
	}
	changed, err = repo.BindUnownedToDefaultAdmin(context.Background())
	if err != nil || changed != 0 {
		t.Fatalf("idempotent retry: changed=%d err=%v", changed, err)
	}
}

func TestAPIKeyOwnerBackfillWithoutAdminCanRetryLater(t *testing.T) {
	for _, existingUsers := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-users-%t", existingUsers), func(t *testing.T) {
			db, repo := apiKeyOwnerBackfillDB(t)
			if existingUsers {
				insertOwnerBackfillUser(t, db, 1, "operator", false)
				insertOwnerBackfillUser(t, db, 2, "admin", true)
			}
			insertOwnerBackfillKey(t, db, 1, 0, false)
			insertOwnerBackfillKey(t, db, 2, 0, true)
			changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
			if err != nil || changed != 0 {
				t.Fatalf("no active administrator: changed=%d err=%v", changed, err)
			}
			requireOwnerBackfillOwners(t, db, map[uint]string{1: "0", 2: "NULL"})
			insertOwnerBackfillUser(t, db, 8, "admin", false)
			changed, err = repo.BindUnownedToDefaultAdmin(context.Background())
			if err != nil || changed != 2 {
				t.Fatalf("retry after administrator created: changed=%d err=%v", changed, err)
			}
			requireOwnerBackfillOwners(t, db, map[uint]string{1: "8", 2: "8"})
			newKey := model.APIKey{ID: 3, Name: "new-unowned", KeyHash: "new-key-hash", KeyPrefix: "new"}
			if err := db.Create(&newKey).Error; err != nil {
				t.Fatal(err)
			}
			changed, err = repo.BindUnownedToDefaultAdmin(context.Background())
			if err != nil || changed != 0 {
				t.Fatalf("later unowned key entered the legacy set: changed=%d err=%v", changed, err)
			}
			requireOwnerBackfillOwners(t, db, map[uint]string{1: "8", 2: "8", 3: "0"})
		})
	}
}

func TestAPIKeyOwnerBackfillConcurrentRetriesBindEachKeyOnce(t *testing.T) {
	db, repo := apiKeyOwnerBackfillDB(t)
	insertOwnerBackfillUser(t, db, 7, "admin", false)
	insertOwnerBackfillUser(t, db, 11, "admin", false)
	expected := map[uint]string{99: "11"}
	for id := uint(1); id <= 12; id++ {
		insertOwnerBackfillKey(t, db, id, 0, id%2 == 0)
		expected[id] = "7"
	}
	insertOwnerBackfillKey(t, db, 99, 11, false)
	type result struct {
		changed int64
		err     error
	}
	results := make(chan result, 4)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
			results <- result{changed: changed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var total int64
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		total += result.changed
	}
	if total != 12 {
		t.Fatalf("concurrent retries reported %d updates, want 12", total)
	}
	requireOwnerBackfillOwners(t, db, expected)
	changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
	if err != nil || changed != 0 {
		t.Fatalf("post-concurrency retry: changed=%d err=%v", changed, err)
	}
}

func TestAPIKeyOwnerBackfillMigratesLegacyOwnerColumn(t *testing.T) {
	t.Run("fresh-schema", func(t *testing.T) {
		db := newTestDB(t)
		repo := NewAPIKeyRepo(db)
		if err := repo.PrepareLegacyOwnerBinding(context.Background()); err != nil {
			t.Fatalf("fresh schema preparation must be a no-op: %v", err)
		}
		if err := db.AutoMigrate(&model.User{}, &model.APIKey{}); err != nil {
			t.Fatal(err)
		}
		changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
		if err != nil || changed != 0 {
			t.Fatalf("fresh schema backfill: changed=%d err=%v", changed, err)
		}
	})
	for _, schema := range []string{"missing-column", "nullable-without-default"} {
		t.Run(schema, func(t *testing.T) {
			db, repo := apiKeyOwnerBackfillDB(t)
			insertOwnerBackfillUser(t, db, 7, "admin", false)
			switch schema {
			case "missing-column":
				if err := db.Exec("ALTER TABLE api_keys DROP COLUMN user_id").Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`INSERT INTO api_keys
					(id, name, key_hash, key_prefix, scopes, server_ids, encrypted_raw_key, salt, created_at)
					VALUES (31, 'legacy-no-owner', 'fixture-key-hash', 'legacy',
					'["servers:read"]'::jsonb, '[17]'::jsonb, 'fixture-ciphertext',
					decode('010203', 'hex'), '2025-04-03T02:01:00Z')`).Error; err != nil {
					t.Fatal(err)
				}
			case "nullable-without-default":
				// The earlier model had no default. Preserve that legacy shape
				// so adding a new default cannot hide a nullable-column upgrade.
				if err := db.Exec("ALTER TABLE api_keys ALTER COLUMN user_id DROP DEFAULT").Error; err != nil {
					t.Fatal(err)
				}
				insertOwnerBackfillKey(t, db, 31, 0, true)
			}
			if err := db.Exec("ALTER TABLE api_keys DROP COLUMN owner_binding_version").Error; err != nil {
				t.Fatal(err)
			}
			payloadBefore := ownerBackfillSnapshots(t, db, "SELECT (to_jsonb(api_keys) - 'user_id' - 'owner_binding_version')::text AS snapshot FROM api_keys ORDER BY id")
			if err := repo.PrepareLegacyOwnerBinding(context.Background()); err != nil {
				t.Fatalf("legacy owner preparation failed: %v", err)
			}
			if schema == "nullable-without-default" {
				requireOwnerBackfillOwners(t, db, map[uint]string{31: "NULL"})
			}
			if err := db.AutoMigrate(&model.User{}, &model.APIKey{}); err != nil {
				t.Fatalf("legacy schema migration failed before owner backfill: %v", err)
			}
			if schema == "missing-column" {
				requireOwnerBackfillOwners(t, db, map[uint]string{31: "0"})
				var defaultValue string
				if err := db.Raw(`SELECT column_default FROM information_schema.columns
					WHERE table_schema = current_schema() AND table_name = 'api_keys' AND column_name = 'user_id'`).Scan(&defaultValue).Error; err != nil {
					t.Fatal(err)
				}
				if defaultValue != "0" {
					t.Fatalf("legacy owner column needs a zero default: %q", defaultValue)
				}
			}
			changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
			if err != nil || changed != 1 {
				t.Fatalf("backfill after migration: changed=%d err=%v", changed, err)
			}
			requireOwnerBackfillOwners(t, db, map[uint]string{31: "7"})
			payloadAfter := ownerBackfillSnapshots(t, db, "SELECT (to_jsonb(api_keys) - 'user_id' - 'owner_binding_version')::text AS snapshot FROM api_keys ORDER BY id")
			if !reflect.DeepEqual(payloadBefore, payloadAfter) {
				t.Fatal("legacy migration or owner backfill changed key material or permissions")
			}
		})
	}
}

func TestAPIKeyOwnerBackfillUpgradeBoundaryExcludesLaterKeys(t *testing.T) {
	db, repo := apiKeyOwnerBackfillDB(t)
	insertOwnerBackfillKey(t, db, 1, 0, false)
	insertOwnerBackfillKey(t, db, 2, 9, false)
	if err := db.Exec("ALTER TABLE api_keys DROP COLUMN owner_binding_version").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := repo.PrepareLegacyOwnerBinding(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	if changed, err := repo.BindUnownedToDefaultAdmin(ctx); err != nil || changed != 0 {
		t.Fatalf("upgrade before administrator: changed=%d err=%v", changed, err)
	}
	// All inserts after preparation are excluded, including raw SQL without a
	// version and non-HTTP GORM callers explicitly passing the zero value.
	newKey := model.APIKey{ID: 3, Name: "new", KeyHash: "new-hash", KeyPrefix: "new", OwnerBindingVersion: 0}
	if err := db.Create(&newKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO api_keys (id, user_id, name, key_hash, key_prefix, created_at)
		VALUES (4, 0, 'raw-new', 'raw-new-hash', 'raw', now())`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.APIKey{}).Where("id = ?", 2).Update("user_id", 0).Error; err != nil {
		t.Fatal(err)
	}
	// A restart must not recapture the rows created or cleared in the meantime.
	if err := NewAPIKeyRepo(db).PrepareLegacyOwnerBinding(ctx); err != nil {
		t.Fatal(err)
	}
	insertOwnerBackfillUser(t, db, 7, "admin", false)
	changed, err := repo.BindUnownedToDefaultAdmin(ctx)
	if err != nil || changed != 1 {
		t.Fatalf("fixed legacy set: changed=%d err=%v", changed, err)
	}
	requireOwnerBackfillOwners(t, db, map[uint]string{1: "7", 2: "0", 3: "0", 4: "0"})
}

func TestAPIKeyOwnerBackfillKeepsBatchesBounded(t *testing.T) {
	db, repo := apiKeyOwnerBackfillDB(t)
	insertOwnerBackfillUser(t, db, 7, "admin", false)
	for id := uint(1); id <= APIKeyOwnerBackfillBatchSize+3; id++ {
		insertOwnerBackfillKey(t, db, id, 0, false)
	}
	changed, err := repo.BindUnownedToDefaultAdmin(context.Background())
	if err != nil || changed != APIKeyOwnerBackfillBatchSize {
		t.Fatalf("first bounded batch: changed=%d err=%v", changed, err)
	}
	changed, err = repo.BindUnownedToDefaultAdmin(context.Background())
	if err != nil || changed != 3 {
		t.Fatalf("remaining batch: changed=%d err=%v", changed, err)
	}
}
