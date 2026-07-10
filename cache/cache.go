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

const (
	defaultSweepInterval = time.Second

	// A sweep pass looks at sampleSize keys and no more, so the lock is held
	// for a constant time no matter how big the cache is.
	sampleSize = 20

	// If more than 1-in-expiredThreshold of the sample was expired, there is
	// probably more garbage, so pass again immediately. Otherwise sleep. This
	// makes the sweep rate track the expiry rate — there is no interval to tune.
	expiredThreshold = 4 // i.e. 25%

	// sampleSize bounds how long the lock is held ONCE. Nothing bounds how many
	// passes a big backlog takes: reclaiming 500k corpses runs 25k passes over
	// 514ms of continuous lock churn, halving reader throughput for half a
	// second. The budget caps the sweeper's share of each interval and carries
	// the rest to the next tick. Redis bounds itself the same way, at 25% CPU.
	sweepBudgetFraction = 4 // spend at most interval/4 sweeping
)

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
	// mu guards data and expiring. Every read or write of either must hold it.
	mu   sync.Mutex
	data map[string]entry

	// expiring indexes just the keys that have a deadline, so the sampler draws
	// from the population it cares about. Sampling all of data would be diluted
	// into uselessness by a cache of mostly-permanent keys: 20 keys drawn from
	// 10M permanent + 1k expiring finds an expired one essentially never, the
	// sampler concludes there's nothing to do, and the 1k rot forever.
	expiring map[string]struct{}

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
		data:     make(map[string]entry),
		expiring: make(map[string]struct{}),

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

// sampleSweep reclaims expired entries by repeated random sampling, and returns
// how many it removed.
//
// Each pass looks at sampleSize keys and releases the lock, so the pause barely
// grows with cache size: 8µs at 50k keys, 20µs at 500k (the drift is cache
// misses, not work), against sweepAll's 27ms at 1M. If a pass found more than
// 1/expiredThreshold of its sample expired there is probably more garbage, so it
// passes again immediately — the sweep rate tracks the expiry rate, and there is
// no interval to tune.
//
// Two separate bounds doing different jobs. sampleSize bounds the PAUSE. The
// budget bounds how long the sweeper keeps COMPETING for the lock, because the
// number of passes is O(expired keys) — set by the workload, not by us.
//
// This never fully cleans the cache, and under a large backlog it won't even
// finish this tick. Both are the same bargain: bounded waste in exchange for
// bounded interference.
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

// samplePass locks, examines up to sampleSize keys drawn from the expiring
// index, deletes the expired ones, and unlocks. It reports how many it looked
// at and how many it removed.
//
// Nothing is carried across the unlock — each pass re-reads under the lock. An
// entry read before releasing a lock is a rumor after it: a concurrent Set may
// have given it a fresh deadline, and deleting on the stale copy would destroy
// live data. Statelessness is what lets us drop the lock at all.
func (c *Cache) samplePass() (scanned, expired int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	// Go starts map iteration at a random bucket, which is exactly the cheap
	// random sample we need. It samples buckets, not keys, so it is not
	// perfectly uniform — good enough to estimate a fraction.
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
// sampleSweep and kept only as the benchmark baseline: it is O(total keys), not
// O(expired keys), and holds the lock throughout.
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

	// Overwriting a TTL'd key with a permanent one must un-index it, or the
	// sampler wastes its budget on a key that can never expire.
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
		delete(c.expiring, key)
		return "", false
	}
	return e.value, true
}
