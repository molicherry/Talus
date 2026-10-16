package repository

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SSHRouteSnapshot contains one coherent view of all data used to authenticate
// a route. Credentials must come from the same database snapshot as the route:
// rejecting a mixed snapshot after authentication cannot undo a leaked secret.
type SSHRouteSnapshot struct {
	Target *model.Server
	Config *model.ServerConnectionConfig
	Jump   *model.Server
}

var ErrSSHJumpUnavailable = errors.New("SSH jump is unavailable")
var ErrSSHJumpProxied = errors.New("SSH jump is no longer direct")

func (r *ServerRepo) FindSSHRouteSnapshot(ctx context.Context, id uint) (*SSHRouteSnapshot, error) {
	var snapshot *SSHRouteSnapshot
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, err = NewServerRepo(tx).readSSHRouteSnapshot(ctx, id)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return snapshot, err
}

func (r *ServerRepo) readSSHRouteSnapshot(ctx context.Context, id uint) (*SSHRouteSnapshot, error) {
	target, err := r.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	config, err := r.FindConnectionConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	snapshot := &SSHRouteSnapshot{Target: target, Config: config}
	if config != nil && config.Mode == model.ConnectionSSHJump && config.JumpServerID != nil {
		snapshot.Jump, err = r.FindByID(ctx, *config.JumpServerID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrSSHJumpUnavailable
			}
			if ctx.Err() != nil {
				return nil, context.Cause(ctx)
			}
			return nil, fmt.Errorf("read SSH jump: %w", err)
		}
		upstream, err := r.FindConnectionConfig(ctx, snapshot.Jump.ID)
		if err != nil {
			return nil, err
		}
		if upstream != nil && upstream.Mode != model.ConnectionDirect {
			return nil, ErrSSHJumpProxied
		}
	}
	return snapshot, nil
}

// CommitSSHRouteHostKey validates the complete authenticated route while its
// server and credential rows are locked. A jump edit must not allow an old
// result to pin, clear, or warn on the target's identity in the new route.
func (r *ServerRepo) CommitSSHRouteHostKey(ctx context.Context, expected *SSHRouteSnapshot, resourceID uint, seen []byte, kind string) (bool, error) {
	if expected == nil || expected.Target == nil {
		return false, nil
	}
	resource := expected.Target
	ids := []uint{resource.ID}
	if expected.Jump != nil {
		ids = append(ids, expected.Jump.ID)
		if resourceID == expected.Jump.ID {
			resource = expected.Jump
		}
	}
	if resource.ID != resourceID {
		return false, nil
	}
	updated := false
	err := r.Transaction(ctx, func(tx *ServerRepo) error {
		if err := tx.LockConnectionServers(ctx, ids); err != nil {
			return err
		}
		// Server locks serialize binding/deletion/config changes. Share-lock the
		// bound credentials too so secret edits cannot cross the comparison/write.
		var servers []model.Server
		if err := tx.db.WithContext(ctx).Where("id IN ?", ids).Find(&servers).Error; err != nil {
			return err
		}
		credentialIDs := make([]uint, 0, len(servers))
		for _, srv := range servers {
			if srv.CredentialID != nil {
				credentialIDs = append(credentialIDs, *srv.CredentialID)
			}
		}
		if len(credentialIDs) > 0 {
			sort.Slice(credentialIDs, func(i, j int) bool { return credentialIDs[i] < credentialIDs[j] })
			var credentials []model.SSHCredential
			if err := tx.db.WithContext(ctx).Clauses(clause.Locking{Strength: "SHARE"}).Where("id IN ?", credentialIDs).Order("id ASC").Find(&credentials).Error; err != nil {
				return err
			}
		}
		current, err := tx.readSSHRouteSnapshot(ctx, expected.Target.ID)
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrSSHJumpUnavailable) || errors.Is(err, ErrSSHJumpProxied) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameSSHRouteSnapshot(expected, current) {
			return nil
		}
		updated, err = tx.CommitSSHHostKey(ctx, resource, seen, kind)
		return err
	})
	return updated, err
}

