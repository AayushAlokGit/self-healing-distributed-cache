package node

import (
	"slices"
	"testing"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/vclock"
)

// strayHolder finds a node holding a key it does NOT own, which is the only situation
// cleanup acts on. It plants the key straight into that node's cache — the same state a
// heal leaves behind when it copies a key to a node that later stops being an owner.
func strayHolder(t *testing.T, nodes map[string]*Node, ids []string, key string, rf int) (*Node, []string) {
	t.Helper()
	owners := ownersOf(ids, key, rf)
	for _, id := range ids {
		if !slices.Contains(owners, id) {
			nodes[id].cache.SetAt(key, "v", time.Time{})
			return nodes[id], owners
		}
	}
	t.Fatalf("every node owns %q at rf=%d — no stray holder possible", key, rf)
	return nil, nil
}

// The whole safety argument for cleanup, exercised directly: a copy is dropped only when
// EVERY owner confirms it holds the key. Anything else — an owner that says no, an owner
// that cannot be reached — and the copy stays.
//
// The point is that a surplus copy and the last copy alive look identical from here. The
// only thing that tells them apart is asking, and the only safe order is ask-then-drop.
// Reverse it and this stops being a memory optimisation and becomes data loss.
func TestCleanupDropsOnlyWhatEveryOwnerConfirms(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	const rf, key = 3, "key:7"

	t.Run("keeps it when no owner has it", func(t *testing.T) {
		nodes := startCluster(t, ids...)
		setReplication(nodes, rf, 1)
		stray, owners := strayHolder(t, nodes, ids, key, rf)

		stray.cleanup()

		if !holds(stray, key) {
			t.Fatalf("%s dropped the ONLY copy of %q: it owns none of %v, but neither does anybody else",
				stray.ID(), key, owners)
		}
		if got := stray.CleanupDropped(); got != 0 {
			t.Errorf("nothing was confirmed anywhere, so nothing may be dropped; dropped %d", got)
		}
	})

	t.Run("keeps it when only some owners have it", func(t *testing.T) {
		nodes := startCluster(t, ids...)
		setReplication(nodes, rf, 1)
		stray, owners := strayHolder(t, nodes, ids, key, rf)

		// All but the last owner. A majority is not enough: R=3 means three copies, and
		// dropping here would leave two.
		for _, id := range owners[:len(owners)-1] {
			nodes[id].cache.SetAt(key, "v", time.Time{})
		}

		stray.cleanup()

		if !holds(stray, key) {
			t.Fatalf("%s dropped its copy while owner %s did not have %q — that is a copy below R, "+
				"and cleanup cannot tell it apart from data loss", stray.ID(), owners[len(owners)-1], key)
		}
	})

	t.Run("keeps it when an owner cannot be reached", func(t *testing.T) {
		nodes := startCluster(t, ids...)
		setReplication(nodes, rf, 1)
		stray, owners := strayHolder(t, nodes, ids, key, rf)

		for _, id := range owners {
			nodes[id].cache.SetAt(key, "v", time.Time{})
		}
		// Every owner genuinely has the key — but this one cannot be asked. Unreachable is
		// not the same fact as "does not have it", and cleanup must not treat silence as
		// consent. Point the address at a closed port rather than killing the node, so the
		// ring is untouched and the test does not race the failure detector.
		stray.SetPeerAddr(owners[0], "127.0.0.1:1")

		stray.cleanup()

		if !holds(stray, key) {
			t.Fatalf("%s dropped its copy of %q on the word of the owners it COULD reach — "+
				"an unreachable owner has confirmed nothing", stray.ID(), key)
		}
	})

	t.Run("drops it once every owner confirms", func(t *testing.T) {
		nodes := startCluster(t, ids...)
		setReplication(nodes, rf, 1)
		stray, owners := strayHolder(t, nodes, ids, key, rf)

		for _, id := range owners {
			nodes[id].cache.SetAt(key, "v", time.Time{})
		}

		stray.cleanup()

		if holds(stray, key) {
			t.Fatalf("all %d owners %v hold %q, so %s's copy is surplus and must go", len(owners), owners, key, stray.ID())
		}
		if got := stray.CleanupDropped(); got != 1 {
			t.Errorf("want 1 dropped copy, got %d", got)
		}
		// And the owners still have it: cleanup drops the surplus, never the real thing.
		for _, id := range owners {
			if !holds(nodes[id], key) {
				t.Errorf("owner %s lost %q to a cleanup that was supposed to only drop a non-owner's copy", id, key)
			}
		}
		// It must be reported, or the dashboard cannot show where the copies went.
		if log := stray.DrainCleanupLog(); len(log) != 1 || !slices.Contains(log[0], key) {
			t.Errorf("cleanup must record the keys it dropped, got %v", log)
		}
	})

	t.Run("keeps a concurrent sibling no owner holds", func(t *testing.T) {
		// The presence≠version hole, closed. Every owner holds bob; the stray holds carol, a
		// concurrent sibling NONE of them has. A has-the-key check would drop carol — every
		// owner "has the key" — losing an acked write. Version-awareness keeps it: no owner
		// covers carol, so the copy stays and the heal is re-armed to propagate it.
		nodes := startCluster(t, ids...)
		setReplication(nodes, rf, 1)
		stray, owners := strayHolder(t, nodes, ids, key, rf)

		var empty vclock.Clock
		bob := empty.Bump("n0")   // {n0:1}
		carol := empty.Bump("n1") // {n1:1} — concurrent with bob

		for _, id := range owners {
			nodes[id].cache.SetVersioned(key, "bob", bob, time.Time{})
		}
		stray.cache.SetVersioned(key, "carol", carol, time.Time{})

		stray.cleanup()

		if !holds(stray, key) {
			t.Fatalf("%s dropped carol, a sibling NO owner holds — a lost acked write, not a surplus copy", stray.ID())
		}
		if got := stray.CleanupDropped(); got != 0 {
			t.Errorf("no owner holds carol, so nothing may be dropped; dropped %d", got)
		}
	})

	t.Run("never drops a key it owns", func(t *testing.T) {
		nodes := startCluster(t, ids...)
		setReplication(nodes, rf, 1)
		owners := ownersOf(ids, key, rf)

		for _, id := range owners {
			nodes[id].cache.SetAt(key, "v", time.Time{})
		}
		// Every owner runs cleanup at once. If ownership were not checked first, each would
		// see "the others all have it" and they would delete the key out of existence
		// between them.
		for _, id := range owners {
			nodes[id].cleanup()
		}
		for _, id := range owners {
			if !holds(nodes[id], key) {
				t.Fatalf("owner %s dropped %q — the owners cleaned each other up and the key is gone", id, key)
			}
		}
	})
}
