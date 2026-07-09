// Package cache is a naive in-memory key→value store.
//
// Phase 1, step 2: bare map with a Set/Get front door, guarded by a mutex.
// Deliberately missing: TTL, size limit. Those come in steps 3–4.
package cache

import "sync"

// Cache is an in-memory key→value store, safe for concurrent use.
// No TTL, no size limit — that's on purpose (step 2).
//
// Must not be copied after first use: copying duplicates the mutex, and the
// copies would no longer exclude each other. Always pass *Cache. (pointer to one in memory cache instance)
type Cache struct {
	// mu guards data. Every read or write of data must hold it.
	mu   sync.Mutex
	data map[string]string
}

// New creates an empty, ready-to-use Cache.
func New() *Cache {
	return &Cache{
		data: make(map[string]string),
	}
}

// Set stores (or overwrites) the value for key.
func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
}

// Get returns the value for key. The bool is false if the key isn't present,
// which lets callers tell a missing key apart from a stored empty value.
//
// Reads lock too: a read racing a write is still a data race, and an
// unlocked read can observe the map mid-rehash.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.data[key]
	return value, ok
}
