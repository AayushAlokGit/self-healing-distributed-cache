// Package cache is a naive in-memory key→value store.
//
// Phase 1, step 4: mutex-guarded map with per-key TTL and LRU eviction at a
// capacity. Expired entries are reclaimed lazily on read, by a background
// sweeper, and preferentially at eviction time.
package cache

import (
	"sync"
	"time"
)

const (
	noLimit = 0

	// Only says how often we check. The sampler sets its own rate.
	defaultSweepInterval = time.Second

	// Keys examined per pass. Bounds how long the lock is held once.
	sampleSize = 20

	// Pass again immediately if more than 1-in-4 of a sample was expired.
	expiredThreshold = 4

	// Bounds how long the sweeper keeps competing for the lock, which
	// sampleSize does not: pass count is O(expired keys), so a 500k backlog
	// runs 25k passes over 514ms. The remainder waits for the next tick.
	sweepBudgetFraction = 4 // at most interval/4 per tick
)

// entry stores the absolute deadline, not the TTL duration: the arithmetic
// happens once at Set time so Get only has to compare. Reads outnumber writes,
// so that's the cheaper place to pay.
type entry struct {
	value string

	// The zero Time means "never expires" — see Cache.Set.
	expires time.Time

	// Cache.clock at the last read or write; the eviction victim is the minimum.
	// Not a timestamp: time.Now() stands still for 541µs at a stretch here, so
	// ~5,400 consecutive Sets would tie and the victim would be picked at random.
	lastUsed uint64
}

func (e entry) expired(now time.Time) bool {
	return !e.expires.IsZero() && now.After(e.expires)
}

// Cache is an in-memory key→value store, safe for concurrent use.
//
// The caller MUST Close it. A Cache owns a background goroutine, and a running
// goroutine's stack is a GC root, so it keeps the whole Cache reachable forever.
//
// Must not be copied after first use: copying duplicates the mutex and the
// copies stop excluding each other. Always pass *Cache.
type Cache struct {
	// mu guards data and expiring. Every read or write of either must hold it.
	mu   sync.Mutex
	data map[string]entry

	// Max entries, or noLimit. Bounding entries rather than bytes is a lie when
	// values vary in size — Redis bounds bytes. Set once, so it needs no lock.
	capacity int

	// expiring indexes only the keys with a deadline. Sampling all of data
	// instead would be diluted to uselessness by a mostly-permanent cache.
	expiring map[string]struct{}

	// Ticks once per access, ordering accesses without measuring time.
	// Guarded by mu. Wraps after 584 years at a billion ops/sec.
	clock uint64

	// Closed, not sent to, so every receiver unblocks — now and forever.
	done chan struct{}

	// Closing a closed channel panics; Close may be called more than once.
	closeOnce sync.Once

	// Lets Close wait for the sweeper to return, not merely be told to.
	wg sync.WaitGroup
}

// New creates an empty Cache holding at most capacity entries, and starts its
// sweeper. A capacity <= 0 means unbounded. Close it when done.
func New(capacity int) *Cache {
	return newWithSweepInterval(capacity, defaultSweepInterval)
}

