package cache

import (
	"math"
	"strconv"
	"testing"
	"time"
)

func has(t *testing.T, c *Cache, key string) {
	t.Helper()
	if _, ok := c.Get(key); !ok {
		t.Fatalf("expected %q to be present", key)
	}
}

func lacks(t *testing.T, c *Cache, key string) {
	t.Helper()
	if _, ok := c.Get(key); ok {
		t.Fatalf("expected %q to be evicted", key)
	}
}

func TestCapacityIsNeverExceeded(t *testing.T) {
	const capacity = 3

	c := newWithSweepInterval(capacity, time.Hour)
	defer c.Close()

	for i := range 100 {
		c.Set("key:"+strconv.Itoa(i), "v", noTTL)
		if got := c.Len(); got > capacity {
			t.Fatalf("after %d Sets, Len()=%d exceeds capacity %d", i+1, got, capacity)
		}
	}
	if got := c.Len(); got != capacity {
		t.Fatalf("expected the cache to fill to %d, Len()=%d", capacity, got)
	}
}

func TestNoLimitNeverEvicts(t *testing.T) {
	c := newWithSweepInterval(noLimit, time.Hour)
	defer c.Close()

	for i := range 1000 {
		c.Set("key:"+strconv.Itoa(i), "v", noTTL)
	}
	if got := c.Len(); got != 1000 {
		t.Fatalf("noLimit cache evicted something: Len()=%d", got)
	}
}

// Guards the bug where Set evicts unconditionally: at capacity 2,
// c.Set("a", ...) would evict "a" itself.
func TestOverwriteDoesNotEvict(t *testing.T) {
	c := newWithSweepInterval(2, time.Hour)
	defer c.Close()

	c.Set("a", "1", noTTL)
	c.Set("b", "1", noTTL)
	c.Set("a", "2", noTTL)

	if got := c.Len(); got != 2 {
		t.Fatalf("overwrite changed the entry count: Len()=%d", got)
	}
	has(t, c, "b")
	if v, _ := c.Get("a"); v != "2" {
		t.Fatalf("overwrite lost the new value, got %q", v)
	}
}

// The policy itself: a read is a use, so it protects a key from eviction.
func TestEvictsLeastRecentlyUsed(t *testing.T) {
	c := newWithSweepInterval(3, time.Hour)
	defer c.Close()

	c.Set("a", "v", noTTL)
	c.Set("b", "v", noTTL)
	c.Set("c", "v", noTTL)

	c.Get("a") // a becomes the most recently used; b is now the least

	c.Set("d", "v", noTTL)

	lacks(t, c, "b")
	has(t, c, "a")
	has(t, c, "c")
	has(t, c, "d")
}

// Every corpse here was Set after the live key was last touched, so plain LRU
// evicts the one entry worth keeping and holds on to 999 dead ones.
func TestEvictsCorpseBeforeLiveKey(t *testing.T) {
	const capacity = 1000

	c := newWithSweepInterval(capacity, time.Hour)
	defer c.Close()

	c.Set("config", "v", noTTL)
	fillExpiring(c, capacity-1, time.Millisecond)

	time.Sleep(10 * time.Millisecond) // the 999 sessions are now corpses

	c.Set("newcomer", "v", noTTL)

	has(t, c, "config")
	has(t, c, "newcomer")
}

// The map and the list index the same nodes, and every removal path has to touch
// both. Exercises all four mutations: insert, move-to-front, overwrite, evict.
func TestRecencyListMatchesMap(t *testing.T) {
	c := newWithSweepInterval(3, time.Hour)
	defer c.Close()

	c.Set("a", "v", noTTL)
	c.Set("b", "v", noTTL)
	c.Get("a")
	c.Set("b", "v2", noTTL)
	c.Set("c", "v", time.Millisecond)
	c.Set("d", "v", noTTL)

	var forward []string
	for n := c.head.next; n != c.tail; n = n.next {
		forward = append(forward, n.key)
	}
	if len(forward) != c.Len() {
		t.Fatalf("list holds %d nodes, map holds %d", len(forward), c.Len())
	}
	for _, k := range forward {
		if _, ok := c.data[k]; !ok {
			t.Fatalf("list holds %q, map does not", k)
		}
	}

	// The list must read the same walked backwards, or a prev pointer is wrong.
	var backward []string
	for n := c.tail.prev; n != c.head; n = n.prev {
		backward = append(backward, n.key)
	}
	for i, k := range forward {
		if got := backward[len(backward)-1-i]; got != k {
			t.Fatalf("prev pointers disagree with next at %d: %q vs %q", i, k, got)
		}
	}
}

