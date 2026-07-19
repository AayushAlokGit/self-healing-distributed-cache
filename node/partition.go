package node

import (
	"fmt"
	"net/http"
	"sync"
)

// A network partition is a fact about a PAIR — "can n0 reach n3?" — not about a node. So it
// lives UNDER the HTTP clients, where a cut peer is indistinguishable from a dead one and no
// handler learns the cut exists. Both of a node's clients (data and health) send through the
// same gate, so one cut drops data AND heartbeats together — which is what makes each side
// convict the other and shrink its ring. See Cluster.Cut.
type gate struct {
	mu      sync.RWMutex
	blocked map[string]bool // peer address ("127.0.0.1:53187") -> refused; nil = all reachable
	base    http.RoundTripper
}

// RoundTrip refuses a request to a blocked peer before it touches the network, and otherwise
// delegates. Gating here rather than at DialContext also refuses a request that would reuse a
// keep-alive connection pooled before the cut, so the partition bites on the very next request.
func (g *gate) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.RLock()
	refused := g.blocked[req.URL.Host] // req.URL.Host is the peer address the caller dialed
	g.mu.RUnlock()
	if refused {
		return nil, fmt.Errorf("peer %s unreachable: network partition (fault injected)", req.URL.Host)
	}
	return g.base.RoundTrip(req)
}

// SetBlockedPeers cuts this node off from these peer addresses — for data and health alike,
// since both clients share the one gate. Nil or empty mends the cut. The block is one-directional
// (this node's outgoing dials), so a symmetric cut needs the far side to block back. See
// Cluster.Cut. Safe to call while serving: the gate reads under an RLock on every request.
func (n *Node) SetBlockedPeers(addrs []string) {
	next := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		next[a] = true
	}
	n.gate.mu.Lock()
	n.gate.blocked = next
	n.gate.mu.Unlock()
}
