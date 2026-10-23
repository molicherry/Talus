package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Rotate replaces only credential material and the shared admission revision.
// Even physical NULL legacy policies and concurrent owner backfills survive.
func (r *APIKeyRepo) Rotate(ctx context.Context, id uint, material *model.APIKey, audit func(*model.APIKey) (*model.AuditEvent, error)) (*model.APIKey, error) {
	tx := r.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var key model.APIKey
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&key, id).Error; err != nil {
		return nil, err
	}
	event, err := audit(&key)
	if err != nil {
		return nil, err
	}
	key.PermissionsRevision++
	key.KeyHash, key.KeyPrefix = material.KeyHash, material.KeyPrefix
	key.EncryptedRawKey, key.Salt, key.CryptoVersion = material.EncryptedRawKey, material.Salt, material.CryptoVersion
	columns := map[string]any{
		"key_hash": key.KeyHash, "key_prefix": key.KeyPrefix,
		"encrypted_raw_key": key.EncryptedRawKey, "salt": key.Salt,
		"crypto_version": key.CryptoVersion, "permissions_revision": key.PermissionsRevision,
	}
	if err := tx.Model(&model.APIKey{}).Where("id = ?", id).Updates(columns).Error; err != nil {
		return nil, err
	}
	if err := tx.Create(event).Error; err != nil {
		return nil, ErrAuditStorage
	}
	if err := tx.Commit().Error; err != nil {
		if commitDefinitelyRolledBack(err) {
			return nil, err
		}
		return nil, ErrAPIKeyCommitUnknown
	}
	return &key, nil
}
