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
