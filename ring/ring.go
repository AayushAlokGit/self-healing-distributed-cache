// Package ring places keys on nodes with consistent hashing.
//
// Phase 2, step 1: the naive ring — one point per node. This fixes hash%N's
// remapping disaster (adding or removing a node moves ~1/N of keys, not ~(N-1)/N)
// but distributes load unevenly, because a handful of random points cut the
// circle into lumpy arcs. Step 2 fixes that with virtual nodes.
package ring

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// hashKey maps a string onto the 32-bit ring.
//
// SHA-256, truncated to its first 4 bytes. Not the built-in map hash, which is
// per-process randomized on purpose — every client must agree on the ring, so
// the hash must be stable across processes. Not FNV-1a either: its avalanche is
// weak, so similar node names (node0..node9) hash to a tight cluster and one
// node ends up owning most of the ring (measured: 96% on one of ten). Every bit
// of a crypto hash is uniformly random, so any 4-byte slice is a uniform point.
// The cost is CPU per lookup; a hand-rolled Murmur3 is the move if it shows up hot.
func hashKey(s string) uint32 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint32(sum[:4])
}

// point is one node's position on the ring.
type point struct {
	hash uint32
	node string
}

// Ring holds node points sorted by hash. Not safe for concurrent use yet;
// membership changes will need a lock once a live cluster mutates it.
type Ring struct {
	points []point
}

func New() *Ring {
	return &Ring{}
}

// Add places node on the ring. Re-sorts every call: fine for a handful of nodes
// added rarely, and membership changes are not the hot path.
func (r *Ring) Add(node string) {
	r.points = append(r.points, point{hash: hashKey(node), node: node})
	sort.Slice(r.points, func(i, j int) bool {
		return r.points[i].hash < r.points[j].hash
	})
}

// Remove takes node off the ring. Filters in place; the survivors keep their
// order, so the slice stays sorted.
func (r *Ring) Remove(node string) {
	kept := r.points[:0]
	for _, p := range r.points {
		if p.node != node {
			kept = append(kept, p)
		}
	}
	r.points = kept
}

// Get returns the node that owns key: the first point clockwise from the key's
// hash, wrapping past the top of the ring. Empty string if the ring has no nodes.
func (r *Ring) Get(key string) string {
	if len(r.points) == 0 {
		return ""
	}

	h := hashKey(key)
	i := sort.Search(len(r.points), func(i int) bool {
		return r.points[i].hash >= h
	})
	if i == len(r.points) {
		i = 0 // past the largest point: wrap to the first
	}
	return r.points[i].node
}
