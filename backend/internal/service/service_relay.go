package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

// hopByHopHeaders are headers that must not be forwarded by proxies (RFC 2616 §13.5.1).
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"TE":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

const relayTimeout = 30 * time.Second

// ServiceRelayService provides business logic for external service management and relay.
type ServiceRelayService struct {
	repo       *repository.ServiceRepo
	masterKey  *crypto.MasterKey
	httpClient *http.Client
}

// NewServiceRelayService creates a ServiceRelayService with the given dependencies.
func NewServiceRelayService(repo *repository.ServiceRepo, masterKey *crypto.MasterKey) *ServiceRelayService {
	return &ServiceRelayService{
		repo:      repo,
		masterKey: masterKey,
		httpClient: &http.Client{
			Timeout: relayTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// CreateServiceInput is the validated input for creating a service.
type CreateServiceInput struct {
	Name            string
	DisplayName     string
	BaseURL         string
	Credentials     map[string]string
	CredentialHints map[string]string
	Description     *string
	UsageGuide      *string
	ServerID        *uint
}

// RelayInput is the validated relay request body.
type RelayInput struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// validateAndEncryptCredentials validates the input fields and encrypts the credential map
// with a freshly generated salt. Create and Update share this helper to prevent rule drift.
func (s *ServiceRelayService) validateAndEncryptCredentials(input CreateServiceInput) (encryptedCreds map[string]string, hints map[string]string, salt []byte, err error) {
	if input.Name == "" {
		return nil, nil, nil, fmt.Errorf("name is required")
	}
	if input.BaseURL == "" {
		return nil, nil, nil, fmt.Errorf("base_url is required")
	}
	if !strings.HasPrefix(input.BaseURL, "http://") && !strings.HasPrefix(input.BaseURL, "https://") {
		return nil, nil, nil, fmt.Errorf("base_url must start with http:// or https://")
	}
	if len(input.Credentials) == 0 {
		return nil, nil, nil, fmt.Errorf("at least one credential is required")
	}

	hints = input.CredentialHints
	if hints == nil {
		hints = map[string]string{}
	}
	for k := range hints {
		if _, ok := input.Credentials[k]; !ok {
			return nil, nil, nil, fmt.Errorf("credential_hints key '%s' does not match any credential", k)
		}
	}

	salt, err = crypto.GenerateSalt()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate salt: %w", err)
	}

	key := s.masterKey.DeriveKey(salt)
	encryptedCreds = make(map[string]string, len(input.Credentials))
	for k, v := range input.Credentials {
		if v == "" {
			return nil, nil, nil, fmt.Errorf("credential '%s' value is required", k)
		}
		encrypted, encErr := crypto.Encrypt([]byte(v), key)
		if encErr != nil {
			return nil, nil, nil, fmt.Errorf("encrypt '%s': %w", k, encErr)
		}
		encryptedCreds[k] = encrypted
	}

	return encryptedCreds, hints, salt, nil
}

// Create validates, encrypts, and stores a new service.
func (s *ServiceRelayService) Create(ctx context.Context, input CreateServiceInput) (*model.Service, error) {
	encryptedCreds, hints, salt, err := s.validateAndEncryptCredentials(input)
	if err != nil {
		return nil, fmt.Errorf("create service: %w", err)
	}

	svc := &model.Service{
		Name:                 input.Name,
		DisplayName:          input.DisplayName,
		BaseURL:              input.BaseURL,
		EncryptedCredentials: encryptedCreds,
		CredentialHints:      hints,
		Description:          input.Description,
		UsageGuide:           input.UsageGuide,
		Salt:                 salt,
		ServerID:             input.ServerID,
	}

	if err := s.repo.Create(ctx, svc); err != nil {
		return nil, fmt.Errorf("create service: %w", err)
	}
	return svc, nil
}

// Get returns a single service by id.
func (s *ServiceRelayService) Get(ctx context.Context, id uint) (*model.Service, error) {
	svc, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, server.NewAppError(http.StatusNotFound, "service not found")
		}
		return nil, fmt.Errorf("get service %d: %w", id, err)
	}
	return svc, nil
}

