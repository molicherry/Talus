package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"gorm.io/gorm"
)

type APIKeyService struct {
	repo       *repository.APIKeyRepo
	serverRepo *repository.ServerRepo
	masterKey  *crypto.MasterKey
}

func NewAPIKeyService(repo *repository.APIKeyRepo, serverRepo *repository.ServerRepo, masterKey *crypto.MasterKey) *APIKeyService {
	return &APIKeyService{repo: repo, serverRepo: serverRepo, masterKey: masterKey}
}

type CreateAPIKeyResult struct {
	Key    string       `json:"key"`
	APIKey *model.APIKey `json:"api_key"`
}

func (s *APIKeyService) Create(ctx context.Context, name string, scopes []string, serverIDs []uint) (*CreateAPIKeyResult, error) {
	if len(scopes) == 0 {
		scopes = model.AllScopeGatedScopes
	}

	if invalid := mw.ValidateScopes(scopes); len(invalid) > 0 {
		return nil, server.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("invalid scopes: %v", invalid))
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
			return nil, server.NewAppError(http.StatusBadRequest,
				fmt.Sprintf("invalid server_ids: servers not found: %v", missing))
		}
	}

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
	key := s.masterKey.DeriveKey(salt)
	encryptedRaw, err := crypto.Encrypt([]byte(rawStr), key)
	if err != nil {
		return nil, fmt.Errorf("encrypt raw key: %w", err)
	}

	k := &model.APIKey{
		Name:            name,
		KeyHash:         hashStr,
		KeyPrefix:       prefix,
		Scopes:          scopes,
		ServerIDs:       serverIDs,
		EncryptedRawKey: encryptedRaw,
		Salt:            salt,
	}
	if err := s.repo.Create(ctx, k); err != nil {
		return nil, fmt.Errorf("save key: %w", err)
	}

	return &CreateAPIKeyResult{Key: rawStr, APIKey: k}, nil
}

func (s *APIKeyService) List(ctx context.Context) ([]model.APIKey, error) {
	return s.repo.FindAll(ctx)
}

func (s *APIKeyService) Delete(ctx context.Context, id uint) error {
	return s.repo.Delete(ctx, id)
}

// Reveal decrypts and returns the raw API key string.
func (s *APIKeyService) Reveal(ctx context.Context, id uint) (string, error) {
	k, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", server.NewAppError(http.StatusNotFound, "api key not found")
		}
		return "", fmt.Errorf("reveal key %d: %w", id, err)
	}
	if k.EncryptedRawKey == "" {
		return "", server.NewAppError(http.StatusNotFound, "raw key not available")
	}
	key := s.masterKey.DeriveKey(k.Salt)
	plain, err := crypto.Decrypt(k.EncryptedRawKey, key)
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
		return nil, fmt.Errorf("validate api key: %w", err)
	}
	return key, nil
}
