package repository

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

func TestAPIKeyPermissionsMigrationPreservesExplicitPolicies(t *testing.T) {
	for _, foreignLedger := range []bool{false, true} {
		t.Run(map[bool]string{false: "native ledger", true: "golang-migrate ledger"}[foreignLedger], func(t *testing.T) {
			db, _ := apiKeyOwnerBackfillDB(t)
			if foreignLedger {
				if err := db.Exec("CREATE TABLE schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL)").Error; err != nil {
					t.Fatal(err)
				}
			}
			for i, scopes := range []string{`["servers:read"]`, `[]`, `null`, `["*","legacy:unknown"]`, `NULL`} {
				value := "?::jsonb"
				args := []any{i + 1, "test-hash-" + scopes, scopes}
				if scopes == "NULL" {
					value = "NULL"
					args = args[:2]
				}
				if err := db.Exec("INSERT INTO api_keys (id,name,key_hash,key_prefix,scopes,server_ids,created_at) VALUES (?, 'legacy', ?, 'prefix', "+value+", '[9999]', now())", args...).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := ownerBackfillSnapshots(t, db, "SELECT to_jsonb(api_keys)::text AS snapshot FROM api_keys ORDER BY id")
			for range 2 {
				if prepared, err := PrepareAPIKeyPermissionsSchema(context.Background(), db); err != nil || !prepared {
					t.Fatal(err)
				}
			}
			after := ownerBackfillSnapshots(t, db, "SELECT to_jsonb(api_keys)::text AS snapshot FROM api_keys ORDER BY id")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("migration changed an existing policy or secret")
			}
			applied, err := ApplyOnce(db, "2026-09-24-services-read-scope", `UPDATE api_keys SET scopes = '["services:read"]'`)
			if err != nil || applied {
				t.Fatalf("retired migration ran: %v, %v", applied, err)
			}
			// Simulate an old bookkeeping backup: retirement still must not grant.
			table, err := migrationTable(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("DELETE FROM " + table).Error; err != nil {
				t.Fatal(err)
			}
			if prepared, err := PrepareAPIKeyPermissionsSchema(context.Background(), db); err != nil || !prepared {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, ownerBackfillSnapshots(t, db, "SELECT to_jsonb(api_keys)::text AS snapshot FROM api_keys ORDER BY id")) {
				t.Fatal("lost ledger regranted access")
			}
		})
	}
}

func TestAPIKeyPermissionsMigrationFromPreScopeSchema(t *testing.T) {
	db, _ := apiKeyOwnerBackfillDB(t)
	if err := db.Exec(`ALTER TABLE api_keys DROP COLUMN scopes, DROP COLUMN server_ids, DROP COLUMN permissions_revision, DROP COLUMN permissions_updated_at`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO api_keys (id,name,key_hash,key_prefix,created_at) VALUES (1,'hash only','unchanged-hash','prefix',now())`).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if prepared, err := PrepareAPIKeyPermissionsSchema(context.Background(), db); err != nil || !prepared {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&model.APIKey{}); err != nil {
			t.Fatal(err)
		}
	}
	var key model.APIKey
	if err := db.First(&key, 1).Error; err != nil {
		t.Fatal(err)
	}
	if key.KeyHash != "unchanged-hash" || key.EncryptedRawKey != "" || key.PermissionsRevision != 1 || key.PermissionsUpdatedAt != nil || len(key.ServerIDs) != 0 {
		t.Fatalf("unexpected migrated metadata: id %d", key.ID)
	}
	want := []string{"servers:read", "servers:exec", "servers:terminal", "metrics:read", "credentials:read", "services:read"}
	if !reflect.DeepEqual(key.Scopes, want) {
		t.Fatalf("legacy baseline = %v", key.Scopes)
	}
}

func TestAPIKeyPermissionsMigrationRejectsMalformedDataAtomically(t *testing.T) {
	for _, assignment := range []string{`scopes='{"private-payload":"do-not-log"}'`, `scopes='[1]'`, `server_ids='{}'`, `server_ids='[0]'`, `server_ids='[1.5]'`, `server_ids='["1"]'`} {
		t.Run(assignment, func(t *testing.T) {
			db, _ := apiKeyOwnerBackfillDB(t)
			insertOwnerBackfillKey(t, db, 1, 0, false)
			if err := db.Exec("UPDATE api_keys SET " + assignment).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("ALTER TABLE api_keys DROP COLUMN permissions_revision, DROP COLUMN permissions_updated_at").Error; err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareAPIKeyPermissionsSchema(context.Background(), db)
			if prepared || err == nil || !strings.Contains(err.Error(), "record 1") || strings.Contains(err.Error(), "do-not-log") {
				t.Fatalf("unsafe/missing error: %v", err)
			}
			if db.Migrator().HasColumn(&model.APIKey{}, "permissions_revision") {
				t.Fatal("failed migration partially committed DDL")
			}
			if err := db.Exec(`UPDATE api_keys SET scopes='["servers:read"]', server_ids='[9999]'`).Error; err != nil {
				t.Fatal(err)
			}
			if prepared, err := PrepareAPIKeyPermissionsSchema(context.Background(), db); err != nil || !prepared {
				t.Fatal(err)
			}
		})
	}
}

func TestAPIKeyPermissionsMigrationSignalsFreshInstall(t *testing.T) {
	db := newTestDB(t)
	prepared, err := PrepareAPIKeyPermissionsSchema(context.Background(), db)
	if err != nil || prepared {
		t.Fatalf("empty database reported prepared=%v, err=%v", prepared, err)
	}
	if db.Migrator().HasTable(&model.APIKey{}) {
		t.Fatal("preparation created a partial API key table")
	}
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	prepared, err = PrepareAPIKeyPermissionsSchema(context.Background(), db)
	if err != nil || !prepared {
		t.Fatalf("fresh schema reported prepared=%v, err=%v", prepared, err)
	}
	var count int64
	table, err := migrationTable(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Table(table).Where("id = ?", "2026-09-24-services-read-scope").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("fresh install did not retire historical automatic grant")
	}
}
