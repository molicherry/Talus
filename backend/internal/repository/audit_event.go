package repository

import (
	"context"
	"errors"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ErrAuditStorage = errors.New("audit_storage_unavailable")

func auditErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrAuditStorage
}

type AuditEventRepo struct {
	db *gorm.DB
}

func NewAuditEventRepo(db *gorm.DB) *AuditEventRepo {
	return &AuditEventRepo{db: db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})}
}

func (r *AuditEventRepo) Create(ctx context.Context, event *model.AuditEvent) error {
	if event.OperationID == nil {
		return auditErr(ctx, r.db.WithContext(ctx).Create(event).Error)
	}
	// A client-side timeout does not tell us whether PostgreSQL committed.
	// Retrying the same operation never adds another security event.
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "operation_id"}}, DoNothing: true,
	}).Create(event).Error; err != nil {
		return auditErr(ctx, err)
	}
	if event.ID == 0 {
		return auditErr(ctx, r.db.WithContext(ctx).Unscoped().Where("operation_id = ?", *event.OperationID).First(event).Error)
	}
	return nil
}
