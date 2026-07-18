// Package cache is an in-memory key→value store: a mutex-guarded map with per-key
// TTL and O(1) LRU eviction at a capacity. Expired entries are reclaimed lazily on
// read, by a background sweeper, and preferentially at eviction time.
package cache

import (
	"sync"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/vclock"
)

const (
	noLimit = 0

	// Only says how often we check. The sampler sets its own rate.
	defaultSweepInterval = time.Second

	// Keys examined per pass. Bounds how long the lock is held once.
	sampleSize = 20

	// Pass again immediately if more than 1-in-4 of a sample was expired.
	expiredThreshold = 4

	// Bounds how long the sweeper keeps competing for the lock, which sampleSize
	// does not: pass count is O(expired keys). The remainder waits for the next tick.
	sweepBudgetFraction = 4 // at most interval/4 per tick

	// Keys drawn from the expiring index before eviction falls back to the LRU
	// tail. A sample cannot prove no corpse exists, but its hit rate tracks the
	// corpse density, and so does the cost of missing — see evictLocked.
	evictProbeSize = 20

	// Reclamations buffered before the oldest are dropped. Nothing guarantees a
	// drainer exists (a plain `go test` has no dashboard), so this must be bounded
	// or it is an unbounded leak. The log is for display, not durability.
	maxReclaimLog = 64
)

// Why an expired entry's memory was actually freed. The key was already dead to
// every reader at its deadline — Get checks the clock, not the map — so this says
// nothing about WHEN the key expired, only when the bytes went.
const (
	ReclaimLazy  = "lazy"  // a Get landed on it and deleted it in passing
	ReclaimSweep = "sweep" // the background sampler drew it
	ReclaimEvict = "evict" // the cache was full and preferred a corpse to a live key
)

// Reclaim is one expired entry whose memory has been freed.
type Reclaim struct {
	Key    string
	Reason string
}

// node holds one key's set of concurrent versions. Almost always len 1 — the set grows
// only when two writes conflict (neither vector clock dominates), and a later dominating
// write collapses it again. Each Entry carries its own absolute deadline, since two
// conflicting writes can have been made with different TTLs.
type node struct {
	// Carried so that removing a node, given only a pointer, can also delete it
	// from data and expiring.
	key string

	entries []Entry

	// Never nil: the sentinels terminate the recency list.
	prev, next *node
}

// fullyExpired reports whether every entry is past its deadline (an empty node counts):
// only then is the whole key reclaimable. A node with one live entry and one corpse is
// still live.
func (n *node) fullyExpired(now time.Time) bool {
	for _, e := range n.entries {
		if !e.expired(now) {
			return false
		}
	}
	return true
}

// pruneExpired drops expired entries in place, keeping the node's remaining versions.
func (n *node) pruneExpired(now time.Time) {
	live := n.entries[:0]
	for _, e := range n.entries {
		if !e.expired(now) {
			live = append(live, e)
		}
	}
	n.entries = live
}

// hasDeadline reports whether any entry has a deadline, i.e. whether the key belongs in
// the expiring index.
func (n *node) hasDeadline() bool {
	for _, e := range n.entries {
		if !e.Expires.IsZero() {
			return true
		}
	}
	return false
}

