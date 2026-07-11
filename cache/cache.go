// Package cache is a naive in-memory key→value store.
//
// Phase 1, step 5: mutex-guarded map with per-key TTL and O(1) LRU eviction at
// a capacity. Expired entries are reclaimed lazily on read, by a background
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

	// Keys drawn from the expiring index before eviction falls back to the LRU
	// tail. A sample cannot prove no corpse exists, but its hit rate tracks the
	// corpse density, and so does the cost of missing — see evictLocked.
	evictProbeSize = 20
)

// node stores the absolute deadline, not the TTL duration: the arithmetic
// happens once at Set time so Get only has to compare. Reads outnumber writes,
// so that's the cheaper place to pay.
type node struct {
	// Carried so that removing a node, given only a pointer, can also delete it
	// from data and expiring.
	key string

	value string

	// The zero Time means "never expires" — see Cache.Set.
	expires time.Time

	// Never nil: the sentinels terminate the recency list.
	prev, next *node
}

func (n *node) expired(now time.Time) bool {
	return !n.expires.IsZero() && now.After(n.expires)
}

// Cache is an in-memory key→value store, safe for concurrent use.
//
// The caller MUST Close it. A Cache owns a background goroutine, and a running
// goroutine's stack is a GC root, so it keeps the whole Cache reachable forever.
//
// Must not be copied after first use: copying duplicates the mutex and the
// copies stop excluding each other. Always pass *Cache.
type Cache struct {
	// mu guards every field below. Every read or write of any must hold it.
	mu sync.Mutex

	// The map has no order; the list has no lookup. Together, both O(1).
	// Nodes are pointers because a map rehash moves its values, and the list
	// is a web of pointers between nodes that must not dangle.
	data map[string]*node

	// expiring indexes only the keys with a deadline. Sampling all of data
	// instead would be diluted to uselessness by a mostly-permanent cache.
	expiring map[string]*node

	// head.next is the most recently used node, tail.prev the least. Sentinels:
	// they hold no data and never move, which is what lets unlink and pushFront
	// run without a nil check.
	head, tail *node

	// Max entries, or noLimit. Bounding entries rather than bytes is a lie when
	// values vary in size — Redis bounds bytes. Set once, so it needs no lock.
	capacity int

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
		data:     make(map[string]*node),
		expiring: make(map[string]*node),
		head:     &node{},
		tail:     &node{},
		capacity: capacity,

		// A channel's zero value is nil, and a receive from nil blocks forever.
		done: make(chan struct{}),
	}
	c.head.next = c.tail
	c.tail.prev = c.head

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

// unlink splices n out of the recency list. O(1) only because the list is
// doubly linked: n.prev is what has to be rewritten, and finding a singly
// linked node's predecessor means walking from the head.
func (c *Cache) unlink(n *node) {
	n.prev.next = n.next
	n.next.prev = n.prev
}

// pushFront makes n the most recently used node.
func (c *Cache) pushFront(n *node) {
	n.prev, n.next = c.head, c.head.next
	c.head.next.prev = n
	c.head.next = n
}

