package node

import (
	"testing"
	"time"
)

// holds reports whether a node physically has the key in its own cache, bypassing
// all routing — the direct question "is there a copy here?" that a heal must make
// true again on the promoted owner.
func holds(n *Node, key string) bool {
	_, ok := n.cache.Get(key)
	return ok
}

// The other half of the money moment. At R=3 a key lives on three owners; killing
// one drops it to two live copies — the range is under-replicated, one death from
// loss. Self-heal restores R: the death promotes a new owner, and the range's
// primary copies the key onto it, back to three copies, with no client involved.
func TestHealRestoresReplicationAfterDeath(t *testing.T) {
	const rf = 3
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, rf, 1)

	const key, value = "user:42", "alice"
	clientSet(t, nodes["n0"], key, value)

	oldOwners := ownersOf(ids, key, rf)
	for _, id := range oldOwners {
		if !holds(nodes[id], key) {
			t.Fatalf("precondition: owner %s should hold %q", id, key)
		}
	}

	// Killing the primary both promotes a new primary and creates a newcomer that
	// has never seen the key — the copy the heal must produce.
	killed := oldOwners[0]
	var remaining []string
	for _, id := range ids {
		if id != killed {
			remaining = append(remaining, id)
		}
	}
	newOwners := ownersOf(remaining, key, rf)

	oldSet := map[string]bool{}
	for _, id := range oldOwners {
		oldSet[id] = true
	}
	var newcomer string
	for _, id := range newOwners {
		if !oldSet[id] {
			newcomer = id
			break
		}
	}
	if newcomer == "" {
		t.Fatalf("expected a newcomer owner after killing %s; old=%v new=%v", killed, oldOwners, newOwners)
	}
	if holds(nodes[newcomer], key) {
		t.Fatalf("baseline: newcomer %s should NOT hold %q before the heal", newcomer, key)
	}

	killedAt := time.Now()
	nodes[killed].Close()

	// The heal has run once the newcomer physically holds the key: the new primary
	// detected the death, recomputed owners, and pushed the copy — no client read.
	waitUntil(t, 3*time.Second, "the promoted owner receives its copy via self-heal", func() bool {
		return holds(nodes[newcomer], key)
	})
	t.Logf("self-heal: killed owner %s; newcomer %s received %q in %v (no client involved)",
		killed, newcomer, key, time.Since(killedAt).Round(10*time.Millisecond))

	// R restored: all three current owners hold a live copy again.
	for _, id := range newOwners {
		if !holds(nodes[id], key) {
			t.Fatalf("after heal, owner %s should hold %q — range is still under-replicated", id, key)
		}
	}
	t.Logf("R=%d restored: %q lives on %v again, two live copies healed back to three", rf, key, newOwners)
}