// Cache is an in-memory key→value store, safe for concurrent use. The caller MUST
// Close it: it owns a background goroutine, whose stack is a GC root, so it keeps
// the whole Cache reachable. Must not be copied after first use; always pass *Cache.
type Cache struct {
	// mu guards every field below. Every read or write of any must hold it.
	mu sync.Mutex

	// The map has no order; the list has no lookup. Together, both O(1). Nodes are
	// pointers because a map rehash moves its values, and the list is a web of
	// pointers between nodes that must not dangle.
	data map[string]*node

	// expiring indexes only the keys with a deadline. Sampling all of data instead
	// would be diluted to uselessness by a mostly-permanent cache.
	expiring map[string]*node

	// head.next is the most recently used node, tail.prev the least. The sentinels
	// hold no data and never move, which is what lets unlink and pushFront run
	// without a nil check.
	head, tail *node

	// Max entries, or noLimit. Set once at construction.
	capacity int

	// Closed, not sent to, so every receiver unblocks — now and forever.
	done chan struct{}

	// Closing a closed channel panics; Close may be called more than once.
	closeOnce sync.Once

	// Lets Close wait for the sweeper to return, not merely be told to.
	wg sync.WaitGroup

	// reclaimed is what the sweeper and the lazy path have freed since the last
	// drain. Written under mu at the delete sites and taken by DrainReclaimed —
	// the cache never calls out to report it, so it can never be half of a
	// lock-order inversion with whatever is collecting.
	reclaimed []Reclaim
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

		done: make(chan struct{}),
	}
	c.head.next = c.tail
	c.tail.prev = c.head

	c.wg.Go(func() { c.sweepLoop(interval) })
	return c
}

// Close stops the sweeper and blocks until it has returned. Safe to call more than
// once. After Close the Cache still answers Get and Set; it just stops reclaiming
// expired entries in the background.
func (c *Cache) Close() {
	c.closeOnce.Do(func() { close(c.done) })
	c.wg.Wait()
}

// unlink splices n out of the recency list. Callers must hold c.mu.
func (c *Cache) unlink(n *node) {
	n.prev.next = n.next
	n.next.prev = n.prev
}

// pushFront makes n the most recently used node. Callers must hold c.mu.
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

// reclaimLocked is removeLocked for an entry that is being freed BECAUSE it expired,
// and records it. Evicting a live key at the LRU tail is not an expiry and must not
// come through here, or the log would report keys as dead that a client can still read.
// Callers must hold c.mu.
func (c *Cache) reclaimLocked(n *node, reason string) {
	c.removeLocked(n)
	c.reclaimed = append(c.reclaimed, Reclaim{Key: n.key, Reason: reason})
	if over := len(c.reclaimed) - maxReclaimLog; over > 0 {
		c.reclaimed = c.reclaimed[over:] // drop oldest
	}
}

// DrainReclaimed returns every expired entry freed since the last drain, and clears
// the buffer. Clearing is the point: whoever polls this would otherwise re-report the
// same reclamations on every call.
func (c *Cache) DrainReclaimed() []Reclaim {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reclaimed) == 0 {
		return nil
	}
	out := c.reclaimed
	c.reclaimed = nil
	return out
}

// sweepLoop sweeps on each tick until done is closed. It waits on both at once,
// which is why it uses a Ticker and not time.Sleep: Sleep cannot be interrupted,
// so Close would block for up to a full interval.
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

// sampleSweep reclaims expired entries by repeated random sampling, returning how
// many it removed. It passes again while samples come back dirty, so the reclaim
// rate tracks the expiry rate. It never fully cleans the cache, and under a large
// backlog it will not finish this tick: bounded waste for bounded interference.
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

// samplePass examines up to sampleSize keys from the expiring index under the lock,
// deleting the expired ones. It carries nothing across the unlock: an entry read
// before releasing the lock is a rumor after it, since a concurrent Set may have
// given it a fresh deadline.
func (c *Cache) samplePass() (scanned, expired int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	// Go starts map iteration at a random bucket, so this is the sample. It draws
	// buckets, not keys — not uniform, fine for estimating a fraction.
	for _, n := range c.expiring {
		if scanned == sampleSize {
			break
		}
		scanned++

		if n.fullyExpired(now) {
			c.reclaimLocked(n, ReclaimSweep)
			expired++
		}
	}
	return scanned, expired
}

// sweepAll deletes every expired entry in one locked pass. Kept only as the
// baseline sampleSweep is measured against: it is O(total keys), not O(expired),
// and it holds the lock throughout.
func (c *Cache) sweepAll() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0
	for _, n := range c.data {
		if n.fullyExpired(now) {
			c.reclaimLocked(n, ReclaimSweep)
			removed++
		}
	}
	return removed
}

