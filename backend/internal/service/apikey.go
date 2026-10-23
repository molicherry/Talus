package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/gorm"
)

type APIKeyService struct {
	repo            *repository.APIKeyRepo
	serverRepo      *repository.ServerRepo
	masterKey       *crypto.MasterKey
	permissionsGate *gate.Gate
	terminals       *gate.Registry
}

func NewAPIKeyService(repo *repository.APIKeyRepo, serverRepo *repository.ServerRepo, masterKey *crypto.MasterKey) *APIKeyService {
	return &APIKeyService{repo: repo, serverRepo: serverRepo, masterKey: masterKey,
		permissionsGate: gate.New(repo.PermissionsRevision, repo.LockedPermissionsRevision), terminals: gate.NewRegistry()}
}

type CreateAPIKeyResult struct {
	Key    string        `json:"key"`
	APIKey *model.APIKey `json:"api_key"`
}

// resolveScopes decides what a create request means. A nil slice means the
// caller omitted `scopes` entirely (backwards-compatible default set); an
// explicit empty list is a client error, because silently granting the default
// scopes — which include exec and terminal — is the opposite of the request.
func resolveScopes(scopes *[]string) ([]string, error) {
	if scopes == nil {
		return append([]string(nil), model.AllScopeGatedScopes...), nil
	}
	if len(*scopes) == 0 {
		return nil, server.NewAppError(http.StatusBadRequest, server.ReasonScopesRequired)
	}
	return *scopes, nil
}

func (s *APIKeyService) Create(ctx context.Context, name string, scopes *[]string, serverIDs []uint) (*CreateAPIKeyResult, error) {
	if err := requireAPIKeyAdmin(ctx); err != nil {
		return nil, err
	}
	resolved, err := resolveScopes(scopes)
	if err != nil {
		return nil, err
	}

	if invalid := mw.ValidateScopes(resolved); len(invalid) > 0 {
		return nil, server.NewAppErrorParams(http.StatusBadRequest, server.ReasonInvalidScopes, map[string]any{"scopes": invalid})
	}

	if len(serverIDs) > 0 {
		existing, err := s.serverRepo.FindByIDs(ctx, serverIDs)
		if err != nil {
			return nil, fmt.Errorf("validate server ids: %w", err)
		}
		if len(existing) != len(serverIDs) {
			existingIDs := make(map[uint]bool)
			for _, srv := range existing {
				existingIDs[srv.ID] = true
			}
			var missing []uint
			for _, id := range serverIDs {
				if !existingIDs[id] {
					missing = append(missing, id)
				}
			}
			return nil, server.NewAppErrorParams(http.StatusBadRequest, server.ReasonServersNotFound, map[string]any{"servers": missing})
		}
	}

	result, err := s.generateKey()
	if err != nil {
		return nil, err
	}
	k := result.APIKey
	now := time.Now().UTC()
	k.Name, k.Scopes, k.ServerIDs = name, resolved, serverIDs
	k.PermissionsUpdatedAt = &now
	p := mw.GetPrincipal(ctx)
	if p.AuthType == "jwt" && p.UserID != nil {
		k.UserID = *p.UserID
	}
	if err := s.repo.Create(ctx, k); err != nil {
		return nil, fmt.Errorf("save key: %w", err)
	}
	return result, nil
}

// Creation and rotation use the same cryptographic randomness and encryption.
func (s *APIKeyService) generateKey() (*CreateAPIKeyResult, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	rawStr := hex.EncodeToString(raw)
	prefix := rawStr[:8]

	hash := sha256.Sum256([]byte(rawStr))
	hashStr := hex.EncodeToString(hash[:])

	salt, err := crypto.GenerateSalt()
	if err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	cipher, err := s.masterKey.ForRecord(crypto.VersionCurrent, salt, crypto.PurposeAPIKey)
	if err != nil {
		return nil, err
	}
	encryptedRaw, err := cipher.Encrypt("raw_key", []byte(rawStr))
	if err != nil {
		return nil, fmt.Errorf("encrypt raw key: %w", err)
	}

	k := &model.APIKey{
		CryptoVersion:   crypto.VersionCurrent,
		KeyHash:         hashStr,
		KeyPrefix:       prefix,
		EncryptedRawKey: encryptedRaw,
		Salt:            salt,
	}

	return &CreateAPIKeyResult{Key: rawStr, APIKey: k}, nil
}

func (s *APIKeyService) List(ctx context.Context) ([]model.APIKey, error) {
	if err := requireAPIKeyAdmin(ctx); err != nil {
		return nil, err
	}
	return s.repo.FindAll(ctx)
}

func (s *APIKeyService) Delete(ctx context.Context, id uint) error {
	if err := requireAPIKeyAdmin(ctx); err != nil {
		return err
	}
	var revoked []*gate.Session
	err := s.permissionsGate.Update(ctx, id, func(updateCtx context.Context, _ int64) (bool, error) {
		err := s.repo.Delete(updateCtx, id)
		unknown := errors.Is(err, repository.ErrAPIKeyCommitUnknown)
		if err == nil || unknown {
			revoked = s.terminals.MarkRevoked(id)
		}
		return unknown, err
	})
	s.finishPermissionChange(ctx, id, revoked, err)
	return apiKeyPolicyError(err)
}

// CaptureSnapshot copies only the resource's safe historical label.
func (s *APIKeyService) CaptureSnapshot(ctx context.Context, id uint) {
	if usage.FromContext(ctx) == nil {
		return
	}
	if k, err := s.repo.FindByID(ctx, id); err == nil {
		usage.FromContext(ctx).SetResource("api_key", &id, k.Name, nil)
	}
}

// Reveal decrypts and returns the raw API key string.
func (s *APIKeyService) Reveal(ctx context.Context, id uint) (string, error) {
	if err := requireAPIKeyAdmin(ctx); err != nil {
		return "", err
	}
	k, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", server.NewAppError(http.StatusNotFound, server.ReasonAPIKeyNotFound)
		}
		return "", fmt.Errorf("reveal key %d: %w", id, err)
	}
	if k.EncryptedRawKey == "" {
		return "", server.NewAppError(http.StatusNotFound, server.ReasonRawKeyUnavailable)
	}
	usage.FromContext(ctx).SetResource("api_key", &id, k.Name, nil)
	cipher, err := s.masterKey.ForRecord(k.CryptoVersion, k.Salt, crypto.PurposeAPIKey)
	if err != nil {
		return "", err
	}
	plain, err := cipher.Decrypt("raw_key", k.EncryptedRawKey)
	if err != nil {
		return "", fmt.Errorf("decrypt key: %w", err)
	}
	return string(plain), nil
}

func (s *APIKeyService) Validate(ctx context.Context, rawKey string) (*model.APIKey, error) {
	hash := sha256.Sum256([]byte(rawKey))
	hashStr := hex.EncodeToString(hash[:])
	key, err := s.repo.FindByHash(ctx, hashStr)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, gate.ErrUnavailable
	}
	err = s.permissionsGate.Admit(ctx, key.ID, func(revision int64) error {
		if revision == 0 {
			return gorm.ErrRecordNotFound
		}
		if revision != key.PermissionsRevision {
			key, err = s.repo.FindByID(ctx, key.ID)
			if err != nil {
				return err
			}
		}
		// Rotation may finish between the initial hash lookup and admission.
		// Reloading by ID alone must never admit the replaced credential.
		if key.KeyHash != hashStr {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, gate.ErrUnavailable
	}
	return key, nil
}