// GetCredentials decrypts and returns the credential map for a service.
func (s *ServiceRelayService) GetCredentials(ctx context.Context, id uint) (map[string]string, error) {
	svc, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, server.NewAppError(http.StatusNotFound, "service not found")
		}
		return nil, fmt.Errorf("get credentials %d: %w", id, err)
	}
	key := s.masterKey.DeriveKey(svc.Salt)
	creds := make(map[string]string, len(svc.EncryptedCredentials))
	for k, v := range svc.EncryptedCredentials {
		plain, err := crypto.Decrypt(v, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential '%s': %w", k, server.NewAppError(http.StatusInternalServerError, "credential decryption failed"))
		}
		creds[k] = string(plain)
	}
	return creds, nil
}

// Update fully replaces an existing service's fields and credentials with a new salt.
func (s *ServiceRelayService) Update(ctx context.Context, id uint, input CreateServiceInput) (*model.Service, error) {
	existing, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, server.NewAppError(http.StatusNotFound, "service not found")
		}
		return nil, fmt.Errorf("update service %d: %w", id, err)
	}

	encryptedCreds, hints, salt, err := s.validateAndEncryptCredentials(input)
	if err != nil {
		return nil, fmt.Errorf("update service %d: %w", id, err)
	}

	existing.Name = input.Name
	existing.DisplayName = input.DisplayName
	existing.BaseURL = input.BaseURL
	existing.EncryptedCredentials = encryptedCreds
	existing.CredentialHints = hints
	existing.Description = input.Description
	// Preserve the usage guide when the caller omits it (nil) — otherwise an
	// API client that does not know about usage_guide would silently wipe a
	// service's guide on every update. Explicitly sending an empty string
	// (or a shorter guide) still updates it, so clearing via the UI works.
	if input.UsageGuide != nil {
		existing.UsageGuide = input.UsageGuide
	}
	existing.Salt = salt
	existing.ServerID = input.ServerID

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("update service %d: %w", id, err)
	}
	return existing, nil
}

// Delete soft-deletes a service by id.
func (s *ServiceRelayService) Delete(ctx context.Context, id uint) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return server.NewAppError(http.StatusNotFound, "service not found")
		}
		return fmt.Errorf("delete service %d: %w", id, err)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete service %d: %w", id, err)
	}
	return nil
}

// List returns all services, optionally filtered by server ID.
// UsageGuide full text is stripped from list responses; only a short excerpt
// is included so AI agents can build a service directory cheaply.
func (s *ServiceRelayService) List(ctx context.Context, serverID *uint) ([]model.Service, error) {
	var services []model.Service
	var err error
	if serverID != nil {
		services, err = s.repo.FindByServerID(ctx, *serverID)
	} else {
		services, err = s.repo.FindAll(ctx)
	}
	if err != nil {
		return nil, err
	}
	for i := range services {
		services[i].UsageGuideExcerpt = excerpt(services[i].UsageGuide, defaultExcerptRunes)
		services[i].UsageGuide = nil
	}
	return services, nil
}

