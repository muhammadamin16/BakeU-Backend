package auth

import (
	"sync"
	"time"
)

// Login brute-force protection tunables.
const (
	// maxPairFailures is how many failed logins a single (IP, account) pair may
	// make before being throttled. The Nth+1 attempt is blocked.
	maxPairFailures = 5
	// pairWindow is the rolling window over which failures are counted and the
	// cooldown applied once the pair is throttled.
	pairWindow = 15 * time.Minute
	// maxAccountIPs is the number of distinct IPs that may fail against one
	// account before the whole account is locked (distributed brute force).
	// Lock kicks in on more than 5 distinct IPs, i.e. the 6th.
	maxAccountIPs = 6
	// accountLockout is how long an account stays locked once tripped.
	accountLockout = time.Hour
	// cleanupInterval is how often expired tracking state is purged.
	cleanupInterval = 10 * time.Minute
)

// pairState tracks failed logins for a single (IP, account) pair.
type pairState struct {
	count       int
	windowStart time.Time
	blockUntil  time.Time
}

// accountState tracks the distinct IPs failing against one account within a
// rolling window, so a slow trickle of failures over months cannot accumulate
// into a lockout (or leak memory).
type accountState struct {
	ips         map[string]struct{}
	windowStart time.Time
	lockUntil   time.Time
}

// LoginGuard rate-limits failed logins per (IP, account) pair and locks an
// account that is attacked from many distinct IPs. All state is in-memory and
// mutex-guarded; it resets on restart and is per-process (fine for a single
// instance). A background goroutine purges expired entries to bound memory.
type LoginGuard struct {
	mu       sync.Mutex
	pairs    map[string]*pairState
	accounts map[string]*accountState
}

// NewLoginGuard builds a guard and starts its background cleanup loop.
func NewLoginGuard() *LoginGuard {
	g := &LoginGuard{
		pairs:    make(map[string]*pairState),
		accounts: make(map[string]*accountState),
	}
	go g.cleanupLoop()
	return g
}

func pairKey(ip, email string) string { return ip + "|" + email }

// Check reports whether a login attempt for (ip, email) may proceed. Call it
// before verifying the password. When blocked it returns retryAfter, the
// remaining cooldown to surface in a Retry-After header.
func (g *LoginGuard) Check(ip, email string) (allowed bool, retryAfter time.Duration) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	if acct := g.accounts[email]; acct != nil && now.Before(acct.lockUntil) {
		return false, time.Until(acct.lockUntil)
	}
	if p := g.pairs[pairKey(ip, email)]; p != nil {
		if now.Before(p.blockUntil) {
			return false, time.Until(p.blockUntil)
		}
		// Window expired: stale counter no longer blocks.
		if now.Sub(p.windowStart) >= pairWindow {
			return true, 0
		}
		if p.count >= maxPairFailures {
			return false, pairWindow
		}
	}
	return true, 0
}

// RecordFailure registers a failed login for (ip, email): it advances the pair
// counter (tripping a cooldown at maxPairFailures) and the account's distinct-IP
// set (locking the account at maxAccountIPs).
func (g *LoginGuard) RecordFailure(ip, email string) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	key := pairKey(ip, email)
	p := g.pairs[key]
	if p == nil || now.Sub(p.windowStart) >= pairWindow {
		p = &pairState{windowStart: now}
		g.pairs[key] = p
	}
	p.count++
	if p.count >= maxPairFailures {
		p.blockUntil = now.Add(pairWindow)
	}

	acct := g.accounts[email]
	if acct == nil || (acct.lockUntil.IsZero() && now.Sub(acct.windowStart) >= pairWindow) {
		acct = &accountState{ips: make(map[string]struct{}), windowStart: now}
		g.accounts[email] = acct
	}
	acct.ips[ip] = struct{}{}
	if len(acct.ips) >= maxAccountIPs {
		acct.lockUntil = now.Add(accountLockout)
	}
}

// Reset clears the failed-attempt counter for (ip, email) after a successful
// login. The account-wide IP set is left to expire via cleanup.
func (g *LoginGuard) Reset(ip, email string) {
	g.mu.Lock()
	delete(g.pairs, pairKey(ip, email))
	g.mu.Unlock()
}

func (g *LoginGuard) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		g.purge()
	}
}

// purge drops pair and account state that can no longer block anything.
func (g *LoginGuard) purge() {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	for key, p := range g.pairs {
		if now.After(p.blockUntil) && now.Sub(p.windowStart) >= pairWindow {
			delete(g.pairs, key)
		}
	}
	for email, acct := range g.accounts {
		// Drop once any lock has expired and the failure window has gone stale.
		if now.After(acct.lockUntil) && now.Sub(acct.windowStart) >= pairWindow {
			delete(g.accounts, email)
		}
	}
}
