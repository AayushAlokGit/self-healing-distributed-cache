// Package cache is a naive in-memory key→value store.
//
// Phase 1, step 1: bare map with a Set/Get front door.
// Deliberately missing: thread safety, TTL, size limit. Those come in steps 2–4.
package cache

// Cache is a naive in-memory key→value store.
// NOT safe for concurrent use, no TTL, no size limit — that's on purpose (step 1).
type Cache struct {
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
	c.data[key] = value
}

// Get returns the value for key. The bool is false if the key isn't present,
// which lets callers tell a missing key apart from a stored empty value.
func (c *Cache) Get(key string) (string, bool) {
	value, ok := c.data[key]
	return value, ok
}
