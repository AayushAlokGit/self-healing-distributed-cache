package cluster

import (
	"fmt"
	"sort"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/node"
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
	reclaimed := map[string][]node.Reclaim{}
	var totalHeal int64
	for _, id := range aliveIDs {
		n := c.nodes[id]
		totalHeal += n.HealCopies()
		if rc := n.DrainReclaimed(); len(rc) > 0 {
			reclaimed[id] = rc
		}
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

		// And its cleanups, in the same list: a cleanup is always the consequence of an
		// earlier heal, and the order shows it.
		for _, keys := range n.DrainCleanupLog() {
			c.appendEvent(Event{
				Kind: "cleanup",
				Msg: fmt.Sprintf("%s dropped %d surplus cop%s — it no longer owns %s, and every owner confirmed holding it",
					id, len(keys), plural2(len(keys), "y", "ies"), plural2(len(keys), "that key", "those keys")),
				From: id,
				Keys: keys,
			})
		}
	}

	c.noteExpiries(now, holdersByKey, expiresByKey)
	c.noteReclamations(aliveIDs, reclaimed)

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

// noteExpiries emits one event per key whose deadline has passed since the last poll.
// Caller holds c.mu.
//
// A key that has left holdersByKey has left it for one of two reasons, and they are
// not the same fact:
//
//   - its deadline passed. Snapshot() hides an entry the instant it expires, so every
//     replica drops it from the snapshot AT THE DEADLINE — together, because SetAt gave
//     them all the same absolute instant. This is an expiry, and it is one event, no
//     matter how many replicas held the key.
//   - every node holding it was killed. The key is simply gone, below the deadline it
//     was promised. That is data loss, and reporting it as an expiry would be a lie —
//     the demo's headline action would spray fake expiry events on every kill.
//
// The deadline is what tells them apart, which is why the previous poll's deadlines
// are kept: at the moment a key vanishes it is too late to ask it when it was due.
func (c *Cluster) noteExpiries(now time.Time, holders map[string][]string, expires map[string]time.Time) {
	gone := make([]string, 0, len(c.deadlines))
	for k := range c.deadlines {
		if _, still := holders[k]; !still {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone) // stable event order; map range is not

	for _, k := range gone {
		deadline := c.deadlines[k]
		delete(c.deadlines, k)
		if deadline.IsZero() || now.Before(deadline) {
			continue // killed out from under us, not expired
		}
		c.appendEvent(Event{
			Kind: "expire",
			Msg:  fmt.Sprintf("%s expired — its deadline passed on every replica at once", k),
			Keys: []string{k},
		})
	}

	for k := range holders {
		c.deadlines[k] = expires[k]
	}
}

// noteReclamations emits one event per node per reason for the expired entries that
// node actually freed. Caller holds c.mu.
//
// This is NOT the expiry — the key was already dead to every reader when its deadline
// passed, whether or not the bytes were still in the map. This is the memory being
// handed back, which happens later, at a different time on every node, and only when
// something goes looking: a Get that lands on the corpse, or the sampler drawing it.
// The lag between the two is not a defect; it is the sampling sweeper working.
//
// Batched per node, like heals: one pass freeing 8 keys is one line, not eight, or the
// log would drown the fault that people are actually here to watch.
//
// It carries no Cause. A heal is caused by a membership change; a reclamation is caused
// by nothing but the clock, and attributing it to the nearest kill would tell a viewer
// that killing a node destroyed their data.
func (c *Cluster) noteReclamations(aliveIDs []string, byNode map[string][]node.Reclaim) {
	for _, id := range aliveIDs {
		rcs := byNode[id]
		if len(rcs) == 0 {
			continue
		}
		byReason := map[string][]string{}
		for _, rc := range rcs {
			byReason[rc.Reason] = append(byReason[rc.Reason], rc.Key)
		}
		for _, reason := range []string{node.ReclaimSweep, node.ReclaimLazy, node.ReclaimEvict} {
			keys := byReason[reason]
			if len(keys) == 0 {
				continue
			}
			sort.Strings(keys)
			c.appendEvent(Event{
				Kind: "reclaim",
				Msg: fmt.Sprintf("%s freed %d expired key%s — %s",
					id, len(keys), plural(len(keys)), reclaimText(reason)),
				From: id,
				Keys: keys,
			})
		}
	}
}

// reclaimText says who did the freeing, in the dashboard's voice.
func reclaimText(reason string) string {
	switch reason {
	case node.ReclaimSweep:
		return "the background sweeper drew them"
	case node.ReclaimLazy:
		return "a read landed on them"
	case node.ReclaimEvict:
		return "the cache was full and preferred a corpse to a live key"
	}
	return reason
}
