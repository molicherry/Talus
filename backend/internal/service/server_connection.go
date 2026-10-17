package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"gorm.io/gorm"
)

// OptionalSecret keeps omission, null and a replacement distinct. It is only
// used in write DTOs and must never be included in response or audit data.
type OptionalSecret struct {
	Set   bool
	Value *string
}

func (s *OptionalSecret) UnmarshalJSON(data []byte) error {
	s.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		s.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	s.Value = &value
	return nil
}

type ConnectionInput struct {
	Mode         string         `json:"mode"`
	JumpServerID *uint          `json:"jump_server_id,omitempty"`
	Host         string         `json:"host,omitempty"`
	Port         int            `json:"port,omitempty"`
	AuthType     string         `json:"auth_type,omitempty"`
	Username     string         `json:"username,omitempty"`
	Password     OptionalSecret `json:"password,omitempty"`
	fields       map[string]json.RawMessage
}

func (c *ConnectionInput) UnmarshalJSON(data []byte) error {
	type plain ConnectionInput
	var input plain
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	*c = ConnectionInput(input)
	return json.Unmarshal(data, &c.fields)
}

// ConnectionMutation is the three-state connection field on server writes.
// ExpectedRevision lives beside connection in the HTTP request.
type ConnectionMutation struct {
	Set              bool
	Config           *ConnectionInput
	ExpectedRevision *uint64
}

func (c *ConnectionMutation) UnmarshalJSON(data []byte) error {
	c.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		c.Config = nil
		return nil
	}
	var input ConnectionInput
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	c.Config = &input
	return nil
}

func IsConnectionAdmin(ctx context.Context) bool {
	principal := mw.GetPrincipal(ctx)
	return principal.AuthType == "jwt" && principal.Role == "admin" && principal.UserID != nil && *principal.UserID != 0
}

func invalidConnection() error {
	return server.NewAppError(http.StatusBadRequest, server.ReasonInvalidConnection)
}
func connectionForbidden() error {
	return server.NewAppError(http.StatusForbidden, server.ReasonForbidden)
}

func normalizeProxyHost(host string) (string, bool) {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 256 || strings.ContainsAny(host, " /@?#\\\t\r\n") {
		return "", false
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.String(), true
	}
	if strings.ContainsAny(host, ":[]") {
		return "", false
	}
	// DNS names are case-insensitive; a trailing root dot does not change the
	// endpoint and should not force the operator to re-enter a saved secret.
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
				return "", false
			}
		}
	}
	return host, true
}

func (input *ConnectionInput) validate() error {
	if input == nil {
		return nil
	}
	allowed := map[string]bool{"mode": true}
	switch input.Mode {
	case model.ConnectionDirect:
	case model.ConnectionSSHJump:
		allowed["jump_server_id"] = true
		if input.JumpServerID == nil || *input.JumpServerID == 0 {
			return invalidConnection()
		}
	case model.ConnectionSOCKS5:
		for _, key := range []string{"host", "port", "auth_type", "username", "password"} {
			allowed[key] = true
		}
		if _, ok := normalizeProxyHost(input.Host); !ok || input.Port < 1 || input.Port > 65535 {
			return invalidConnection()
		}
		switch input.AuthType {
		case model.SOCKSAuthNone:
			if input.Username != "" || input.Password.Value != nil {
				return invalidConnection()
			}
		case model.SOCKSAuthPassword:
			if len(input.Username) < 1 || len(input.Username) > 255 {
				return invalidConnection()
			}
			if input.Password.Set && (input.Password.Value == nil || len(*input.Password.Value) < 1 || len(*input.Password.Value) > 255) {
				return invalidConnection()
			}
		default:
			return invalidConnection()
		}
	default:
		return invalidConnection()
	}
	for field := range input.fields {
		if !allowed[field] {
			return invalidConnection()
		}
	}
	// These checks also cover inputs assembled directly by internal callers.
	if input.Mode != model.ConnectionSSHJump && input.JumpServerID != nil {
		return invalidConnection()
	}
	if input.Mode != model.ConnectionSOCKS5 && (input.Host != "" || input.Port != 0 || input.AuthType != "" || input.Username != "" || input.Password.Set) {
		return invalidConnection()
	}
	return nil
}

