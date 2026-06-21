package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestLoginGuardThrottlesPairAfterFiveFailures(t *testing.T) {
	g := &LoginGuard{pairs: map[string]*pairState{}, accounts: map[string]*accountState{}}
	const ip, email = "1.2.3.4", "a@b.com"

	// First five attempts are allowed; each records a failure.
	for i := 0; i < maxPairFailures; i++ {
		if allowed, _ := g.Check(ip, email); !allowed {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		g.RecordFailure(ip, email)
	}

	// The sixth attempt is blocked with a positive retry-after.
	allowed, retry := g.Check(ip, email)
	if allowed {
		t.Fatal("sixth attempt should be blocked")
	}
	if retry <= 0 {
		t.Fatalf("expected positive retry-after, got %v", retry)
	}
}

func TestLoginGuardResetClearsThrottle(t *testing.T) {
	g := &LoginGuard{pairs: map[string]*pairState{}, accounts: map[string]*accountState{}}
	const ip, email = "1.2.3.4", "a@b.com"

	for i := 0; i < maxPairFailures; i++ {
		g.RecordFailure(ip, email)
	}
	if allowed, _ := g.Check(ip, email); allowed {
		t.Fatal("should be blocked before reset")
	}

	g.Reset(ip, email)
	if allowed, _ := g.Check(ip, email); !allowed {
		t.Fatal("should be allowed after a successful login reset")
	}
}

func TestLoginGuardIsolatesDifferentIPs(t *testing.T) {
	g := &LoginGuard{pairs: map[string]*pairState{}, accounts: map[string]*accountState{}}
	const email = "a@b.com"

	for i := 0; i < maxPairFailures; i++ {
		g.RecordFailure("1.1.1.1", email)
	}
	if allowed, _ := g.Check("9.9.9.9", email); !allowed {
		t.Fatal("a different IP should not be throttled by another IP's failures")
	}
}

func TestLoginGuardLocksAccountAfterManyIPs(t *testing.T) {
	g := &LoginGuard{pairs: map[string]*pairState{}, accounts: map[string]*accountState{}}
	const email = "victim@b.com"

	for i := 0; i < maxAccountIPs; i++ {
		g.RecordFailure("10.0."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256), email)
	}

	// Even a brand-new IP is refused while the account is locked.
	allowed, retry := g.Check("203.0.113.7", email)
	if allowed {
		t.Fatal("account should be locked after maxAccountIPs distinct IPs")
	}
	if retry <= 0 || retry > accountLockout {
		t.Fatalf("unexpected lockout retry-after: %v", retry)
	}
}

func TestLoginGuardPairWindowExpires(t *testing.T) {
	g := &LoginGuard{pairs: map[string]*pairState{}, accounts: map[string]*accountState{}}
	const ip, email = "1.2.3.4", "a@b.com"

	for i := 0; i < maxPairFailures; i++ {
		g.RecordFailure(ip, email)
	}
	// Backdate the window so it has fully elapsed.
	g.pairs[pairKey(ip, email)].windowStart = time.Now().Add(-2 * pairWindow)
	g.pairs[pairKey(ip, email)].blockUntil = time.Now().Add(-time.Minute)

	if allowed, _ := g.Check(ip, email); !allowed {
		t.Fatal("expired window should no longer block")
	}
}
