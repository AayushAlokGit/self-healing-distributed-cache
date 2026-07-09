// Package cache is a naive in-memory key→value store.
//
// Phase 1, step 3: mutex-guarded map with per-key TTL, expired lazily.
// Deliberately missing: a sweeper (expired keys nobody reads are never freed —
// see Cache.Get) and a size limit. Those come in steps 3b–4.
package cache

import (
	"sync"
	"time"
)

// entry stores the absolute deadline, not the TTL duration: the arithmetic
// happens once at Set time so Get only has to compare. Reads outnumber writes,
// so that's the cheaper place to pay.
type entry struct {
	value string

	// expires is the instant this entry becomes invisible to Get.
	// The zero Time means "never expires" — see Cache.Set.
	expires time.Time
}

func (e entry) expired(now time.Time) bool {
	return !e.expires.IsZero() && now.After(e.expires)
}

// Cache is an in-memory key→value store, safe for concurrent use.
//
// Must not be copied after first use: copying duplicates the mutex, and the
// copies would no longer exclude each other. Always pass *Cache. (pointer to one in memory cache instance)
type Cache struct {
	// mu guards data. Every read or write of data must hold it.
	mu   sync.Mutex
	data map[string]entry
}

// New creates an empty, ready-to-use Cache.
func New() *Cache {
	return &Cache{
		data: make(map[string]entry),
	}
}

// Set stores (or overwrites) the value for key, expiring it after ttl.
// A ttl <= 0 means the entry never expires. Overwriting resets the deadline:
// the new ttl fully replaces the old one.
func (c *Cache) Set(key, value string, ttl time.Duration) {
	// Computed before the lock: no reason to hold it across a clock read.
	var expires time.Time // zero value == never expires
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = entry{value: value, expires: expires}
}

// Get returns the value for key. The bool is false if the key is absent or
// expired — the caller cannot tell those apart, and shouldn't need to.
//
// Expiry is lazy: nothing runs on a timer, we just compare the deadline on
// read. An expired entry found here is deleted on the spot, but entries nobody
// reads again are never freed — the leak the sweeper (step 3b) exists to fix.
//
// Note this makes Get a writer, so it cannot take a read lock if we ever move
// to sync.RWMutex.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.data[key]
	if !ok {
		return "", false
	}
	if e.expired(time.Now()) {
		delete(c.data, key)
		return "", false
	}
	return e.value, true
}
