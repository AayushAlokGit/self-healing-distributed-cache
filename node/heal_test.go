package node

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

// setHealGracePeriod sets the heal grace period on every node.
func setHealGracePeriod(nodes map[string]*Node, d time.Duration) {
	for _, n := range nodes {
		n.SetHealGracePeriod(d)
	}
}

// logHealCopies prints a per-node breakdown of pushed copies, so a storm shows
// which nodes did the needless work, not just the total.
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

// totalHealCopies sums the key copies every node has pushed during heals.
func totalHealCopies(nodes map[string]*Node) int64 {
	var total int64
	for _, n := range nodes {
		total += n.HealCopies()
	}
	return total
}

// waitHealSettled waits until the cluster's heal-copy count stops climbing, so a
// storm is measured in full rather than sampled mid-flight. Terminates because a
// stable membership fires no new heal triggers.
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

// The storm, with NO grace period (the naive heal). PauseHealth makes a
// fully-alive node look dead; its peers each heal the instant they convict it and
// copy keys around to "restore" a replication that was never lost. Nothing died,
// yet the cluster copies hundreds of keys — Q6's "gigabytes copied for nothing"
// made countable. TestGracePeriodPreventsHealStorm is the same scenario fixed.
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

// The fix. Same false positive, but with a grace period the expensive
// re-replication waits and rechecks: n1 recovers inside the window, nothing is
// left suspected dead, and the heal is skipped entirely. The storm that cost
// 200+ copies now costs zero — the cheap re-route still happened instantly, only
// the expensive copy was withheld until the death was confirmed.
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

	// The same false positive as the storm test: n1 is alive but looks silent.
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
	t.Logf("grace period (%v) absorbed the false positive: 0 copies, versus 200+ with no grace", grace)
	logHealCopies(t, nodes, "grace")
}
