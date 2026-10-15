package repository

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func sshSnapshotFixture(t *testing.T) (*ServerRepo, *gorm.DB, *model.Server, *model.Server) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Server{}, &model.SSHCredential{}, &model.ServerConnectionConfig{}); err != nil {
		t.Fatal(err)
	}
	credential := &model.SSHCredential{Name: "snapshot", AuthType: "password", Username: "snapshot-user", EncryptedPassword: stringPointer("encrypted-v1")}
	if err := db.Create(credential).Error; err != nil {
		t.Fatal(err)
	}
	jump := &model.Server{Name: "snapshot-jump", Host: "old-jump.test", Port: 22, CredentialID: &credential.ID, ConnectionRevision: 1}
	target := &model.Server{Name: "snapshot-target", Host: "old-target.test", Port: 22, CredentialID: &credential.ID, ConnectionRevision: 1}
	repo := NewServerRepo(db)
	for _, srv := range []*model.Server{jump, target} {
		if err := repo.Create(context.Background(), srv); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SaveConnectionConfig(context.Background(), &model.ServerConnectionConfig{ServerID: target.ID, Mode: model.ConnectionSSHJump, JumpServerID: &jump.ID}); err != nil {
		t.Fatal(err)
	}
	return repo, db, target, jump
}

func stringPointer(value string) *string { return &value }

func TestSSHRouteSnapshotReadIsCoherentAcrossAtomicUpdate(t *testing.T) {
	repo, db, target, jump := sshSnapshotFixture(t)
	ctx := context.Background()
	before, err := repo.FindSSHRouteSnapshot(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	const hook = "test:change-route-after-target-read"
	if err := db.Callback().Query().After("gorm:query").Before("gorm:preload").Register(hook, func(query *gorm.DB) {
		row, ok := query.Statement.Dest.(*model.Server)
		if !ok || query.Statement.Table != "servers" || row.ID != target.ID || !changed.CompareAndSwap(false, true) {
			return
		}
		// Commit all edits after the target SELECT but before its credential,
		// connection config, and jump are loaded by the snapshot reader.
		_ = query.AddError(db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Server{}).Where("id = ?", target.ID).Updates(map[string]any{"host": "new-target.test", "connection_revision": 2}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Server{}).Where("id = ?", jump.ID).Updates(map[string]any{"host": "new-jump.test", "connection_revision": 2}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.SSHCredential{}).Where("id = ?", *target.CredentialID).Update("encrypted_password", "encrypted-v2").Error; err != nil {
				return err
			}
			return tx.Model(&model.ServerConnectionConfig{}).Where("server_id = ?", target.ID).Updates(map[string]any{"mode": model.ConnectionSOCKS5, "jump_server_id": nil, "socks_host": "new-proxy.test", "socks_port": 1080, "socks_auth_type": model.SOCKSAuthNone}).Error
		}))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(hook) })
	during, err := repo.FindSSHRouteSnapshot(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Load() || !sameSSHRouteSnapshot(before, during) {
		t.Fatal("snapshot mixed target, route, jump, or credential versions across an atomic update")
	}
	after, err := repo.FindSSHRouteSnapshot(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Target.Host != "new-target.test" || after.Target.ConnectionRevision != 2 || after.Config.Mode != model.ConnectionSOCKS5 || after.Config.SocksHost != "new-proxy.test" || after.Jump != nil || *after.Target.Credential.EncryptedPassword != "encrypted-v2" {
		t.Fatal("a later snapshot did not observe the complete committed route")
	}
}

func TestSSHRouteHostKeyCASRejectsChangedJumpDependency(t *testing.T) {
	for _, kind := range []string{"pin", "mismatch", "clear"} {
		t.Run(kind, func(t *testing.T) {
			repo, db, target, jump := sshSnapshotFixture(t)
			ctx := context.Background()
			seen := []byte("target-identity")
			if kind != "pin" {
				updates := map[string]any{"host_key": seen}
				if kind == "clear" {
					updates["host_key_seen"], updates["host_key_mismatch_at"] = []byte("pending-identity"), time.Now().UTC()
				}
				if err := db.Model(&model.Server{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			}
			expected, err := repo.FindSSHRouteSnapshot(ctx, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.Transaction(ctx, func(tx *ServerRepo) error {
				if err := tx.LockConnectionServers(ctx, []uint{jump.ID}); err != nil {
					return err
				}
				return tx.db.Model(&model.Server{}).Where("id = ?", jump.ID).Updates(map[string]any{"host": "replacement-jump.test", "connection_revision": 2}).Error
			}); err != nil {
				t.Fatal(err)
			}
			if updated, err := repo.CommitSSHRouteHostKey(ctx, expected, target.ID, seen, kind); err != nil || updated {
				t.Fatalf("stale %s changed target identity after an upstream edit: updated=%v err=%v", kind, updated, err)
			}
			fresh, err := repo.FindSSHRouteSnapshot(ctx, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !sameSSHServer(expected.Target, fresh.Target) {
				t.Fatal("rejected route result mutated persisted target state")
			}
			if updated, err := repo.CommitSSHRouteHostKey(ctx, fresh, target.ID, seen, kind); err != nil || !updated {
				t.Fatalf("current %s could not commit: updated=%v err=%v", kind, updated, err)
			}
		})
	}
}

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
