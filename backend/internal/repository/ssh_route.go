package repository

import (
	"bytes"
	"context"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

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
