package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseRequiresExpirationAndHS256(t *testing.T) {
	const secret = "test-only-secret"
	manager := NewManager(secret)
	valid, err := manager.Sign("seller")
	if err != nil {
		t.Fatal(err)
	}
	if uid, err := manager.Parse(valid); err != nil || uid != "seller" {
		t.Fatalf("Parse(valid) = %q, %v", uid, err)
	}

	for _, tc := range []struct {
		name   string
		method jwt.SigningMethod
		expiry *jwt.NumericDate
	}{
		{"missing expiration", jwt.SigningMethodHS256, nil},
		{"expired", jwt.SigningMethodHS256, jwt.NewNumericDate(time.Now().Add(-time.Hour))},
		{"different algorithm", jwt.SigningMethodHS384, jwt.NewNumericDate(time.Now().Add(time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(tc.method, jwt.RegisteredClaims{
				Subject: "seller", ExpiresAt: tc.expiry,
			}).SignedString([]byte(secret))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Parse(token); err == nil {
				t.Fatal("Parse accepted an invalid access token")
			}
		})
	}
}
