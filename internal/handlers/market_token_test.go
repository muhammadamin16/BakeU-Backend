package handlers

import "testing"

// The review links are only safe if the HMAC token can't be forged or replayed
// against a different market.
func TestReviewToken(t *testing.T) {
	h := NewMarketHandler(nil, nil, nil, nil, nil, "https://app", "secret")

	tok := h.reviewToken("market-123")

	if !h.validToken("market-123", tok) {
		t.Fatal("valid token rejected")
	}
	if h.validToken("market-123", "deadbeef") {
		t.Fatal("forged token accepted")
	}
	if h.validToken("market-999", tok) {
		t.Fatal("token accepted for a different market")
	}

	// A handler with a different secret must not accept the first one's token.
	other := NewMarketHandler(nil, nil, nil, nil, nil, "https://app", "different")
	if other.validToken("market-123", tok) {
		t.Fatal("token accepted under a different secret")
	}
}
