package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ErrAPIKeyCommitUnknown = errors.New("api key transaction outcome unknown")

// PrepareAPIKeyPermissionsSchema runs before AutoMigrate (to recognize an
// absent scopes column) and afterwards (for fresh installs). It never derives
// existing permissions from today's defaults or from a missing migration marker.
func PrepareAPIKeyPermissionsSchema(ctx context.Context, db *gorm.DB) error {
	db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	var reviewCount int64
	var reviewIDs []uint
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '5s'").Error; err != nil {
			return err
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "talus_api_key_permissions_v1").Error; err != nil {
			return err
		}
		var exists bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'api_keys')`).Scan(&exists).Error; err != nil {
			return err
		}
		if !exists {
			return nil
		}
		var hadScopes bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'api_keys' AND column_name = 'scopes')`).Scan(&hadScopes).Error; err != nil {
			return err
		}
		// This literal is the frozen legacy upgrade baseline, not a reference to
		// AllScopeGatedScopes, which may change for future keys.
		for _, sql := range []string{
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scopes jsonb DEFAULT '["servers:read","servers:exec","servers:terminal","metrics:read","credentials:read","services:read"]'::jsonb`,
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS server_ids jsonb`,
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS permissions_revision bigint NOT NULL DEFAULT 1`,
			`ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS permissions_updated_at timestamptz`,
		} {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
		// Only IDs leave this validation query; malformed data must not be
		// silently converted to unrestricted access or included in an error log.
		var bad struct{ ID uint }
		if err := tx.Raw(`SELECT id FROM api_keys WHERE permissions_revision IS NULL OR permissions_revision <= 0
			OR NOT (CASE WHEN scopes IS NULL OR scopes = 'null'::jsonb THEN true
			 WHEN jsonb_typeof(scopes) = 'array' THEN NOT EXISTS (SELECT 1 FROM jsonb_array_elements(scopes) AS v WHERE jsonb_typeof(v) <> 'string') ELSE false END)
			OR NOT (CASE WHEN server_ids IS NULL OR server_ids = 'null'::jsonb THEN true
			 WHEN jsonb_typeof(server_ids) = 'array' THEN NOT EXISTS (SELECT 1 FROM jsonb_array_elements(server_ids) AS v
			 WHERE CASE WHEN jsonb_typeof(v) = 'number' AND (v #>> '{}') ~ '^[1-9][0-9]*$'
			 THEN (v #>> '{}')::numeric > 9223372036854775807 ELSE true END) ELSE false END)
			ORDER BY id LIMIT 1`).Scan(&bad).Error; err != nil {
			return err
		}
		if bad.ID != 0 {
			return fmt.Errorf("invalid API key permissions: record %d", bad.ID)
		}
		table, err := migrationTable(tx)
		if err != nil {
			return err
		}
		var marked int64
		if err := tx.Table(table).Where("id = ?", "2026-09-24-services-read-scope").Count(&marked).Error; err != nil {
			return err
		}
		if hadScopes && marked == 0 {
			candidates := tx.Table("api_keys").Where(`scopes IS NOT NULL AND scopes <> 'null'::jsonb AND NOT scopes @> '["services:read"]'::jsonb AND NOT scopes @> '["*"]'::jsonb`)
			if err := candidates.Count(&reviewCount).Error; err != nil {
				return err
			}
			if err := candidates.Order("id").Limit(20).Pluck("id", &reviewIDs).Error; err != nil {
				return err
			}
		}
		// Retire the old scope grant, even if its marker was lost/restored. An
		// existing explicit omission must survive every restart. Never run UPDATE.
		for _, id := range []string{"2026-09-24-services-read-scope", "2026-10-23-api-key-permissions-v1-retires-services-backfill"} {
			if err := tx.Exec("INSERT INTO "+table+" (id) VALUES (?) ON CONFLICT (id) DO NOTHING", id).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && reviewCount > 0 {
		slog.Warn("legacy service-read permissions retained without granting access; review in API Keys", "count", reviewCount, "sample_api_key_ids", reviewIDs)
	}
	return err
}

// Missing keys have revision zero so a deleted key can still be fenced and its
// terminals revoked; zero never permits authentication.
func (r *APIKeyRepo) PermissionsRevision(ctx context.Context, id uint) (int64, error) {
	return r.permissionsRevision(ctx, id, false)
}

func (r *APIKeyRepo) LockedPermissionsRevision(ctx context.Context, id uint) (int64, error) {
	return r.permissionsRevision(ctx, id, true)
}

func (r *APIKeyRepo) permissionsRevision(ctx context.Context, id uint, lock bool) (int64, error) {
	db := r.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	var key model.APIKey
	read := func(tx *gorm.DB) error {
		if lock {
			tx = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		return tx.Select("id", "permissions_revision").First(&key, id).Error
	}
	var err error
	if lock {
		err = db.Transaction(read)
	} else {
		err = read(db)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return key.PermissionsRevision, err
}

// UpdatePermissions locks the current policy and persists only the permission
// columns and its audit event. Unrelated owner/crypto backfills cannot be lost.
func (r *APIKeyRepo) UpdatePermissions(ctx context.Context, id uint, change func(*gorm.DB, *model.APIKey) (*model.AuditEvent, error)) (*model.APIKey, bool, error) {
	tx := r.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Begin()
	if tx.Error != nil {
		return nil, false, tx.Error
	}
	defer tx.Rollback()
	var key model.APIKey
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&key, id).Error; err != nil {
		return nil, false, err
	}
	previousScopes, previousServers := slices.Clone(key.Scopes), slices.Clone(key.ServerIDs)
	event, err := change(tx, &key)
	if err != nil {
		return nil, false, err
	}
	if event == nil {
		return &key, false, nil
	}
	now := time.Now().UTC()
	key.PermissionsRevision++
	key.PermissionsUpdatedAt = &now
	scopes, err := json.Marshal(key.Scopes)
	if err != nil {
		return nil, false, err
	}
	ids, err := json.Marshal(key.ServerIDs)
	if err != nil {
		return nil, false, err
	}
	columns := map[string]any{"permissions_revision": key.PermissionsRevision, "permissions_updated_at": now}
	if !slices.Equal(previousScopes, key.Scopes) {
		columns["scopes"] = gorm.Expr("?::jsonb", string(scopes))
	}
	if !slices.Equal(previousServers, key.ServerIDs) {
		columns["server_ids"] = gorm.Expr("?::jsonb", string(ids))
	}
	if err := tx.Model(&model.APIKey{}).Where("id = ?", id).Updates(columns).Error; err != nil {
		return nil, false, err
	}
	if err := tx.Create(event).Error; err != nil {
		return nil, false, ErrAuditStorage
	}
	if err := tx.Commit().Error; err != nil {
		if commitDefinitelyRolledBack(err) {
			return nil, false, err
		}
		return nil, false, ErrAPIKeyCommitUnknown
	}
	return &key, true, nil
}
