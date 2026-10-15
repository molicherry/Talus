package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/bcrypt"
)

// TestChangePasswordPreservesAPIKeyIdentity verifies session revocation does
// not change a key's ownership version, identity, or scope authorization.
func TestChangePasswordPreservesAPIKeyIdentity(t *testing.T) {
	svc, _, user, _ := newRevocationAuth(t)
	const rawKey = "test-password-independent-key"
	hash := sha256.Sum256([]byte(rawKey))
	key := model.APIKey{UserID: user.ID, Name: "automation", KeyHash: hex.EncodeToString(hash[:]), Scopes: []string{"servers:read"}}
	if err := svc.db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	keySvc := NewAPIKeyService(repository.NewAPIKeyRepo(svc.db), nil, nil)
	validator := mw.APIKeyIdentityValidatorFunc(func(ctx context.Context, raw string) (usage.Principal, error) {
		k, err := keySvc.Validate(ctx, raw)
		if err != nil {
			return usage.Principal{}, err
		}
		return usage.Principal{AuthType: "api_key", APIKeyID: &k.ID, UserID: &k.UserID, Scopes: k.Scopes, Role: "admin"}, nil
	})
	h := mw.Auth(svc.jwtSvc, validator, svc.gate)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := mw.GetPrincipal(r.Context())
		if p.AuthType != "api_key" || p.APIKeyID == nil || *p.APIKeyID != key.ID || p.UserID == nil || *p.UserID != user.ID {
			t.Errorf("key identity changed: %+v", p)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	check := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
		req.Header.Set("X-API-Key", rawKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("API key authorization = %d, want 204", w.Code)
		}
		stored, err := keySvc.Validate(context.Background(), rawKey)
		if err != nil || stored.OwnerBindingVersion != key.OwnerBindingVersion || stored.UserID != user.ID {
			t.Fatalf("key ownership changed: key=%+v err=%v", stored, err)
		}
	}
	check()
	if err := svc.ChangePassword(context.Background(), user.ID, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	check()
	if err := svc.ChangePassword(context.Background(), user.ID, "new-password", "newer-password"); err != nil {
		t.Fatal(err)
	}
	check()
}

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
	}, repo.LockedTokenVersion))
	svc.SetTerminalRegistry(registry)
	return svc, repo, user, registry
}

// TestChangePasswordRevokesRegisteredTerminals verifies that a successful
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

// TestChangePasswordCleanupSurvivesRequestCancellation verifies the committed
// change still joins local resources when its HTTP client disconnects at revoke.
func TestChangePasswordCleanupSurvivesRequestCancellation(t *testing.T) {
	svc, _, user, registry := newRevocationAuth(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var session *gate.Session
	finished := make(chan struct{})
	session = registry.Register(user.ID, 0, func() {
		cancel()
		go func() {
			time.Sleep(100 * time.Millisecond)
			session.MarkDone()
			close(finished)
		}()
	}, nil)
	if err := svc.ChangePassword(ctx, user.ID, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("client cancellation skipped post-commit cleanup")
	}
	if registry.LiveCount() != 0 {
		t.Fatal("completed session still registered")
	}
}

// TestChangePasswordReportsIncompleteCleanup pins the production three-second
// absolute budget and ensures timeout cannot become a successful HTTP response.
func TestChangePasswordReportsIncompleteCleanup(t *testing.T) {
	svc, _, user, registry := newRevocationAuth(t)
	var started time.Time
	var forced atomic.Bool
	session := registry.Register(user.ID, 0, func() { started = time.Now() }, func() { forced.Store(true) })
	defer registry.Done(session.ID)
	err := svc.ChangePassword(context.Background(), user.ID, "old-password", "new-password")
	requireAuthState(t, err, "terminal_cleanup_incomplete")
	if elapsed := time.Since(started); elapsed < 2900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("cleanup took %v, want the fixed three-second budget", elapsed)
	}
	if !forced.Load() {
		t.Fatal("cleanup never force-closed the stalled terminal")
	}
	if registry.LiveCount() != 1 {
		t.Fatal("timed-out terminal must stay registered until resources are reclaimed")
	}
	select {
	case <-session.Done():
		t.Fatal("timeout must not falsely mark resources reclaimed")
	default:
	}
}