// evictLocked frees exactly one slot. Callers must hold c.mu and must not call it
// on an empty cache.
//
// Corpses first, because recency and expiry are independent orderings: the LRU tail
// can easily be the one permanent key while 999 corpses sit ahead of it. Probing the
// expiring index cannot prove a corpse absent, but the probe's hit rate equals the
// corpse density, so a miss is cheap exactly when it is likely.
// See TestEvictionProbeTracksCorpseDensity.
func (c *Cache) evictLocked(now time.Time) {
	probed := 0
	for _, n := range c.expiring {
		if probed == evictProbeSize {
			break
		}
		probed++

		if n.fullyExpired(now) {
			c.reclaimLocked(n, ReclaimEvict)
			return
		}
	}
	c.removeLocked(c.tail.prev) // a live key, evicted for capacity: not an expiry
}

// Set stores (or overwrites) the value for key, expiring it after ttl. A ttl <= 0
// means the entry never expires; an overwrite's ttl fully replaces the old deadline.
// Inserting a new key into a full cache evicts one entry; an overwrite never does.
func (c *Cache) Set(key, value string, ttl time.Duration) {
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	c.SetAt(key, value, expires)
}

// SetAt is Set with the deadline given as an absolute instant rather than a
// duration. The zero Time means "never expires".
//
// This is the form a REPLICA must be written with. A duration is re-based against
// the receiver's clock every time it crosses the network, so a key copied by a heal
// at t=52s of its 60s life would get a *fresh* 60s on its new replica and outlive
// its own deadline. Deciding the instant once, at the coordinator, means every copy
// of a key dies together no matter when or how often it was replicated.
func (c *Cache) SetAt(key, value string, expires time.Time) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	// Unversioned replace: the legacy path stores a single entry, overwriting whatever
	// was there. Versioned writers use SetVersioned, which reconciles instead.
	c.installLocked(key, []Entry{{Value: value, Expires: expires}}, now)
}

// SetVersioned reconciles a versioned write into key's set: it drops every stored version
// the new one dominates, keeps the ones concurrent with it, and ignores the write when a
// stored version already dominates it (a stale replica). The common case — a write that
// dominates the single value there — collapses the set back to one entry.
func (c *Cache) SetVersioned(key, value string, version vclock.Clock, expires time.Time) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	var existing []Entry
	if n, ok := c.data[key]; ok {
		n.pruneExpired(now)
		existing = n.entries
	}
	incoming := Entry{Value: value, Version: version, Expires: expires}
	c.installLocked(key, reconcile(existing, incoming), now)
}

// installLocked replaces key's version set with entries (already reconciled, never empty),
// refreshing recency and the expiring index. Callers hold c.mu.
func (c *Cache) installLocked(key string, entries []Entry, now time.Time) {
	n, ok := c.data[key]
	if ok {
		n.entries = entries
		c.unlink(n)
	} else {
		if c.capacity > noLimit && len(c.data) >= c.capacity {
			c.evictLocked(now)
		}
		n = &node{key: key, entries: entries}
		c.data[key] = n
	}
	c.pushFront(n)

	if n.hasDeadline() {
		c.expiring[key] = n
	} else {
		delete(c.expiring, key)
	}
}

// reconcile folds incoming into existing and returns the maximal set under vector-clock
// dominance — no kept version dominates another. It assumes existing is already such a set.
// Dropping this to a bare append is the resurfacing bug the vclock tests guard against.
func reconcile(existing []Entry, incoming Entry) []Entry {
	out := make([]Entry, 0, len(existing)+1)
	superseded := false // a stored version dominates or equals incoming: incoming adds nothing
	for _, e := range existing {
		switch vclock.Compare(incoming.Version, e.Version) {
		case vclock.After:
			// incoming dominates e: drop e
		case vclock.Before, vclock.Equal:
			out = append(out, e)
			superseded = true
		default: // Concurrent
			out = append(out, e)
		}
	}
	if !superseded {
		out = append(out, incoming)
	}
	return out
}

