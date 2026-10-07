package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
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

// NormalizeLegacyOwnerNulls runs before AutoMigrate adds the owner default and
// NOT NULL constraint. Both NULL and zero represent an unassigned legacy key.
// Fresh databases and older tables without the column need no normalization.
func (r *APIKeyRepo) NormalizeLegacyOwnerNulls(ctx context.Context) error {
	return r.db.WithContext(ctx).Exec(`
		DO $talus_key_owner$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				AND table_name = 'api_keys' AND column_name = 'user_id'
			) THEN
				UPDATE api_keys SET user_id = 0 WHERE user_id IS NULL;
			END IF;
		END $talus_key_owner$`).Error
}

// BindUnownedToDefaultAdmin assigns legacy keys to the first active admin.
// The conditional update is safe to repeat or run concurrently: existing owners
// and all key material/permissions remain unchanged. Without an admin, no rows
// change, so initial setup or a later startup can retry the assignment.
func (r *APIKeyRepo) BindUnownedToDefaultAdmin(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).Exec(`
		WITH default_admin AS (
			SELECT id FROM users
			WHERE role = 'admin' AND deleted_at IS NULL
			ORDER BY id ASC LIMIT 1
		)
		UPDATE api_keys SET user_id = default_admin.id
		FROM default_admin
		WHERE api_keys.user_id = 0 OR api_keys.user_id IS NULL`)
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