// removeLocked deletes n from all three structures. Callers must hold c.mu.
func (c *Cache) removeLocked(n *node) {
	c.unlink(n)
	delete(c.data, n.key)
	delete(c.expiring, n.key)
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
	for _, n := range c.expiring {
		if scanned == sampleSize {
			break
		}
		scanned++

		if n.expired(now) {
			c.removeLocked(n)
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
	for _, n := range c.data {
		if n.expired(now) {
			c.removeLocked(n)
			removed++
		}
	}
	return removed
}

// evictLocked frees exactly one slot. Callers must hold c.mu and must not call
// it on an empty cache.
//
// Corpses first, because recency and expiry are independent orderings: 999
// sessions Set 100ms ago with a 50ms TTL are all more recently used than a
// permanent config key read a minute ago, so the LRU tail is the config key.
//
// The list is ordered by recency, so it cannot say whether a corpse exists; only
// a scan of every entry could, and a scan is what this rewrite deleted. Probing
// the expiring index cannot prove a corpse absent, but the trade is
// self-correcting: the probe's hit rate equals the corpse density, and a miss
// wastes one slot out of however many entries diluted the sample. Accurate where
// accuracy matters, sloppy where sloppiness is free.
// See TestEvictionProbeTracksCorpseDensity.
func (c *Cache) evictLocked(now time.Time) {
	probed := 0
	for _, n := range c.expiring {
		if probed == evictProbeSize {
			break
		}
		probed++

		if n.expired(now) {
			c.removeLocked(n)
			return
		}
	}
	c.removeLocked(c.tail.prev)
}

// Set stores (or overwrites) the value for key, expiring it after ttl.
// A ttl <= 0 means the entry never expires. Overwriting resets the deadline:
// the new ttl fully replaces the old one.
//
// Inserting a new key into a full cache evicts one entry first. Overwriting
// does not grow the cache, so it never evicts.
func (c *Cache) Set(key, value string, ttl time.Duration) {
	// Read before the lock: no reason to hold it across a clock read.
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	c.SetAt(key, value, expires)
}

// SetAt is Set with the deadline given as an absolute instant rather than a
// duration. The zero Time means "never expires".
//
// This is the form a REPLICA must be written with, and the distinction is not
// cosmetic. A duration is re-based against the receiver's clock every time it
// crosses the network, so a key with 60s of life copied by a heal at t=52s would
// get a *fresh* 60s on its new replica and outlive its own deadline — and each
// subsequent heal would push it further out. Deciding the instant once, at the
// coordinator, and shipping that instant means every copy of a key dies together
// no matter when or how often it was replicated.
func (c *Cache) SetAt(key, value string, expires time.Time) {
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	n, overwrite := c.data[key]
	if overwrite {
		n.value, n.expires = value, expires
		c.unlink(n)
	} else {
		if c.capacity > noLimit && len(c.data) >= c.capacity {
			c.evictLocked(now)
		}
		n = &node{key: key, value: value, expires: expires}
		c.data[key] = n
	}
	c.pushFront(n)

	// Overwriting a TTL'd key with a permanent one must un-index it.
	if !expires.IsZero() {
		c.expiring[key] = n
	} else {
		delete(c.expiring, key)
	}
}

// Entry is a live entry as seen from outside the cache: its value and the instant
// it dies. A zero Expires means it never does.
type Entry struct {
	Value   string
	Expires time.Time
}

// Snapshot returns a copy of every live entry, skipping the expired.
//
// It carries the deadline, not just the value, because its callers are the ones
// that copy data between nodes — and a copy that loses the deadline is a copy that
// never expires. See SetAt.
//
// It deliberately does NOT touch recency. A bulk scan for replication must not
// look like user access: marking every key most-recently-used would be the very
// sequential-scan pollution LRU is vulnerable to (see Phase 1) — a background
// heal would evict the hot set it is trying to protect.
func (c *Cache) Snapshot() map[string]Entry {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	out := make(map[string]Entry, len(c.data))
	for k, n := range c.data {
		if n.expired(now) {
			continue
		}
		out[k] = Entry{Value: n.value, Expires: n.expires}
	}
	return out
}

// Len returns how many entries the cache physically holds, including expired
// ones neither Get nor the sweeper has reclaimed yet.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

// Get returns the value for key. The bool is false if the key is absent or
// expired; the caller cannot tell those apart, and shouldn't need to.
//
// A hit is a use, so it moves the node to the front. Between that and deleting
// expired entries, Get is a writer twice over — it could not take a read lock
// under sync.RWMutex.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.data[key]
	if !ok {
		return "", false
	}
	if n.expired(time.Now()) {
		c.removeLocked(n)
		return "", false
	}

	// No write-back: n is a pointer, so unlike a map value it is addressable.
	c.unlink(n)
	c.pushFront(n)
	return n.value, true
}
