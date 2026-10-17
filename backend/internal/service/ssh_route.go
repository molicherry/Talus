package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/vpsmanager/backend/internal/model"
	appcrypto "github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/usage"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// Read the complete dependency graph even when a connection is cached. No
// route is inferred from environment variables or the user's SSH config.
type serverSource interface {
	FindByID(context.Context, uint) (*model.Server, error)
}
type connectionSource interface {
	FindConnectionConfig(context.Context, uint) (*model.ServerConnectionConfig, error)
}
type sshHostKeyStore interface {
	CommitSSHRouteHostKey(context.Context, *repository.SSHRouteSnapshot, uint, []byte, string) (bool, error)
}
type sshRouteSource interface {
	FindSSHRouteSnapshot(context.Context, uint) (*repository.SSHRouteSnapshot, error)
}

type sshRouteSnapshot struct {
	target *model.Server
	config *model.ServerConnectionConfig
	jump   *model.Server
}

func (r *sshRouteSnapshot) mode() string {
	if r.config == nil {
		return "direct"
	}
	return r.config.Mode
}

func connectionConflict() error {
	return server.NewAppError(http.StatusConflict, server.ReasonConnectionConfigChanged)
}

func (s *SSHService) readRoute(ctx context.Context, id uint) (*sshRouteSnapshot, error) {
	var snap *sshRouteSnapshot
	var err error
	coherent := false
	if source, ok := s.serverRepo.(sshRouteSource); ok {
		var stored *repository.SSHRouteSnapshot
		stored, err = source.FindSSHRouteSnapshot(ctx, id)
		if err == nil {
			snap = &sshRouteSnapshot{target: stored.Target, config: stored.Config, jump: stored.Jump}
			coherent = true
		}
	} else {
		var target *model.Server
		target, err = s.serverRepo.FindByID(ctx, id)
		if err == nil {
			snap = &sshRouteSnapshot{target: target}
		}
	}
	if err != nil {
		if errors.Is(err, repository.ErrSSHJumpUnavailable) {
			return nil, routeFailure("ssh_connection_failed", "jump_connect", "jump")
		}
		if errors.Is(err, repository.ErrSSHJumpProxied) {
			return nil, connectionConflict()
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, server.ErrNotFound
		}
		return nil, fmt.Errorf("read SSH target: %w", err)
	}
	if source, ok := s.serverRepo.(connectionSource); ok && !coherent {
		snap.config, err = source.FindConnectionConfig(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read SSH route: %w", err)
		}
	}
	switch snap.mode() {
	case "direct":
	case "ssh_jump":
		if snap.config.JumpServerID == nil || *snap.config.JumpServerID == id {
			return nil, connectionConflict()
		}
		if !coherent {
			snap.jump, err = s.serverRepo.FindByID(ctx, *snap.config.JumpServerID)
			if err != nil {
				return nil, routeFailure("ssh_connection_failed", "jump_connect", "jump")
			}
		}
		if snap.jump == nil {
			return nil, routeFailure("ssh_connection_failed", "jump_connect", "jump")
		}
		if source, ok := s.serverRepo.(connectionSource); ok && !coherent {
			cfg, err := source.FindConnectionConfig(ctx, snap.jump.ID)
			if err != nil || (cfg != nil && cfg.Mode != "direct") {
				return nil, connectionConflict()
			}
		}
		if !validSSHCredential(snap.jump) {
			return nil, routeFailure("ssh_authentication_failed", "jump_auth", "jump")
		}
	case "socks5":
		if snap.config.SocksHost == "" || snap.config.SocksPort < 1 || snap.config.SocksPort > 65535 {
			return nil, connectionConflict()
		}
		if snap.config.SocksAuthType != "none" && snap.config.SocksAuthType != "username_password" {
			return nil, connectionConflict()
		}
		if snap.config.SocksAuthType == "username_password" && (snap.config.SocksUsername == "" || snap.config.EncryptedPassword == nil || len(snap.config.Salt) == 0) {
			return nil, connectionConflict()
		}
	default:
		return nil, connectionConflict()
	}
	if !validSSHCredential(snap.target) {
		return nil, routeFailure("ssh_authentication_failed", "target_auth", "target")
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	return snap, nil
}

func (r *sshRouteSnapshot) stored() *repository.SSHRouteSnapshot {
	return &repository.SSHRouteSnapshot{Target: r.target, Config: r.config, Jump: r.jump}
}

func validSSHCredential(srv *model.Server) bool {
	if srv.CredentialID == nil || srv.Credential == nil || srv.Credential.ID != *srv.CredentialID || srv.Credential.DeletedAt.Valid {
		return false
	}
	c := srv.Credential
	return c.Username != "" && ((c.AuthType == "password" && c.EncryptedPassword != nil) || (c.AuthType == "private_key" && c.EncryptedPrivateKey != nil))
}

// Hash structured values to avoid delimiter ambiguity and exposing secret
// material in pool fingerprints. Include identity and warning state so a hit
// cannot bypass new host-key validation.
func fingerprintValue(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func serverFingerprint(srv *model.Server) string {
	var cred any
	if srv.Credential != nil {
		c := srv.Credential
		cred = struct {
			ID                   uint
			UpdatedAt            any
			AuthType, Username   string
			Password, PrivateKey *string
			CryptoVersion        int
			Salt                 []byte
			Deleted              bool
		}{c.ID, c.UpdatedAt, c.AuthType, c.Username, c.EncryptedPassword, c.EncryptedPrivateKey, c.CryptoVersion, c.Salt, c.DeletedAt.Valid}
	}
	return fingerprintValue(struct {
		Host         string
		Port         int
		Revision     uint64
		CredentialID *uint
		Credential   any
		Key, Seen    *[]byte
		Mismatch     any
	}{srv.Host, srv.Port, srv.ConnectionRevision, srv.CredentialID, cred, srv.HostKey, srv.HostKeySeen, srv.HostKeyMismatchAt})
}
func (r *sshRouteSnapshot) fingerprint() string {
	if r.mode() == "direct" {
		return serverFingerprint(r.target)
	}
	cfg := r.config
	jump := ""
	if r.jump != nil {
		jump = serverFingerprint(r.jump)
	}
	return fingerprintValue(struct {
		Target, Mode, Jump string
		JumpID             *uint
		Host               string
		Port               int
		AuthType, Username string
		Password           *string
		CryptoVersion      int
		Salt               []byte
	}{serverFingerprint(r.target), r.mode(), jump, cfg.JumpServerID, cfg.SocksHost, cfg.SocksPort, cfg.SocksAuthType, cfg.SocksUsername, cfg.EncryptedPassword, cfg.CryptoVersion, cfg.Salt})
}

func (s *SSHService) endpoint(srv *model.Server) (sshpool.Endpoint, error) {
	if s.credSvc == nil {
		return sshpool.Endpoint{}, server.ErrInternal
	}
	username, password, privateKey, err := s.credSvc.decryptCredential(srv.Credential)
	if err != nil {
		return sshpool.Endpoint{}, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt)
	}
	auth, err := buildAuthMethod(password, privateKey)
	if err != nil {
		return sshpool.Endpoint{}, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt)
	}
	var key []byte
	if srv.HostKey != nil {
		key = *srv.HostKey
	}
	return sshpool.Endpoint{Host: srv.Host, Port: srv.Port, Username: username, AuthMethod: auth, KnownHostKey: key}, nil
}
func (s *SSHService) dialRoute(ctx context.Context, snap *sshRouteSnapshot) (*ssh.Client, []byte, []byte, error) {
	target, err := s.endpoint(snap.target)
	if err != nil {
		return nil, nil, nil, err
	}
	route := sshpool.Route{Target: target, Mode: snap.mode()}
	if snap.jump != nil {
		jump, err := s.endpoint(snap.jump)
		if err != nil {
			return nil, nil, nil, err
		}
		route.Jump = &jump
	}
	if snap.mode() == "socks5" {
		cfg := snap.config
		socks := &sshpool.SOCKSConfig{Host: cfg.SocksHost, Port: cfg.SocksPort, AuthType: cfg.SocksAuthType, Username: cfg.SocksUsername}
		if cfg.SocksAuthType == "username_password" {
			if s.credSvc == nil || s.credSvc.masterKey == nil {
				return nil, nil, nil, server.ErrInternal
			}
			cipher, err := s.credSvc.masterKey.ForRecord(cfg.CryptoVersion, cfg.Salt, appcrypto.PurposeSOCKS5)
			if err != nil {
				return nil, nil, nil, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt)
			}
			password, err := cipher.Decrypt("password", *cfg.EncryptedPassword)
			if err != nil {
				return nil, nil, nil, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt)
			}
			socks.Password = string(password)
		}
		route.SOCKS = socks
	}
	return sshpool.DialRouteContext(ctx, route, s.sshDialTimeout)
}