// newWithSweepInterval lets tests sweep on a millisecond timescale.
func newWithSweepInterval(capacity int, interval time.Duration) *Cache {
	c := &Cache{
		data:     make(map[string]entry),
		expiring: make(map[string]struct{}),
		capacity: capacity,

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

	budget := interval / sweepBudgetFraction
	for {
		select {
		case <-ticker.C:
			c.sampleSweep(budget)
		case <-c.done:
			return
		}
	}
}

// sampleSweep reclaims expired entries by repeated random sampling, returning
// how many it removed. It passes again while samples come back dirty, so the
// reclaim rate tracks the expiry rate.
//
// Each pass releases the lock, so the pause stays ~7µs at 1M keys against
// sweepAll's 27ms. It never fully cleans the cache, and under a large backlog
// it won't finish this tick: bounded waste in exchange for bounded interference.
func (c *Cache) sampleSweep(budget time.Duration) int {
	deadline := time.Now().Add(budget)
	total := 0

	for {
		scanned, expired := c.samplePass()
		total += expired

		// Nothing left with a TTL, or the sample came back mostly clean.
		if scanned == 0 || expired*expiredThreshold <= scanned {
			return total
		}
		if time.Now().After(deadline) {
			return total // out of budget; the rest waits for the next tick
		}
	}
}

// samplePass examines up to sampleSize keys from the expiring index under the
// lock, deleting the expired ones.
//
// It carries nothing across the unlock — that statelessness is what lets us drop
// the lock at all. An entry read before releasing a lock is a rumor after it: a
// concurrent Set may have given it a fresh deadline we'd then delete.
func (c *Cache) samplePass() (scanned, expired int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	// Go starts map iteration at a random bucket, so this is the sample. It
	// draws buckets, not keys — not uniform, fine for estimating a fraction.
	for k := range c.expiring {
		if scanned == sampleSize {
			break
		}
		scanned++

		e, ok := c.data[k]
		if !ok {
			delete(c.expiring, k) // Get already reclaimed it; drop the stale index entry
			continue
		}
		if e.expired(now) {
			delete(c.data, k)
			delete(c.expiring, k)
			expired++
		}
	}
	return scanned, expired
}

// sweepAll deletes every expired entry in one locked pass. Superseded by
// sampleSweep, kept as the benchmark baseline: O(total keys), not O(expired),
// and it holds the lock throughout.
func (c *Cache) sweepAll() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0
	for k, e := range c.data {
		if e.expired(now) {
			delete(c.data, k)
			delete(c.expiring, k)
			removed++
		}
	}
	return removed
}

// evictLocked frees exactly one slot. Callers must hold c.mu and must not call
// it on an empty map.
//
// Corpses first, because recency and expiry are independent orderings: 999
// sessions Set 100ms ago with a 50ms TTL are all more recently used than a
// permanent config key read a minute ago. Plain LRU evicts the config key.
//
// O(total entries), and unlike sweepAll it runs on the write path, on every
// Set, once the cache is full. See BenchmarkSetAtCapacity.
func (c *Cache) evictLocked(now time.Time) {
	var victim string
	var oldest uint64 // clock starts at 1, so 0 means "unset"

	for k, e := range c.data {
		if e.expired(now) {
			victim = k
			break
		}
		if oldest == 0 || e.lastUsed < oldest {
			victim, oldest = k, e.lastUsed
		}
	}

	delete(c.data, victim)
	delete(c.expiring, victim)
}

// tickLocked stamps the next access. Callers must hold c.mu.
func (c *Cache) tickLocked() uint64 {
	c.clock++
	return c.clock
}

// Set stores (or overwrites) the value for key, expiring it after ttl.
// A ttl <= 0 means the entry never expires. Overwriting resets the deadline:
// the new ttl fully replaces the old one.
//
// Inserting a new key into a full cache evicts one entry first. Overwriting
// does not grow the map, so it never evicts.
func (c *Cache) Set(key, value string, ttl time.Duration) {
	// Read before the lock: no reason to hold it across a clock read.
	now := time.Now()
	var expires time.Time
	if ttl > 0 {
		expires = now.Add(ttl)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	_, overwrite := c.data[key]
	if !overwrite && c.capacity > noLimit && len(c.data) >= c.capacity {
		c.evictLocked(now)
	}
	c.data[key] = entry{value: value, expires: expires, lastUsed: c.tickLocked()}

	// Overwriting a TTL'd key with a permanent one must un-index it.
	if ttl > 0 {
		c.expiring[key] = struct{}{}
	} else {
		delete(c.expiring, key)
	}
}

// Len returns how many entries the map physically holds, including expired
// ones neither Get nor the sweeper has reclaimed yet.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

// Get returns the value for key. The bool is false if the key is absent or
// expired; the caller cannot tell those apart, and shouldn't need to.
//
// A hit is a use, so it refreshes lastUsed. Between that, deleting expired
// entries, and dropping them from the index, Get writes three ways — it could
// not take a read lock under sync.RWMutex.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.data[key]
	if !ok {
		return "", false
	}

	now := time.Now()
	if e.expired(now) {
		delete(c.data, key)
		delete(c.expiring, key)
		return "", false
	}

	// e is a copy: map values are not addressable, so mutating e alone changes
	// nothing, and c.data[key].lastUsed = ... would not compile.
	e.lastUsed = c.tickLocked()
	c.data[key] = e
	return e.value, true
}
