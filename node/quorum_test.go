package node

import (
	"net/http"
	"testing"
)

// The dial's core: enforcement refuses (503) when a quorum cannot be assembled, rather than
// serving without it. These isolate the coordinator from the other owners with SetBlockedPeers
// (the same gate a cut uses) and read/write immediately, before the heartbeat can convict — so
// the ring still names all R owners and the refusal is purely about reachability, not the ring.

// A read refuses when fewer than R_read owners answer. rf=3=N so every node owns every key;
// blocking the two non-coordinator owners leaves the coordinator reaching only itself.
func TestReadRefusedBelowReadQuorum(t *testing.T) {
	nodes := startCluster(t, "n0", "n1", "n2")
	setReplication(nodes, 3, 1) // W=1 seed so the value lands on all three while all are reachable
	coord := nodes["n0"]

	if code := clientSet(t, coord, "k", "v"); code != http.StatusNoContent {
		t.Fatalf("seed write should succeed, got %d", code)
	}

	for _, n := range nodes {
		n.SetReadQuorum(2)
	}
	coord.SetBlockedPeers([]string{nodes["n1"].Addr(), nodes["n2"].Addr()})

	// answered = 1 (the coordinator) < R_read = 2 → refuse, even though the coordinator HOLDS a
	// copy: holding data is not knowing it is current.
	if code, _, _ := rawGet(t, coord, "k"); code != http.StatusServiceUnavailable {
		t.Fatalf("read reaching 1 of 2 required owners should be 503, got %d", code)
	}

	// R_read = 1 is the gather-all default: one reachable answer is enough.
	coord.SetReadQuorum(1)
	if code, _, body := rawGet(t, coord, "k"); code != http.StatusOK || body != "v" {
		t.Fatalf("read at R_read=1 should return the value, got %d %q", code, body)
	}
}

// A write refuses when fewer than W owners ack. Same isolation: the coordinator can only ack
// itself, which is below W=2 but meets W=1.
func TestWriteRefusedBelowWriteQuorum(t *testing.T) {
	nodes := startCluster(t, "n0", "n1", "n2")
	setReplication(nodes, 3, 2) // W=2
	coord := nodes["n0"]

	coord.SetBlockedPeers([]string{nodes["n1"].Addr(), nodes["n2"].Addr()})

	// acks = 1 (local only) < W = 2 → 503, a refusal, not a 502: the request was well-formed
	// and the cluster is up, it just cannot assemble the quorum right now.
	if code := clientSet(t, coord, "k", "v"); code != http.StatusServiceUnavailable {
		t.Fatalf("write reaching 1 of 2 required owners should be 503, got %d", code)
	}

	// W = 1 accepts the same lone ack.
	coord.SetReplication(3, 1)
	if code := clientSet(t, coord, "k", "v"); code != http.StatusNoContent {
		t.Fatalf("write at W=1 should succeed, got %d", code)
	}
}
