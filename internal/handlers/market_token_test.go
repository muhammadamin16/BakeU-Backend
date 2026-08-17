package handlers

import (
	"strconv"
	"testing"
	"time"
)

// The review links are only safe if the HMAC token can't be forged, replayed
// against a different market, used past its expiry, or extended by tampering
// with the exp parameter.
func TestReviewToken(t *testing.T) {
	h := NewMarketHandler(nil, nil, nil, nil, nil, "https://app", "secret")

	exp := time.Now().Add(time.Hour).Unix()
	expStr := strconv.FormatInt(exp, 10)
	tok := h.reviewToken("market-123", exp)

	if !h.validToken("market-123", expStr, tok) {
		t.Fatal("valid token rejected")
	}
	if h.validToken("market-123", expStr, "deadbeef") {
		t.Fatal("forged token accepted")
	}
	if h.validToken("market-999", expStr, tok) {
		t.Fatal("token accepted for a different market")
	}

	// exp is signed into the MAC: shifting it must invalidate the token.
	later := strconv.FormatInt(exp+3600, 10)
	if h.validToken("market-123", later, tok) {
		t.Fatal("token accepted with a tampered expiry")
	}

	// An expired token must be rejected even with a valid MAC.
	past := time.Now().Add(-time.Hour).Unix()
	pastTok := h.reviewToken("market-123", past)
	if h.validToken("market-123", strconv.FormatInt(past, 10), pastTok) {
		t.Fatal("expired token accepted")
	}

	if h.validToken("market-123", "not-a-number", tok) {
		t.Fatal("token accepted with a malformed expiry")
	}

	// A handler with a different secret must not accept the first one's token.
	other := NewMarketHandler(nil, nil, nil, nil, nil, "https://app", "different")
	if other.validToken("market-123", expStr, tok) {
		t.Fatal("token accepted under a different secret")
	}
}
