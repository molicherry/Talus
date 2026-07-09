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

func (r *APIKeyRepo) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&model.APIKey{}, id).Error
}
