package cache

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// heapMB forces a collection first, so the number is what's still reachable rather
// than what's merely uncollected.
func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// The session-cache workload: a key per login, short TTL, never read back. Lazy
// expiry cannot reclaim these — no Get ever comes for them — and neither can the GC,
// since every corpse is reachable from c.data. Only a sweep can.
//
// The sweep is called directly, not waited for: a background sweeper would race the
// write loop, which takes ~1s for 200k keys under -race.
func TestSweepReclaimsUnreadKeys(t *testing.T) {
	const (
		keys = 200_000
		ttl  = 50 * time.Millisecond
	)

	c := newWithSweepInterval(noLimit, time.Hour) // no background sweeper; we drive it
	defer c.Close()

	for i := range keys {
		// Distinct values, or all 200k entries share one backing array and the
		// payload isn't real.
		c.Set("session:"+strconv.Itoa(i), strings.Repeat("x", 100)+strconv.Itoa(i), ttl)
	}
	time.Sleep(2 * ttl) // every key is now a corpse

	leaked := heapMB()
	if got := c.Len(); got != keys {
		t.Fatalf("lazy expiry should reclaim nothing on its own, Len()=%d want %d", got, keys)
	}

	c.sweepAll()

	// The heap is logged, not asserted: what survives is the bucket arrays, whose
	// size tracks sizeof(node), so any threshold here is a disguised assertion about
	// sizeof(node). Len()==0 below is the real check.
	t.Logf("%d corpses held %.1f MB through a forced GC; %.1f MB survives the sweep",
		keys, leaked, heapMB())

	if got := c.Len(); got != 0 {
		t.Fatalf("sweep should have reclaimed every expired entry, Len()=%d", got)
	}
}

// If sweepLoop ignored done, Close would block here forever.
func TestCloseStopsSweeper(t *testing.T) {
	before := runtime.NumGoroutine()

	c := newWithSweepInterval(noLimit, time.Millisecond)
	c.Set("k", "v", noTTL)
	c.Close()

	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("sweeper outlived Close: %d goroutines before, %d after", before, after)
	}
}

// Cache.closeOnce ensures the second Close does not close a closed channel and panic.
func TestCloseIsIdempotent(t *testing.T) {
	c := newWithSweepInterval(noLimit, time.Millisecond)
	c.Close()
	c.Close()
}

func TestSweeperSparesLiveAndPermanentKeys(t *testing.T) {
	c := newWithSweepInterval(noLimit, 5*time.Millisecond)
	defer c.Close()

	c.Set("doomed", "v", 10*time.Millisecond)
	c.Set("longlived", "v", time.Hour)
	c.Set("permanent", "v", noTTL)

	time.Sleep(50 * time.Millisecond)

	if _, ok := c.Get("doomed"); ok {
		t.Fatalf("expired key should have been swept")
	}
	if _, ok := c.Get("longlived"); !ok {
		t.Fatalf("sweeper deleted a key that had an hour left")
	}
	if _, ok := c.Get("permanent"); !ok {
		t.Fatalf("sweeper deleted a ttl<=0 key")
	}
}
