package cache

import (
	"strconv"
	"testing"
	"time"
)

// fillExpiring inserts n entries that all expire after ttl.
func fillExpiring(c *Cache, n int, ttl time.Duration) {
	for i := range n {
		c.Set("session:"+strconv.Itoa(i), "value", ttl)
	}
}

// The whole point of the rewrite, against TestSweepStallsReaders' 751x:
//
//	gets in 500ms:  no sweep = 6,606,691   during sampling = 3,362,694   (2.0x)
//
// And the residual 2.0x is not a stall. This test runs sampleSweep flat out in
// a `default:` loop, so two goroutines fight over one mutex full-time and each
// gets about half. That is the correct answer, not a defect. The real sweepLoop
// runs one pass per tick: 7µs per second, 0.0007% of wall time, against the
// full scan's 2.5%.
func TestSampleSweepDoesNotStallReaders(t *testing.T) {
	const (
		keys   = 1_000_000
		window = 500 * time.Millisecond
	)

	c := newWithSweepInterval(time.Hour)
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

	// The full-scan sweeper cost 751x. Sampling shares the lock with a reader
	// that is itself hammering it, so some contention is expected and honest.
	if during*10 < baseline {
		t.Fatalf("sampling still stalls readers badly: %d → %d gets", baseline, during)
	}
}

// Sampling gives up on ever being exactly clean, but must still converge.
//
//	reclaimed 50000 corpses in 2 sampleSweep calls, Len()=0
//
// Twenty keys at a time cleared 50k corpses in two calls, because a sample that
// comes back 100% expired fails the expiredThreshold check and passes again
// immediately — no sleep, no tick. "Sample 20 keys" does not mean "reclaim 20
// keys per interval"; it means reclaim as fast as there is garbage, in bites
// that never block a reader for long. The rate is emergent, not configured, so
// defaultSweepInterval no longer sets it — it only says how often we check.
//
// The converse is the case that matters: a healthy cache's first sample comes
// back clean and sampleSweep returns having done ~1µs of work.
func TestSampleSweepReclaimsCorpses(t *testing.T) {
	const keys = 50_000

	c := newWithSweepInterval(time.Hour)
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

// A cache of mostly-permanent keys must not dilute the sample. Without the
// expiring index, 20 keys drawn from 100k permanent + 100 expiring would find
// an expired one almost never, and the sampler would conclude it had no work.
func TestSampleSweepIgnoresPermanentKeys(t *testing.T) {
	const (
		permanent = 100_000
		expiring  = 100
	)

	c := newWithSweepInterval(time.Hour)
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

// Set must un-index a key overwritten from a TTL to permanent, or the sampler
// spends its budget on a key that can never expire.
func TestExpiringIndexStaysConsistent(t *testing.T) {
	c := newWithSweepInterval(time.Hour)
	defer c.Close()

	c.Set("k", "v", time.Hour)
	if len(c.expiring) != 1 {
		t.Fatalf("TTL'd key should be indexed, index has %d", len(c.expiring))
	}

	c.Set("k", "v", noTTL) // now permanent
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

// The headline claim, and a correction to it.
//
//	                         sampling      full scan (BenchmarkSweep)     ratio
//	BenchmarkSamplePass/1000-20        952.9 ns        24,380 ns            26x
//	BenchmarkSamplePass/10000-20     1,049   ns       242,371 ns           231x
//	BenchmarkSamplePass/100000-20    1,756   ns     2,351,620 ns         1,339x
//	BenchmarkSamplePass/1000000-20   7,064   ns    27,489,911 ns         3,891x
//
// I claimed the pause would be CONSTANT. It isn't: 953ns -> 7,064ns, a 7.4x
// growth over 1000x the data. It touches exactly sampleSize keys at every size;
// what changes is what a touch costs. At 1k the map is in L1; at 1M it's spread
// over hundreds of MB and every random bucket probe is a cache and TLB miss.
// 48 ns/key at 1k, 353 ns/key at 1M. Not algorithmic — physics.
//
// The full scan grew 1,128x over the same range, exactly as O(n) demands. So it
// is flat-ish vs linear, not constant vs linear, and the gap widens with n.
//
// Worth keeping the correction: the algorithm said constant, the measurement
// said nearly. Memory locality is not visible from the algorithm.
func BenchmarkSamplePass(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c := newWithSweepInterval(time.Hour)
			defer c.Close()
			fillExpiring(c, n, time.Hour) // present, indexed, none expired

			for b.Loop() {
				c.samplePass()
			}
		})
	}
}
