package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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
	Update(ctx context.Context, userID uint, fn func(context.Context, int64) (bool, error)) error
	Recover(ctx context.Context, userID uint) error
}

// AuthService handles authentication including first-user bootstrap.
type AuthService struct {
	userRepo             *repository.UserRepo
	jwtSvc               *token.JWTService
	db                   *gorm.DB
	ownerBackfillTrigger func()
	gate                 userGate
	terminalRegistry     *gate.Registry
	bootstrapTokenHash   *[sha256.Size]byte
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
		gate:     gate.New(userRepo.TokenVersion, userRepo.LockedTokenVersion),
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

// SetBootstrapToken configures the independent, deployment-only ownership proof.
// Missing or invalid configuration disables setup; existing-user login still works.
// Call only during startup, before accepting requests. Retain only its hash here.
func (s *AuthService) SetBootstrapToken(value string) {
	s.bootstrapTokenHash = nil
	if len(value) >= 32 && len(value) <= 256 {
		hash := sha256.Sum256([]byte(value))
		s.bootstrapTokenHash = &hash
	}
}

// Login authenticates an existing user. It never creates an administrator.
func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
	return s.authenticateExisting(ctx, username, password)
}

// Bootstrap creates the first administrator only for a caller who proves
// deployment ownership. The database lock below makes the empty check and
// creation atomic across Hub instances. Initialized instances cannot be claimed.
func (s *AuthService) Bootstrap(ctx context.Context, proof, username, password string) (string, error) {
	provided := sha256.Sum256([]byte(proof))
	if s.bootstrapTokenHash == nil || subtle.ConstantTimeCompare(provided[:], s.bootstrapTokenHash[:]) != 1 {
		return "", server.ErrForbidden
	}
	count, err := s.userRepo.Count(ctx)
	if err != nil {
		return "", fmt.Errorf("bootstrap: %w", err)
	}
	if count != 0 {
		return "", server.ErrConflict
	}
	return s.createFirstUser(ctx, username, password)
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
			// Setup is one-shot, even when racing callers chose the same credentials.
			return "", server.ErrConflict
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
	var revoked []*gate.Session
	changeErr := s.gate.Update(ctx, userID, func(updateCtx context.Context, _ int64) (bool, error) {
		_, err := s.userRepo.ChangePassword(updateCtx, userID, verify, string(hash))
		unknown := errors.Is(err, repository.ErrCommitUnknown)
		// A lost acknowledgement may already have changed the password. Fence
		// and revoke while still holding the admission lock, just as on success.
		if (err == nil || unknown) && s.terminalRegistry != nil {
			revoked = s.terminalRegistry.MarkRevoked(userID)
		}
		return unknown, err
	})

	unknown := errors.Is(changeErr, repository.ErrCommitUnknown)
	var recovered chan error
	if unknown {
		recovered = make(chan error, 1)
		go func() {
			// HTTP cancellation cannot abandon reconciliation. Recover has its
			// own bounded deadline and leaves the fence intact on failure.
			recovered <- s.gate.Recover(context.WithoutCancel(ctx), userID)
		}()
	}
	// Closing and joining happen outside the user lock and share the deadline
	// frozen by MarkRevoked. A disconnected HTTP client cannot skip this work.
	var cleanupErr error
	if s.terminalRegistry != nil && len(revoked) > 0 {
		result := s.terminalRegistry.Await(context.WithoutCancel(ctx), revoked)
		if !result.Complete {
			cleanupErr = fmt.Errorf("terminal cleanup deadline exceeded: %d sessions pending", result.Pending)
			slog.Error("password revocation cleanup incomplete", "user_id", userID, "pending", result.Pending, "forced", result.Forced, "error", cleanupErr)
		}
	}
	if recovered != nil {
		if err := <-recovered; err != nil {
			slog.Warn("password commit remains unresolved", "user_id", userID, "error", err)
		}
	}
	if unknown {
		err := server.NewAppErrorParams(http.StatusServiceUnavailable, server.ReasonUnavailable, map[string]any{"state": "password_commit_unknown"})
		err.Err = errors.Join(changeErr, cleanupErr)
		return err
	}
	if cleanupErr != nil {
		err := server.NewAppErrorParams(http.StatusServiceUnavailable, server.ReasonUnavailable, map[string]any{"state": "terminal_cleanup_incomplete"})
		err.Err = cleanupErr
		return err
	}
	if errors.Is(changeErr, gate.ErrUnavailable) || errors.Is(changeErr, context.DeadlineExceeded) || errors.Is(changeErr, context.Canceled) {
		err := server.NewAppError(http.StatusServiceUnavailable, server.ReasonUnavailable)
		err.Err = changeErr
		return err
	}
	return changeErr
}
