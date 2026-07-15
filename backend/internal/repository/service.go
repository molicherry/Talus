package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// ServiceRepo provides database access for service records.
type ServiceRepo struct {
	db *gorm.DB
}

// NewServiceRepo creates a ServiceRepo backed by the given database handle.
func NewServiceRepo(db *gorm.DB) *ServiceRepo {
	return &ServiceRepo{db: db}
}

// FindAll returns all services ordered by name ascending.
func (r *ServiceRepo) FindAll(ctx context.Context) ([]model.Service, error) {
	var services []model.Service
	err := r.db.WithContext(ctx).Order("name ASC").Find(&services).Error
	if err != nil {
		return nil, err
	}
	return services, nil
}

// FindByServerID returns all services assigned to the given server.
func (r *ServiceRepo) FindByServerID(ctx context.Context, serverID uint) ([]model.Service, error) {
	var services []model.Service
	err := r.db.WithContext(ctx).Where("server_id = ?", serverID).Order("name ASC").Find(&services).Error
	if err != nil {
		return nil, err
	}
	return services, nil
}

// FindByID returns the service with the given primary key, or gorm.ErrRecordNotFound.
func (r *ServiceRepo) FindByID(ctx context.Context, id uint) (*model.Service, error) {
	var svc model.Service
	err := r.db.WithContext(ctx).First(&svc, id).Error
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

// Create inserts a new service record.
func (r *ServiceRepo) Create(ctx context.Context, svc *model.Service) error {
	return r.db.WithContext(ctx).Create(svc).Error
}
