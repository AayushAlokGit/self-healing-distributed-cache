package node

import (
	"net/http"
	"testing"
)

// A read repairs a reachable replica that missed a write: the key converges on the READ, not on
// the next membership change. This is the gap read-repair closes — the heal is event-driven, so a
// lagging-but-alive replica (one that missed a write while never declared dead) is invisible to it.
func TestReadRepairsAStaleReplica(t *testing.T) {
	nodes := startCluster(t, "n0", "n1", "n2")
	setReplication(nodes, 3, 1) // rf=3=N: every node owns every key
	const key, val = "k", "v"

	// Make n2 miss the write: block it from the coordinator, write, unblock — fast, so n0 never
	// convicts n2 and its ring keeps all three owners.
	nodes["n0"].SetBlockedPeers([]string{nodes["n2"].Addr()})
	if code := clientSet(t, nodes["n0"], key, val); code != http.StatusNoContent {
		t.Fatalf("write should ack at W=1, got %d", code)
	}
	nodes["n0"].SetBlockedPeers(nil)

	if _, ok := nodes["n2"].cache.GetEntries(key); ok {
		t.Fatal("precondition failed: n2 should have missed the blocked write")
	}

	// A read gathers all three owners, reconciles to {v}, and repairs n2 in-band.
	if body, code := clientGet(t, nodes["n0"], key); code != http.StatusOK || body != val {
		t.Fatalf("read should return %q, got %d %q", val, code, body)
	}

	entries, ok := nodes["n2"].cache.GetEntries(key)
	if !ok || len(entries) != 1 || entries[0].Value != val {
		t.Fatalf("read-repair should have stored %q on n2, got entries=%v ok=%v", val, entries, ok)
	}

	// And it is idempotent: a second read finds every owner already covering {v} and repairs nothing.
	if body, code := clientGet(t, nodes["n0"], key); code != http.StatusOK || body != val {
		t.Fatalf("second read should still return %q, got %d %q", val, code, body)
	}
}
