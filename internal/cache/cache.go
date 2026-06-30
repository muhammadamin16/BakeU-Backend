// Package cache provides a tiny in-memory, TTL-based key/value store. It mirrors
// the in-memory, mutex-guarded style of internal/auth.LoginGuard: state lives in
// the process, resets on restart, and a background goroutine purges expired
// entries. It is intended for caching heavy analytical aggregations for a few
// minutes, not as a durable or cross-instance cache.
package cache

import (
	"sync"
	"time"
)

// cleanupInterval is how often expired entries are purged.
const cleanupInterval = time.Minute

type entry struct {
	value     any
	expiresAt time.Time
}

// Cache is a concurrency-safe map of string keys to values with per-entry TTLs.
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
}

// New builds a Cache and starts its background cleanup loop.
func New() *Cache {
	c := &Cache{entries: make(map[string]entry)}
	go c.cleanupLoop()
	return c
}

// Get returns the cached value for key and whether it was present and unexpired.
func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.value, true
}

// Set stores value under key for the given ttl.
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	c.entries[key] = entry{value: value, expiresAt: time.Now().Add(ttl)}
	c.mu.Unlock()
}

func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.mu.Lock()
		for key, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, key)
			}
		}
		c.mu.Unlock()
	}
}
