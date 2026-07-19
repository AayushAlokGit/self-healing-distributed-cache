package cluster

import (
	"testing"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/ring"
)

// SetQuorum validates against rf (fixed at New): 1<=w<=rf and 1<=rRead<=rf, so W>R is refused.
func TestSetQuorumValidation(t *testing.T) {
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	for _, p := range [][2]int{{0, 2}, {4, 2}, {2, 0}, {2, 4}, {-1, 1}} {
		if err := c.SetQuorum(p[0], p[1]); err == nil {
			t.Errorf("SetQuorum(%d,%d) is out of [1,3] and should error", p[0], p[1])
		}
	}
	for _, p := range [][2]int{{1, 1}, {2, 2}, {3, 3}, {1, 3}, {3, 1}} {
		if err := c.SetQuorum(p[0], p[1]); err != nil {
			t.Errorf("SetQuorum(%d,%d) should be valid, got %v", p[0], p[1], err)
		}
	}
}

// ownersAndRest computes a key's rf owners on the demo ring (the points the cluster routes by),
// and the ids that are not owners, so a test can split the owners across a cut a known way.
func ownersAndRest(key string, rf int, ids ...string) (owners, rest []string) {
	r := ring.NewWithReplicas(demoRingReplicas)
	for _, id := range ids {
		r.Add(id)
	}
	owners = r.GetClockwiseN(key, rf)
	own := map[string]bool{}
	for _, o := range owners {
		own[o] = true
	}
	for _, id := range ids {
		if !own[id] {
			rest = append(rest, id)
		}
	}
	return owners, rest
}

// The dial's payoff: at W=2 with W+R_read>R the ring is HELD, so under a cut the side that can
// reach fewer than W of a key's owners REFUSES rather than re-owning the keyspace among its own
// survivors. We split key k's three owners 2|1 across the cut: the 2-owner side can ack W=2, the
// 1-owner side cannot and refuses — the checkerboard, and the CP end of the dial.
func TestHeldRingRefusesLosingSideOfCut(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	c := New(3, 1, 200*time.Millisecond, ids...)
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	const key = "cart"
	owners, rest := ownersAndRest(key, 3, ids...)
	if len(owners) != 3 || len(rest) != 2 {
		t.Fatalf("expected 3 owners and 2 non-owners, got owners=%v rest=%v", owners, rest)
	}
	// Side A holds 2 of the owners, side B holds 1 — so B is the losing side for key at W=2.
	sideA := []string{owners[0], owners[1], rest[0]}
	sideB := []string{owners[2], rest[1]}

	if err := c.SetQuorum(2, 2); err != nil { // W+R_read=4 > R=3 → ring held
		t.Fatalf("set quorum: %v", err)
	}
	if err := c.Cut(sideA, sideB); err != nil {
		t.Fatalf("cut: %v", err)
	}

	// The gate blocks cross-side dials immediately, and the ring is held, so ownersFor still names
	// all three owners. A write via the 1-owner side reaches only that owner (1 < W=2) → refused.
	if err := c.Set(key, "fromB", 0, owners[2]); err == nil {
		t.Errorf("write via the losing side (%s, reaches 1 of 2 needed owners) must be refused, got nil", owners[2])
	}
	// A write via the 2-owner side reaches both → W=2 met → accepted.
	if err := c.Set(key, "fromA", 0, owners[0]); err != nil {
		t.Errorf("write via the winning side (%s, reaches 2 owners) must succeed, got %v", owners[0], err)
	}
}

// The contrast that proves holdRing is load-bearing: at W=2, R_read=1 (sum 3, NOT > 3), the ring
// is NOT held. Under the same cut the losing side convicts the far owners, shrinks its ring, and
// re-owns key among its OWN nodes — so W=2 is met by an invented owner set and the write it should
// have refused now succeeds. That rubber stamp is exactly what the held ring prevents above.
func TestUnheldRingLetsLosingSideServeAfterShrink(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	c := New(3, 1, 200*time.Millisecond, ids...)
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	const key = "cart"
	owners, rest := ownersAndRest(key, 3, ids...)
	if len(owners) != 3 || len(rest) != 2 {
		t.Fatalf("expected 3 owners and 2 non-owners, got owners=%v rest=%v", owners, rest)
	}
	sideA := []string{owners[0], owners[1], rest[0]}
	sideB := []string{owners[2], rest[1]} // 2 nodes: enough to satisfy W=2 among themselves once re-owned

	if err := c.Set(key, "seed", 0, ""); err != nil { // seed while whole so a read has a path to poll
		t.Fatalf("seed: %v", err)
	}
	if err := c.SetQuorum(2, 1); err != nil { // W+R_read=3, NOT > R=3 → ring NOT held
		t.Fatalf("set quorum: %v", err)
	}
	if err := c.Cut(sideA, sideB); err != nil {
		t.Fatalf("cut: %v", err)
	}

	// Wait for side B to convict side A and shrink its ring to its own two nodes (the trace via a
	// side-B node then names only side-B owners). Polling, not sleeping.
	sideBset := map[string]bool{owners[2]: true, rest[1]: true}
	waitUntil(t, 3*time.Second, "side B shrinks its ring to its own members", func() bool {
		res, err := c.Get(key, owners[2])
		return err == nil && onlyOnSide(res, sideBset)
	})

	// Now side B has re-owned key among its two nodes, so a write via side B reaches 2 owners and
	// W=2 is satisfied — the write the held ring refused above now succeeds. The rubber stamp.
	if err := c.Set(key, "fromB", 0, owners[2]); err != nil {
		t.Errorf("with the ring unheld, the losing side re-owns key and should serve W=2, got %v", err)
	}
}