// Relay decrypts service credentials, substitutes placeholders, and proxies the request.
func (s *ServiceRelayService) Relay(ctx context.Context, serviceID uint, input RelayInput, w http.ResponseWriter) error {
	if input.Method == "" {
		return server.NewAppError(http.StatusBadRequest, "method is required")
	}

	svc, err := s.repo.FindByID(ctx, serviceID)
	if err != nil {
		return server.NewAppError(http.StatusNotFound, "service not found")
	}

	// Decrypt all credentials.
	key := s.masterKey.DeriveKey(svc.Salt)
	creds := make(map[string]string, len(svc.EncryptedCredentials))
	for k, v := range svc.EncryptedCredentials {
		plain, err := crypto.Decrypt(v, key)
		if err != nil {
			return fmt.Errorf("decrypt credential '%s': %w", k, server.NewAppError(http.StatusInternalServerError, "credential decryption failed"))
		}
		creds[k] = string(plain)
	}

	// Build target URL. Preserves any query string in the relay path —
	// url.JoinPath escapes '?' into %3F and silently drops query parameters,
	// which broke parameterized requests (e.g. Dokploy tRPC endpoints that pass
	// all arguments via ?input=...).
	targetURL, err := buildTargetURL(svc.BaseURL, input.Path)
	if err != nil {
		return fmt.Errorf("build target url: %w", server.NewAppError(http.StatusBadRequest, "invalid relay path"))
	}

	// Substitute placeholders in path.
	targetURL = substitute(targetURL, creds)

	// Build request.
	var bodyReader io.Reader
	if input.Body != nil && string(input.Body) != "null" {
		bodyStr := substitute(string(input.Body), creds)
		bodyReader = bytes.NewReader([]byte(bodyStr))
	}

	req, err := http.NewRequestWithContext(ctx, input.Method, targetURL, bodyReader)
	if err != nil {
		return fmt.Errorf("build relay request: %w", server.NewAppError(http.StatusBadRequest, "invalid relay request"))
	}

	// Set headers with placeholder substitution.
	if input.Headers != nil {
		for k, v := range input.Headers {
			req.Header.Set(substitute(k, creds), substitute(v, creds))
		}
	}

	// Execute request.
	resp, err := s.httpClient.Do(req)
	if err != nil {
		if isTimeout(err) {
			return server.NewAppError(http.StatusGatewayTimeout, "target service timeout")
		}
		return server.NewAppError(http.StatusBadGateway, fmt.Sprintf("target service unreachable: %v", err))
	}
	defer resp.Body.Close()

	// Copy response — bypasses the WriteJSON envelope for raw passthrough.
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return nil
}

// substitute replaces all {{key}} placeholders in the input string.
func substitute(input string, credentials map[string]string) string {
	for k, v := range credentials {
		input = strings.ReplaceAll(input, "{{"+k+"}}", v)
	}
	return input
}

// buildTargetURL joins a service base URL with a relay path, preserving any
// query string embedded in the path.
//
// url.JoinPath escapes '?' into %3F, silently dropping query parameters —
// services like Dokploy (tRPC over HTTP) pass every argument via ?input=...,
// so the escaped form made parameterized requests unusable through relay.
// This builds the URL manually so ?query survives.
func buildTargetURL(baseURL, path string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	relPath := path
	rawQuery := ""
	if i := strings.Index(path, "?"); i >= 0 {
		relPath = path[:i]
		rawQuery = path[i+1:]
	}

	basePath := strings.TrimSuffix(base.Path, "/")
	rel := strings.TrimPrefix(relPath, "/")
	switch {
	case basePath == "" && rel == "":
		base.Path = "/"
	case rel == "":
		base.Path = basePath + "/"
	default:
		base.Path = basePath + "/" + rel
	}

	// Normalize dot segments (./, ../) the way url.JoinPath did, so relay
	// paths behave identically to the pre-query-fix behavior. Preserve a
	// trailing slash if the caller sent one.
	base.Path = cleanRelayPath(base.Path)

	// Only override the base URL's own query when the relay path carries one;
	// otherwise keep the base URL's query string.
	if rawQuery != "" {
		base.RawQuery = rawQuery
	}
	return base.String(), nil
}

// cleanRelayPath removes dot segments from a path while preserving an
// explicit trailing slash. Encoded dots (%2E) are untouched — cleaning only
// applies to literal '.'/'..' segments.
func cleanRelayPath(p string) string {
	if p == "" || p == "/" {
		return p
	}
	cleaned := path.Clean(p)
	if strings.HasSuffix(p, "/") && !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	return cleaned
}

// copyHeaders copies headers from src to dst, dropping hop-by-hop headers.
func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if hopByHopHeaders[k] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// defaultExcerptRunes is the max length of the usage_guide excerpt surfaced in
// list responses (rune count, not bytes, so UTF-8 text is never cut mid-codepoint).
const defaultExcerptRunes = 200

// excerpt returns the first max runes of s as a summary. nil → "".
// A trailing ellipsis is appended when the guide was truncated.
func excerpt(s *string, max int) string {
	if s == nil {
		return ""
	}
	runes := []rune(*s)
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

// isTimeout checks if the error represents a timeout.
func isTimeout(err error) bool {
	if ue, ok := err.(*url.Error); ok {
		return ue.Timeout()
	}
	return false
}
