package cache

import (
	"testing"
	"time"
)

// Set treats any ttl <= 0 as "never expires".
const noTTL = 0

func TestSetGet(t *testing.T) {
	c := New(noLimit)
	defer c.Close()
	c.Set("user:1", "Aayush", noTTL)

	got, ok := c.Get("user:1")
	if !ok {
		t.Fatalf("expected key to be present, got ok=false")
	}
	if got != "Aayush" {
		t.Fatalf("expected value %q, got %q", "Aayush", got)
	}
}

func TestMiss(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	_, ok := c.Get("nope")
	if ok {
		t.Fatalf("expected ok=false for absent key, got ok=true")
	}
}

// A key deliberately storing "" must be a hit, so callers can tell an empty value
// from a missing key.
func TestEmptyValueIsAHit(t *testing.T) {
	c := New(noLimit)
	defer c.Close()
	c.Set("greeting", "", noTTL)

	got, ok := c.Get("greeting")
	if !ok {
		t.Fatalf("stored empty value should be a hit (ok=true), got ok=false")
	}
	if got != "" {
		t.Fatalf("expected empty value, got %q", got)
	}
}

func TestTTLExpires(t *testing.T) {
	c := New(noLimit)
	defer c.Close()
	c.Set("session:abc", "aayush", 500*time.Millisecond)

	if _, ok := c.Get("session:abc"); !ok {
		t.Fatalf("key should be live before its deadline")
	}

	time.Sleep(800 * time.Millisecond)

	if _, ok := c.Get("session:abc"); ok {
		t.Fatalf("key should be a miss after its deadline")
	}
}

// Guards the ttl <= 0 sentinel: without it, a zero ttl would compute expires = now
// and the key would die on its own birth.
func TestZeroTTLNeverExpires(t *testing.T) {
	c := New(noLimit)
	defer c.Close()
	c.Set("permanent", "v", noTTL)

	time.Sleep(200 * time.Millisecond)

	if _, ok := c.Get("permanent"); !ok {
		t.Fatalf("ttl<=0 should mean the key never expires")
	}
}

// Both halves are asserted: the old deadline must stop applying and the new one must
// start. Checking only the first would pass even if overwrite made the key permanent.
func TestOverwriteResetsDeadline(t *testing.T) {
	c := New(noLimit)
	defer c.Close()
	c.Set("k", "a", 300*time.Millisecond)
	c.Set("k", "b", 2*time.Second)

	time.Sleep(600 * time.Millisecond) // past the FIRST deadline, not the second

	got, ok := c.Get("k")
	if !ok {
		t.Fatalf("overwrite should have reset the deadline; key died early")
	}
	if got != "b" {
		t.Fatalf("expected the overwritten value %q, got %q", "b", got)
	}

	time.Sleep(2 * time.Second) // now past the SECOND deadline too

	if _, ok := c.Get("k"); ok {
		t.Fatalf("key should be a miss after the overwrite's own deadline")
	}
}

// Delete-on-read is invisible through Get — an expired key reads as a miss whether
// or not it was removed — so this reaches into c.data directly.
func TestExpiredKeyIsDeletedOnRead(t *testing.T) {
	c := New(noLimit)
	defer c.Close()
	c.Set("tmp", "v", 100*time.Millisecond)

	time.Sleep(300 * time.Millisecond)

	c.Get("tmp") // the read that should evict it

	c.mu.Lock()
	_, stillThere := c.data["tmp"]
	c.mu.Unlock()

	if stillThere {
		t.Fatalf("Get should delete an entry it finds expired")
	}
}

// The reclaim log records WHY the memory went, and a live key evicted for capacity is
// not an expiry — logging it as one would tell a dashboard that a key a client can still
// read is dead.
func TestReclaimLogRecordsExpiriesOnlyAndWhy(t *testing.T) {
	c := newWithSweepInterval(noLimit, time.Hour) // no background sweeper; we drive it
	defer c.Close()

	c.Set("lazy", "v", time.Millisecond)
	c.Set("swept", "v", time.Millisecond)
	time.Sleep(10 * time.Millisecond) // both are corpses now

	c.Get("lazy") // the read that reclaims it
	c.sweepAll()  // and the sweeper takes the other

	got := map[string]string{}
	for _, r := range c.DrainReclaimed() {
		got[r.Key] = r.Reason
	}
	if got["lazy"] != ReclaimLazy {
		t.Errorf("a key reclaimed by a Get should say why: got %q want %q", got["lazy"], ReclaimLazy)
	}
	if got["swept"] != ReclaimSweep {
		t.Errorf("a key reclaimed by the sweeper should say why: got %q want %q", got["swept"], ReclaimSweep)
	}

	// Drained means drained: a second call must not re-report the same reclamations, or a
	// dashboard polling twice a second would show the same key dying forever.
	if again := c.DrainReclaimed(); len(again) != 0 {
		t.Errorf("DrainReclaimed must clear what it returns, second drain got %+v", again)
	}
}

// An LRU eviction of a LIVE key is not an expiry and must never reach the reclaim log.
func TestEvictingALiveKeyIsNotReclaimed(t *testing.T) {
	c := newWithSweepInterval(2, time.Hour)
	defer c.Close()

	// Permanent keys, so there is no corpse to prefer: the third Set must evict a live one.
	c.Set("a", "v", noTTL)
	c.Set("b", "v", noTTL)
	c.Set("c", "v", noTTL) // evicts "a" from the LRU tail

	if c.Len() != 2 {
		t.Fatalf("capacity 2 should hold 2 entries, holds %d", c.Len())
	}
	if got := c.DrainReclaimed(); len(got) != 0 {
		t.Fatalf("evicting a live key for capacity is not an expiry and must not be logged "+
			"as one — a client could still have read it. Got %+v", got)
	}
}
