package repository

import (
	"context"
	"errors"
	"sort"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Transaction gives configuration services one atomic unit of work without
// exposing a raw database handle to their callers.
func (r *ServerRepo) Transaction(ctx context.Context, fn func(*ServerRepo) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(NewServerRepo(tx)) })
}

// LockConnectionServers serializes route binding, route changes and deletion.
// All callers lock the target and new jump together in ascending ID order.
func (r *ServerRepo) LockConnectionServers(ctx context.Context, ids []uint) error {
	ordered := append([]uint(nil), ids...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var servers []model.Server
	return r.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ordered).Order("id ASC").Find(&servers).Error
}

// FindConnectionConfig deliberately returns nil, nil for direct connections.
func (r *ServerRepo) FindConnectionConfig(ctx context.Context, serverID uint) (*model.ServerConnectionConfig, error) {
	var config model.ServerConnectionConfig
	err := r.db.WithContext(ctx).First(&config, "server_id = ?", serverID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (r *ServerRepo) FindConnectionModes(ctx context.Context, ids []uint) (map[uint]string, error) {
	var configs []struct {
		ServerID uint
		Mode     string
	}
	if len(ids) > 0 {
		if err := r.db.WithContext(ctx).Model(&model.ServerConnectionConfig{}).
			Select("server_id", "mode").Where("server_id IN ?", ids).Find(&configs).Error; err != nil {
			return nil, err
		}
	}
	modes := make(map[uint]string, len(configs))
	for _, config := range configs {
		modes[config.ServerID] = config.Mode
	}
	return modes, nil
}

// FindJumpDependentIDs only returns live managed targets.
func (r *ServerRepo) FindJumpDependentIDs(ctx context.Context, jumpID uint) ([]uint, error) {
	var ids []uint
	err := r.db.WithContext(ctx).Model(&model.ServerConnectionConfig{}).
		Joins("JOIN servers ON servers.id = server_connection_configs.server_id AND servers.deleted_at IS NULL").
		Where("server_connection_configs.jump_server_id = ?", jumpID).
		Order("server_connection_configs.server_id ASC").Pluck("server_connection_configs.server_id", &ids).Error
	return ids, err
}

func (r *ServerRepo) SaveConnectionConfig(ctx context.Context, config *model.ServerConnectionConfig) error {
	return r.db.WithContext(ctx).Omit("Server", "JumpServer").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "server_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"mode", "jump_server_id", "socks_host", "socks_port", "socks_auth_type", "socks_username", "encrypted_password", "salt", "crypto_version"}),
	}).Create(config).Error
}

func (r *ServerRepo) DeleteConnectionConfig(ctx context.Context, serverID uint) error {
	return r.db.WithContext(ctx).Where("server_id = ?", serverID).Delete(&model.ServerConnectionConfig{}).Error
}

// UpdateConnectionFields writes only editable columns. Host-key state is
// included because host/port edits intentionally reset it while holding a lock.
func (r *ServerRepo) UpdateConnectionFields(ctx context.Context, server *model.Server) error {
	return r.db.WithContext(ctx).Model(&model.Server{}).Where("id = ?", server.ID).Updates(map[string]any{
		"name": server.Name, "host": server.Host, "port": server.Port,
		"description": server.Description, "notes": server.Notes, "credential_id": server.CredentialID,
		"connection_revision": server.ConnectionRevision, "host_key": server.HostKey,
		"host_key_seen": server.HostKeySeen, "host_key_mismatch_at": server.HostKeyMismatchAt,
	}).Error
}
