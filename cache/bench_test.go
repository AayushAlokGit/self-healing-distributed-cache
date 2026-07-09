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
func BenchmarkGet(b *testing.B) {
	c := newWithSweepInterval(time.Hour)
	defer c.Close()
	c.Set("live", "v", noTTL)

	for b.Loop() {
		c.Get("live")
	}
}

// BenchmarkSweep shows lock-hold time growing linearly with the key count.
// This is the pause every reader waits out.
func BenchmarkSweep(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(time.Hour)
			defer c.Close()
			fill(c, n)

			for b.Loop() {
				c.sweep()
			}
		})
	}
}

// getsIn counts how many Gets complete in d. Individual reads are ~67ns, far
// below this machine's ~829µs clock resolution, so per-operation latency is
// unmeasurable — count operations over a fixed window instead.
func getsIn(c *Cache, d time.Duration) int {
	n := 0
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.Get("live")
		n++
	}
	return n
}

// TestSweepStallsReaders is the measurement that earns the sampling rewrite.
//
// A reader does nothing but Get one live key. Start a sweeper scanning a
// million keys and that same Get blocks behind the lock for a whole scan.
// Throughput collapses: the cache has traded a memory problem for a latency
// problem, and latency is what a cache exists to fix.
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
	c.sweep()
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
				c.sweep()
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