// AcquireLease preserves the target-pinned operation contract used by Exec,
// terminals and multi-step agent deployment while sharing proxy route validation.
func (s *SSHService) AcquireLease(ctx context.Context, id uint, consumer ...LeaseConsumer) (*sshLease, error) {
	h, err := s.getLease(sshpool.WithQuotaConsumer(ctx, string(leaseConsumer(consumer))), id, false)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, err
	}
	return newSSHLease(s, h), nil
}

// GetLease returns ownership of one concurrency slot and the entire SSH chain.
// Every successful acquisition has exactly one idempotent Release or Discard.
func (s *SSHService) GetLease(ctx context.Context, id uint) (*sshpool.Lease, error) {
	return s.getLease(ctx, id, false)
}

// GetClient retains the legacy API for callers that return clients through the
// pool's Release/Discard methods. New code should retain the lease itself.
func (s *SSHService) GetClient(ctx context.Context, id uint) (*ssh.Client, error) {
	lease, err := s.GetLease(ctx, id)
	if err != nil {
		return nil, err
	}
	return lease.LegacyClient(), nil
}

func (s *SSHService) getLease(ctx context.Context, id uint, fresh bool) (*sshpool.Lease, error) {
	lease, _, err := s.getLeaseWithSnapshot(ctx, id, fresh)
	return lease, err
}

