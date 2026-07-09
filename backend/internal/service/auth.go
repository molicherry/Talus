package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// errFirstUserTaken signals that another concurrent request created the first user.
var errFirstUserTaken = errors.New("first user already created")

// AuthService handles authentication including first-user bootstrap.
type AuthService struct {
	userRepo *repository.UserRepo
	jwtSvc   *token.JWTService
	db       *gorm.DB
}

// NewAuthService creates an AuthService with the given dependencies.
func NewAuthService(userRepo *repository.UserRepo, jwtSvc *token.JWTService, db *gorm.DB) *AuthService {
	return &AuthService{
		userRepo: userRepo,
		jwtSvc:   jwtSvc,
		db:       db,
	}
}

// NeedsSetup returns true if no users exist (initial setup required).
func (s *AuthService) NeedsSetup(ctx context.Context) (bool, error) {
	count, err := s.userRepo.Count(ctx)
	if err != nil {
		return false, fmt.Errorf("needs setup: %w", err)
	}
	return count == 0, nil
}

// Login authenticates a user or bootstraps the first admin account.
// If no users exist, the first Login call auto-creates an admin.
// Returns a signed JWT on success.
func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
	count, err := s.userRepo.Count(ctx)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}

	if count == 0 {
		return s.createFirstUser(ctx, username, password)
	}

	return s.authenticateExisting(ctx, username, password)
}

// createFirstUser inserts the initial admin account inside a transaction
// to detect concurrent first-user registration.
func (s *AuthService) createFirstUser(ctx context.Context, username, password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("login: bcrypt: %w", server.ErrInternal)
	}

	user := &model.User{
		Username:     username,
		PasswordHash: string(hash),
		Role:         "admin",
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var txCount int64
		if err := tx.Model(&model.User{}).Count(&txCount).Error; err != nil {
			return err
		}
		if txCount > 0 {
			return errFirstUserTaken
		}
		return tx.Create(user).Error
	})

	if err != nil {
		if errors.Is(err, errFirstUserTaken) {
			// Another request created the first user; fall back to normal authentication.
			return s.authenticateExisting(ctx, username, password)
		}
		return "", fmt.Errorf("login: %w", err)
	}

	return s.jwtSvc.GenerateToken(user.ID, user.Username, user.Role)
}

// authenticateExisting validates credentials for an existing user.
func (s *AuthService) authenticateExisting(ctx context.Context, username, password string) (string, error) {
	user, err := s.userRepo.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("login: invalid credentials: %w", server.ErrUnauthorized)
		}
		return "", fmt.Errorf("login: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", fmt.Errorf("login: invalid credentials: %w", server.ErrUnauthorized)
	}

	return s.jwtSvc.GenerateToken(user.ID, user.Username, user.Role)
}

func (s *AuthService) ChangePassword(ctx context.Context, userID uint, currentPassword, newPassword string) error {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return fmt.Errorf("current password is incorrect: %w", server.ErrUnauthorized)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("change password: bcrypt: %w", server.ErrInternal)
	}

	user.PasswordHash = string(hash)
	return s.userRepo.Update(ctx, user)
}
