package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
)

func requireRelayAuthorizationError(t *testing.T, result RelayResult, err error, status int, reason string) {
	t.Helper()
	var app *server.AppError
	if !errors.As(err, &app) || app.Code != status || app.Reason != reason {
		t.Fatalf("relay authorization error = %v, want status=%d reason=%s", err, status, reason)
	}
	if result.Outcome != "rejected" || result.Reason != reason || result.HeadersWritten || result.UpstreamStatus != 0 {
		t.Fatalf("rejected relay already performed transport work: %+v", result)
	}
}

// This invokes the service directly, without the handler's scope check. The
// malformed credential and absent master key would fail if decryption were
// attempted; neither decryption nor the upstream may precede authorization.
func TestRelaySnapshotRejectsUnauthorizedBeforeCredentials(t *testing.T) {
	allowed, denied := uint(101), uint(202)
	for _, tc := range []struct {
		name   string
		claims *token.Claims
		server *uint
		status int
		reason string
	}{
		{name: "missing identity for bound service", server: &denied, status: http.StatusUnauthorized, reason: server.ReasonUnauthorized},
		{name: "missing identity for unbound service", status: http.StatusUnauthorized, reason: server.ReasonUnauthorized},
		{name: "outside server selector", claims: &token.Claims{UserID: 7, ServerIDs: []uint{allowed}}, server: &denied, status: http.StatusForbidden, reason: server.ReasonAPIKeyServiceDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := relayTestService(time.Second, time.Second, time.Second, time.Second)
			svc.httpClient = &http.Client{Transport: relayRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unauthorized upstream contacted")
			})}
			resource := &model.Service{ServerID: tc.server, BaseURL: "http://forbidden.invalid", CryptoVersion: 1,
				Salt: []byte{1}, EncryptedCredentials: map[string]string{"token": "not-a-valid-ciphertext"}}
			recorder := httptest.NewRecorder()
			result, err := svc.RelaySnapshotWithResult(context.Background(), tc.claims, resource, RelayInput{Method: http.MethodGet}, recorder)
			requireRelayAuthorizationError(t, result, err, tc.status, tc.reason)
			if calls != 0 || recorder.Body.Len() != 0 {
				t.Fatalf("unauthorized relay performed work: requests=%d body=%q", calls, recorder.Body.String())
			}
		})
	}
}

func TestRelaySnapshotPreservesAuthorizedServerSemantics(t *testing.T) {
	allowed, other := uint(101), uint(202)
	for _, tc := range []struct {
		name   string
		claims *token.Claims
		server *uint
	}{
		{name: "restricted key on allowed server", claims: &token.Claims{UserID: 7, ServerIDs: []uint{allowed}}, server: &allowed},
		{name: "restricted key on unbound service", claims: &token.Claims{UserID: 7, ServerIDs: []uint{allowed}}},
		{name: "administrator on another server", claims: &token.Claims{UserID: 7, Role: "admin"}, server: &other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := relayTestService(time.Second, time.Second, time.Second, time.Second)
			svc.httpClient = &http.Client{Transport: relayRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "authorized.invalid" {
					t.Errorf("relay used another configuration: %s", r.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})}
			recorder := httptest.NewRecorder()
			result, err := svc.RelaySnapshotWithResult(context.Background(), tc.claims,
				&model.Service{ServerID: tc.server, BaseURL: "http://authorized.invalid"}, RelayInput{Method: http.MethodGet}, recorder)
			if err != nil || calls != 1 || result.UpstreamStatus != http.StatusOK || recorder.Body.String() != "ok" {
				t.Fatalf("authorized snapshot rejected: result=%+v error=%v calls=%d body=%q", result, err, calls, recorder.Body.String())
			}
		})
	}
}

func TestRelayByIDRequiresIdentityBeforeLookup(t *testing.T) {
	// No repository is installed: an unauthenticated entry must return before
	// lookup rather than opening a second route around the snapshot invariant.
	svc := &ServiceRelayService{}
	result, err := svc.RelayWithResult(context.Background(), nil, 1, RelayInput{Method: http.MethodGet}, httptest.NewRecorder())
	requireRelayAuthorizationError(t, result, err, http.StatusUnauthorized, server.ReasonUnauthorized)
	if err := svc.Relay(context.Background(), nil, 1, RelayInput{Method: http.MethodGet}, httptest.NewRecorder()); !errors.Is(err, server.ErrUnauthorized) {
		t.Fatalf("Relay wrapper bypassed identity admission: %v", err)
	}
}

func TestRelayByIDAppliesSnapshotAuthorization(t *testing.T) {
	db := authOwnerTestDB(t)
	if err := db.AutoMigrate(&model.Service{}); err != nil {
		t.Fatal(err)
	}
	allowed, denied := uint(101), uint(202)
	resource := &model.Service{Name: "forbidden-native-relay", ServerID: &denied, BaseURL: "http://forbidden.invalid", Salt: []byte{1},
		EncryptedCredentials: map[string]string{"token": "not-a-valid-ciphertext"}}
	if err := db.Create(resource).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewServiceRelayService(repository.NewServiceRepo(db), nil)
	result, err := svc.RelayWithResult(context.Background(), &token.Claims{UserID: 7, ServerIDs: []uint{allowed}}, resource.ID,
		RelayInput{Method: http.MethodGet}, httptest.NewRecorder())
	requireRelayAuthorizationError(t, result, err, http.StatusForbidden, server.ReasonAPIKeyServiceDenied)
}
