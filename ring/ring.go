// Package ring places keys on nodes with consistent hashing: adding or removing a
// node moves ~1/N of the keys, not ~(N-1)/N as hash%N does. Each node contributes
// many virtual points, which keeps the load balanced.
package ring

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"
)

// Virtual points per physical node. One point per node cuts the ring into lumpy arcs
// (measured: 65x span over 10 nodes) and dumps a dead node's whole load on one
// neighbour. ~150 points each fixes both; 100-200 is the usual range.
const defaultReplicas = 150

// hashKey maps a string onto the 32-bit ring: SHA-256, truncated to 4 bytes.
//
// Must be stable across processes, so not Go's map hash (per-process randomized). Not
// FNV-1a either: its weak avalanche puts similar names (node0..node9) in a tight
// cluster, and one node ends up owning 96% of the ring.
func hashKey(s string) uint32 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint32(sum[:4])
}

// point is one node's position on the ring.
type point struct {
	hash uint32
	node string
}

// Ring holds node points sorted by hash, replicas points per physical node.
// Not safe for concurrent use: callers must serialize membership changes.
type Ring struct {
	replicas int
	points   []point
}

// New builds a ring with the default virtual-point count.
func New() *Ring {
	return NewWithReplicas(defaultReplicas)
}

// NewWithReplicas builds a ring with a given virtual-point count per node.
func NewWithReplicas(replicas int) *Ring {
	return &Ring{replicas: replicas}
}

// Add places node on the ring as replicas scattered points. The "#i" suffix gives each
// a distinct hash. Re-sorts every call; membership changes are not the hot path.
func (r *Ring) Add(node string) {
	for i := range r.replicas {
		h := hashKey(node + "#" + strconv.Itoa(i))
		r.points = append(r.points, point{hash: h, node: node})
	}
	sort.Slice(r.points, func(i, j int) bool {
		return r.points[i].hash < r.points[j].hash
	})
}

// Remove takes node off the ring. Filters in place; survivors keep their order, so the
// slice stays sorted.
func (r *Ring) Remove(node string) {
	kept := r.points[:0]
	for _, p := range r.points {
		if p.node != node {
			kept = append(kept, p)
		}
	}
	r.points = kept
}

// Point is a node's position on the ring, exported for visualization.
type Point struct {
	Hash uint32
	Node string
}

// Points returns the ring's virtual points in hash order, for the dashboard to draw.
func (r *Ring) Points() []Point {
	out := make([]Point, len(r.points))
	for i, p := range r.points {
		out[i] = Point{Hash: p.hash, Node: p.node}
	}
	return out
}

// Hash exposes the ring's key→position mapping, so a visualizer can place a key at the
// same angle the ring routes it by. The ring space is [0, 2^32).
func Hash(s string) uint32 { return hashKey(s) }

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

// GetClockwiseN returns up to n distinct physical nodes for key: the primary (== Get)
// plus the next n-1 distinct nodes clockwise. Fewer than n only when the ring holds
// fewer than n nodes.
//
// Distinct *physical* nodes: the next points clockwise are often virtual nodes of the
// same machine, and replicas that share a machine die together.
func (r *Ring) GetClockwiseN(key string, n int) []string {
	if n <= 0 || len(r.points) == 0 {
		return nil
	}

	h := hashKey(key)
	start := sort.Search(len(r.points), func(i int) bool {
		return r.points[i].hash >= h
	})

	owners := make([]string, 0, n)
	seen := make(map[string]bool, n)
	// At most one full lap: bounded even when n exceeds the node count.
	for step := 0; step < len(r.points) && len(owners) < n; step++ {
		node := r.points[(start+step)%len(r.points)].node
		if !seen[node] {
			seen[node] = true
			owners = append(owners, node)
		}
	}
	return owners
}
