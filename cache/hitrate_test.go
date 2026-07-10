// Hit rate, not latency, is the metric for an eviction policy: a cache that
// instantly evicts exactly the wrong key has excellent latency. Numbers below
// are measured, not predicted; nothing asserts on them exactly.
package cache

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

const capacity = 1_000

// request is the cache-aside read path: on a miss the application queries the
// database and fills the cache. The cache never chooses to admit a key — it is
// handed one ordinary Set, with no flag saying "this came from a batch job."
// That is how a scan pollutes a cache that never agreed to hold it.
func request(c *Cache, key string) bool {
	if _, ok := c.Get(key); ok {
		return true
	}
	c.Set(key, "value", noTTL)
	return false
}

func hitRate(c *Cache, next func() string, requests int) float64 {
	hits := 0
	for range requests {
		if request(c, next()) {
			hits++
		}
	}
	return float64(hits) / float64(requests)
}

// zipfKeys models real traffic: the k-th most popular key is requested ~1/k^s as
// often as the first. A few keys carry most of the load.
//
// ⚠️ rand.NewZipf returns nil for s <= 1, and panics only when you draw from it.
func zipfKeys(keyspace uint64, s float64) func() string {
	z := rand.NewZipf(rand.New(rand.NewPCG(1, 1)), s, 1, keyspace-1)
	return func() string { return "hot:" + strconv.FormatUint(z.Uint64(), 10) }
}

// uniformKeys models a flat working set: every key equally likely, so every
// cached entry is worth exactly as much as every other.
func uniformKeys(working int) func() string {
	r := rand.New(rand.NewPCG(1, 1))
	return func() string { return "hot:" + strconv.Itoa(r.IntN(working)) }
}

// loopKeys walks a working set in order, forever — a report, a nightly job, a
// table scan repeated every hour.
func loopKeys(working int) func() string {
	i := 0
	return func() string {
		k := "loop:" + strconv.Itoa(i%working)
		i++
		return k
	}
}

// interleave runs a user workload while a batch job scans, one scan request per
// perScan user requests, and returns the hit rate of the USER traffic alone —
// the scan's own requests always miss and are not the application's problem.
func interleave(c *Cache, next func() string, requests, perScan int) float64 {
	hits, scanned := 0, 0
	for i := range requests {
		if request(c, next()) {
			hits++
		}
		if i%perScan == 0 {
			request(c, "scan:"+strconv.Itoa(scanned))
			scanned++
		}
	}
	return float64(hits) / float64(requests)
}

func warm(next func() string) (*Cache, float64) {
	c := newWithSweepInterval(capacity, time.Hour)
	hitRate(c, next, 100_000)
	return c, hitRate(c, next, 20_000)
}

// LRU does not degrade. It falls off a cliff.
//
//	cyclic loop over  900 keys (capacity 1000)   100.0%
//	cyclic loop over 1100 keys (capacity 1000)     0.0%
//
// A 22% wider working set turns a perfect cache into a useless one. Every key is
// evicted exactly one request before it is wanted again: by the time the loop
// comes back to key 0, keys 1..1000 have pushed it off the tail. This is LRU's
// bet on temporal locality losing outright — a loop has locality, just at a
// distance one entry beyond what the cache can hold.
//
// Bélády's optimal policy would keep 1000 of the keys and cycle the remaining
// 100, scoring ~91%. MRU — evict the *most* recent — would score ~91% here too.
// Nothing about a smaller cache is the problem; the policy is.
func TestLRUFallsOffACliffWhenTheWorkingSetGrows(t *testing.T) {
	for _, working := range []int{capacity - 100, capacity + 100} {
		c, steady := warm(loopKeys(working))
		t.Logf("cyclic loop over %4d keys (capacity %d)   %5.1f%%", working, capacity, steady*100)
		c.Close()

		if working < capacity && steady < 0.99 {
			t.Fatalf("a working set that fits should never miss: %.1f%%", steady*100)
		}
		if working > capacity && steady > 0.01 {
			t.Fatalf("a working set one step too large should never hit: %.1f%%", steady*100)
		}
	}
}

// The scan that costs money: a batch job running while user traffic continues.
// Every scan request evicts a working-set key, which the next user request must
// reload, which evicts another. The cache thrashes for the job's whole duration.
//
//	uniform working set 900, capacity 1000
//	  no batch job                 100.0%
//	  1 scan request per 10 user    89.2%
//	  1 scan request per  4 user    76.0%
//	  1 scan request per  1 user    47.7%
//
// A flat working set is the vulnerable shape: every cached entry is worth as
// much as every other, so a stolen slot is a lost hit. Compare
// TestZipfTrafficShrugsOffAScan, where it barely registers.
func TestScanStarvesAFlatWorkingSet(t *testing.T) {
	const working = 900

	c, steady := warm(uniformKeys(working))
	c.Close()
	t.Logf("uniform working set %d, capacity %d", working, capacity)
	t.Logf("  no batch job                %5.1f%%", steady*100)

	var worst float64
	for _, perScan := range []int{10, 4, 1} {
		c, _ := warm(uniformKeys(working))
		worst = interleave(c, uniformKeys(working), 50_000, perScan)
		c.Close()
		t.Logf("  1 scan request per %2d user %5.1f%%", perScan, worst*100)
	}

	if worst > 0.6*steady {
		t.Fatalf("a scan at parity should gut a flat working set: %.1f%% vs %.1f%%",
			worst*100, steady*100)
	}
}

// The negative result, and the reason we are NOT building segmented LRU yet.
//
//	zipf s=1.1 over 10k keys, capacity 1000
//	  no batch job                  78.2%
//	  1 scan request per 10 user    75.4%
//	  1 scan request per  4 user    72.7%
//	  1 scan request per  1 user    65.6%
//
// Real traffic is a power law, and a power law's working set is tiny. The top
// hundred keys carry most of the load, get re-requested every few operations,
// and never drift near the tail. The scan's keys, touched once, sink to the tail
// immediately and evict EACH OTHER. The scan chews on a slice of the cache and
// leaves the hot core alone.
//
// A batch job issuing as many requests as every user combined costs 12.6 points.
// Real, and not the collapse this project asserted for four sessions before
// measuring it. Scan resistance is earned by TestScanStarvesAFlatWorkingSet, not
// by this workload.
func TestZipfTrafficShrugsOffAScan(t *testing.T) {
	const (
		keyspace = 10_000
		skew     = 1.1
	)

	c, steady := warm(zipfKeys(keyspace, skew))
	c.Close()
	t.Logf("zipf s=%.1f over %d keys, capacity %d", skew, keyspace, capacity)
	t.Logf("  no batch job               %5.1f%%", steady*100)

	var worst float64
	for _, perScan := range []int{10, 4, 1} {
		c, _ := warm(zipfKeys(keyspace, skew))
		worst = interleave(c, zipfKeys(keyspace, skew), 50_000, perScan)
		c.Close()
		t.Logf("  1 scan request per %2d user %5.1f%%", perScan, worst*100)
	}

	if worst < 0.75*steady {
		t.Fatalf("zipf traffic should absorb a scan, but lost %.1f%% -> %.1f%%",
			steady*100, worst*100)
	}
}
