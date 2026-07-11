package node

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

func setHealGracePeriod(nodes map[string]*Node, d time.Duration) {
	for _, n := range nodes {
		n.SetHealGracePeriod(d)
	}
}

// logHealCopies prints a per-node breakdown, so a storm shows which nodes did the
// needless work, not just the total.
func logHealCopies(t *testing.T, nodes map[string]*Node, label string) {
	t.Helper()
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var total int64
	for _, id := range ids {
		c := nodes[id].HealCopies()
		total += c
		t.Logf("  [%s] %s pushed %d copies", label, id, c)
	}
	t.Logf("  [%s] total %d copies", label, total)
}

func totalHealCopies(nodes map[string]*Node) int64 {
	var total int64
	for _, n := range nodes {
		total += n.HealCopies()
	}
	return total
}

// waitHealSettled waits until the cluster's heal-copy count stops climbing, so a storm
// is measured in full rather than sampled mid-flight.
func waitHealSettled(t *testing.T, nodes map[string]*Node) int64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	prev := int64(-1)
	for {
		cur := totalHealCopies(nodes)
		if cur == prev {
			return cur
		}
		if time.Now().After(deadline) {
			t.Fatalf("heal-copy count never settled (last %d)", cur)
		}
		prev = cur
		time.Sleep(150 * time.Millisecond)
	}
}

// holds reports whether a node physically has the key in its own cache, bypassing all
// routing.
func holds(n *Node, key string) bool {
	_, ok := n.cache.Get(key)
	return ok
}

// Killing one of a key's three owners leaves it under-replicated. The heal restores R:
// the death promotes a new owner, and a holder copies the key onto it, with no client
// involved.
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

	// Killing the primary promotes a new primary and creates a newcomer owner that has
	// never seen the key: the copy the heal must produce.
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

	// The heal has run once the newcomer physically holds the key, with no client read.
	waitUntil(t, 3*time.Second, "the promoted owner receives its copy via self-heal", func() bool {
		return holds(nodes[newcomer], key)
	})
	t.Logf("self-heal: killed owner %s; newcomer %s received %q in %v (no client involved)",
		killed, newcomer, key, time.Since(killedAt).Round(10*time.Millisecond))

	// R restored: every current owner holds a live copy again.
	for _, id := range newOwners {
		if !holds(nodes[id], key) {
			t.Fatalf("after heal, owner %s should hold %q — range is still under-replicated", id, key)
		}
	}
	t.Logf("R=%d restored: %q lives on %v again, two live copies healed back to three", rf, key, newOwners)
}

// With no grace period, a false positive costs a storm: peers convict a fully-alive
// node the instant they see silence, and the promoted newcomers get copies of keys that
// were never lost. TestGracePeriodPreventsHealStorm is the same scenario fixed.
func TestFalsePositiveTriggersHealStorm(t *testing.T) {
	const rf = 3
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, rf, 1)
	setHealGracePeriod(nodes, 0) // naive: heal the instant a death is detected

	const keys = 100
	for i := range keys {
		clientSet(t, nodes["n0"], "k:"+strconv.Itoa(i), "v"+strconv.Itoa(i))
	}

	// No death has happened, so the heal has never run.
	if c := totalHealCopies(nodes); c != 0 {
		t.Fatalf("baseline: no death yet, want 0 heal copies, got %d", c)
	}

	// n1 is alive and well; it just stops answering /health in time.
	nodes["n1"].PauseHealth(true)
	waitUntil(t, 3*time.Second, "peers falsely declare the alive n1 dead", func() bool {
		return !nodes["n0"].AlivePeers()["n1"] && !nodes["n2"].AlivePeers()["n1"]
	})

	storm := waitHealSettled(t, nodes)
	if storm == 0 {
		t.Fatalf("expected a heal storm from the false positive, got 0 copies")
	}
	t.Logf("false positive, NO grace period: n1 never died, yet the cluster copied %d keys", storm)
	logHealCopies(t, nodes, "storm")
}

// The fix: with a grace period the expensive re-replication waits and rechecks, n1
// recovers inside the window, and the storm costs zero copies. The cheap re-route still
// happened instantly; only the copying was withheld until the death was confirmed.
func TestGracePeriodPreventsHealStorm(t *testing.T) {
	const rf = 3
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, rf, 1)
	const grace = 2 * time.Second
	setHealGracePeriod(nodes, grace)

	const keys = 100
	for i := range keys {
		clientSet(t, nodes["n0"], "k:"+strconv.Itoa(i), "v"+strconv.Itoa(i))
	}

	// The same false positive as the storm test.
	nodes["n1"].PauseHealth(true)
	waitUntil(t, 3*time.Second, "peers falsely declare the alive n1 dead", func() bool {
		return !nodes["n0"].AlivePeers()["n1"] && !nodes["n2"].AlivePeers()["n1"]
	})

	// Recover well within the grace window, before any copy is committed.
	nodes["n1"].PauseHealth(false)
	waitUntil(t, 3*time.Second, "peers re-admit the recovered n1", func() bool {
		return nodes["n0"].AlivePeers()["n1"] && nodes["n2"].AlivePeers()["n1"]
	})

	// Wait past the grace period: the pending heal rechecks, finds nothing dead,
	// and skips.
	time.Sleep(grace + 500*time.Millisecond)
	if c := totalHealCopies(nodes); c != 0 {
		t.Fatalf("grace period should have prevented the storm, but %d copies were made", c)
	}
	t.Logf("grace period (%v) absorbed the false positive: 0 copies, versus dozens with no grace", grace)
	logHealCopies(t, nodes, "grace")
}