// getLeaseWithSnapshot returns the exact route verified for the borrowed client.
// Callers reporting a connection test must not read a newer route after handoff
// and attribute that route to an earlier successful handshake.
func (s *SSHService) getLeaseWithSnapshot(ctx context.Context, id uint, fresh bool) (*sshpool.Lease, *sshRouteSnapshot, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, context.Cause(ctx)
		}
		snap, err := s.readRoute(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		usage.FromContext(ctx).SetResource("server", &snap.target.ID, snap.target.Name, &snap.target.ID)
		op := usage.FromContext(ctx)
		op.SetMetadata("connection_mode", snap.mode())
		op.SetMetadata("connection_revision", snap.target.ConnectionRevision)
		var lease *sshpool.Lease
		if fresh || !hasTrustedHostKey(snap.target) || (snap.jump != nil && !hasTrustedHostKey(snap.jump)) {
			lease, err = s.pool.AcquireFreshContext(ctx, id, snap.fingerprint())
		} else {
			lease, err = s.pool.AcquireContext(ctx, id, snap.fingerprint())
		}
		if err != nil {
			if errors.Is(err, sshpool.ErrStaleLease) {
				continue
			}
			return nil, nil, fmt.Errorf("acquire SSH connection: %w", err)
		}
		var targetKey, jumpKey []byte
		if lease.Client == nil {
			dialCtx, cleanup := lease.DialContext(ctx)
			client, tk, jk, dialErr := s.dialRoute(dialCtx, snap)
			if dialErr == nil {
				dialErr = dialCtx.Err()
			}
			if dialErr == nil {
				lease.SetClient(client)
			}
			cleanup()
			targetKey, jumpKey = tk, jk
			if dialErr != nil {
				if client != nil {
					_ = client.Close()
				}
				lease.Discard()
				if ctx.Err() != nil {
					return nil, nil, context.Cause(ctx)
				}
				if errors.Is(dialErr, context.Canceled) {
					return nil, nil, sshpool.ErrClosed
				}
				if recordErr := s.recordMismatch(ctx, snap, dialErr); recordErr != nil {
					return nil, nil, recordErr
				}
				return nil, nil, safeRouteError(ctx, dialErr)
			}
		}
		if ctx.Err() != nil {
			lease.Discard()
			return nil, nil, context.Cause(ctx)
		}
		// The slot wait, cache probe and handshake can all span a configuration
		// update. Re-read before committing identity or opening a business session.
		current, readErr := s.readRoute(ctx, id)
		if readErr != nil || current.fingerprint() != snap.fingerprint() || !lease.Valid() {
			lease.Discard()
			if ctx.Err() != nil {
				return nil, nil, context.Cause(ctx)
			}
			if readErr != nil && !errors.Is(readErr, server.ErrNotFound) {
				return nil, nil, readErr
			}
			continue
		}
		changed, commitErr := s.confirmHostKeys(ctx, snap, targetKey, jumpKey)
		if commitErr != nil {
			lease.Discard()
			if errors.Is(commitErr, errRouteChanged) {
				continue
			}
			return nil, nil, commitErr
		}
		current, readErr = s.readRoute(ctx, id)
		if readErr != nil || current.fingerprint() != snap.fingerprint() || !lease.Valid() {
			lease.Discard()
			if ctx.Err() != nil {
				return nil, nil, context.Cause(ctx)
			}
			continue
		}
		if changed && !lease.RebindFingerprint(snap.fingerprint()) {
			lease.Discard()
			continue
		}
		return lease, snap, nil
	}
	return nil, nil, connectionConflict()
}

