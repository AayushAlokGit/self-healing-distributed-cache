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
	VNodes          []VNode     `json:"vnodes"` // virtual points of the alive ring, for the tick layer
	RF              int         `json:"rf"`
	W               int         `json:"w"`     // write quorum; with RRead, the consistency dial
	RRead           int         `json:"rRead"` // read quorum. W+RRead>RF ⇒ no stale reads
	AliveCount      int         `json:"aliveCount"`
	TotalHealCopies int64       `json:"totalHealCopies"`
	Events          []Event     `json:"events"` // kills, writes AND heals, in order

	// Partition is the active network cut, nil when the network is whole. Reported so a
	// reload keeps the banner and the ring can split into the two rings the sides actually
	// hold. Only the manager sees this — it injected the cut (docs/HLD §9).
	Partition *Partition `json:"partition,omitempty"`
}

// Partition is one active cut: the two sides Cut was given, plus each side's ring points.
// A side convicts the far side and rings only what it can reach, so VNodesA and VNodesB
// disagree about who owns which arc — that disagreement is the whole CAP story, drawn.
type Partition struct {
	SideA   []string `json:"sideA"`
	SideB   []string `json:"sideB"`
	VNodesA []VNode  `json:"vnodesA"` // side A's ring: alive nodes minus side B
	VNodesB []VNode  `json:"vnodesB"` // side B's ring: alive nodes minus side A
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

	// OwnersA/OwnersB are the key's owners as each side of an active cut sees them: each
	// side rings only its own reachable nodes, so a key can have a different owner per side
	// (side A convicted side B, so it re-owns B's arcs, and vice versa). Nil (omitted) when
	// the network is whole — then Owners, from the single alive ring, is authoritative.
	OwnersA []string `json:"ownersA,omitempty"`
	OwnersB []string `json:"ownersB,omitempty"`

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
	r := c.ringOver(aliveIDs)

	// Under an active cut, each side rings only what it can reach: a cut blocks A<->B, so
	// side A's view is every alive node EXCEPT side B (and vice versa). An unassigned bridge
	// node is reachable by both, so it lands in both views. inA/inB are membership sets for
	// the per-side under-replication check below. Computing reach from aliveIDs (not from the
	// stored sides) stays correct even if a node is killed after the cut — it just drops out.
	var partition *Partition
	var ringA, ringB *ring.Ring
	var inA, inB map[string]bool
	if c.cutA != nil {
		blockedFromA := setOf(c.cutB) // A cannot reach B
		blockedFromB := setOf(c.cutA)
		reachA := make([]string, 0, len(aliveIDs))
		reachB := make([]string, 0, len(aliveIDs))
		inA, inB = map[string]bool{}, map[string]bool{}
		for _, id := range aliveIDs {
			if !blockedFromA[id] {
				reachA = append(reachA, id)
				inA[id] = true
			}
			if !blockedFromB[id] {
				reachB = append(reachB, id)
				inB[id] = true
			}
		}
		ringA, ringB = c.ringOver(reachA), c.ringOver(reachB)
		partition = &Partition{
			SideA:   append([]string{}, c.cutA...),
			SideB:   append([]string{}, c.cutB...),
			VNodesA: vnodesOf(ringA),
			VNodesB: vnodesOf(ringB),
		}
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
		W:               c.wq,
		RRead:           c.rrq,
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

		ks := KeyState{
			Key:     k,
			Angle:   angleOf(ring.Hash(k)),
			Owners:  r.GetClockwiseN(k, c.rf),
			Holders: holders,
			TTLMs:   ttlMs,
		}

		if partition != nil {
			// Each side sees its own owners, and "fully replicated" is judged per side: a key
			// written on side A can only reach side A's nodes, so its target is min(rf, |side A|),
			// not min(rf, |all alive|). Judging it globally would paint a key that is complete for
			// its side as under-replicated the whole time the cut is up. A bridge holder is
			// reachable by both, so it counts toward both sides.
			ks.OwnersA = ringA.GetClockwiseN(k, c.rf)
			ks.OwnersB = ringB.GetClockwiseN(k, c.rf)
			ha, hb := 0, 0
			for _, h := range holders {
				if inA[h] {
					ha++
				}
				if inB[h] {
					hb++
				}
			}
			urA := ha > 0 && ha < min(c.rf, len(inA))
			urB := hb > 0 && hb < min(c.rf, len(inB))
			ks.UnderReplicated = urA || urB
		} else {
			ks.UnderReplicated = len(holders) < wantCopies
		}

		st.Keys = append(st.Keys, ks)
	}

	// Virtual points of the alive ring — the ring routes to those. Under a cut the frontend
	// draws Partition.VNodesA/B instead; this stays the whole-network ring for the normal case.
	st.VNodes = vnodesOf(r)
	st.Partition = partition

	return st
}

// ringOver builds a ring of the given ids at the demo virtual-point count. Every ring the
// dashboard reasons about — the whole-network one and each side of a cut — must use the same
// count, or two rings would disagree about ownership for a reason that is not the partition.
func (c *Cluster) ringOver(ids []string) *ring.Ring {
	r := ring.NewWithReplicas(c.ringReplicas)
	for _, id := range ids {
		r.Add(id)
	}
	return r
}

// vnodesOf turns a ring's virtual points into the dashboard's angle-tagged form. Non-nil
// even for an empty ring, so the JSON is [] not null and the frontend's .map is safe.
func vnodesOf(r *ring.Ring) []VNode {
	pts := r.Points()
	out := make([]VNode, 0, len(pts))
	for _, p := range pts {
		out = append(out, VNode{Angle: angleOf(p.Hash), Node: p.Node})
	}
	return out
}

// setOf is a membership set of ids.
func setOf(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
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