func sameSSHRouteSnapshot(left, right *SSHRouteSnapshot) bool {
	return sameSSHServer(left.Target, right.Target) && sameSSHServer(left.Jump, right.Jump) && sameSSHConfig(left.Config, right.Config)
}

func sameSSHServer(left, right *model.Server) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.ID != right.ID || left.DeletedAt.Valid != right.DeletedAt.Valid || left.Host != right.Host || left.Port != right.Port || left.ConnectionRevision != right.ConnectionRevision || !sameSSHUint(left.CredentialID, right.CredentialID) || !sameSSHBytes(left.HostKey, right.HostKey) || !sameSSHBytes(left.HostKeySeen, right.HostKeySeen) || !sameSSHTime(left.HostKeyMismatchAt, right.HostKeyMismatchAt) {
		return false
	}
	first, second := left.Credential, right.Credential
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.ID == second.ID && first.DeletedAt.Valid == second.DeletedAt.Valid && first.UpdatedAt.Equal(second.UpdatedAt) && first.AuthType == second.AuthType && first.Username == second.Username && sameSSHString(first.EncryptedPassword, second.EncryptedPassword) && sameSSHString(first.EncryptedPrivateKey, second.EncryptedPrivateKey) && bytes.Equal(first.Salt, second.Salt)
}

func sameSSHConfig(left, right *model.ServerConnectionConfig) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ServerID == right.ServerID && left.Mode == right.Mode && sameSSHUint(left.JumpServerID, right.JumpServerID) && left.SocksHost == right.SocksHost && left.SocksPort == right.SocksPort && left.SocksAuthType == right.SocksAuthType && left.SocksUsername == right.SocksUsername && sameSSHString(left.EncryptedPassword, right.EncryptedPassword) && bytes.Equal(left.Salt, right.Salt)
}

func sameSSHUint(left, right *uint) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
func sameSSHString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
func sameSSHBytes(left, right *[]byte) bool {
	return left == nil && right == nil || left != nil && right != nil && bytes.Equal(*left, *right)
}
func sameSSHTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

// CommitSSHHostKey conditionally commits one result from an SSH handshake.
// Comparing the complete identity state prevents an old success from clearing
// a newer warning, or a first connection from pinning a key after a route edit.
func (r *ServerRepo) CommitSSHHostKey(ctx context.Context, expected *model.Server, seen []byte, kind string) (bool, error) {
	query := r.db.WithContext(ctx).Model(&model.Server{}).Where("id = ? AND host = ? AND port = ? AND connection_revision = ?", expected.ID, expected.Host, expected.Port, expected.ConnectionRevision)
	if expected.HostKey == nil || len(*expected.HostKey) == 0 {
		query = query.Where("(host_key IS NULL OR octet_length(host_key) = 0)")
	} else {
		query = query.Where("host_key = ?", *expected.HostKey)
	}
	if expected.HostKeySeen == nil {
		query = query.Where("host_key_seen IS NULL")
	} else {
		query = query.Where("host_key_seen = ?", *expected.HostKeySeen)
	}
	if expected.HostKeyMismatchAt == nil {
		query = query.Where("host_key_mismatch_at IS NULL")
	} else {
		query = query.Where("host_key_mismatch_at = ?", *expected.HostKeyMismatchAt)
	}
	updates := map[string]any{"connection_revision": gorm.Expr("connection_revision + 1")}
	if len(seen) == 0 {
		return false, nil
	}
	switch kind {
	case "pin":
		if expected.HostKey != nil && len(*expected.HostKey) > 0 {
			return false, nil
		}
		updates["host_key"], updates["host_key_seen"], updates["host_key_mismatch_at"] = seen, nil, nil
	case "mismatch":
		updates["host_key_seen"], updates["host_key_mismatch_at"] = seen, time.Now().UTC()
	case "clear":
		if expected.HostKey == nil || !bytes.Equal(*expected.HostKey, seen) {
			return false, nil
		}
		updates["host_key_seen"], updates["host_key_mismatch_at"] = nil, nil
	default:
		return false, nil
	}
	result := query.Updates(updates)
	return result.RowsAffected == 1, result.Error
}