var errRouteChanged = errors.New("SSH connection configuration changed")

func hasTrustedHostKey(srv *model.Server) bool {
	return srv.HostKey != nil && len(*srv.HostKey) > 0
}

func (s *SSHService) confirmHostKeys(ctx context.Context, snap *sshRouteSnapshot, targetKey, jumpKey []byte) (bool, error) {
	changed := false
	for _, pair := range []struct {
		srv *model.Server
		key []byte
	}{{snap.jump, jumpKey}, {snap.target, targetKey}} {
		if pair.srv == nil || len(pair.key) == 0 {
			continue
		}
		srv := pair.srv
		kind := ""
		if !hasTrustedHostKey(srv) {
			return false, server.ErrSSHHostKeyUnverified
		} else if srv.HostKeyMismatchAt != nil {
			kind = "clear"
		}
		if kind == "" {
			continue
		}
		store, ok := s.serverRepo.(sshHostKeyStore)
		if !ok {
			return false, server.ErrInternal
		}
		updated, err := store.CommitSSHRouteHostKey(ctx, snap.stored(), srv.ID, pair.key, kind)
		if err != nil {
			return false, fmt.Errorf("commit SSH host identity: %w", err)
		}
		confirmed := *srv
		confirmed.ConnectionRevision++
		confirmed.HostKeySeen, confirmed.HostKeyMismatchAt = nil, nil
		if !updated {
			// An identical concurrent clear may win once. Accept only that exact
			// identity transition; a route, endpoint or credential edit is a conflict.
			confirmedRoute := *snap
			if srv == snap.target {
				confirmedRoute.target = &confirmed
			} else {
				confirmedRoute.jump = &confirmed
			}
			latest, err := s.readRoute(ctx, snap.target.ID)
			if err != nil || latest.fingerprint() != confirmedRoute.fingerprint() {
				return false, errRouteChanged
			}
		}
		*srv = confirmed
		changed = true
	}
	return changed, nil
}

