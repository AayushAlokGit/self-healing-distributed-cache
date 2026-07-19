package node

import (
	"net/http"
	"strings"
	"sync"
)

// A network partition is the one fault that does not belong to any node. A kill stops a
// node; a pause stalls its /health; but a partition is a fact about a PAIR — "can n0 reach
// n3?" — and the honest place for it is UNDER the HTTP clients, where a node cannot tell a
// cut peer from a dead one (CAP.md §1–2). No handler ever learns the cut exists.
//
// The mechanism is a per-node gate that both http.Clients share: the data client and the
// health client route through the same blocker, so a cut drops data AND heartbeats together
// — which is what makes each side convict the other and shrink its ring (CAP.md §2). A
// blocked request fails immediately, indistinguishable from an unreachable node, never a
// hang: pingHealth reads the error as a missed beat, fetchFrom/storeOn as an unreachable
// owner.

// blocker is a node's set of peer addresses the network currently refuses to deliver to.
// Empty means every peer is reachable. Guarded by a mutex because it is READ on every dial,
// from the heartbeat and forwarding goroutines, while the manager WRITES it on a cut/mend —
// exactly the read-mostly, occasionally-written pattern RWMutex is for, and the reason -race
// must stay green.
type blocker struct {
	mu      sync.RWMutex
	blocked map[string]bool // peer address ("127.0.0.1:53187") -> refused
}

// block installs the set of blocked addresses, replacing any previous set. A copy is taken:
// the caller's slice must not alias the gate's map, or a later mutation would silently
// re-route live traffic.
func (b *blocker) block(addrs []string) {
	next := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		next[a] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blocked = next
}

// clear mends the cut: every peer is reachable again.
func (b *blocker) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blocked = nil
}

// isBlocked reports whether addr is currently on the wrong side of a cut from this node.
func (b *blocker) isBlocked(addr string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.blocked[addr]
}

// blockingTransport is the gate itself: a RoundTripper that refuses a request to a blocked
// address before it touches the network, and otherwise delegates to base. Gating at the
// RoundTrip level rather than at DialContext is deliberate — it catches a request that would
// otherwise reuse a keep-alive connection pooled BEFORE the cut, so the partition takes
// effect on the very next request, not whenever the idle connection happens to expire. A
// real cut drops in-flight traffic too; this is the closest a localhost demo gets.
type blockingTransport struct {
	blocker *blocker
	base    http.RoundTripper
}

func (t *blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// req.URL.Host is exactly the peer address the caller dialed ("127.0.0.1:53187"), the
	// same string held in n.peers and in the block set — so the match is direct.
	if t.blocker.isBlocked(req.URL.Host) {
		return nil, &partitionedError{addr: req.URL.Host}
	}
	return t.base.RoundTrip(req)
}

// partitionedError is what a blocked request fails with. A distinct type, not a bare string,
// so a reader of the logs can tell "the cut ate this" apart from a genuine connection
// refusal — though to the calling code they are correctly identical (both just "unreachable").
type partitionedError struct{ addr string }

func (e *partitionedError) Error() string {
	return "peer " + e.addr + " is unreachable: network partition (fault injected)"
}

// newGatedTransport wraps a fresh clone of the default transport behind b. Each client gets
// its own base (own connection pool), but they share the one blocker, so a single cut moves
// both.
func newGatedTransport(b *blocker) http.RoundTripper {
	return &blockingTransport{blocker: b, base: http.DefaultTransport.(*http.Transport).Clone()}
}

// SetBlockedPeers installs the set of peer addresses this node's network can no longer reach
// — for data and health alike, since both HTTP clients share one gate. A nil or empty slice
// mends the cut. Blocking is by ADDRESS, which nodes already know for every peer; the manager
// (Cluster.Cut) hands each side the opposite side's addresses. Safe to call while serving.
//
// ⚠️ This blocks only this node's OUTGOING dials. A two-way cut is still symmetric because
// the OTHER side blocks its outgoing dials to this one: A refuses to reach B and B refuses to
// reach A, so every A<->B pair is dead in both directions. See Cluster.Cut.
func (n *Node) SetBlockedPeers(addrs []string) {
	if len(addrs) == 0 {
		n.blocker.clear()
		n.logger().Info("network cut cleared: all peers reachable again")
		return
	}
	n.blocker.block(addrs)
	n.logger().Warn("network cut (fault injected): peers now unreachable",
		"blocked", strings.Join(addrs, ","))
}

// assert blockingTransport satisfies the interface at compile time — a wrong signature here
// would otherwise fail far away, at the http.Client that silently falls back to the default.
var _ http.RoundTripper = (*blockingTransport)(nil)
