package cluster

import (
	"sort"

	"github.com/AayushAlokGit/self-healing-distributed-cache/ring"
)

// State is the god's-eye snapshot the dashboard renders. Positions are in degrees
// [0, 360) on the hash ring, so the frontend can place nodes and keys directly.
type State struct {
	Nodes           []NodeState `json:"nodes"`
	Keys            []KeyState  `json:"keys"`
	VNodes          []VNode     `json:"vnodes"` // virtual points, for the tick layer
	RF              int         `json:"rf"`
	AliveCount      int         `json:"aliveCount"`
	TotalHealCopies int64       `json:"totalHealCopies"`
	Events          []Event     `json:"events"`
}

// NodeState is one physical node's status and ring position.
type NodeState struct {
	ID         string  `json:"id"`
	Alive      bool    `json:"alive"`  // ground truth: is it running?
	Paused     bool    `json:"paused"` // health stalled (false-positive injection)
	Angle      float64 `json:"angle"`
	KeyCount   int     `json:"keyCount"`
	HealCopies int64   `json:"healCopies"`
}

// KeyState is one key: where the ring says it belongs vs. where it actually lives.
type KeyState struct {
	Key             string   `json:"key"`
	Angle           float64  `json:"angle"`
	Owners          []string `json:"owners"`  // intended, from the alive ring
	Holders         []string `json:"holders"` // actual, from node caches
	UnderReplicated bool     `json:"underReplicated"`
}

// VNode is one virtual point on the ring, colored by its physical node.
type VNode struct {
	Angle float64 `json:"angle"`
	Node  string  `json:"node"`
}

// State assembles the snapshot: ground-truth liveness from the manager, intended
// ownership from a ring of the alive nodes, and actual placement by asking each
// live node what it holds. The difference between Owners and Holders is exactly
// the heal in flight.
func (c *Cluster) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()

	aliveIDs := make([]string, 0, len(c.nodes))
	for id := range c.nodes {
		aliveIDs = append(aliveIDs, id)
	}
	sort.Strings(aliveIDs)

	// Authoritative ring of the currently-alive nodes: where keys *should* live.
	// Same virtual-point count the nodes use, so the owners shown here match what
	// the nodes actually compute for routing and heal.
	r := ring.NewWithReplicas(c.ringReplicas)
	for _, id := range aliveIDs {
		r.Add(id)
	}

	// Actual placement: union of what live nodes hold.
	holdersByKey := map[string][]string{}
	var totalHeal int64
	for _, id := range aliveIDs {
		n := c.nodes[id]
		totalHeal += n.HealCopies()
		for _, k := range n.HeldKeys() {
			holdersByKey[k] = append(holdersByKey[k], id)
		}
	}

	// Non-nil empty slices, not nil: a nil slice marshals to JSON null, and the
	// frontend does keys.filter(...) / vnodes.map(...) — null crashes it. This
	// matters exactly when everything is dead (no keys, no ring points).
	st := State{
		RF:              c.rf,
		AliveCount:      len(aliveIDs),
		TotalHealCopies: totalHeal,
		Nodes:           []NodeState{},
		Keys:            []KeyState{},
		VNodes:          []VNode{},
		Events:          append([]Event{}, c.events...),
	}

	// Nodes: every known id, alive or not. A dead node keeps its angle so it stays
	// put on the ring (greyed out) instead of vanishing.
	for _, id := range c.ids {
		n, alive := c.nodes[id]
		ns := NodeState{
			ID:    id,
			Alive: alive,
			Angle: angleOf(ring.Hash(id)),
		}
		if alive {
			ns.Paused = n.HealthPaused()
			ns.HealCopies = n.HealCopies()
			ns.KeyCount = len(n.HeldKeys())
		}
		st.Nodes = append(st.Nodes, ns)
	}

	// Keys: sorted for a stable render order.
	keys := make([]string, 0, len(holdersByKey))
	for k := range holdersByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	wantCopies := min(c.rf, len(aliveIDs))
	for _, k := range keys {
		holders := holdersByKey[k]
		sort.Strings(holders)
		st.Keys = append(st.Keys, KeyState{
			Key:             k,
			Angle:           angleOf(ring.Hash(k)),
			Owners:          r.GetClockwiseN(k, c.rf),
			Holders:         holders,
			UnderReplicated: len(holders) < wantCopies,
		})
	}

	// Virtual points, colored by node, so a viewer can see the scattered arcs that
	// make load balanced. Only for alive nodes (the ring routes to those).
	for _, p := range r.Points() {
		st.VNodes = append(st.VNodes, VNode{Angle: angleOf(p.Hash), Node: p.Node})
	}

	return st
}
