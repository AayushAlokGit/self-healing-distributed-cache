package cluster

import (
	"testing"
	"time"
)

// waitUntil polls f until true or the deadline, so the test waits on an event
// whose timing is only bounded, not exact.
func waitUntil(t *testing.T, within time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for: %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// keyState finds a key in a state snapshot.
func keyState(st State, key string) (KeyState, bool) {
	for _, k := range st.Keys {
		if k.Key == key {
			return k, true
		}
	}
	return KeyState{}, false
}

// The whole demo loop through the manager: seed keys, snapshot god's-eye state,
// kill an owner, watch the key drop to under-replicated then heal back to R, all
// while reads keep serving.
func TestClusterDemoFlow(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Every seeded key should live on R=3 holders.
	st := c.State()
	if len(st.Keys) != 12 {
		t.Fatalf("want 12 keys in state, got %d", len(st.Keys))
	}
	var victimKey string
	for _, k := range st.Keys {
		if len(k.Holders) != 3 {
			t.Fatalf("key %q should have 3 holders, got %v", k.Key, k.Holders)
		}
		if victimKey == "" {
			victimKey = k.Key
		}
	}

	// Read a key back through the cluster.
	if v, ok, err := c.Get(victimKey); err != nil || !ok {
		t.Fatalf("get %q before kill: (%q, %v, %v)", victimKey, v, ok, err)
	}

	// Kill the primary owner of the victim key.
	ks, _ := keyState(st, victimKey)
	primary := ks.Owners[0]
	if err := c.Kill(primary); err != nil {
		t.Fatalf("kill %s: %v", primary, err)
	}

	// Reads keep serving immediately (fallback), even while under-replicated.
	if v, ok, err := c.Get(victimKey); err != nil || !ok {
		t.Fatalf("get %q right after kill should still serve via fallback: (%q, %v, %v)", victimKey, v, ok, err)
	}

	// The heal restores R: the key returns to 3 holders, and none is the dead node.
	waitUntil(t, 4*time.Second, "victim key heals back to 3 holders", func() bool {
		st := c.State()
		k, ok := keyState(st, victimKey)
		if !ok {
			return false
		}
		for _, h := range k.Holders {
			if h == primary {
				return false // dead node must not be counted a holder
			}
		}
		return len(k.Holders) == 3
	})
	t.Logf("heal restored R=3 for %q after killing its primary %s", victimKey, primary)

	// Total heal copies climbed — the manager saw the re-replication happen.
	if st := c.State(); st.TotalHealCopies == 0 {
		t.Fatalf("expected heal copies after a kill, got 0")
	}
	if st := c.State(); st.AliveCount != 4 {
		t.Fatalf("want 4 alive nodes after one kill, got %d", st.AliveCount)
	}
}

// nodeKeyCount reads a node's key count from a snapshot.
func nodeKeyCount(st State, id string) int {
	for _, n := range st.Nodes {
		if n.ID == id {
			return n.KeyCount
		}
	}
	return -1
}

// A revived node comes back empty, but the check-first heal repopulates it: once
// it rejoins the ring as an owner of some ranges, the primaries of those ranges
// notice it is missing the keys and push them over. Without this, a returned node
// stays empty until new writes happen to land on it.
func TestRevivedNodeRepopulates(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(15); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const victim = "n2"
	if kc := nodeKeyCount(c.State(), victim); kc <= 0 {
		t.Fatalf("precondition: %s should hold keys, got %d", victim, kc)
	}

	if err := c.Kill(victim); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// Let the death heal settle so its keys live on the survivors.
	time.Sleep(2 * time.Second)

	if err := c.Revive(victim); err != nil {
		t.Fatalf("revive: %v", err)
	}
	// It returns empty…
	if kc := nodeKeyCount(c.State(), victim); kc != 0 {
		t.Fatalf("a revived node should return empty, got %d keys", kc)
	}
	// …then the heal repopulates it as peers notice it owns keys it lacks.
	waitUntil(t, 6*time.Second, "revived node repopulates via the check-first heal", func() bool {
		return nodeKeyCount(c.State(), victim) > 0
	})
	t.Logf("revived %s repopulated to %d keys with no client writes", victim, nodeKeyCount(c.State(), victim))
}

// A false positive with a grace period costs no heal copies: pause a node's
// health, let peers suspect it, resume before the grace period, and confirm the
// cluster did not re-replicate.
func TestClusterGraceAbsorbsFalsePositive(t *testing.T) {
	const grace = 2 * time.Second
	c := New(3, 1, grace, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.Pause("n1", true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitUntil(t, 3*time.Second, "n1 shows paused in state", func() bool {
		for _, n := range c.State().Nodes {
			if n.ID == "n1" {
				return n.Paused
			}
		}
		return false
	})

	// Resume within the grace window.
	if err := c.Pause("n1", false); err != nil {
		t.Fatalf("resume: %v", err)
	}

	time.Sleep(grace + 700*time.Millisecond)
	if st := c.State(); st.TotalHealCopies != 0 {
		t.Fatalf("grace period should have absorbed the false positive, got %d heal copies", st.TotalHealCopies)
	}
	t.Logf("grace period absorbed the false positive: 0 heal copies")
}
