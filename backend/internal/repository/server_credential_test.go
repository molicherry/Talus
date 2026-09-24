package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

// TestServerRepoUpdateSwitchesCredential guards a GORM belongs-to trap:
// FindByID preloads Server.Credential, and Save() re-derives the foreign key
// from that populated association, silently writing a credential_id change
// back to the credential that was loaded. ServerRepo.Update therefore has to
// Omit the association.
//
// Opt-in integration test; see testdb_test.go for TEST_DATABASE_URL.
func TestServerRepoUpdateSwitchesCredential(t *testing.T) {
	db := newTestDB(t)
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
