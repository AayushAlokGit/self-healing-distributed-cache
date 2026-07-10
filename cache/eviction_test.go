package cache

import (
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

// The defect, on the write path this time:
//
//	BenchmarkSetAtCapacity/1000-20         22,843 ns/op   -> 22.8 ns/key
//	BenchmarkSetAtCapacity/10000-20       223,358 ns/op   -> 22.3 ns/key
//	BenchmarkSetAtCapacity/100000-20    2,010,846 ns/op   -> 20.1 ns/key
//	BenchmarkSetAtCapacity/1000000-20  25,608,480 ns/op   -> 25.6 ns/key
//
// 23 ns/key is BenchmarkSweep's 27.5 ns/key: it is the same scan. What changed
// is where it runs. sweepAll paid it once a second on a background goroutine;
// this pays it on the caller's goroutine on every Set, and a cache that is not
// full has the wrong capacity.
//
// Fixed in step 5 by a doubly linked list: O(1) evict, no scan.
func BenchmarkSetAtCapacity(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(n, time.Hour)
			defer c.Close()
			fill(c, n) // exactly full and all permanent: every Set scans and evicts

			i := 0
			for b.Loop() {
				c.Set("new:"+strconv.Itoa(i), "v", noTTL)
				i++
			}
		})
	}
}
