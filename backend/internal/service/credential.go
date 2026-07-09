package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

// CredentialService provides business logic for SSH credential management.
type CredentialService struct {
	repo      *repository.CredentialRepo
	masterKey *crypto.MasterKey
}

// NewCredentialService creates a CredentialService with the given dependencies.
func NewCredentialService(repo *repository.CredentialRepo, masterKey *crypto.MasterKey) *CredentialService {
	return &CredentialService{
		repo:      repo,
		masterKey: masterKey,
	}
}

// CreateCredentialInput is the validated input for creating a credential.
type CreateCredentialInput struct {
	Name       string
	AuthType   string
	Username   string
	Password   string
	PrivateKey string
}

// Create validates, encrypts, and stores a new SSH credential.
func (s *CredentialService) Create(ctx context.Context, input CreateCredentialInput) (*model.SSHCredential, error) {
	if input.AuthType != "password" && input.AuthType != "private_key" {
		return nil, fmt.Errorf("create credential: auth_type must be 'password' or 'private_key'")
	}
	if input.Username == "" {
		return nil, fmt.Errorf("create credential: username is required")
	}

	salt, err := crypto.GenerateSalt()
	if err != nil {
		return nil, fmt.Errorf("create credential: %w", err)
	}

	cred := &model.SSHCredential{
		Name:     input.Name,
		AuthType: input.AuthType,
		Username: input.Username,
		Salt:     salt,
	}

	key := s.masterKey.DeriveKey(salt)
	switch input.AuthType {
	case "password":
		if input.Password == "" {
			return nil, fmt.Errorf("create credential: password is required for auth_type 'password'")
		}
		encrypted, err := crypto.Encrypt([]byte(input.Password), key)
		if err != nil {
			return nil, fmt.Errorf("create credential: encrypt password: %w", err)
		}
		cred.EncryptedPassword = &encrypted

	case "private_key":
		if input.PrivateKey == "" {
			return nil, fmt.Errorf("create credential: private_key is required for auth_type 'private_key'")
		}
		encrypted, err := crypto.Encrypt([]byte(input.PrivateKey), key)
		if err != nil {
			return nil, fmt.Errorf("create credential: encrypt private key: %w", err)
		}
		cred.EncryptedPrivateKey = &encrypted

		// Generate fingerprint as SHA256 of the raw key text.
		sum := sha256.Sum256([]byte(input.PrivateKey))
		fp := fmt.Sprintf("%x", sum)
		cred.KeyFingerprint = &fp
	}

	if err := s.repo.Create(ctx, cred); err != nil {
		return nil, fmt.Errorf("create credential: %w", err)
	}
	return cred, nil
}

// GetByServerID returns the credential for a server with encrypted fields masked.
func (s *CredentialService) GetByServerID(ctx context.Context, serverID uint) (*model.SSHCredential, error) {
	cred, err := s.repo.FindByServerID(ctx, serverID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("credential for server %d: %w", serverID, server.ErrNotFound)
		}
		return nil, fmt.Errorf("credential for server %d: %w", serverID, err)
	}
	return cred, nil
}

// GetDecryptedByID returns the decrypted username, password, and private key for a credential.
func (s *CredentialService) GetDecryptedByID(ctx context.Context, credID uint) (username string, password string, privateKey string, err error) {
	cred, err := s.repo.FindByID(ctx, credID)
	if err != nil {
		return "", "", "", fmt.Errorf("credential %d: %w", credID, err)
	}
	return s.decryptCredential(cred)
}

// GetDecrypted returns the decrypted username and password for a server credential by server ID (deprecated).
func (s *CredentialService) GetDecrypted(ctx context.Context, serverID uint) (username string, password string, privateKey string, err error) {
	cred, err := s.repo.FindByServerID(ctx, serverID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", "", fmt.Errorf("credential for server %d: %w", serverID, server.ErrNotFound)
		}
		return "", "", "", fmt.Errorf("credential for server %d: %w", serverID, err)
	}
	return s.decryptCredential(cred)
}

