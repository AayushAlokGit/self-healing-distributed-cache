package cache

import "testing"

// TestSetGet verifies a stored key comes back with ok=true.
func TestSetGet(t *testing.T) {
	c := New()
	c.Set("user:1", "Aayush")

	got, ok := c.Get("user:1")
	if !ok {
		t.Fatalf("expected key to be present, got ok=false")
	}
	if got != "Aayush" {
		t.Fatalf("expected value %q, got %q", "Aayush", got)
	}
}

// TestMiss verifies an absent key reports ok=false.
func TestMiss(t *testing.T) {
	c := New()

	_, ok := c.Get("nope")
	if ok {
		t.Fatalf("expected ok=false for absent key, got ok=true")
	}
}

// TestEmptyValueIsAHit is the important one: a key deliberately storing ""
// must report ok=true, so callers can tell "empty value" from "missing key".
func TestEmptyValueIsAHit(t *testing.T) {
	c := New()
	c.Set("greeting", "")

	got, ok := c.Get("greeting")
	if !ok {
		t.Fatalf("stored empty value should be a hit (ok=true), got ok=false")
	}
	if got != "" {
		t.Fatalf("expected empty value, got %q", got)
	}
}
