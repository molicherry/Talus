package repository

import (
	"sync"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

// TestPrepareUserTokenVersionEmptyDatabase exercises the production ordering:
// explicit security migration first, then the ordinary model migration.
func TestPrepareUserTokenVersionEmptyDatabase(t *testing.T) {
	db := newTestDB(t)
	if err := PrepareUserTokenVersionSchema(db); err != nil {
		t.Fatalf("prepare an empty database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("complete the user schema: %v", err)
	}
	user := model.User{Username: "first-admin", PasswordHash: "hash", Role: "admin"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	var version int64 = -1
	if err := db.Raw("SELECT token_version FROM users WHERE id = ?", user.ID).Scan(&version).Error; err != nil || version != 0 {
		t.Fatalf("initial token version = %d, %v; want 0", version, err)
	}
}

// TestPrepareUserTokenVersionUpgradePreservesUsers verifies an old installation
// keeps its users and subsequent starts never reset a revoked token version.
func TestPrepareUserTokenVersionUpgradePreservesUsers(t *testing.T) {
	db := newTestDB(t)
	if err := db.Exec(`CREATE TABLE users (
		id bigserial PRIMARY KEY,
		username varchar(64) NOT NULL,
		password_hash varchar(256) NOT NULL
	); INSERT INTO users (username, password_hash) VALUES ('existing-admin', 'existing-hash')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := PrepareUserTokenVersionSchema(db); err != nil {
		t.Fatal(err)
	}
	var user struct {
		Username     string
		PasswordHash string
		TokenVersion int64
	}
	if err := db.Table("users").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Username != "existing-admin" || user.PasswordHash != "existing-hash" || user.TokenVersion != 0 {
		t.Fatalf("upgrade changed existing user: %+v", user)
	}
	if err := db.Exec("UPDATE users SET token_version = 7").Error; err != nil {
		t.Fatal(err)
	}
	if err := PrepareUserTokenVersionSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Table("users").First(&user).Error; err != nil || user.TokenVersion != 7 {
		t.Fatalf("repeat migration reset token version: %+v, %v", user, err)
	}
}

// TestPrepareUserTokenVersionConcurrentBootstrap verifies first-start races
// apply exactly one atomic schema migration on a previously empty database.
func TestPrepareUserTokenVersionConcurrentBootstrap(t *testing.T) {
	db := newTestDB(t)
	const workers = 4
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- PrepareUserTokenVersionSchema(db) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Table(migrationsTable).Where("id = ?", userTokenVersionMigrationID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("migration markers = %d, %v; want 1", count, err)
	}
}
