package repository

import (
	"context"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

// TestSetHostKeyIfUnchanged guards the TOFU write-back: the host key is
// captured after a dial that can take seconds, so it must not overwrite a row
// that changed meanwhile. The update is conditional on the dialed host/port and
// only fills an empty key.
func TestSetHostKeyIfUnchanged(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewServerRepo(db)
	ctx := context.Background()

	srv := &model.Server{Name: "srv", Host: "old-host", Port: 22, OwnerID: 1}
	if err := repo.Create(ctx, srv); err != nil {
		t.Fatalf("create server: %v", err)
	}

	// Wrong host (the row was edited during the dial): must not write.
	ok, err := repo.SetHostKeyIfUnchanged(ctx, srv.ID, "new-host", 22, []byte("key-a"))
	if err != nil {
		t.Fatalf("conditional write: %v", err)
	}
	if ok {
		t.Fatal("host key written for a host that no longer matches the row")
	}

	// Matching host/port with an empty key: writes.
	ok, err = repo.SetHostKeyIfUnchanged(ctx, srv.ID, "old-host", 22, []byte("key-b"))
	if err != nil {
		t.Fatalf("conditional write: %v", err)
	}
	if !ok {
		t.Fatal("host key not written for the matching host")
	}

	// A second TOFU write must not replace the recorded key.
	ok, err = repo.SetHostKeyIfUnchanged(ctx, srv.ID, "old-host", 22, []byte("key-c"))
	if err != nil {
		t.Fatalf("conditional write: %v", err)
	}
	if ok {
		t.Fatal("existing host key was overwritten")
	}

	after, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.HostKey == nil || string(*after.HostKey) != "key-b" {
		t.Fatalf("host_key = %v, want key-b", after.HostKey)
	}
	if after.Host != "old-host" {
		t.Fatalf("host = %q, want old-host (conditional write must not touch it)", after.Host)
	}
}

// TestCredentialDeleteAndUnbind guards the dangling-binding bug: a soft delete
// never fires the FK's ON DELETE SET NULL, so without an explicit unbind a
// server keeps a credential_id that can no longer be loaded.
func TestCredentialDeleteAndUnbind(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	serverRepo := NewServerRepo(db)
	credRepo := NewCredentialRepo(db)
	ctx := context.Background()

	cred := &model.SSHCredential{Name: "cred", AuthType: "password", Username: "root"}
	if err := credRepo.Create(ctx, cred); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	srv := &model.Server{Name: "srv", Host: "h", Port: 22, OwnerID: 1, CredentialID: &cred.ID}
	if err := serverRepo.Create(ctx, srv); err != nil {
		t.Fatalf("create server: %v", err)
	}

	ids, err := serverRepo.FindIDsByCredentialID(ctx, cred.ID)
	if err != nil {
		t.Fatalf("find referencing servers: %v", err)
	}
	if len(ids) != 1 || ids[0] != srv.ID {
		t.Fatalf("referencing servers = %v, want [%d]", ids, srv.ID)
	}

	if err := credRepo.DeleteAndUnbind(ctx, cred.ID); err != nil {
		t.Fatalf("delete and unbind: %v", err)
	}

	after, err := serverRepo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload server: %v", err)
	}
	if after.CredentialID != nil {
		t.Fatalf("server still references the deleted credential: %d", *after.CredentialID)
	}
	if _, err := credRepo.FindByID(ctx, cred.ID); err == nil {
		t.Fatal("credential was not soft-deleted")
	}
}

// TestServerRepoUpdateClearsCredential guards the other half of the binding
// contract: a nil CredentialID must actually clear the column (the update path
// omits the association, not the column).
func TestServerRepoUpdateClearsCredential(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewServerRepo(db)
	ctx := context.Background()

	cred := &model.SSHCredential{Name: "cred", AuthType: "password", Username: "root"}
	if err := db.Create(cred).Error; err != nil {
		t.Fatalf("create credential: %v", err)
	}
	srv := &model.Server{Name: "srv", Host: "h", Port: 22, OwnerID: 1, CredentialID: &cred.ID}
	if err := repo.Create(ctx, srv); err != nil {
		t.Fatalf("create server: %v", err)
	}

	loaded, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	loaded.CredentialID = nil
	if err := repo.Update(ctx, loaded); err != nil {
		t.Fatalf("update: %v", err)
	}

	after, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload after update: %v", err)
	}
	if after.CredentialID != nil {
		t.Fatalf("credential binding not cleared: %d", *after.CredentialID)
	}
}

// TestHostKeyMismatchStateLifecycle guards the UI-visible host-key state: a
// mismatch records the presented key without touching the pin, a successful
// connection clears it, and trusting it re-pins.
func TestHostKeyMismatchStateLifecycle(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewServerRepo(db)
	ctx := context.Background()

	srv := &model.Server{Name: "srv", Host: "h", Port: 22, OwnerID: 1}
	if err := repo.Create(ctx, srv); err != nil {
		t.Fatalf("create server: %v", err)
	}
	pinned := []byte("pinned-key")
	if ok, err := repo.SetHostKeyIfUnchanged(ctx, srv.ID, "h", 22, pinned); err != nil || !ok {
		t.Fatalf("pin host key: ok=%v err=%v", ok, err)
	}

	seen := []byte("presented-key")
	if err := repo.RecordHostKeyMismatch(ctx, srv.ID, seen); err != nil {
		t.Fatalf("record mismatch: %v", err)
	}
	loaded, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.HostKeyMismatchAt == nil {
		t.Fatal("mismatch timestamp not recorded")
	}
	if loaded.HostKeySeen == nil || string(*loaded.HostKeySeen) != "presented-key" {
		t.Fatalf("host_key_seen = %v, want presented-key", loaded.HostKeySeen)
	}
	if loaded.HostKey == nil || string(*loaded.HostKey) != "pinned-key" {
		t.Fatal("recording a mismatch must not change the pin")
	}

	if err := repo.ClearHostKeyMismatch(ctx, srv.ID); err != nil {
		t.Fatalf("clear mismatch: %v", err)
	}
	loaded, _ = repo.FindByID(ctx, srv.ID)
	if loaded.HostKeyMismatchAt != nil || loaded.HostKeySeen != nil {
		t.Fatal("clear did not drop the pending mismatch")
	}

	if err := repo.RecordHostKeyMismatch(ctx, srv.ID, seen); err != nil {
		t.Fatalf("record mismatch: %v", err)
	}
	if err := repo.TrustHostKey(ctx, srv.ID, seen); err != nil {
		t.Fatalf("trust host key: %v", err)
	}
	loaded, _ = repo.FindByID(ctx, srv.ID)
	if loaded.HostKey == nil || string(*loaded.HostKey) != "presented-key" {
		t.Fatalf("pin not updated: %v", loaded.HostKey)
	}
	if loaded.HostKeySeen != nil || loaded.HostKeyMismatchAt != nil {
		t.Fatal("trust did not clear the pending mismatch")
	}
}
