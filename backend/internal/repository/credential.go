package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// CredentialRepo provides database access for SSH credential records.
type CredentialRepo struct {
	db *gorm.DB
}

// NewCredentialRepo creates a CredentialRepo backed by the given database handle.
func NewCredentialRepo(db *gorm.DB) *CredentialRepo {
	return &CredentialRepo{db: db}
}

// FindByServerID returns the credential for the given server, or gorm.ErrRecordNotFound.
func (r *CredentialRepo) FindByServerID(ctx context.Context, serverID uint) (*model.SSHCredential, error) {
	var cred model.SSHCredential
	err := r.db.WithContext(ctx).Where("server_id = ?", serverID).First(&cred).Error
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// FindByID returns the credential with the given id, or gorm.ErrRecordNotFound.
func (r *CredentialRepo) FindByID(ctx context.Context, id uint) (*model.SSHCredential, error) {
	var cred model.SSHCredential
	err := r.db.WithContext(ctx).First(&cred, id).Error
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// FindAll returns all credential records.
func (r *CredentialRepo) FindAll(ctx context.Context) ([]model.SSHCredential, error) {
	var creds []model.SSHCredential
	err := r.db.WithContext(ctx).Find(&creds).Error
	if err != nil {
		return nil, err
	}
	return creds, nil
}

// Create inserts a new credential record.
func (r *CredentialRepo) Create(ctx context.Context, cred *model.SSHCredential) error {
	return r.db.WithContext(ctx).Create(cred).Error
}

// Update saves changes to an existing credential.
func (r *CredentialRepo) Update(ctx context.Context, cred *model.SSHCredential) error {
	return r.db.WithContext(ctx).Save(cred).Error
}

// Delete performs a soft delete of the credential with the given id.
func (r *CredentialRepo) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&model.SSHCredential{}, id).Error
}

// DeleteByServerID performs a soft delete of credentials for the given server.
func (r *CredentialRepo) DeleteByServerID(ctx context.Context, serverID uint) error {
	return r.db.WithContext(ctx).Where("server_id = ?", serverID).Delete(&model.SSHCredential{}).Error
}
