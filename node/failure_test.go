package node

import (
	"testing"
	"time"
)

// waitUntil polls f until it returns true or the deadline passes, so tests never
// hard-code a sleep for an event whose timing is only bounded, not exact.
func waitUntil(t *testing.T, within time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for: %s", within, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The heartbeat detects a death: after a node is killed, its peers drop it from
// their alive view (and, in lockstep, from their ring) within ~failureTimeout.
func TestHeartbeatDetectsDeath(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)

	// SetMembership seeds everyone alive; confirm before killing.
	if !nodes["n0"].AlivePeers()["n1"] {
		t.Fatalf("n1 should start alive in n0's view")
	}

	killed := time.Now()
	nodes["n1"].Close()

	waitUntil(t, 3*time.Second, "n0 detects n1 dead", func() bool {
		return !nodes["n0"].AlivePeers()["n1"]
	})
	t.Logf("n0 detected n1 dead in %v (interval %v, timeout %v)",
		time.Since(killed).Round(10*time.Millisecond), defaultHeartbeatInterval, defaultFailureTimeout)

	// A death must not demote the living.
	view := nodes["n0"].AlivePeers()
	if !view["n0"] || !view["n2"] {
		t.Fatalf("n0 wrongly demoted a live node: %v", view)
	}

	// n2 should reach the same conclusion independently — no shared authority.
	waitUntil(t, 3*time.Second, "n2 detects n1 dead", func() bool {
		return !nodes["n2"].AlivePeers()["n1"]
	})
}

// Once a node is detected dead it leaves the ring, so ownership recomputes and a
// key it used to own routes to a live node with no failed hop. At R=1 this is
// visible as the owner id changing.
func TestDeadNodeLeavesTheRing(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, 1, 1)

	// Find a key owned by n1, then kill n1.
	var key string
	for i := 0; ; i++ {
		k := "k:" + time.Now().Format("150405.000") + "-" + string(rune('a'+i%26))
		if ownersOf(ids, k, 1)[0] == "n1" {
			key = k
			break
		}
	}
	survivor := nodes["n0"]
	if survivor.ownersFor(key)[0].id != "n1" {
		t.Fatalf("precondition: n0 should route %q to n1", key)
	}

	nodes["n1"].Close()
	waitUntil(t, 3*time.Second, "n0 reroutes key off n1", func() bool {
		owners := survivor.ownersFor(key)
		return len(owners) == 1 && owners[0].id != "n1"
	})
	t.Logf("after detection, n0 routes %q to %s instead of the dead n1", key, survivor.ownersFor(key)[0].id)
}
