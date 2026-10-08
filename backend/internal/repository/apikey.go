package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type APIKeyRepo struct {
	db *gorm.DB
}

func NewAPIKeyRepo(db *gorm.DB) *APIKeyRepo {
	return &APIKeyRepo{db: db}
}

func (r *APIKeyRepo) Create(ctx context.Context, key *model.APIKey) error {
	return r.db.WithContext(ctx).Create(key).Error
}

// PrepareLegacyOwnerBinding fixes the upgrade boundary as part of schema
// migration. Existing rows receive version zero, then the database default is
// changed to one in the same transaction. Later inserts can never enter the
// legacy set, even while assignment retries or no administrator exists.
func (r *APIKeyRepo) PrepareLegacyOwnerBinding(ctx context.Context) error {
	db := r.db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize schema preparation across instances; it performs no owner
		// updates and is separate from the first-administrator setup lock.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(0x54414c55534b4559)).Error; err != nil {
			return err
		}
		var tableExists, versionExists, ownerExists bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = 'api_keys')`).Scan(&tableExists).Error; err != nil {
			return err
		}
		if !tableExists {
			return nil
		}
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'api_keys'
			AND column_name = 'owner_binding_version')`).Scan(&versionExists).Error; err != nil {
			return err
		}
		if versionExists {
			return nil
		}
		if err := tx.Exec("ALTER TABLE api_keys ADD COLUMN owner_binding_version smallint NOT NULL DEFAULT 0").Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'api_keys'
			AND column_name = 'user_id')`).Scan(&ownerExists).Error; err != nil {
			return err
		}
		if ownerExists {
			// Only keys unowned at this boundary are migration candidates. An
			// already-owned key must remain excluded if its owner later clears.
			if err := tx.Exec("UPDATE api_keys SET owner_binding_version = 1 WHERE user_id <> 0").Error; err != nil {
				return err
			}
		}
		return tx.Exec("ALTER TABLE api_keys ALTER COLUMN owner_binding_version SET DEFAULT 1").Error
	})
}

const APIKeyOwnerBackfillBatchSize = 100

// BindUnownedToDefaultAdmin assigns a bounded batch of pre-upgrade keys. It is
// safe to retry or run concurrently and never touches later unowned keys.
func (r *APIKeyRepo) BindUnownedToDefaultAdmin(ctx context.Context) (int64, error) {
	db := r.db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	result := db.WithContext(ctx).Exec(`
		WITH default_admin AS (
			SELECT id FROM users
			WHERE role = 'admin' AND deleted_at IS NULL
			ORDER BY id ASC LIMIT 1
		), batch AS (
			SELECT k.id FROM api_keys k CROSS JOIN default_admin
			WHERE k.owner_binding_version = 0 AND (k.user_id = 0 OR k.user_id IS NULL)
			ORDER BY k.id ASC FOR UPDATE OF k SKIP LOCKED LIMIT ?
		)
		UPDATE api_keys k SET user_id = default_admin.id, owner_binding_version = 1
		FROM default_admin, batch
		WHERE k.id = batch.id AND k.owner_binding_version = 0
		AND (k.user_id = 0 OR k.user_id IS NULL)`, APIKeyOwnerBackfillBatchSize)
	return result.RowsAffected, result.Error
}

func (r *APIKeyRepo) FindAll(ctx context.Context) ([]model.APIKey, error) {
	var keys []model.APIKey
	err := r.db.WithContext(ctx).Order("created_at DESC").Find(&keys).Error
	return keys, err
}

func (r *APIKeyRepo) FindByHash(ctx context.Context, hash string) (*model.APIKey, error) {
	var key model.APIKey
	err := r.db.WithContext(ctx).Where("key_hash = ?", hash).First(&key).Error
	if err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *APIKeyRepo) FindByID(ctx context.Context, id uint) (*model.APIKey, error) {
	var key model.APIKey
	err := r.db.WithContext(ctx).First(&key, id).Error
	if err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *APIKeyRepo) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&model.APIKey{}, id).Error
}
