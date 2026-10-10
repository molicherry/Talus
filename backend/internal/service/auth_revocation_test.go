package service

import (
	"context"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

func newRevocationAuth(t *testing.T) (*AuthService, *repository.UserRepo, *model.User, *gate.Registry) {
	t.Helper()
	db := authOwnerTestDB(t)
	repo := repository.NewUserRepo(db)
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "revoker", PasswordHash: string(hash), Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	registry := gate.NewRegistry()
	svc := NewAuthService(repo, token.NewJWTService("secret", time.Hour), db)
	svc.SetUserGate(gate.New(func(ctx context.Context, userID uint) (int64, error) {
		return repo.TokenVersion(ctx, userID)
	}))
	svc.SetTerminalRegistry(registry)
	return svc, repo, user, registry
}

// TestChangePasswordRevokesRegisteredTerminals pins REQ-05 §5.2: a successful
// password change marks the user's live terminals revoked and cancels them, and
// the cleanup join happens (bounded).
func TestChangePasswordRevokesRegisteredTerminals(t *testing.T) {
	svc, _, user, registry := newRevocationAuth(t)

	var canceled bool
	var sess *gate.Session
	sess = registry.Register(user.ID, 0, func() { canceled = true; sess.MarkDone() }, nil)
	if !registry.Admit(sess.ID) {
		t.Fatal("session should be admitted before the change")
	}

	if err := svc.ChangePassword(context.Background(), user.ID, "old-password", "new-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if !canceled {
		t.Fatal("a successful password change must cancel the user's terminals")
	}
	select {
	case <-sess.Done():
	default:
		t.Fatal("the revoked session must be joined before ChangePassword returns")
	}
	if sess.State() != gate.StateRevoked {
		t.Fatalf("state = %q, want revoked", sess.State())
	}
	if registry.LiveCount() != 0 {
		t.Fatalf("live sessions = %d, want 0", registry.LiveCount())
	}
}

// TestChangePasswordWrongCurrentDoesNotRevoke pins that a failed verification
// leaves live terminals untouched.
func TestChangePasswordWrongCurrentDoesNotRevoke(t *testing.T) {
	svc, _, user, registry := newRevocationAuth(t)

	var canceled bool
	var sess *gate.Session
	sess = registry.Register(user.ID, 0, func() { canceled = true; sess.MarkDone() }, nil)

	if err := svc.ChangePassword(context.Background(), user.ID, "wrong", "new-password"); err == nil {
		t.Fatal("expected a verification error")
	}
	if canceled {
		t.Fatal("a failed change must not revoke terminals")
	}
	if sess.State() != gate.StatePreparing {
		t.Fatalf("state = %q, want preparing", sess.State())
	}
	if registry.LiveCount() != 1 {
		t.Fatal("the session must still be live")
	}
}
