package handlers

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPhoneSignupCredentials(t *testing.T) {
	email, hash, err := phoneSignupCredentials("", "")
	second, _, secondErr := phoneSignupCredentials("", "")
	if err != nil || secondErr != nil || !strings.HasSuffix(email, phoneAccountEmailSuffix) || email == second || hash != "" {
		t.Fatal("phone-only signup must use a unique reserved key without a password")
	}

	email, hash, err = phoneSignupCredentials("Legacy@Example.com", "legacy-password")
	if err != nil || email != "legacy@example.com" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("legacy-password")) != nil {
		t.Fatal("existing clients must still be able to supply both credentials")
	}
	for _, pair := range [][2]string{{"legacy@example.com", ""}, {"", "legacy-password"}, {"existing" + phoneAccountEmailSuffix, "legacy-password"}} {
		if _, _, err := phoneSignupCredentials(pair[0], pair[1]); err == nil {
			t.Fatal("incomplete or reserved credentials were accepted")
		}
	}
}