func (s *CredentialService) decryptCredential(cred *model.SSHCredential) (string, string, string, error) {
	key := s.deriveKeyForCredential(cred)
	if cred.AuthType == "password" && cred.EncryptedPassword != nil {
		plaintext, err := crypto.Decrypt(*cred.EncryptedPassword, key)
		if err != nil {
			return "", "", "", fmt.Errorf("decrypt password: %w", err)
		}
		return cred.Username, string(plaintext), "", nil
	}
	if cred.AuthType == "private_key" && cred.EncryptedPrivateKey != nil {
		plaintext, err := crypto.Decrypt(*cred.EncryptedPrivateKey, key)
		if err != nil {
			return "", "", "", fmt.Errorf("decrypt private key: %w", err)
		}
		return cred.Username, "", string(plaintext), nil
	}
	return "", "", "", fmt.Errorf("no encrypted credential found")
}

// deriveKeyForCredential returns the AES key for the credential, using either
// the per-credential salt or the legacy fixed salt for unmigrated records.
func (s *CredentialService) deriveKeyForCredential(cred *model.SSHCredential) []byte {
	if len(cred.Salt) > 0 {
		return s.masterKey.DeriveKey(cred.Salt)
	}
	return s.masterKey.LegacyDeriveKey()
}

// UpdateCredentialInput is the validated input for updating a credential.
type UpdateCredentialInput struct {
	Username   *string
	Password   *string
	PrivateKey *string
}

// Update modifies an existing credential. Only non-nil fields are updated.
func (s *CredentialService) Update(ctx context.Context, id uint, input UpdateCredentialInput) (*model.SSHCredential, error) {
	cred, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("credential %d: %w", id, server.ErrNotFound)
		}
		return nil, fmt.Errorf("credential %d: %w", id, err)
	}

	if input.Username != nil {
		cred.Username = *input.Username
	}

	needsReEncrypt := (input.Password != nil && *input.Password != "") || (input.PrivateKey != nil && *input.PrivateKey != "")

	if needsReEncrypt {
		salt, err := crypto.GenerateSalt()
		if err != nil {
			return nil, fmt.Errorf("credential %d: %w", id, err)
		}
		cred.Salt = salt
		key := s.masterKey.DeriveKey(salt)

		if input.Password != nil && *input.Password != "" {
			encrypted, err := crypto.Encrypt([]byte(*input.Password), key)
			if err != nil {
				return nil, fmt.Errorf("credential %d: encrypt password: %w", id, err)
			}
			cred.EncryptedPassword = &encrypted
			cred.EncryptedPrivateKey = nil
			cred.AuthType = "password"
		}
		if input.PrivateKey != nil && *input.PrivateKey != "" {
			encrypted, err := crypto.Encrypt([]byte(*input.PrivateKey), key)
			if err != nil {
				return nil, fmt.Errorf("credential %d: encrypt private key: %w", id, err)
			}
			cred.EncryptedPrivateKey = &encrypted
			cred.EncryptedPassword = nil
			cred.AuthType = "private_key"

			sum := sha256.Sum256([]byte(*input.PrivateKey))
			fp := fmt.Sprintf("%x", sum)
			cred.KeyFingerprint = &fp
		}
	}

	if err := s.repo.Update(ctx, cred); err != nil {
		return nil, fmt.Errorf("credential %d: %w", id, err)
	}
	return cred, nil
}

// Delete soft-deletes a credential by id.
func (s *CredentialService) Delete(ctx context.Context, id uint) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("credential %d: %w", id, err)
	}
	return nil
}

// List returns all credentials with encrypted fields masked.
func (s *CredentialService) List(ctx context.Context) ([]model.SSHCredential, error) {
	creds, err := s.repo.FindAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return creds, nil
}