func (s *SSHService) recordMismatch(ctx context.Context, snap *sshRouteSnapshot, err error) error {
	var mismatch *sshpool.HostKeyMismatchError
	var unverified *sshpool.HostKeyUnverifiedError
	var presented []byte
	if errors.As(err, &mismatch) {
		presented = mismatch.Presented
	} else if errors.As(err, &unverified) {
		presented = unverified.Presented
	} else {
		return nil
	}
	srv := snap.target
	var routeErr *sshpool.RouteError
	if errors.As(err, &routeErr) && routeErr.Role == "jump" {
		srv = snap.jump
	}
	if srv == nil {
		return server.ErrInternal
	}
	if srv.HostKeySeen != nil && bytes.Equal(*srv.HostKeySeen, presented) && srv.HostKeyMismatchAt != nil {
		return nil
	}
	store, ok := s.serverRepo.(sshHostKeyStore)
	if !ok {
		return server.ErrInternal
	}
	updated, commitErr := store.CommitSSHRouteHostKey(ctx, snap.stored(), srv.ID, presented, "mismatch")
	if commitErr != nil {
		return server.ErrInternal
	}
	if !updated {
		return connectionConflict()
	}
	s.invalidateRouteIdentity(ctx, srv.ID)
	s.pool.Invalidate(snap.target.ID)
	return nil
}

func routeFailure(reason, stage, role string) *server.AppError {
	status := http.StatusBadGateway
	switch reason {
	case server.ReasonSSHTimeout:
		status = http.StatusGatewayTimeout
	case server.ReasonSSHHostKeyUnverified:
		status = http.StatusConflict
	}
	app := server.NewAppErrorParams(status, reason, map[string]any{"stage": stage, "role": role})
	switch reason {
	case server.ReasonSSHConnection:
		app.Err = server.ErrSSHConnection
	case server.ReasonSSHAuth:
		app.Err = server.ErrSSHAuth
	case server.ReasonSSHTimeout:
		app.Err = server.ErrSSHTimeout
	case server.ReasonSSHHostKeyMismatch:
		app.Err = server.ErrSSHHostKeyMismatch
	case server.ReasonSSHHostKeyUnverified:
		app.Err = server.ErrSSHHostKeyUnverified
	}
	return app
}
func safeRouteError(ctx context.Context, err error) error {
	var app *server.AppError
	if errors.As(err, &app) {
		return app
	}
	reason, stage, role := server.ReasonSSHConnection, "target_connect", "target"
	var routeErr *sshpool.RouteError
	if errors.As(err, &routeErr) {
		reason, stage, role = routeErr.Reason, routeErr.Stage, routeErr.Role
		if reason == "ssh_auth_failed" {
			reason = server.ReasonSSHAuth
		}
	}
	if errors.Is(err, sshpool.ErrHostKeyMismatch) {
		reason = server.ReasonSSHHostKeyMismatch
	}
	if errors.Is(err, sshpool.ErrHostKeyUnverified) {
		reason = server.ReasonSSHHostKeyUnverified
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = server.ReasonSSHTimeout
	}
	usage.FromContext(ctx).SetMetadata("connection_stage", stage)
	usage.FromContext(ctx).SetMetadata("connection_role", role)
	return routeFailure(reason, stage, role)
}

func (s *SSHService) invalidateRouteIdentity(ctx context.Context, id uint) {
	s.pool.Invalidate(id)
	if source, ok := s.serverRepo.(interface {
		FindJumpDependentIDs(context.Context, uint) ([]uint, error)
	}); ok {
		if ids, err := source.FindJumpDependentIDs(ctx, id); err == nil {
			for _, dependent := range ids {
				s.pool.Invalidate(dependent)
			}
		}
	}
}
