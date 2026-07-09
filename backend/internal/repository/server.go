package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// ServerRepo provides database access for server records.
type ServerRepo struct {
	db *gorm.DB
}

// NewServerRepo creates a ServerRepo backed by the given database handle.
func NewServerRepo(db *gorm.DB) *ServerRepo {
	return &ServerRepo{db: db}
}

// FindAll returns all servers ordered by name ascending.
func (r *ServerRepo) FindAll(ctx context.Context) ([]model.Server, error) {
	var servers []model.Server
	err := r.db.WithContext(ctx).Order("name ASC").Preload("Credential").Find(&servers).Error
	if err != nil {
		return nil, err
	}
	return servers, nil
}

// FindByID returns the server with the given primary key, or gorm.ErrRecordNotFound.
func (r *ServerRepo) FindByID(ctx context.Context, id uint) (*model.Server, error) {
	var server model.Server
	err := r.db.WithContext(ctx).Preload("Credential").First(&server, id).Error
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// Create inserts a new server record.
func (r *ServerRepo) Create(ctx context.Context, server *model.Server) error {
	return r.db.WithContext(ctx).Create(server).Error
}

// Update saves all fields of an existing server record.
func (r *ServerRepo) Update(ctx context.Context, server *model.Server) error {
	return r.db.WithContext(ctx).Save(server).Error
}

// Delete performs a soft delete of the server with the given id.
func (r *ServerRepo) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&model.Server{}, id).Error
}