// The claim in evictLocked, measured against 1-(1-density)^evictProbeSize:
//
//	density 0.001   probe hit   2%   (theory   2%)
//	density 0.010   probe hit  16%   (theory  18%)
//	density 0.100   probe hit  88%   (theory  88%)
//	density 0.500   probe hit 100%   (theory 100%)
//	density 0.990   probe hit 100%   (theory 100%)
//
// Only the ends matter. At 0.99 the probe never misses — that is quiz S4d Q4,
// where missing means evicting the one live key and keeping 999 corpses. At
// 0.001 it almost always misses, and the miss costs one wasted slot in a
// thousand, which the sweeper collects within the second.
//
// The live keys go in first, so the LRU tail is always live and the fallback
// always costs a real entry. Deadlines are backdated rather than slept through:
// a 1ns TTL is not expired until the clock ticks, and it does so every 541µs.
func TestEvictionProbeTracksCorpseDensity(t *testing.T) {
	const (
		keys   = 1_000
		trials = 50
	)

	countExpired := func(c *Cache) int {
		now := time.Now()
		n := 0
		for _, nd := range c.data {
			if nd.expired(now) {
				n++
			}
		}
		return n
	}

	for _, density := range []float64{0.001, 0.01, 0.1, 0.5, 0.99} {
		dead := int(float64(keys) * density)
		hits := 0

		for range trials {
			c := newWithSweepInterval(keys, time.Hour)

			for i := range keys - dead {
				c.Set("live:"+strconv.Itoa(i), "v", time.Hour)
			}
			past := time.Now().Add(-time.Hour)
			for i := range dead {
				key := "dead:" + strconv.Itoa(i)
				c.Set(key, "v", time.Hour)
				c.data[key].expires = past
			}

			if got := countExpired(c); got != dead {
				t.Fatalf("setup: %d corpses, want %d", got, dead)
			}
			c.Set("newcomer", "v", noTTL)
			if countExpired(c) < dead {
				hits++
			}
			c.Close()
		}

		hitRate := float64(hits) / trials
		t.Logf("density %.3f  probe hit %3.0f%%  (theory %3.0f%%)",
			density, hitRate*100, (1-math.Pow(1-density, evictProbeSize))*100)

		if density >= 0.99 && hitRate < 0.95 {
			t.Fatalf("probe missed a near-certain corpse: density %.3f, hit rate %.2f", density, hitRate)
		}
		if density <= 0.001 && hitRate > 0.30 {
			t.Fatalf("probe found a rare corpse too often to be a %d-key sample: hit rate %.2f",
				evictProbeSize, hitRate)
		}
	}
}

func TestEvictsLiveKeyWhenNoCorpseExists(t *testing.T) {
	c := newWithSweepInterval(3, time.Hour)
	defer c.Close()

	c.Set("old", "v", time.Hour)
	c.Set("b", "v", time.Hour)
	c.Set("c", "v", time.Hour)
	c.Set("d", "v", time.Hour)

	lacks(t, c, "old")
	if got := len(c.expiring); got != 3 {
		t.Fatalf("eviction left the expiring index stale: %d entries", got)
	}
}

// The victim scan, before and after the recency list:
//
//	         scan for min lastUsed   unlink the tail
//	  1k          22,843 ns/op          410.1 ns/op       56x
//	 10k         223,358 ns/op          489.3 ns/op      456x
//	100k       2,010,846 ns/op          452.4 ns/op    4,445x
//	  1M      25,608,480 ns/op          579.4 ns/op   44,199x
//
// The scan cost 23 ns/key — BenchmarkSweep's 27.5 ns/key, because it was the
// same loop, moved onto the caller's goroutine and run on every Set. The list
// is flat: 1.4x over 1000x the data, which is cache and TLB misses, not work.
//
// The 2 allocs/op are the node and the key string, not the eviction.
func BenchmarkSetAtCapacity(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(n, time.Hour)
			defer c.Close()
			fill(c, n) // exactly full, and permanent, so every Set evicts and no probe hits

			i := 0
			for b.Loop() {
				c.Set("new:"+strconv.Itoa(i), "v", noTTL)
				i++
			}
		})
	}
}
