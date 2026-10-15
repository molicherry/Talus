package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

func seedVersionedUser(t *testing.T, hash string) (*UserRepo, *model.User) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "u", PasswordHash: hash, Role: "admin"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return NewUserRepo(db), user
}

func TestChangePasswordBumpsVersionOncePerCall(t *testing.T) {
	repo, user := seedVersionedUser(t, "old-hash")
	ctx := context.Background()

	v1, err := repo.ChangePassword(ctx, user.ID, expectHash("old-hash"), "new-hash-1")
	if err != nil {
		t.Fatalf("first change: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("version after first change = %d, want 1", v1)
	}
	v2, err := repo.ChangePassword(ctx, user.ID, expectHash("new-hash-1"), "new-hash-2")
	if err != nil {
		t.Fatalf("second change: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("version after second change = %d, want 2", v2)
	}
	got, err := repo.TokenVersion(ctx, user.ID)
	if err != nil || got != 2 {
		t.Fatalf("TokenVersion = %d, %v; want 2", got, err)
	}
}

func TestChangePasswordWrongPasswordKeepsVersion(t *testing.T) {
	repo, user := seedVersionedUser(t, "old-hash")
	if _, err := repo.ChangePassword(context.Background(), user.ID,
		func(string) error { return errors.New("current password is incorrect") }, "new-hash"); err == nil {
		t.Fatal("expected the change to fail")
	}
	got, err := repo.TokenVersion(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("version changed on a failed update: %d", got)
	}
}

// TestChangePasswordConcurrentBumpsExactlyOnce pins the row lock: with the same
// original password, exactly one of several concurrent updates succeeds (the
// others re-read the bumped hash and fail verification), so the version is never
// double-incremented or lost.
func TestChangePasswordConcurrentBumpsExactlyOnce(t *testing.T) {
	repo, user := seedVersionedUser(t, "old-hash")

	const workers = 6
	var wg sync.WaitGroup
	var successes int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := repo.ChangePassword(context.Background(), user.ID, expectHash("old-hash"), "new-hash"); err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}
	got, err := repo.TokenVersion(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("version = %d, want 1", got)
	}
}

func expectHash(want string) func(string) error {
	return func(current string) error {
		if current != want {
			return errors.New("current password is incorrect")
		}
		return nil
	}
}
