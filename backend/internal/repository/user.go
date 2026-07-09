package repository

import (
	"context"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
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
