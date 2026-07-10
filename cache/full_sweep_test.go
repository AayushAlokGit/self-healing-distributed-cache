// Numbers below are from a 20-CPU Windows machine, Go 1.26. Recorded for their
// shape and ratios; nothing asserts against them.
//
//	go test -count=1 -run xxx -bench . ./cache/
package cache

import (
	"strconv"
	"testing"
	"time"
)

// fill inserts n PERMANENT entries, so a sweep must scan every one and delete
// none: it isolates the cost of the scan from the cost of deleting.
func fill(c *Cache, n int) {
	for i := range n {
		c.Set("key:"+strconv.Itoa(i), "value", noTTL)
	}
}

// BenchmarkGet-20   16944270   66.99 ns/op   0 allocs/op
//
// Decomposed: mutex Lock+Unlock 26.87ns, map lookup 22.18ns, time.Now() 8.03ns.
// An uncontended mutex is 40% of a read, with only ~22ns of work to overlap —
// that's the case against sync.RWMutex, whose RLock costs more than a Lock.
func BenchmarkGet(b *testing.B) {
	c := newWithSweepInterval(time.Hour)
	defer c.Close()
	c.Set("live", "v", noTTL)

	for b.Loop() {
		c.Get("live")
	}
}

// The defect: lock-hold time is linear in the TOTAL key count.
//
//	BenchmarkSweep/1000-20         24,380 ns/op   -> 24.4 ns/key
//	BenchmarkSweep/10000-20       242,371 ns/op   -> 24.2 ns/key
//	BenchmarkSweep/100000-20    2,351,620 ns/op   -> 23.5 ns/key
//	BenchmarkSweep/1000000-20  27,489,911 ns/op   -> 27.5 ns/key
//
// fill() writes permanent keys, so every sweep here deleted nothing and still
// cost 27ms at 1M: the scan pays for looking, not for finding.
//
// 24 ns/key is already one map-iteration step plus a compare, so a full scan
// cannot be optimized below this — see BenchmarkSamplePass, 7,064ns at 1M.
func BenchmarkSweep(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(time.Hour)
			defer c.Close()
			fill(c, n)

			for b.Loop() {
				c.sweepAll()
			}
		})
	}
}

// getsIn counts Gets completed in d rather than timing each one: a Get is ~67ns
// and this machine's time.Now() resolves to ~829µs. Reading the clock per
// iteration implies ~76ns/read, but both callers pay it, so ratios survive.
func getsIn(c *Cache, d time.Duration) int {
	n := 0
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.Get("live")
		n++
	}
	return n
}

// The measurement that earned the sampling rewrite.
//
//	one sweep of 1,000,000 keys holds the lock for 47.9ms
//	gets in 500ms:  no sweep = 6,584,449   during sweep = 8,769   (751x fewer)
//
// Those 8,769 reads are 0.67ms of work in a 500ms window — the reader was
// productive 0.13% of the time. It gets 8,769 rather than 0 only because Go's
// mutex hands the lock to a waiter that has blocked 1ms (starvation mode).
//
// 47.9ms vs the benchmark's 27.5ms: this is the first sweep after writing 1M
// entries, so caches and TLB are cold. That's what a real sweeper pays.
//
// Now a regression guard on a known defect in sweepAll. Counterpart:
// TestSampleSweepDoesNotStallReaders.
func TestSweepStallsReaders(t *testing.T) {
	const (
		keys   = 1_000_000
		window = 500 * time.Millisecond
	)

	c := newWithSweepInterval(time.Hour) // no background sweeper; we drive it
	defer c.Close()
	fill(c, keys)
	c.Set("live", "v", noTTL)

	start := time.Now()
	c.sweepAll()
	pause := time.Since(start)

	baseline := getsIn(c, window)

	stop, swept := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(swept)
		for {
			select {
			case <-stop:
				return
			default:
				c.sweepAll()
			}
		}
	}()

	during := getsIn(c, window)

	close(stop)
	<-swept

	t.Logf("one sweep of %d keys holds the lock for %v", keys, pause)
	t.Logf("gets in %v:  no sweep = %d,  during sweep = %d  (%.0fx fewer)",
		window, baseline, during, float64(baseline)/float64(during))

	if during*10 > baseline {
		t.Fatalf("expected sweeps to stall readers hard: %d → %d gets", baseline, during)
	}
}
