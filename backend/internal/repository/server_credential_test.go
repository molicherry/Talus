package repository

import (
	"context"
	"fmt"
	"os"
	"testing"

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
// Opt-in integration test: it needs a throwaway Postgres and is skipped
// otherwise, e.g.
//
//	docker run -d --rm -e POSTGRES_PASSWORD=repro -e POSTGRES_USER=repro \
//	  -e POSTGRES_DB=repro -p 127.0.0.1:55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL='host=127.0.0.1 port=55432 user=repro password=repro dbname=repro sslmode=disable' \
//	  go test ./internal/repository -run TestServerRepoUpdateSwitchesCredential -v
func TestServerRepoUpdateSwitchesCredential(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run this integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Exec("DROP TABLE IF EXISTS ssh_credentials, servers CASCADE").Error; err != nil {
		t.Fatalf("reset tables: %v", err)
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
