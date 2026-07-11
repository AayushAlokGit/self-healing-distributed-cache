package cache

import (
	"strconv"
	"testing"
	"time"
)

// fill inserts n PERMANENT entries, so a sweep must scan every one and delete none:
// it isolates the cost of the scan from the cost of deleting.
func fill(c *Cache, n int) {
	for i := range n {
		c.Set("key:"+strconv.Itoa(i), "value", noTTL)
	}
}

func BenchmarkGet(b *testing.B) {
	c := newWithSweepInterval(noLimit, time.Hour)
	defer c.Close()
	c.Set("live", "v", noTTL)

	for b.Loop() {
		c.Get("live")
	}
}

// The defect it measures: sweepAll's lock-hold time is linear in the TOTAL key
// count, not in the number of expired keys.
func BenchmarkSweep(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(noLimit, time.Hour)
			defer c.Close()
			fill(c, n)

			for b.Loop() {
				c.sweepAll()
			}
		})
	}
}

// getsIn counts Gets completed in d rather than timing each one: a Get is ~67ns and
// this machine's time.Now() resolves to ~829µs. Both callers pay the per-iteration
// clock read, so ratios survive it.
func getsIn(c *Cache, d time.Duration) int {
	n := 0
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.Get("live")
		n++
	}
	return n
}

// Regression guard on a known defect in sweepAll: one locked full scan stalls readers
// by ~750x. Counterpart: TestSampleSweepDoesNotStallReaders.
func TestSweepStallsReaders(t *testing.T) {
	const (
		keys   = 1_000_000
		window = 500 * time.Millisecond
	)

	c := newWithSweepInterval(noLimit, time.Hour) // no background sweeper; we drive it
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
