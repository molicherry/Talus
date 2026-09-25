package repository

import (
	"context"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// ServerRepo provides database access for server records.
type ServerRepo struct {
	db *gorm.DB
}

// NewServerRepo creates a ServerRepo backed by the given database handle.
func NewServerRepo(db *gorm.DB) *ServerRepo {
	return &ServerRepo{db: db}
}

// FindAll returns all servers ordered by name ascending.
func (r *ServerRepo) FindAll(ctx context.Context) ([]model.Server, error) {
	var servers []model.Server
	err := r.db.WithContext(ctx).Order("name ASC").Preload("Credential").Find(&servers).Error
	if err != nil {
		return nil, err
	}
	return servers, nil
}

// FindAllSummaries returns all servers as lightweight summaries, ordered by
// name ascending. Only the ServerSummary fields are selected; the transient
// Status field is populated by the service layer.
func (r *ServerRepo) FindAllSummaries(ctx context.Context) ([]model.ServerSummary, error) {
	var summaries []model.ServerSummary
	err := r.db.WithContext(ctx).Model(&model.Server{}).Order("name ASC").Find(&summaries).Error
	return summaries, err
}

// FindByID returns the server with the given primary key, or gorm.ErrRecordNotFound.
func (r *ServerRepo) FindByID(ctx context.Context, id uint) (*model.Server, error) {
	var server model.Server
	err := r.db.WithContext(ctx).Preload("Credential").First(&server, id).Error
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// Create inserts a new server record.
func (r *ServerRepo) Create(ctx context.Context, server *model.Server) error {
	return r.db.WithContext(ctx).Create(server).Error
}

// Update saves all fields of an existing server record.
//
// The Credential association is omitted on purpose: FindByID preloads it, and
// GORM's Save re-derives the belongs-to foreign key from a populated
// association — which would silently write a credential_id change back to the
// previously loaded credential. A server update never writes the credential
// row itself.
func (r *ServerRepo) Update(ctx context.Context, server *model.Server) error {
	return r.db.WithContext(ctx).Omit("Credential").Save(server).Error
}

// Delete performs a soft delete of the server with the given id.
func (r *ServerRepo) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&model.Server{}, id).Error
}

// SetHostKeyIfUnchanged stores a TOFU host key only when the row still has the
// host and port that were dialed and no key yet. The dial happens before the
// write, so a full-row save would revert a concurrent edit; this updates one
// column and reports whether it applied.
func (r *ServerRepo) SetHostKeyIfUnchanged(ctx context.Context, id uint, host string, port int, hostKey []byte) (bool, error) {
	res := r.db.WithContext(ctx).Model(&model.Server{}).
		Where("id = ? AND host = ? AND port = ? AND (host_key IS NULL OR octet_length(host_key) = 0)", id, host, port).
		Update("host_key", hostKey)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FindIDsByCredentialID returns the ids of servers bound to a credential.
func (r *ServerRepo) FindIDsByCredentialID(ctx context.Context, credentialID uint) ([]uint, error) {
	var ids []uint
	err := r.db.WithContext(ctx).Model(&model.Server{}).Where("credential_id = ?", credentialID).Pluck("id", &ids).Error
	return ids, err
}

// RecordHostKeyMismatch stores the host key a server just presented when it did
// not match the pinned one, so the UI can show the new fingerprint and let the
// operator decide whether to trust it.
//
// The write is conditional on the host/port that was dialed: the dial can take
// seconds, and a concurrent edit must not have the old host's result recorded
// against the new one. Returns whether a row was updated.
func (r *ServerRepo) RecordHostKeyMismatch(ctx context.Context, id uint, host string, port int, seen []byte) (bool, error) {
	res := r.db.WithContext(ctx).Model(&model.Server{}).
		Where("id = ? AND host = ? AND port = ?", id, host, port).
		Updates(map[string]any{
			"host_key_seen":        seen,
			"host_key_mismatch_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ClearHostKeyMismatch drops a recorded mismatch after a connection succeeds.
func (r *ServerRepo) ClearHostKeyMismatch(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Model(&model.Server{}).Where("id = ?", id).Updates(map[string]any{
		"host_key_seen":        nil,
		"host_key_mismatch_at": nil,
	}).Error
}

// TrustHostKey pins the presented key as the new host key and clears the
// pending-mismatch state. The update only applies while host_key_seen still
// equals the key the caller verified, so a key that changed in the meantime
// cannot be trusted by accident. Returns whether a row was updated.
func (r *ServerRepo) TrustHostKey(ctx context.Context, id uint, seen []byte) (bool, error) {
	res := r.db.WithContext(ctx).Model(&model.Server{}).
		Where("id = ? AND host_key_seen = ?", id, seen).
		Updates(map[string]any{
			"host_key":             seen,
			"host_key_seen":        nil,
			"host_key_mismatch_at": nil,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FindByIDs returns servers whose IDs are in the given slice, ordered by name.
func (r *ServerRepo) FindByIDs(ctx context.Context, ids []uint) ([]model.Server, error) {
	var servers []model.Server
	result := r.db.WithContext(ctx).Where("id IN ?", ids).Order("name ASC").Preload("Credential").Find(&servers)
	return servers, result.Error
}

// FindByIDSummaries returns summaries for the given server IDs, ordered by
// name ascending. Used for API-key-scoped summary listings.
func (r *ServerRepo) FindByIDSummaries(ctx context.Context, ids []uint) ([]model.ServerSummary, error) {
	var summaries []model.ServerSummary
	err := r.db.WithContext(ctx).Model(&model.Server{}).Where("id IN ?", ids).Order("name ASC").Find(&summaries).Error
	return summaries, err
}
