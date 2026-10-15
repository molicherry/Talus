package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/pkg/token"
)

type stubGate struct{ version int64 }

func (g stubGate) Admit(ctx context.Context, userID uint, fn func(version int64) error) error {
	return fn(g.version)
}

type failingGate struct{ err error }

func (g failingGate) Admit(ctx context.Context, userID uint, fn func(version int64) error) error {
	return g.err
}

func runAuth(t *testing.T, gateImpl UserGate, raw string) *httptest.ResponseRecorder {
	t.Helper()
	jwtSvc := token.NewJWTService("secret", time.Hour)
	rec := httptest.NewRecorder()
	handler := Auth(jwtSvc, nil, gateImpl)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	handler.ServeHTTP(rec, req)
	return rec
}

// legacyToken signs a token without the `tv` claim, as an older build would.
func legacyToken(t *testing.T) string {
	t.Helper()
	claims := &token.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "vpsmanager",
		},
		UserID:   1,
		Username: "admin",
		Role:     "admin",
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAuthAcceptsMatchingVersion(t *testing.T) {
	raw, err := token.NewJWTService("secret", time.Hour).GenerateToken(1, "admin", "admin", 3)
	if err != nil {
		t.Fatal(err)
	}
	if rec := runAuth(t, stubGate{version: 3}, raw); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAuthRejectsStaleVersion(t *testing.T) {
	raw, err := token.NewJWTService("secret", time.Hour).GenerateToken(1, "admin", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec := runAuth(t, stubGate{version: 5}, raw); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthRejectsLegacyTokenWithoutClaim(t *testing.T) {
	if rec := runAuth(t, stubGate{}, legacyToken(t)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token status = %d, want 401", rec.Code)
	}
}

func TestAuthReturnsUnavailableWhenVersionLookupFails(t *testing.T) {
	raw, err := token.NewJWTService("secret", time.Hour).GenerateToken(1, "admin", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec := runAuth(t, failingGate{err: gate.ErrUnavailable}, raw); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
