// Numbers recorded below are from a 20-logical-CPU Windows machine, Go 1.26.
// They are here to make the *shape* and the *ratios* legible; absolute values
// move with hardware, so nothing asserts against them.
//
//	go test -count=1 -run xxx -bench . ./cache/
package cache

import (
	"strconv"
	"testing"
	"time"
)

// fill inserts n permanent entries. Permanent, so a sweep must scan every one
// and delete none: it isolates the cost of holding the lock across the scan.
func fill(c *Cache, n int) {
	for i := range n {
		c.Set("key:"+strconv.Itoa(i), "value", noTTL)
	}
}

// BenchmarkGet is the baseline: an uncontended map lookup behind a mutex.
//
//	BenchmarkGet-20   16944270   66.99 ns/op   0 B/op   0 allocs/op
//
// Decomposed (measured separately, with typed sinks so nothing boxes into an
// interface and allocates):
//
//	mutex Lock+Unlock   26.87 ns   <- 40% of a Get, and the lock is UNCONTENDED
//	map lookup          22.18 ns
//	time.Now()           8.03 ns   <- the TTL check is nearly free
//
// The mutex being the most expensive part is the sync.RWMutex argument, with
// evidence: an RLock costs *more* than a Lock, and it would have only ~22ns of
// real work to overlap. Not an obvious win. Measure before switching.
func BenchmarkGet(b *testing.B) {
	c := newWithSweepInterval(time.Hour)
	defer c.Close()
	c.Set("live", "v", noTTL)

	for b.Loop() {
		c.Get("live")
	}
}

// BenchmarkSweep is the defect, quantified: lock-hold time grows linearly with
// the TOTAL key count. This is the pause every reader waits out.
//
//	BenchmarkSweep/1000-20         24,380 ns/op   -> 24.4 ns/key
//	BenchmarkSweep/10000-20       242,371 ns/op   -> 24.2 ns/key
//	BenchmarkSweep/100000-20    2,351,620 ns/op   -> 23.5 ns/key
//	BenchmarkSweep/1000000-20  27,489,911 ns/op   -> 27.5 ns/key
//
// Ten times the keys, ten times the time, across three orders of magnitude.
//
// Note fill() writes PERMANENT keys, so every one of these sweeps deleted
// nothing and still cost 27ms at 1M. The sweep pays for LOOKING, not for
// finding: its cost is set by how big the cache is, not how much garbage is in
// it. A cache of 1M live keys and 3 corpses pays the full 27ms to reclaim 3.
//
// ~24 ns/key is already about one map-iteration step (22.18ns, see
// BenchmarkGet) plus an IsZero+After compare. A full scan cannot be optimized
// below this. You have to stop scanning -- see sample_test.go BenchmarkSamplePass,
// where 1M keys cost 7,064 ns instead of 27,489,911 (3,891x).
//
// The drift from 23.5 to 27.5 ns/key at 1M is cache misses: the map no longer
// fits in L2/L3, so each step reaches further into RAM.
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

// getsIn counts how many Gets complete in d.
//
// It counts rather than times because a Get takes ~67ns and this machine's
// time.Now() resolves to ~829µs — 12,000x coarser. Timing each read printed
// p50=0s. An earlier version also collected latencies into a growing slice and
// reported a phantom 10ms worst case with nothing running: that was its own
// append() triggering a GC. It measured the measurement.
//
//	Fix the count and time the batch (what b.Loop does), or fix the time and
//	count the operations (this). Never allocate inside the measured loop.
//
// The clock is read every iteration, so this implies ~76ns/read against
// BenchmarkGet's clean 67ns. Both conditions pay that overhead identically, so
// the ratio survives even though neither absolute number is a clean Get cost.
func getsIn(c *Cache, d time.Duration) int {
	n := 0
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.Get("live")
		n++
	}
	return n
}

// TestSweepStallsReaders is the measurement that earned the sampling rewrite.
//
//	one sweep of 1,000,000 keys holds the lock for 47.9ms
//	gets in 500ms:  no sweep = 6,584,449   during sweep = 8,769   (751x fewer)
//
// 8,769 reads x ~76ns is 0.67ms of work in a 500ms window: the reader was
// productive 0.13% of the time. Not "slower" — an outage that answers 8,769
// requests. It gets 8,769 rather than 0 only because Go's mutex enters
// starvation mode after a waiter blocks 1ms and hands the lock over directly.
//
// 47.9ms here vs 27.5ms in BenchmarkSweep because this is the FIRST sweep after
// writing 1M entries: cold caches, cold TLB. The benchmark reports warm steady
// state. The cold number is what a real sweeper pays.
//
// On a 1s interval a 27ms pause freezes the cache 2.5% of the time. One request
// in forty stalls, on an idle machine, by construction — blowing any p99 budget
// for a store whose uncontended read is 67 NANOseconds. The naive sweeper
// converts a memory problem into a tail-latency problem, which for a cache is a
// bad trade. Tuning the interval slides along that curve; it doesn't leave it.
//
// This test now guards a KNOWN DEFECT in sweepAll, which sampleSweep replaced.
// Its counterpart is TestSampleSweepDoesNotStallReaders (751x -> 2.0x).
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
