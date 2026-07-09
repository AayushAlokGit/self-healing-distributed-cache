// Package cache is a naive in-memory key→value store.
//
// Phase 1, step 3b: mutex-guarded map with per-key TTL. Expired entries are
// reclaimed lazily on read and by a background sweeper.
// Deliberately missing: a size limit. That's step 4.
package cache

import (
	"sync"
	"time"
)

// Picked by gut, not measurement: too often burns CPU scanning live keys, too
// rarely carries corpses. Step 3c replaces the trade-off rather than tuning it.
const defaultSweepInterval = time.Second

// entry stores the absolute deadline, not the TTL duration: the arithmetic
// happens once at Set time so Get only has to compare. Reads outnumber writes,
// so that's the cheaper place to pay.
type entry struct {
	value string

	// The zero Time means "never expires" — see Cache.Set.
	expires time.Time
}

func (e entry) expired(now time.Time) bool {
	return !e.expires.IsZero() && now.After(e.expires)
}

// Cache is an in-memory key→value store, safe for concurrent use.
//
// A Cache owns a background goroutine, so the caller MUST Close it or both the
// goroutine and everything the Cache holds leak — a running goroutine's stack
// is a GC root, so it keeps the Cache reachable forever.
//
// Must not be copied after first use: copying duplicates the mutex, and the
// copies would no longer exclude each other. Always pass *Cache. (pointer to one in memory cache instance)
type Cache struct {
	// mu guards data. Every read or write of data must hold it.
	mu   sync.Mutex
	data map[string]entry

	// Closed, not sent to, so every receiver unblocks — now and forever.
	done chan struct{}

	// Closing a closed channel panics; Close may be called more than once.
	closeOnce sync.Once

	// Lets Close wait for the sweeper to return, not merely be told to.
	wg sync.WaitGroup
}

// New creates an empty Cache and starts its sweeper. Close it when done.
func New() *Cache {
	return newWithSweepInterval(defaultSweepInterval)
}

// newWithSweepInterval lets tests sweep on a millisecond timescale.
func newWithSweepInterval(interval time.Duration) *Cache {
	c := &Cache{
		data: make(map[string]entry),

		// A channel's zero value is nil, and a receive from nil blocks forever.
		done: make(chan struct{}),
	}
	c.wg.Go(func() { c.sweepLoop(interval) })
	return c
}

// Close stops the sweeper and blocks until it has returned. Safe to call more
// than once. After Close the Cache still answers Get and Set; it just stops
// reclaiming expired entries in the background.
func (c *Cache) Close() {
	c.closeOnce.Do(func() { close(c.done) })
	c.wg.Wait()
}

// sweepLoop waits on the tick and the stop signal simultaneously, which is why
// it uses a Ticker rather than time.Sleep: Sleep can't be interrupted, so Close
// would block for up to a full interval before the goroutine noticed.
func (c *Cache) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.sweep()
		case <-c.done:
			return
		}
	}
}

// sweep deletes every expired entry and reports how many it removed.
//
// Deleting during a range is safe: an entry removed before it's reached is
// simply never produced. What is NOT safe is releasing the lock mid-scan to
// shorten the pause — an entry read before the gap is a rumor after it, and a
// concurrent Set could have given it a fresh deadline we'd then delete.
//
// So the lock is held for the whole scan, and that is this design's flaw: the
// pause grows with the number of keys. Step 3c fixes it.
func (c *Cache) sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0
	for k, e := range c.data {
		if e.expired(now) {
			delete(c.data, k)
			removed++
		}
	}
	return removed
}

// Set stores (or overwrites) the value for key, expiring it after ttl.
// A ttl <= 0 means the entry never expires. Overwriting resets the deadline:
// the new ttl fully replaces the old one.
func (c *Cache) Set(key, value string, ttl time.Duration) {
	// Computed before the lock: no reason to hold it across a clock read.
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = entry{value: value, expires: expires}
}

// Len returns how many entries the map physically holds, including expired
// ones neither Get nor the sweeper has reclaimed yet.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

// Get returns the value for key. The bool is false if the key is absent or
// expired — the caller cannot tell those apart, and shouldn't need to.
//
// An expired entry found here is deleted on the spot. Note this makes Get a
// writer, so it cannot take a read lock if we ever move to sync.RWMutex.
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
