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
	// A remainder, not a deadline: an absolute instant would be read against the
	// browser's clock, and any skew would look like a cache bug.
	TTLMs int64 `json:"ttlMs"`
}

// VNode is one virtual point on the ring, colored by its physical node.
type VNode struct {
	Angle float64 `json:"angle"`
	Node  string  `json:"node"`
}

// State assembles the snapshot: liveness from the manager, intended ownership from a
// ring of the alive nodes, and actual placement by asking each live node what it holds.
// The difference between Owners and Holders is the heal in flight.
func (c *Cluster) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()

	aliveIDs := make([]string, 0, len(c.nodes))
	for id := range c.nodes {
		aliveIDs = append(aliveIDs, id)
	}
	sort.Strings(aliveIDs)

	// Where keys *should* live. Must use the same virtual-point count as the nodes, or
	// the owners shown here would not match the ones the nodes route and heal by.
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
			// Replicas should all carry the same deadline. Take the latest anyway: if they
			// ever drifted, a reader would still see the key while any copy holds it.
			if e.Expires.After(expiresByKey[k]) {
				expiresByKey[k] = e.Expires
			}
		}
		// File this node's heals into the SAME log as the kills. Only the node knows what
		// it copied and why, and appending now (after the kill's append) makes the list
		// itself causal, with no ordering logic anywhere.
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

	// Non-nil empty slices: a nil slice marshals to JSON null, and the frontend's
	// keys.filter(...) / vnodes.map(...) crash on null (i.e. when everything is dead).
	st := State{
		RF:              c.rf,
		AliveCount:      len(aliveIDs),
		TotalHealCopies: totalHeal,
		Nodes:           []NodeState{},
		Keys:            []KeyState{},
		VNodes:          []VNode{},
		Events:          append([]Event{}, c.events...),
	}

	// Every known id, alive or not: a dead node keeps its angle so it stays put on the
	// ring instead of vanishing.
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

		ttlMs := int64(-1)
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

	// Virtual points, alive nodes only — the ring routes to those.
	for _, p := range r.Points() {
		st.VNodes = append(st.VNodes, VNode{Angle: angleOf(p.Hash), Node: p.Node})
	}

	return st
}
