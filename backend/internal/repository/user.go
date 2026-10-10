package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserRepo provides database access for user records.
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo creates a UserRepo backed by the given database handle.
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

// FindByUsername returns the user with the given username, or gorm.ErrRecordNotFound.
func (r *UserRepo) FindByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByID returns the user with the given primary key, or gorm.ErrRecordNotFound.
func (r *UserRepo) FindByID(ctx context.Context, id uint) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Create inserts a new user record.
func (r *UserRepo) Create(ctx context.Context, user *model.User) error {
	return r.db.WithContext(ctx).Create(user).Error
}

// Count returns the total number of users (used for first-user detection).
func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.User{}).Count(&count).Error
	return count, err
}

// Update saves changes to an existing user record.
func (r *UserRepo) Update(ctx context.Context, user *model.User) error {
	return r.db.WithContext(ctx).Save(user).Error
}

// TokenVersion reads the current session version from the primary database.
// It returns gorm.ErrRecordNotFound for a missing user, so a deleted account's
// token cannot be accepted as version 0.
func (r *UserRepo) TokenVersion(ctx context.Context, id uint) (int64, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Select("id", "token_version").Where("id = ?", id).First(&user).Error; err != nil {
		return 0, err
	}
	return user.TokenVersion, nil
}

// ChangePassword verifies the current password hash and bumps the session
// version in one transaction. The row is locked FOR UPDATE, so concurrent
// updates cannot interleave and cannot lose a version increment. It returns the
// new version.
func (r *UserRepo) ChangePassword(ctx context.Context, id uint, verify func(currentHash string) error, newHash string) (int64, error) {
	var newVersion int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&user).Error; err != nil {
			return err
		}
		if err := verify(user.PasswordHash); err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", id).
			Updates(map[string]any{"password_hash": newHash, "token_version": gorm.Expr("token_version + 1")}).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ?", id).Select("token_version").Scan(&newVersion).Error
	})
	if err != nil {
		return 0, err
	}
	return newVersion, nil
}
