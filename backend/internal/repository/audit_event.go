package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

type AuditEventRepo struct {
	db *gorm.DB
}

func NewAuditEventRepo(db *gorm.DB) *AuditEventRepo {
	return &AuditEventRepo{db: db}
}

func (r *AuditEventRepo) Create(ctx context.Context, event *model.AuditEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}
