package cluster

import (
	"fmt"
	"sort"
	"time"

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
	Events          []Event     `json:"events"` // kills, writes AND heals, in order
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

	// TTLMs is the key's remaining life in milliseconds; -1 means it never expires.
	//
	// Remaining time, not the deadline itself, and computed here rather than in the
	// browser on purpose: an absolute instant would have to be read against the
	// *browser's* clock, which is not the server's. A countdown that renders "expires
	// in 4s" on one laptop and "expired 20s ago" on another would be blamed on the
	// cache rather than on the clock. Shipping the remainder makes the skew irrelevant
	// — the dashboard re-polls several times a second, so it just re-reads it.
	TTLMs int64 `json:"ttlMs"`
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
	now := time.Now()
	holdersByKey := map[string][]string{}
	expiresByKey := map[string]time.Time{} // zero value = never expires
	var totalHeal int64
	for _, id := range aliveIDs {
		n := c.nodes[id]
		totalHeal += n.HealCopies()
		for k, e := range n.HeldEntries() {
			holdersByKey[k] = append(holdersByKey[k], id)
			// Every replica of a key should carry the same deadline — that is what
			// shipping an absolute instant buys. Take the latest of them anyway: if a
			// bug ever did let them drift, the dashboard would show the key living as
			// long as its longest-lived copy, which is what a reader would actually
			// observe (the read falls back to whichever replica still has it).
			if e.Expires.After(expiresByKey[k]) {
				expiresByKey[k] = e.Expires
			}
		}
		// Collect what this node re-replicated since the last poll and file it in the
		// SAME activity log as the kills. The node is the only one that knows what it
		// copied, and only IT can say why (its own heartbeat saw the peer go silent) —
		// the manager just appends. Because the append happens now, and the kill's
		// append happened earlier, the heal lands after its cause with no ordering
		// logic anywhere: the list IS the causality.
		for _, hc := range n.DrainHealLog() {
			c.appendEvent(Event{
				Kind:  "heal",
				Msg:   fmt.Sprintf("%s → %s: re-replicated %d key%s", id, hc.To, len(hc.Keys), plural(len(hc.Keys))),
				From:  id,
				To:    hc.To,
				Keys:  hc.Keys,
				Cause: hc.Cause,
			})
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

		ttlMs := int64(-1) // -1: no deadline at all
		if exp := expiresByKey[k]; !exp.IsZero() {
			ttlMs = max(exp.Sub(now).Milliseconds(), 0)
		}

		st.Keys = append(st.Keys, KeyState{
			Key:             k,
			Angle:           angleOf(ring.Hash(k)),
			Owners:          r.GetClockwiseN(k, c.rf),
			Holders:         holders,
			UnderReplicated: len(holders) < wantCopies,
			TTLMs:           ttlMs,
		})
	}

	// Virtual points, colored by node, so a viewer can see the scattered arcs that
	// make load balanced. Only for alive nodes (the ring routes to those).
	for _, p := range r.Points() {
		st.VNodes = append(st.VNodes, VNode{Angle: angleOf(p.Hash), Node: p.Node})
	}

	return st
}
