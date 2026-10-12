package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// LockedTokenVersion waits for any earlier password transaction to finish before
// observing its final version. A plain SELECT can see the old MVCC row while
// COMMIT is unresolved and must not be used for uncertainty recovery.
func (r *UserRepo) LockedTokenVersion(ctx context.Context, id uint) (int64, error) {
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return 0, tx.Error
	}
	defer tx.Rollback()
	var user model.User
	if err := tx.Select("id", "token_version").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&user).Error; err != nil {
		return 0, err
	}
	return user.TokenVersion, nil
}

// ErrCommitUnknown distinguishes a lost COMMIT acknowledgement from a known
// rollback. No caller may assume this error means the password stayed unchanged.
var ErrCommitUnknown = errors.New("password transaction commit outcome unknown")

// commitDefinitelyRolledBack recognizes explicit PostgreSQL rejection of COMMIT.
// Transport failures (including EOF, canceled connections and timeouts) remain
// uncertain, even if a driver also attempted a best-effort rollback.
func commitDefinitelyRolledBack(err error) bool {
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "25P02":
			return true
		}
	}
	return false
}

// ChangePassword locks, verifies and increments in one transaction. Begin and
// statement errors precede COMMIT and are known failures; commit transport errors
// are returned as ErrCommitUnknown for the admission fence and active revocation.
func (r *UserRepo) ChangePassword(ctx context.Context, id uint, verify func(currentHash string) error, newHash string) (int64, error) {
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return 0, tx.Error
	}
	defer tx.Rollback()
	var user model.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&user).Error; err != nil {
		return 0, err
	}
	if err := verify(user.PasswordHash); err != nil {
		return 0, err
	}
	if err := tx.Model(&model.User{}).Where("id = ?", id).Updates(map[string]any{
		"password_hash": newHash, "token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		return 0, err
	}
	if err := tx.Commit().Error; err != nil {
		if commitDefinitelyRolledBack(err) {
			return 0, err
		}
		return 0, fmt.Errorf("%w: %v", ErrCommitUnknown, err)
	}
	return user.TokenVersion + 1, nil
}
