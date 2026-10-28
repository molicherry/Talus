package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
)

func TestBootstrapProofIsRequiredBeforeDatabaseAccess(t *testing.T) {
	proof := strings.Repeat("a", 64)
	for _, tc := range []struct{ name, configured, supplied string }{
		{"disabled", "", proof},
		{"missing", proof, ""},
		{"incorrect", proof, strings.Repeat("b", 64)},
		{"invalid_configuration", "short", "short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Nil dependencies make accidental database/credential work before
			// proof verification fail this test instead of masking it.
			svc := NewAuthService(nil, nil, nil)
			svc.SetBootstrapToken(tc.configured)
			if signed, err := svc.Bootstrap(context.Background(), tc.supplied, "owner", "test-password"); signed != "" || !errors.Is(err, server.ErrForbidden) {
				t.Fatalf("unproved setup returned a session: token present=%v err=%v", signed != "", err)
			}
		})
	}
}

func TestLoginNeverBootstrapsEmptyDatabase(t *testing.T) {
	db := authOwnerTestDB(t)
	repo := repository.NewUserRepo(db)
	svc := NewAuthService(repo, token.NewJWTService("dummy-jwt", time.Hour), db)
	svc.SetBootstrapToken(strings.Repeat("a", 64))
	if signed, err := svc.Login(context.Background(), "untrusted", "test-password"); signed != "" || !errors.Is(err, server.ErrInvalidCredentials) {
		t.Fatalf("empty-database login authenticated: token present=%v err=%v", signed != "", err)
	}
	if count, err := repo.Count(context.Background()); err != nil || count != 0 {
		t.Fatalf("ordinary login created a user: count=%d err=%v", count, err)
	}
}

func TestBootstrapOwnershipAndInitializedRestart(t *testing.T) {
	db := authOwnerTestDB(t)
	ctx := context.Background()
	repo := repository.NewUserRepo(db)
	jwt := token.NewJWTService("dummy-jwt", time.Hour)
	svc := NewAuthService(repo, jwt, db)
	proof := strings.Repeat("c", 64)
	if _, err := svc.Bootstrap(ctx, proof, "owner", "test-password"); !errors.Is(err, server.ErrForbidden) {
		t.Fatalf("disabled setup: %v", err)
	}
	svc.SetBootstrapToken(proof)
	if _, err := svc.Bootstrap(ctx, strings.Repeat("d", 64), "untrusted", "test-password"); !errors.Is(err, server.ErrForbidden) {
		t.Fatalf("incorrect proof: %v", err)
	}
	if count, err := repo.Count(ctx); err != nil || count != 0 {
		t.Fatalf("unproved setup changed users: count=%d err=%v", count, err)
	}
	signed, err := svc.Bootstrap(ctx, proof, "owner", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := jwt.ValidateToken(signed)
	if err != nil || claims.Username != "owner" || claims.Role != "admin" {
		t.Fatalf("setup did not create the owner: claims=%+v err=%v", claims, err)
	}
	if _, err := svc.Bootstrap(ctx, proof, "owner", "test-password"); !errors.Is(err, server.ErrConflict) {
		t.Fatalf("completed setup must reject even identical credentials: %v", err)
	}
	var users []model.User
	if err := db.Find(&users).Error; err != nil || len(users) != 1 {
		t.Fatalf("setup must create exactly one user: count=%d err=%v", len(users), err)
	}
	// The deployment token can be removed after setup. A restarted Hub must
	// continue authenticating the existing administrator without that token.
	restarted := NewAuthService(repo, jwt, db)
	if needed, err := restarted.NeedsSetup(ctx); err != nil || needed {
		t.Fatalf("restart reopened setup: needed=%v err=%v", needed, err)
	}
	if _, err := restarted.Login(ctx, "owner", "test-password"); err != nil {
		t.Fatalf("initialized instance needs no setup token for login: %v", err)
	}
	if _, err := restarted.Bootstrap(ctx, proof, "second-owner", "test-password"); !errors.Is(err, server.ErrForbidden) {
		t.Fatalf("restart accepted setup without configured proof: %v", err)
	}
}
