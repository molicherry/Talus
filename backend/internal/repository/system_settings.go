package repository

import (
	"context"
	"errors"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ErrSettingsConflict = errors.New("settings revision conflict")

type SystemSettingsRepo struct{ db *gorm.DB }

func NewSystemSettingsRepo(db *gorm.DB) *SystemSettingsRepo {
	return &SystemSettingsRepo{db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})}
}

func (r *SystemSettingsRepo) Load(ctx context.Context) (model.SystemSettings, error) {
	var existing model.SystemSettings
	if err := r.db.WithContext(ctx).First(&existing, 1).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		return existing, err
	}
	row := model.SystemSettings{ID: 1, Revision: 1, Overrides: []byte(`{}`)}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return row, err
	}
	err := r.db.WithContext(ctx).First(&row, 1).Error
	return row, err
}

// Save commits the settings and the audit trail together. A failed audit insert
// cannot leave an unaudited configuration change in effect.
func (r *SystemSettingsRepo) Save(ctx context.Context, expected uint64, row model.SystemSettings, audit *model.AuditEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.SystemSettings{}).Where("id = 1 AND revision = ?", expected).Updates(map[string]any{"revision": row.Revision, "overrides": row.Overrides})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSettingsConflict
		}
		return tx.Create(audit).Error
	})
}