func (s *ServerService) buildConnectionConfig(serverID uint, input *ConnectionInput, old *model.ServerConnectionConfig) (*model.ServerConnectionConfig, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	if input == nil || input.Mode == model.ConnectionDirect {
		return nil, nil
	}
	config := &model.ServerConnectionConfig{ServerID: serverID, Mode: input.Mode, JumpServerID: input.JumpServerID}
	if input.Mode == model.ConnectionSSHJump {
		return config, nil
	}
	config.SocksHost, _ = normalizeProxyHost(input.Host)
	config.SocksPort, config.SocksAuthType, config.SocksUsername = input.Port, input.AuthType, input.Username
	if input.AuthType == model.SOCKSAuthNone {
		return config, nil
	}
	if !input.Password.Set {
		if old == nil || old.Mode != model.ConnectionSOCKS5 || old.SocksHost != config.SocksHost || old.SocksPort != config.SocksPort || old.SocksAuthType != config.SocksAuthType || old.SocksUsername != config.SocksUsername || old.EncryptedPassword == nil {
			return nil, invalidConnection()
		}
		config.EncryptedPassword, config.Salt = old.EncryptedPassword, append([]byte(nil), old.Salt...)
		return config, nil
	}
	if s.masterKey == nil {
		return nil, errors.New("connection encryption is unavailable")
	}
	salt, err := crypto.GenerateSalt()
	if err != nil {
		return nil, errors.New("generate connection encryption salt")
	}
	encrypted, err := crypto.Encrypt([]byte(*input.Password.Value), s.masterKey.DeriveKey(salt))
	if err != nil {
		return nil, errors.New("encrypt connection password")
	}
	config.Salt, config.EncryptedPassword = salt, &encrypted
	return config, nil
}

func validateJump(ctx context.Context, repo *repository.ServerRepo, serverID uint, config *model.ServerConnectionConfig) error {
	if config == nil || config.Mode != model.ConnectionSSHJump {
		return nil
	}
	if config.JumpServerID == nil || *config.JumpServerID == serverID {
		return invalidConnection()
	}
	jump, err := repo.FindByID(ctx, *config.JumpServerID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return invalidConnection()
	}
	if err != nil {
		return err
	}
	if jump.CredentialID == nil || jump.Credential == nil || jump.Credential.Username == "" {
		return invalidConnection()
	}
	credential := jump.Credential
	if (credential.AuthType != "password" || credential.EncryptedPassword == nil) && (credential.AuthType != "private_key" || credential.EncryptedPrivateKey == nil) {
		return invalidConnection()
	}
	upstream, err := repo.FindConnectionConfig(ctx, jump.ID)
	if err != nil {
		return err
	}
	if upstream != nil {
		return invalidConnection()
	}
	return nil
}

func configMode(config *model.ServerConnectionConfig) string {
	if config == nil {
		return model.ConnectionDirect
	}
	return config.Mode
}

func (s *ServerService) decorateConnection(ctx context.Context, srv *model.Server, detail bool) error {
	config, err := s.repo.FindConnectionConfig(ctx, srv.ID)
	if err != nil {
		return err
	}
	srv.Connection = connectionView(config, detail && IsConnectionAdmin(ctx))
	return nil
}

func (s *ServerService) decorateConnectionModes(ctx context.Context, servers []model.Server) error {
	ids := make([]uint, len(servers))
	for i := range servers {
		ids[i] = servers[i].ID
	}
	modes, err := s.repo.FindConnectionModes(ctx, ids)
	if err != nil {
		return err
	}
	for i := range servers {
		mode := modes[servers[i].ID]
		if mode == "" {
			mode = model.ConnectionDirect
		}
		servers[i].Connection = &model.ConnectionConfig{Mode: mode}
	}
	return nil
}

func (s *ServerService) invalidateWithDependents(ctx context.Context, id uint) {
	s.invalidate(id)
	if ids, err := s.repo.FindJumpDependentIDs(ctx, id); err == nil {
		for _, dependent := range ids {
			s.invalidate(dependent)
		}
	}
}

func firstConnectionMutation(inputs []ConnectionMutation) ConnectionMutation {
	if len(inputs) == 0 {
		return ConnectionMutation{}
	}
	return inputs[0]
}

