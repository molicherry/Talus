package repository

import (
	"context"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

func TestSSHHostKeyCASRejectsStaleIdentityAndWarningResults(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatal(err)
	}
	repo := NewServerRepo(db)
	ctx := context.Background()
	srv := &model.Server{Name: "identity", Host: "example.test", Port: 22, ConnectionRevision: 1}
	if err := repo.Create(ctx, srv); err != nil {
		t.Fatal(err)
	}
	original, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("first-server-key")
	if ok, err := repo.CommitSSHHostKey(ctx, original, key, "pin"); err != nil || !ok {
		t.Fatalf("pin = %v, %v", ok, err)
	}
	pinned, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.ConnectionRevision != 2 {
		t.Fatalf("pin revision = %d", pinned.ConnectionRevision)
	}
	changedKey := []byte("changed-server-key")
	if ok, err := repo.CommitSSHHostKey(ctx, pinned, changedKey, "mismatch"); err != nil || !ok {
		t.Fatalf("mismatch = %v, %v", ok, err)
	}
	if ok, err := repo.CommitSSHHostKey(ctx, pinned, key, "clear"); err != nil || ok {
		t.Fatalf("stale success cleared newer warning: %v, %v", ok, err)
	}
	if ok, err := repo.CommitSSHHostKey(ctx, original, changedKey, "pin"); err != nil || ok {
		t.Fatalf("stale TOFU changed identity: %v, %v", ok, err)
	}
	warned, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if warned.ConnectionRevision != 3 || warned.HostKeyMismatchAt == nil || warned.HostKeySeen == nil || string(*warned.HostKeySeen) != string(changedKey) || string(*warned.HostKey) != string(key) {
		t.Fatalf("warning state changed unexpectedly: %#v", warned)
	}
	if ok, err := repo.CommitSSHHostKey(ctx, warned, key, "clear"); err != nil || !ok {
		t.Fatalf("fresh clear = %v, %v", ok, err)
	}
	cleared, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ConnectionRevision != 4 || cleared.HostKeyMismatchAt != nil || cleared.HostKeySeen != nil {
		t.Fatalf("clear state = %#v", cleared)
	}
}

func TestSSHHostKeyCASRejectsRouteAndEndpointEdits(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}); err != nil {
		t.Fatal(err)
	}
	repo := NewServerRepo(db)
	ctx := context.Background()
	srv := &model.Server{Name: "route-edit", Host: "example.test", Port: 22, ConnectionRevision: 1}
	if err := repo.Create(ctx, srv); err != nil {
		t.Fatal(err)
	}
	original, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", srv.ID).Updates(map[string]any{"connection_revision": 2}).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.CommitSSHHostKey(ctx, original, []byte("old-route-key"), "pin"); err != nil || ok {
		t.Fatalf("old route TOFU = %v, %v", ok, err)
	}
	original, err = repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", srv.ID).Updates(map[string]any{"host": "new.example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.CommitSSHHostKey(ctx, original, []byte("old-endpoint-key"), "pin"); err != nil || ok {
		t.Fatalf("old endpoint TOFU = %v, %v", ok, err)
	}
	current, err := repo.FindByID(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HostKey != nil {
		t.Fatal("stale key was saved")
	}
}
