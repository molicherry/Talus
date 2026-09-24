package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/server"
)

func TestResolveScopes(t *testing.T) {
	empty := []string{}
	ops := []string{"servers:read", "metrics:read"}

	t.Run("omitted scopes fall back to the default set", func(t *testing.T) {
		got, err := resolveScopes(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != len(model.AllScopeGatedScopes) {
			t.Fatalf("got %v, want the %d default scopes", got, len(model.AllScopeGatedScopes))
		}
		// Must be a copy: mutating the result must not corrupt the shared default.
		got[0] = "mutated"
		if model.AllScopeGatedScopes[0] == "mutated" {
			t.Fatal("resolveScopes returned the shared default slice, not a copy")
		}
	})

	t.Run("an explicit empty list is rejected, not defaulted", func(t *testing.T) {
		_, err := resolveScopes(&empty)
		if err == nil {
			t.Fatal("expected an error for an explicit empty scope list")
		}
		var appErr *server.AppError
		if !errors.As(err, &appErr) {
			t.Fatalf("got %T, want *server.AppError", err)
		}
		if appErr.Reason != server.ReasonScopesRequired {
			t.Fatalf("reason = %q, want %q", appErr.Reason, server.ReasonScopesRequired)
		}
	})

	t.Run("a non-empty list is passed through unchanged", func(t *testing.T) {
		got, err := resolveScopes(&ops)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != len(ops) {
			t.Fatalf("got %v, want %v", got, ops)
		}
	})
}

func TestCreateRejectsExplicitEmptyScopes(t *testing.T) {
	// No repos needed: resolveScopes rejects before any repository call.
	svc := NewAPIKeyService(nil, nil, nil)
	empty := []string{}
	if _, err := svc.Create(context.Background(), "name", &empty, nil); err == nil {
		t.Fatal("expected Create to reject an explicit empty scope list")
	}
}
