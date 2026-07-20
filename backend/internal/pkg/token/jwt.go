package token

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims holds the JWT payload for an authenticated user.
type Claims struct {
	jwt.RegisteredClaims
	UserID    uint   `json:"uid"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	ServerIDs []uint `json:"server_ids,omitempty"`
}

// JWTService signs and validates JSON Web Tokens using HMAC-SHA256.
type JWTService struct {
	secret     []byte
	expiryTime time.Duration
}

// NewJWTService creates a JWTService with the given signing secret and token lifetime.
func NewJWTService(secret string, expiry time.Duration) *JWTService {
	return &JWTService{
		secret:     []byte(secret),
		expiryTime: expiry,
	}
}

// GenerateToken creates a signed JWT for the given user.
func (s *JWTService) GenerateToken(userID uint, username, role string) (string, error) {
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.expiryTime)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "vpsmanager",
		},
		UserID:   userID,
		Username: username,
		Role:     role,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

// ValidateToken parses and validates a JWT string, returning the embedded claims.
// Returns an error if the token is expired, malformed, or signed with a different secret.
func (s *JWTService) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, jwt.ErrSignatureInvalid
	}

	return claims, nil
}
