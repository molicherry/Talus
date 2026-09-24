package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestServerRepoUpdateSwitchesCredential guards a GORM belongs-to trap:
// FindByID preloads Server.Credential, and Save() re-derives the foreign key
// from that populated association, silently writing a credential_id change
// back to the credential that was loaded. ServerRepo.Update therefore has to
// Omit the association.
//
// Opt-in integration test. It only touches its own throwaway schema (created
// and dropped via t.Cleanup) and is skipped when TEST_DATABASE_URL is unset,
// so it can safely point at any Postgres, including a development one:
//
//	TEST_DATABASE_URL='host=127.0.0.1 port=55432 user=repro password=repro dbname=repro sslmode=disable' \
//	  go test ./internal/repository -run TestServerRepoUpdateSwitchesCredential -v
func TestServerRepoUpdateSwitchesCredential(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		// Never let a missing database turn into a silent skip on CI: the whole
		// point of the service in the workflow is to exercise this regression.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI (see .github/workflows/ci.yml)")
		}
		t.Skip("set TEST_DATABASE_URL to run this integration test")
	}

	// Scope every table this test creates to a dedicated schema. The public
	// schema is never touched: no DROP TABLE, no writes, only our own objects.
	schema := fmt.Sprintf("talus_test_%d", time.Now().UnixNano())
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	// search_path is a per-connection pgx runtime parameter, so every pooled
	// connection resolved from this DSN sees only the test schema.
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open scoped connection: %v", err)
	}
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	credA := &model.SSHCredential{Name: "A", AuthType: "password", Username: "root"}
	credB := &model.SSHCredential{Name: "B", AuthType: "password", Username: "root"}
	if err := db.Create(credA).Error; err != nil {
		t.Fatalf("create credential A: %v", err)
	}
	if err := db.Create(credB).Error; err != nil {
		t.Fatalf("create credential B: %v", err)
	}

	repo := NewServerRepo(db)
	srv := &model.Server{Name: "srv", Host: "h", Port: 22, OwnerID: 1, CredentialID: &credA.ID}
	if err := repo.Create(ctx, srv); err != nil {
		t.Fatalf("create server: %v", err)
	}

	// Exactly what service.Update does: load (preloading Credential), swap the
	// credential id, then save.
	loaded, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload server: %v", err)
	}
	if loaded.Credential == nil {
		t.Fatal("expected FindByID to preload Credential")
	}
	newID := credB.ID
	loaded.CredentialID = &newID
	if err := repo.Update(ctx, loaded); err != nil {
		t.Fatalf("update server: %v", err)
	}

	after, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload after update: %v", err)
	}
	got := "nil"
	if after.CredentialID != nil {
		got = fmt.Sprint(*after.CredentialID)
	}
	if after.CredentialID == nil || *after.CredentialID != credB.ID {
		t.Fatalf("credential_id = %s after switching to %d", got, credB.ID)
	}
}