func (s *ServerService) createServer(ctx context.Context, srv *model.Server, mutation ConnectionMutation) (*model.Server, error) {
	if srv.Name == "" || srv.Host == "" {
		return nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
	}
	if srv.Port <= 0 {
		srv.Port = 22
	}
	if srv.Port > 65535 {
		return nil, invalidConnection()
	}
	if mutation.Set && !IsConnectionAdmin(ctx) {
		return nil, connectionForbidden()
	}
	var config *model.ServerConnectionConfig
	var err error
	if mutation.Set {
		config, err = s.buildConnectionConfig(0, mutation.Config, nil)
		if err != nil {
			return nil, err
		}
	}
	err = s.repo.Transaction(ctx, func(tx *repository.ServerRepo) error {
		if config != nil && config.JumpServerID != nil {
			if err := tx.LockConnectionServers(ctx, []uint{*config.JumpServerID}); err != nil {
				return err
			}
			if err := validateJump(ctx, tx, 0, config); err != nil {
				return err
			}
		}
		srv.ConnectionRevision = 1
		if err := tx.Create(ctx, srv); err != nil {
			return err
		}
		if config != nil {
			config.ServerID = srv.ID
			return tx.SaveConnectionConfig(ctx, config)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	srv.Status = "unknown"
	decorateHostKey(srv)
	// Build the response from the committed configuration without requiring a
	// metric sample (or a second write) for a newly created server.
	srv.Connection = connectionView(config, IsConnectionAdmin(ctx))
	return srv, nil
}

func equalUint(left, right *uint) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalConnection(left, right *model.ServerConnectionConfig) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Mode != right.Mode || !equalUint(left.JumpServerID, right.JumpServerID) || left.SocksHost != right.SocksHost || left.SocksPort != right.SocksPort || left.SocksAuthType != right.SocksAuthType || left.SocksUsername != right.SocksUsername || !bytes.Equal(left.Salt, right.Salt) {
		return false
	}
	return left.EncryptedPassword == nil && right.EncryptedPassword == nil || left.EncryptedPassword != nil && right.EncryptedPassword != nil && *left.EncryptedPassword == *right.EncryptedPassword
}

func (s *ServerService) updateServer(ctx context.Context, id uint, input *model.Server, clearCredential bool, mutation ConnectionMutation) (*model.Server, error) {
	if mutation.Set {
		if !IsConnectionAdmin(ctx) {
			return nil, connectionForbidden()
		}
		if mutation.ExpectedRevision == nil {
			return nil, invalidConnection()
		}
		if err := mutation.Config.validate(); err != nil {
			return nil, err
		}
	}
	ids := []uint{id}
	if mutation.Set && mutation.Config != nil && mutation.Config.JumpServerID != nil {
		ids = append(ids, *mutation.Config.JumpServerID)
	}
	changed := false
	err := s.repo.Transaction(ctx, func(tx *repository.ServerRepo) error {
		if err := tx.LockConnectionServers(ctx, ids); err != nil {
			return err
		}
		existing, err := tx.FindByID(ctx, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("server %d: %w", id, server.ErrNotFound)
		}
		if err != nil {
			return err
		}
		if mutation.Set && *mutation.ExpectedRevision != existing.ConnectionRevision {
			return server.NewAppError(http.StatusConflict, server.ReasonConnectionConfigChanged)
		}
		oldConfig, err := tx.FindConnectionConfig(ctx, id)
		if err != nil {
			return err
		}
		dependents, err := tx.FindJumpDependentIDs(ctx, id)
		if err != nil {
			return err
		}
		credential := resolveCredentialBinding(existing.CredentialID, input.CredentialID, clearCredential)
		endpointChanged := input.Host != "" && input.Host != existing.Host || input.Port > 0 && input.Port != existing.Port
		credentialChanged := !equalUint(existing.CredentialID, credential)
		if (endpointChanged || credentialChanged) && (oldConfig != nil || len(dependents) > 0) && !IsConnectionAdmin(ctx) {
			return connectionForbidden()
		}
		newConfig := oldConfig
		if mutation.Set {
			newConfig, err = s.buildConnectionConfig(id, mutation.Config, oldConfig)
			if err != nil {
				return err
			}
			if newConfig != nil && len(dependents) > 0 {
				return server.NewAppError(http.StatusConflict, server.ReasonConnectionInUse)
			}
			if err := validateJump(ctx, tx, id, newConfig); err != nil {
				return err
			}
		}
		changed = endpointChanged || credentialChanged || !equalConnection(oldConfig, newConfig)
		if endpointChanged {
			existing.HostKey, existing.HostKeySeen, existing.HostKeyMismatchAt = nil, nil, nil
		}
		if input.Name != "" {
			existing.Name = input.Name
		}
		if input.Host != "" {
			existing.Host = input.Host
		}
		if input.Port > 0 {
			existing.Port = input.Port
		}
		if existing.Port > 65535 {
			return invalidConnection()
		}
		if input.Description != nil {
			existing.Description = input.Description
		}
		if input.Notes != nil {
			existing.Notes = input.Notes
		}
		existing.CredentialID = credential
		if changed {
			existing.ConnectionRevision++
		}
		if err := tx.UpdateConnectionFields(ctx, existing); err != nil {
			return err
		}
		if mutation.Set {
			if newConfig == nil {
				return tx.DeleteConnectionConfig(ctx, id)
			}
			return tx.SaveConnectionConfig(ctx, newConfig)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if changed {
		s.invalidateWithDependents(ctx, id)
	}
	return s.Get(ctx, id)
}

func (s *ServerService) deleteServer(ctx context.Context, id uint) error {
	err := s.repo.Transaction(ctx, func(tx *repository.ServerRepo) error {
		if err := tx.LockConnectionServers(ctx, []uint{id}); err != nil {
			return err
		}
		deps, err := tx.FindJumpDependentIDs(ctx, id)
		if err != nil {
			return err
		}
		if len(deps) > 0 {
			return server.NewAppError(http.StatusConflict, server.ReasonConnectionInUse)
		}
		if err := tx.DeleteConnectionConfig(ctx, id); err != nil {
			return err
		}
		return tx.Delete(ctx, id)
	})
	if err == nil {
		s.invalidate(id)
	}
	return err
}

func (s *ServerService) trustHostKey(ctx context.Context, id uint, fingerprint string, revision ...uint64) (*model.Server, error) {
	if !IsConnectionAdmin(ctx) {
		return nil, connectionForbidden()
	}
	err := s.repo.Transaction(ctx, func(tx *repository.ServerRepo) error {
		if err := tx.LockConnectionServers(ctx, []uint{id}); err != nil {
			return err
		}
		srv, err := tx.FindByID(ctx, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("server %d: %w", id, server.ErrNotFound)
		}
		if err != nil {
			return err
		}
		if srv.HostKeySeen == nil || srv.HostKeyMismatchAt == nil {
			return server.NewAppError(http.StatusConflict, server.ReasonNoHostKeyMismatch)
		}
		if fingerprint == "" || fingerprint != sshpool.Fingerprint(*srv.HostKeySeen) {
			return server.NewAppError(http.StatusConflict, server.ReasonHostKeyChanged)
		}
		if len(revision) > 0 && revision[0] != srv.ConnectionRevision {
			return server.NewAppError(http.StatusConflict, server.ReasonHostKeyChanged)
		}
		srv.HostKey, srv.HostKeySeen, srv.HostKeyMismatchAt = srv.HostKeySeen, nil, nil
		srv.ConnectionRevision++
		return tx.UpdateConnectionFields(ctx, srv)
	})
	if err != nil {
		return nil, err
	}
	s.invalidateWithDependents(ctx, id)
	return s.Get(ctx, id)
}

func connectionView(config *model.ServerConnectionConfig, detail bool) *model.ConnectionConfig {
	view := &model.ConnectionConfig{Mode: configMode(config)}
	if detail && config != nil {
		view.JumpServerID = config.JumpServerID
		if config.Mode == model.ConnectionSOCKS5 {
			view.Host, view.Port, view.AuthType, view.Username = config.SocksHost, config.SocksPort, config.SocksAuthType, config.SocksUsername
			hasPassword := config.EncryptedPassword != nil
			view.HasPassword = &hasPassword
		}
	}
	return view
}
