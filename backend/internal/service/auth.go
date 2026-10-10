package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// errFirstUserTaken signals that another request initialized the administrator
// before this request acquired the setup transaction lock.
var errFirstUserTaken = errors.New("first user already created")

const firstUserSetupLock int64 = 0x54414c5553415554 // "TALUSAUT", database-wide setup lock.

// userGate serializes admission and password updates for one user. *gate.Gate
// satisfies it; tests may supply a stub.
type userGate interface {
	Admit(ctx context.Context, userID uint, fn func(version int64) error) error
}

// AuthService handles authentication including first-user bootstrap.
type AuthService struct {
	userRepo             *repository.UserRepo
	jwtSvc               *token.JWTService
	db                   *gorm.DB
	ownerBackfillTrigger func()
	gate                 userGate
	terminalRegistry     *gate.Registry
}

// SetOwnerBackfillTrigger connects optional background work to successful
// initial setup. The callback must be nonblocking and is configured at startup.
func (s *AuthService) SetOwnerBackfillTrigger(trigger func()) {
	s.ownerBackfillTrigger = trigger
}

// SetUserGate wires the shared per-user gate so a password change and JWT
// admission for the same user are serialized.
func (s *AuthService) SetUserGate(g userGate) {
	s.gate = g
}

// SetTerminalRegistry wires the JWT terminal registry so a successful password
// change revokes the user's existing terminals.
func (s *AuthService) SetTerminalRegistry(r *gate.Registry) {
	s.terminalRegistry = r
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

// createFirstUser serializes initial administrator creation across instances.
// Optional legacy key assignment runs separately after the user commits.
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
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", firstUserSetupLock).Error; err != nil {
			return err
		}
		var txCount int64
		if err := tx.Model(&model.User{}).Count(&txCount).Error; err != nil {
			return err
		}
		if txCount > 0 {
			return errFirstUserTaken
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, errFirstUserTaken) {
			// Another request created the first user; fall back to normal authentication.
			return s.authenticateExisting(ctx, username, password)
		}
		return "", fmt.Errorf("login: %w", err)
	}

	if s.ownerBackfillTrigger != nil {
		s.ownerBackfillTrigger()
	}
	return s.jwtSvc.GenerateToken(user.ID, user.Username, user.Role, user.TokenVersion)
}

// authenticateExisting validates credentials for an existing user.
func (s *AuthService) authenticateExisting(ctx context.Context, username, password string) (string, error) {
	user, err := s.userRepo.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("login: invalid credentials: %w", server.ErrInvalidCredentials)
		}
		return "", fmt.Errorf("login: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", fmt.Errorf("login: invalid credentials: %w", server.ErrInvalidCredentials)
	}

	return s.jwtSvc.GenerateToken(user.ID, user.Username, user.Role, user.TokenVersion)
}

func (s *AuthService) ChangePassword(ctx context.Context, userID uint, currentPassword, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("change password: bcrypt: %w", server.ErrInternal)
	}
	verify := func(currentHash string) error {
		if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(currentPassword)); err != nil {
			return fmt.Errorf("current password is incorrect: %w", server.ErrWrongCurrentPassword)
		}
		return nil
	}
	change := func() error {
		_, err := s.userRepo.ChangePassword(ctx, userID, verify, string(hash))
		return err
	}
	// apply runs the change and, inside the gate, marks the user's existing JWT
	// sessions revoked (COMMIT is the revocation linearization point).
	apply := func() ([]*gate.Session, error) {
		if err := change(); err != nil {
			return nil, err
		}
		if s.terminalRegistry == nil {
			return nil, nil
		}
		return s.terminalRegistry.MarkRevoked(userID), nil
	}

	var revoked []*gate.Session
	var changeErr error
	if s.gate == nil {
		// Legacy wiring without a gate: the repository transaction still makes the
		// password update and version bump atomic.
		revoked, changeErr = apply()
	} else {
		// The version is read after acquiring the gate, so it cannot be older than
		// a concurrent update's commit.
		changeErr = s.gate.Admit(ctx, userID, func(int64) error {
			var err error
			revoked, err = apply()
			return err
		})
	}
	if changeErr != nil {
		return changeErr
	}
	// Close and join outside the gate.
	if s.terminalRegistry != nil && len(revoked) > 0 {
		s.terminalRegistry.Await(ctx, revoked)
	}
	return nil
}
