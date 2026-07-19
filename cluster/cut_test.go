package cluster

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// onlyOnSide reports whether every owner a read touched is in the allowed set — the signal
// that a side has convicted the far side and shrunk its ring to its own members. Before the
// cut is detected, a read via a side-A node still lists side-B owners (as unreachable); once
// the ring shrinks, the trace names only the near side. This is the observable the test polls
// on instead of sleeping a fixed failure-timeout.
func onlyOnSide(res ReadResult, side map[string]bool) bool {
	if len(res.Path) == 0 {
		return false
	}
	for _, h := range res.Path {
		if !side[h.Node] {
			return false
		}
	}
	return true
}

// The money moment, end to end: a cut lets the SAME key be written on both sides, both writes
// succeed because each side serves alone, and on the mend the two survive as concurrent
// siblings — proving the cut produced genuinely concurrent versions the version-aware heal
// preserved rather than one side clobbering the other.
//
// Why the two writes are concurrent and not a race (CAP.md §4): under the cut, n0's coordinator
// can see only side A and n4's can see only side B, so each merges a different owner set and
// bumps a different slot — neither vector dominates. There is no "later" one to find.
func TestCutProducesConcurrentSiblingsBothKeptOnHeal(t *testing.T) {
	// Short grace so the post-mend heal fires quickly; failure timeout is the node's fixed
	// 500ms, which is what detection (and the ring shrink) waits on either way.
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	sideA := map[string]bool{"n0": true, "n1": true, "n2": true}
	sideB := map[string]bool{"n3": true, "n4": true}

	// (1) One agreed write before the cut, so both sides start from the same value/version.
	const key = "user:1"
	if err := c.Set(key, "alice", 0, ""); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if res, err := c.Get(key, ""); err != nil || res.Value != "alice" {
		t.Fatalf("precondition: %q should read back alice, got (%+v, %v)", key, res, err)
	}

	// (2) Cut into A = {n0,n1,n2} | B = {n3,n4}.
	if err := c.Cut([]string{"n0", "n1", "n2"}, []string{"n3", "n4"}); err != nil {
		t.Fatalf("cut: %v", err)
	}

	// (3) Wait for BOTH sides to convict the other and re-own the keyspace among themselves,
	// so each side's write lands on a near-side owner (W=1) instead of failing to reach the
	// far owners. Polling the read trace, not sleeping.
	waitUntil(t, 3*time.Second, "side A shrinks its ring to its own members", func() bool {
		res, err := c.Get(key, "n0")
		return err == nil && onlyOnSide(res, sideA)
	})
	waitUntil(t, 3*time.Second, "side B shrinks its ring to its own members", func() bool {
		res, err := c.Get(key, "n4")
		return err == nil && onlyOnSide(res, sideB)
	})

	// Drive one write through each side. Both MUST succeed — the point of AP under a cut.
	if err := c.Set(key, "bob", 0, "n0"); err != nil {
		t.Fatalf("write via side A (n0) must succeed under a cut, got: %v", err)
	}
	if err := c.Set(key, "carol", 0, "n4"); err != nil {
		t.Fatalf("write via side B (n4) must succeed under a cut, got: %v", err)
	}

	// (4) Mend, and let the heal reconcile.
	c.Mend()

	// (5) Both values must survive as concurrent siblings: neither vector dominates, so the
	// version-aware heal kept both, and a read returns them as a conflict.
	waitUntil(t, 10*time.Second, "both bob and carol survive the heal as concurrent siblings", func() bool {
		res, err := c.Get(key, "")
		if err != nil || !res.Conflict {
			return false
		}
		return slices.Contains(res.Siblings, "bob") && slices.Contains(res.Siblings, "carol")
	})

	res, err := c.Get(key, "")
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !res.Conflict {
		t.Fatalf("expected a conflict after the cut, got a single value %q — a write was clobbered", res.Value)
	}
	if !slices.Contains(res.Siblings, "bob") || !slices.Contains(res.Siblings, "carol") {
		t.Fatalf("both concurrent writes must survive; siblings = %v, want both bob and carol", res.Siblings)
	}
	// "alice" was dominated by "bob" (n0 merged it, then bumped), so it must NOT survive: the
	// heal keeps concurrent versions, not stale ones.
	if slices.Contains(res.Siblings, "alice") {
		t.Errorf("the pre-cut value alice was dominated by bob and should be gone, got siblings %v", res.Siblings)
	}
	t.Logf("cut produced concurrent siblings %v, both kept across the heal", res.Siblings)
}

// A cut must name only live nodes: a killed or unknown id is refused with *NoSuchNodeError, the
// same error via uses, so the control API can map it to a 400 rather than half-applying a cut.
func TestCutRejectsDeadOrUnknownNodes(t *testing.T) {
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Kill("n2"); err != nil {
		t.Fatalf("kill: %v", err)
	}

	var nse *NoSuchNodeError
	if err := c.Cut([]string{"n0", "n2"}, []string{"n3", "n4"}); !errors.As(err, &nse) || nse.ID != "n2" {
		t.Errorf("cut naming the killed n2 should be *NoSuchNodeError{n2}, got %v", err)
	}
	if err := c.Cut([]string{"n0", "n1"}, []string{"n3", "n9"}); !errors.As(err, &nse) || nse.ID != "n9" {
		t.Errorf("cut naming the unknown n9 should be *NoSuchNodeError{n9}, got %v", err)
	}

	// A node named on both sides is a contradiction, refused before anything is applied.
	if err := c.Cut([]string{"n0", "n1"}, []string{"n1", "n3"}); err == nil {
		t.Errorf("cut with n1 on both sides should error, got nil")
	}
}
