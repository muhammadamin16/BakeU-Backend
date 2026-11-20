// Package auth issues and verifies the app's own HMAC-signed JWTs, replacing
// Firebase Auth.
package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenTTL is how long an issued token stays valid.
const tokenTTL = 7 * 24 * time.Hour

// Manager signs and parses JWTs with a single HMAC secret.
type Manager struct {
	secret []byte
}

// NewManager builds a Manager from the configured signing secret.
func NewManager(secret string) *Manager {
	return &Manager{secret: []byte(secret)}
}

// Sign returns a signed token whose subject is the given user ID.
func (m *Manager) Sign(userID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

// Parse validates the token and returns the user ID from its subject claim.
func (m *Manager) Parse(tokenString string) (string, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(_ *jwt.Token) (any, error) {
		return m.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return "", errors.New("invalid token")
	}
	if claims.Subject == "" {
		return "", errors.New("token missing subject")
	}
	return claims.Subject, nil
}
