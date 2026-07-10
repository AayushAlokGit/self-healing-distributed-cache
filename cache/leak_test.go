package cache

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// heapMB forces a collection first, so the number is what's still reachable
// rather than what's merely uncollected.
func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// The session-cache workload: a key per login, short TTL, never read back.
// Lazy expiry cannot reclaim these — no Get ever comes for them — and the GC
// cannot either, since every corpse is reachable from c.data. Only a sweep can.
//
// The sweep is invoked directly rather than waited for: a background sweeper
// would race the write loop, which takes ~1s for 200k keys under -race.
func TestSweepReclaimsUnreadKeys(t *testing.T) {
	const (
		keys = 200_000
		ttl  = 50 * time.Millisecond
	)

	c := newWithSweepInterval(noLimit, time.Hour) // effectively disables the background sweeper
	defer c.Close()

	before := heapMB()
	for i := range keys {
		// Distinct values, or all 200k entries share one backing array and the
		// payload isn't real.
		c.Set("session:"+strconv.Itoa(i), strings.Repeat("x", 100)+strconv.Itoa(i), ttl)
	}
	afterWrite := heapMB()

	time.Sleep(2 * ttl)

	if got := c.Len(); got != keys {
		t.Fatalf("lazy expiry should reclaim nothing on its own, Len()=%d want %d", got, keys)
	}
	leaked := heapMB()

	c.sweepAll()
	afterSweep := heapMB()

	reclaimed := leaked - afterSweep

	t.Logf("heap: %.1f MB empty → %.1f MB written → %.1f MB all-expired → %.1f MB swept",
		before, afterWrite, leaked, afterSweep)
	t.Logf("Len(): %d written → %d all-expired → %d swept", keys, keys, c.Len())
	t.Logf("sweep returned %.1f MB; %.1f MB of bucket array survives Len()==0", reclaimed, afterSweep)

	if got := c.Len(); got != 0 {
		t.Fatalf("sweep should have reclaimed every expired entry, Len()=%d", got)
	}

	// The sweep owes us the payload: 200k values of ~106 B plus 200k keys of
	// ~13 B, so ~24 MB. It cannot owe us more. Go maps never shrink — delete
	// frees the key and value bytes, but the bucket arrays of BOTH data and
	// expiring stay sized for the all-time peak. Asserting on a fraction of
	// afterWrite instead would track that residue, which grows whenever entry
	// does: adding lastUsed (40 B → 48 B) moved it from 16.5 MB to 25.2 MB.
	const minReclaimedMB = 20
	if reclaimed < minReclaimedMB {
		t.Fatalf("sweep returned only %.1f MB of the ~24 MB payload; corpses still reachable?", reclaimed)
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

// Cache.closeOnce ensures that the second c.Close() does not close a closed
// channel and cause a runtime panic.
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
