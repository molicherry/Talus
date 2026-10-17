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
	"sync"
	"sync/atomic"
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

const (
	// relayStandardBudget is the total business budget for the default mode.
	relayStandardBudget = 30 * time.Second
	// relayBoundedStreamBudget is the total business budget for an explicit
	// bounded_stream request (REQUIREMENTS §7.1.3).
	relayBoundedStreamBudget = 300 * time.Second
	// relayHeaderWait caps the wait for upstream response headers, and also the
	// body progress idle window; neither resets the total budget.
	relayHeaderWait  = 30 * time.Second
	relayIdleTimeout = 30 * time.Second
)

// ServiceRelayService provides business logic for external service management and relay.
type ServiceRelayService struct {
	repo       *repository.ServiceRepo
	masterKey  *crypto.MasterKey
	httpClient *http.Client
	// Budgets are fields so tests can use short values; zero falls back to the
	// package defaults below.
	standardBudget time.Duration
	boundedBudget  time.Duration
	headerWait     time.Duration
	idleTimeout    time.Duration
}

// NewServiceRelayService creates a ServiceRelayService with the given dependencies.
func NewServiceRelayService(repo *repository.ServiceRepo, masterKey *crypto.MasterKey) *ServiceRelayService {
	return &ServiceRelayService{
		repo:      repo,
		masterKey: masterKey,
		httpClient: &http.Client{
			// No client-wide Timeout: the total budget is per request (standard
			// 30s, bounded_stream 300s), and a fixed client timeout would cut a
			// long stream short.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		standardBudget: relayStandardBudget,
		boundedBudget:  relayBoundedStreamBudget,
		headerWait:     relayHeaderWait,
		idleTimeout:    relayIdleTimeout,
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
	Mode    string            `json:"mode"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// RelayModes are the accepted relay modes; the empty value means standard.
var RelayModes = map[string]bool{"": true, "standard": true, "bounded_stream": true}

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

	cipher, err := s.masterKey.ForRecord(crypto.VersionCurrent, salt, crypto.PurposeService)
	if err != nil {
		return nil, nil, nil, err
	}
	encryptedCreds = make(map[string]string, len(input.Credentials))
	for k, v := range input.Credentials {
		if v == "" {
			return nil, nil, nil, fmt.Errorf("credential '%s' value is required", k)
		}
		encrypted, encErr := cipher.Encrypt(k, []byte(v))
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
		CryptoVersion:        crypto.VersionCurrent,
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
		if ctx.Value(relayBudgetKey{}) != nil && ctx.Err() != nil {
			_, relayErr := relayContextError(ctx.Err())
			return nil, relayErr
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, server.NewAppError(http.StatusNotFound, server.ReasonServiceNotFound)
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
			return nil, server.NewAppError(http.StatusNotFound, server.ReasonServiceNotFound)
		}
		return nil, fmt.Errorf("get credentials %d: %w", id, err)
	}
	cipher, err := s.masterKey.ForRecord(svc.CryptoVersion, svc.Salt, crypto.PurposeService)
	if err != nil {
		return nil, err
	}
	creds := make(map[string]string, len(svc.EncryptedCredentials))
	for k, v := range svc.EncryptedCredentials {
		plain, err := cipher.Decrypt(k, v)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential '%s': %w", k, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt))
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
			return nil, server.NewAppError(http.StatusNotFound, server.ReasonServiceNotFound)
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
	existing.CryptoVersion = crypto.VersionCurrent
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
			return server.NewAppError(http.StatusNotFound, server.ReasonServiceNotFound)
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
	_, err := s.RelayWithResult(ctx, serviceID, input, w)
	return err
}

// RelayResult separates transport status from the operation outcome. CopyError
// is returned to the caller only; its text must never enter usage metadata.
type RelayResult struct {
	UpstreamStatus int
	BytesCopied    int64
	HeadersWritten bool
	CopyError      error
	Outcome        string
	Reason         string
}

// relayBudgetKey marks the budget frozen at the handler boundary. Service calls
// reuse this context rather than restarting the clock after resource lookup.
type relayBudgetKey struct{}

// RelayContext validates the mode and freezes the total business deadline.
// Handlers call it before resource/permission lookups; direct service callers
// receive the same protection before their first repository operation.
func (s *ServiceRelayService) RelayContext(ctx context.Context, mode string) (context.Context, context.CancelFunc, error) {
	if !RelayModes[mode] {
		return nil, nil, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
	}
	if mode == "" {
		mode = "standard"
	}
	if existing, ok := ctx.Value(relayBudgetKey{}).(string); ok && existing == mode {
		return ctx, func() {}, nil
	}
	budget := s.standardBudget
	if budget <= 0 || budget > relayStandardBudget {
		budget = relayStandardBudget
	}
	if mode == "bounded_stream" {
		budget = s.boundedBudget
		if budget <= 0 || budget > relayBoundedStreamBudget {
			budget = relayBoundedStreamBudget
		}
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	return context.WithValue(ctx, relayBudgetKey{}, mode), cancel, nil
}

func (s *ServiceRelayService) RelayWithResult(ctx context.Context, serviceID uint, input RelayInput, w http.ResponseWriter) (RelayResult, error) {
	ctx, cancel, err := s.RelayContext(ctx, input.Mode)
	if err != nil {
		return RelayResult{Outcome: "rejected", Reason: server.ReasonInvalidRequest}, err
	}
	defer cancel()
	if input.Method == "" {
		return RelayResult{Outcome: "rejected", Reason: server.ReasonMethodRequired}, server.NewAppError(http.StatusBadRequest, server.ReasonMethodRequired)
	}
	svc, err := s.Get(ctx, serviceID)
	if err != nil {
		if ctx.Err() != nil {
			return relayContextError(ctx.Err())
		}
		return RelayResult{Outcome: "rejected", Reason: server.ReasonServiceNotFound}, err
	}
	return s.relayFromService(ctx, svc, input, w)
}

func relayContextError(err error) (RelayResult, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return RelayResult{Outcome: "timeout", Reason: "relay_budget_exceeded"}, server.NewAppError(http.StatusGatewayTimeout, server.ReasonRelayTimeout)
	}
	return RelayResult{Outcome: "cancelled", Reason: "client_cancelled"}, err
}

var errRelayIdle = errors.New("relay idle timeout")

func (s *ServiceRelayService) relayFromService(ctx context.Context, svc *model.Service, input RelayInput, w http.ResponseWriter) (RelayResult, error) {
	result := RelayResult{Outcome: "failed"}
	ctx, budgetCancel, err := s.RelayContext(ctx, input.Mode)
	if err != nil {
		return RelayResult{Outcome: "rejected", Reason: server.ReasonInvalidRequest}, err
	}
	defer budgetCancel()
	if ctx.Err() != nil {
		return relayContextError(ctx.Err())
	}
	// Stage caps can only shorten the frozen deadline, never restart it.
	headerWait := s.headerWait
	if headerWait <= 0 || headerWait > relayHeaderWait {
		headerWait = relayHeaderWait
	}
	idleTimeout := s.idleTimeout
	if idleTimeout <= 0 || idleTimeout > relayIdleTimeout {
		idleTimeout = relayIdleTimeout
	}
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Decrypt all credentials.
	var cipher *crypto.RecordCipher
	if len(svc.EncryptedCredentials) > 0 {
		cipher, err = s.masterKey.ForRecord(svc.CryptoVersion, svc.Salt, crypto.PurposeService)
		if err != nil {
			result.Reason = server.ReasonCredentialDecrypt
			return result, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt)
		}
	}
	creds := make(map[string]string, len(svc.EncryptedCredentials))
	for k, v := range svc.EncryptedCredentials {
		plain, err := cipher.Decrypt(k, v)
		if err != nil {
			result.Reason = server.ReasonCredentialDecrypt
			return result, server.NewAppError(http.StatusInternalServerError, server.ReasonCredentialDecrypt)
		}
		creds[k] = string(plain)
	}

	// Build target URL. Preserves any query string in the relay path —
	// url.JoinPath escapes '?' into %3F and silently drops query parameters,
	// which broke parameterized requests (e.g. Dokploy tRPC endpoints that pass
	// all arguments via ?input=...).
	targetURL, err := buildTargetURL(svc.BaseURL, input.Path)
	if err != nil {
		result.Outcome, result.Reason = "rejected", server.ReasonInvalidRelayPath
		return result, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRelayPath)
	}

	// Substitute placeholders in path.
	targetURL = substitute(targetURL, creds)

	// Build request.
	var bodyReader io.Reader
	if input.Body != nil && string(input.Body) != "null" {
		bodyStr := substitute(string(input.Body), creds)
		bodyReader = bytes.NewReader([]byte(bodyStr))
	}

	req, err := http.NewRequestWithContext(reqCtx, input.Method, targetURL, bodyReader)
	if err != nil {
		result.Outcome, result.Reason = "rejected", server.ReasonInvalidRelayRequest
		return result, server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRelayRequest)
	}

	// Set headers with placeholder substitution.
	if input.Headers != nil {
		for k, v := range input.Headers {
			req.Header.Set(substitute(k, creds), substitute(v, creds))
		}
	}

	// Execute request. The header wait has its own cap and never resets the
	// total budget.
	headerTimer := time.AfterFunc(headerWait, func() { cancel(context.DeadlineExceeded) })
	resp, err := s.httpClient.Do(req)
	headerTimer.Stop()
	if err != nil {
		if ctx.Err() != nil {
			return relayContextError(ctx.Err())
		}
		if isTimeout(err) || reqCtx.Err() != nil {
			// Either the header wait or the total budget expired before headers.
			result.Reason = server.ReasonRelayTimeout
			return result, server.NewAppError(http.StatusGatewayTimeout, server.ReasonRelayTimeout)
		}
		result.Reason = server.ReasonRelayUnreachable
		return result, server.NewAppErrorParams(http.StatusBadGateway, server.ReasonRelayUnreachable, map[string]any{"detail": "connection failed"})
	}
	defer resp.Body.Close()
	result.UpstreamStatus = resp.StatusCode
	// This endpoint forwards response bodies, not bidirectional upgrades. A
	// 101 cannot be advertised without implementing the upgraded transport.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		result.Reason = server.ReasonRelayUnreachable
		return result, server.NewAppErrorParams(http.StatusBadGateway, server.ReasonRelayUnreachable, map[string]any{"detail": "protocol upgrade unsupported"})
	}

	// Install supervision before WriteHeader/Flush: even the first flush can
	// block when the downstream stops reading. The watcher only touches transport
	// deadlines, and is joined before returning so no worker outlives the handler.
	controller := http.NewResponseController(w)
	var deadlineMu sync.Mutex
	setWriteDeadline := func() error {
		deadlineMu.Lock()
		defer deadlineMu.Unlock()
		deadline := time.Now().Add(idleTimeout)
		if total, ok := reqCtx.Deadline(); ok && total.Before(deadline) {
			deadline = total
		}
		if reqCtx.Err() != nil {
			deadline = time.Now()
		}
		err := controller.SetWriteDeadline(deadline)
		// In-memory recorders do not have transport deadlines. Real net/http
		// HTTP/1 and HTTP/2 writers support them, through middleware Unwrap.
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	var lastProgress atomic.Int64
	lastProgress.Store(time.Now().UnixNano())
	stopWatch := make(chan struct{})
	watchReturned := make(chan struct{})
	go func() {
		defer close(watchReturned)
		timer := time.NewTimer(idleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-reqCtx.Done():
				deadlineMu.Lock()
				_ = controller.SetWriteDeadline(time.Now())
				deadlineMu.Unlock()
				_ = resp.Body.Close()
				return
			case <-timer.C:
				remaining := idleTimeout - time.Since(time.Unix(0, lastProgress.Load()))
				if remaining <= 0 {
					cancel(errRelayIdle)
				} else {
					timer.Reset(remaining)
				}
			}
		}
	}()
	defer func() { close(stopWatch); <-watchReturned }()

	flush := func() error {
		if err := setWriteDeadline(); err != nil {
			return err
		}
		err := controller.Flush()
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	if err := setWriteDeadline(); err != nil {
		return result, err
	}
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	result.HeadersWritten = true
	result.CopyError = flush()
	buf := make([]byte, 32*1024)
	for result.CopyError == nil {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if err := setWriteDeadline(); err != nil {
				result.CopyError = err
				break
			}
			written, werr := w.Write(buf[:n])
			result.BytesCopied += int64(written)
			if werr == nil && written != n {
				werr = io.ErrShortWrite
			}
			if werr == nil {
				werr = flush()
			}
			if werr != nil {
				result.CopyError = werr
				break
			}
			lastProgress.Store(time.Now().UnixNano())
		}
		if rerr != nil {
			if rerr != io.EOF {
				result.CopyError = rerr
			}
			break
		}
	}
	// Cancellation may close a body with EOF rather than a distinct read error.
	if result.CopyError == nil && reqCtx.Err() != nil {
		result.CopyError = reqCtx.Err()
	}
	// The clock may reach its deadline just before context cancellation is
	// scheduled. EOF in that interval must not turn an expired stream into a
	// successful usage record while net/http aborts its write-deadlined socket.
	if deadline, ok := reqCtx.Deadline(); result.CopyError == nil && ok && !time.Now().Before(deadline) {
		result.CopyError = context.DeadlineExceeded
	}
	if result.CopyError != nil {
		ctxErr := ctx.Err()
		deadline, hasDeadline := ctx.Deadline()
		// net/http can cancel r.Context after a socket write deadline fires.
		// Attribute that cancellation to the expired budget, not the client.
		idleExpired := errors.Is(context.Cause(reqCtx), errRelayIdle) ||
			(isTimeout(result.CopyError) && time.Since(time.Unix(0, lastProgress.Load())) >= idleTimeout)
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded), hasDeadline && !time.Now().Before(deadline):
			result.Outcome, result.Reason = "timeout", "relay_budget_exceeded"
		case idleExpired:
			result.Outcome, result.Reason = "timeout", "relay_idle_timeout"
		case ctxErr != nil:
			result.Outcome, result.Reason = "cancelled", "client_cancelled"
		case isTimeout(result.CopyError):
			result.Outcome, result.Reason = "timeout", "relay_idle_timeout"
		default:
			result.Reason = "relay_copy_failed"
		}
		return result, result.CopyError
	}
	if resp.StatusCode >= 400 {
		result.Reason = "relay_upstream_failed"
		return result, nil
	}
	result.Outcome = "succeeded"
	return result, nil
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
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}
