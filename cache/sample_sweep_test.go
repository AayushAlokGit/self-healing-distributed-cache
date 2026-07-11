package cache

import (
	"strconv"
	"testing"
	"time"
)

func fillExpiring(c *Cache, n int, ttl time.Duration) {
	for i := range n {
		c.Set("session:"+strconv.Itoa(i), "value", ttl)
	}
}

// Counterpart to TestSweepStallsReaders' 751x. The residual ~2x here is not a stall:
// this sweeps flat out, so two goroutines split one mutex, where the real sweepLoop
// costs ~7µs/s.
func TestSampleSweepDoesNotStallReaders(t *testing.T) {
	const (
		keys   = 1_000_000
		window = 500 * time.Millisecond
	)

	c := newWithSweepInterval(noLimit, time.Hour)
	defer c.Close()
	fill(c, keys)
	c.Set("live", "v", noTTL)

	baseline := getsIn(c, window)

	stop, swept := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(swept)
		for {
			select {
			case <-stop:
				return
			default:
				c.sampleSweep(time.Millisecond)
			}
		}
	}()

	during := getsIn(c, window)

	close(stop)
	<-swept

	t.Logf("gets in %v:  no sweep = %d,  during sampling = %d  (%.1fx fewer)",
		window, baseline, during, float64(baseline)/float64(during))

	if during*10 < baseline {
		t.Fatalf("sampling still stalls readers badly: %d → %d gets", baseline, during)
	}
}

// Sampling is never exactly clean, but must converge: a sample that comes back 100%
// expired fails expiredThreshold and passes again immediately, so the reclaim rate
// tracks the expiry rate — 20 keys per pass is not 20 keys per interval.
func TestSampleSweepReclaimsCorpses(t *testing.T) {
	const keys = 50_000

	c := newWithSweepInterval(noLimit, time.Hour)
	defer c.Close()
	fillExpiring(c, keys, 10*time.Millisecond)

	time.Sleep(30 * time.Millisecond)

	if got := c.Len(); got != keys {
		t.Fatalf("nothing should have been reclaimed yet, Len()=%d", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	passes := 0
	for c.Len() > 0 && time.Now().Before(deadline) {
		c.sampleSweep(10 * time.Millisecond)
		passes++
	}

	t.Logf("reclaimed %d corpses in %d sampleSweep calls, Len()=%d", keys, passes, c.Len())

	if c.Len() != 0 {
		t.Fatalf("sampling failed to converge: %d entries left", c.Len())
	}
}

// Without the expiring index, 20 keys drawn from 100k permanent + 100 expiring would
// find an expired one almost never, and the sampler would idle while the 100 rot.
func TestSampleSweepIgnoresPermanentKeys(t *testing.T) {
	const (
		permanent = 100_000
		expiring  = 100
	)

	c := newWithSweepInterval(noLimit, time.Hour)
	defer c.Close()
	fill(c, permanent)
	fillExpiring(c, expiring, 10*time.Millisecond)

	if got := len(c.expiring); got != expiring {
		t.Fatalf("expiring index should hold only TTL'd keys, has %d", got)
	}

	time.Sleep(30 * time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for len(c.expiring) > 0 && time.Now().Before(deadline) {
		c.sampleSweep(10 * time.Millisecond)
	}

	if got := c.Len(); got != permanent {
		t.Fatalf("expected the %d permanent keys to survive, Len()=%d", permanent, got)
	}
	if got := len(c.expiring); got != 0 {
		t.Fatalf("expected all expiring keys reclaimed, %d left", got)
	}
}

// data and expiring must agree. Three sites mutate data, so three must mutate the
// index — including Set overwriting a TTL'd key with a permanent one.
func TestExpiringIndexStaysConsistent(t *testing.T) {
	c := newWithSweepInterval(noLimit, time.Hour)
	defer c.Close()

	c.Set("k", "v", time.Hour)
	if len(c.expiring) != 1 {
		t.Fatalf("TTL'd key should be indexed, index has %d", len(c.expiring))
	}

	c.Set("k", "v", noTTL)
	if len(c.expiring) != 0 {
		t.Fatalf("permanent key should be un-indexed, index has %d", len(c.expiring))
	}

	c.Set("gone", "v", time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	c.Get("gone") // lazy expiry deletes it

	if len(c.expiring) != 0 {
		t.Fatalf("Get should drop the index entry too, index has %d", len(c.expiring))
	}
}

// The pause is not constant: it touches exactly sampleSize keys at every size, but a
// random bucket probe becomes a cache and TLB miss as the map grows. Flat-ish, not
// constant — and still ~13,000x cheaper than BenchmarkSweep at 1M keys.
func BenchmarkSamplePass(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(noLimit, time.Hour)
			defer c.Close()
			fillExpiring(c, n, time.Hour) // indexed, none expired

			for b.Loop() {
				c.samplePass()
			}
		})
	}
}