// Delete removes key and reports whether a live entry was there to remove. An expired
// corpse the sweeper has not reached counts as absent: no reader could still see it.
//
// Through removeLocked, never reclaimLocked — an explicit delete is not an expiry, and the
// reclaim log would tell the dashboard the key died of old age.
func (c *Cache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.data[key]
	if !ok {
		return false
	}
	live := !n.fullyExpired(time.Now())
	c.removeLocked(n)
	return live
}

// Clear removes every entry and returns how many it physically held, corpses included.
// Like Delete it leaves the reclaim log alone: nothing here expired.
func (c *Cache) Clear() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	held := len(c.data)
	c.data = make(map[string]*node)
	c.expiring = make(map[string]*node)
	// Re-point the sentinels rather than unlinking node by node. Miss this and the list
	// still refers to the dropped chain — which nothing notices until the next eviction
	// walks off a stale tail. See TestClearLeavesTheCacheUsable.
	c.head.next = c.tail
	c.tail.prev = c.head
	return held
}

// Entry is one version of a key: its value, the vector clock stamped on it, and the
// instant it dies. A zero Expires means it never does; a nil Version is an unversioned
// write (the legacy Set path). A key can hold several concurrent Entries at once.
type Entry struct {
	Value   string
	Version vclock.Clock
	Expires time.Time
}

func (e Entry) expired(now time.Time) bool {
	return !e.Expires.IsZero() && now.After(e.Expires)
}

// Snapshot returns a copy of every live entry, skipping the expired.
//
// It carries the deadline, not just the value, because its callers are the ones that
// copy data between nodes, and a copy that loses the deadline never expires (see
// SetAt). It does not touch recency: marking every key most-recently-used would let
// a background heal evict the hot set it is trying to protect.
func (c *Cache) Snapshot() map[string]Entry {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	out := make(map[string]Entry, len(c.data))
	for k, n := range c.data {
		for _, e := range n.entries {
			if !e.expired(now) {
				out[k] = e // first live version; callers that need the conflict use SnapshotAll
				break
			}
		}
	}
	return out
}

// SnapshotAll returns every live version of every key. The form the versioned heal needs:
// a key can hold concurrent values, and all of them must be reconciled onto its owners, not
// just one. Skips expired entries and does not touch recency, like Snapshot.
func (c *Cache) SnapshotAll() map[string][]Entry {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	out := make(map[string][]Entry, len(c.data))
	for k, n := range c.data {
		var live []Entry
		for _, e := range n.entries {
			if !e.expired(now) {
				live = append(live, e)
			}
		}
		if len(live) > 0 {
			out[k] = live
		}
	}
	return out
}

// Len returns how many entries the cache physically holds, including expired ones
// neither Get nor the sweeper has reclaimed yet.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

// Get returns the value for key. The bool is false if the key is absent or expired;
// the caller cannot tell those apart, and shouldn't need to.
//
// A hit is a use, so it moves the node to the front, and an expired entry is
// deleted: Get is a writer twice over and could not take a read lock under RWMutex.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.data[key]
	if !ok {
		return "", false
	}
	now := time.Now()
	if n.fullyExpired(now) {
		c.reclaimLocked(n, ReclaimLazy)
		return "", false
	}

	n.pruneExpired(now)
	c.unlink(n)
	c.pushFront(n)
	return n.entries[0].Value, true // first live version; GetEntries returns the whole set
}

// GetEntries returns every live version of key — one Entry normally, several when the key
// holds a conflict. The bool is false if key is absent or fully expired. Like Get it counts
// as a use and prunes expired versions in passing; the returned slice is the caller's own.
func (c *Cache) GetEntries(key string) ([]Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.data[key]
	if !ok {
		return nil, false
	}
	now := time.Now()
	if n.fullyExpired(now) {
		c.reclaimLocked(n, ReclaimLazy)
		return nil, false
	}

	n.pruneExpired(now)
	c.unlink(n)
	c.pushFront(n)
	out := make([]Entry, len(n.entries))
	copy(out, n.entries)
	return out, true
}
