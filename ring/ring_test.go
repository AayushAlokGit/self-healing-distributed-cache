package ring

import (
	"math"
	"sort"
	"strconv"
	"testing"
)

func TestEmptyRingReturnsNothing(t *testing.T) {
	if got := New().Get("anything"); got != "" {
		t.Fatalf("empty ring should own nothing, got %q", got)
	}
}

// Placement must be deterministic: the writer and a later reader compute the
// same node, or the read misses data that is really there.
func TestPlacementIsStable(t *testing.T) {
	r := New()
	for i := range 5 {
		r.Add("node" + strconv.Itoa(i))
	}
	first := r.Get("user:42")
	for range 100 {
		if got := r.Get("user:42"); got != first {
			t.Fatalf("same key mapped two ways: %q then %q", first, got)
		}
	}
}

// A single node owns the whole ring, wraparound included: every key, wherever it
// hashes, walks clockwise to the only point there is.
func TestSingleNodeOwnsEverything(t *testing.T) {
	r := New()
	r.Add("solo")
	for i := range 1000 {
		if got := r.Get("k" + strconv.Itoa(i)); got != "solo" {
			t.Fatalf("one-node ring gave key %d to %q", i, got)
		}
	}
}

func distribution(r *Ring, keys int) map[string]int {
	counts := make(map[string]int)
	for i := range keys {
		counts[r.Get("key:"+strconv.Itoa(i))]++
	}
	return counts
}

// spread reports the imbalance: the busiest node's share of a perfectly even
// load. 1.0 is ideal; 3.0 means the busiest node holds three times its share.
func spread(counts map[string]int, nodes, keys int) (float64, float64) {
	ideal := float64(keys) / float64(nodes)
	max, min := 0.0, math.MaxFloat64
	for _, c := range counts {
		f := float64(c)
		if f > max {
			max = f
		}
		if f < min {
			min = f
		}
	}
	return max / ideal, min / ideal
}

// The naive ring's remaining flaw, isolated. With a good hash the node points no
// longer cluster, but ten single points still cut the circle into uneven arcs,
// so load is lumpy even with no failures. Recorded run, 10 nodes, 100k keys:
//
//	busiest node held 3.2x its fair share, quietest 0.16x  -> 20x span
//
// Ideal is 10,000 keys each. No hash fixes this; step 2 (virtual nodes) does,
// by giving each node many points so the arc sizes average out.
func TestNaiveRingIsLumpy(t *testing.T) {
	const (
		nodes = 10
		keys  = 100_000
	)

	r := New()
	for i := range nodes {
		r.Add("node" + strconv.Itoa(i))
	}

	counts := distribution(r, keys)

	var sorted []int
	for _, c := range counts {
		sorted = append(sorted, c)
	}
	sort.Ints(sorted)

	hi, lo := spread(counts, nodes, keys)
	t.Logf("%d keys over %d nodes, ideal %d each", keys, nodes, keys/nodes)
	t.Logf("per-node counts: %v", sorted)
	t.Logf("busiest %.1fx fair share, quietest %.1fx  (%.1fx span)", hi, lo, hi/lo)
}

// The property that justifies the whole construction: removing one node moves
// only that node's keys (~1/N), where hash%N would move ~(N-1)/N of everything.
//
//	removing 1 of 10 nodes moved 9.7% of keys; hash%N would move ~90%
func TestRemovingANodeMovesOnlyItsKeys(t *testing.T) {
	const (
		nodes = 10
		keys  = 100_000
	)

	r := New()
	for i := range nodes {
		r.Add("node" + strconv.Itoa(i))
	}

	before := make([]string, keys)
	for i := range keys {
		before[i] = r.Get("key:" + strconv.Itoa(i))
	}

	r.Remove("node3")

	moved := 0
	for i := range keys {
		if r.Get("key:"+strconv.Itoa(i)) != before[i] {
			moved++
		}
	}
	fraction := float64(moved) / float64(keys)

	t.Logf("removing 1 of %d nodes moved %.1f%% of keys; hash%%N would move ~%.0f%%",
		nodes, fraction*100, float64(nodes-1)/float64(nodes)*100)

	// Only node3's keys should move — a bit under 1/N. Assert it stayed well
	// below what hash%N does, which is the entire point.
	if fraction > 2.0/float64(nodes) {
		t.Fatalf("moved %.1f%% of keys, far more than one node's ~1/N share", fraction*100)
	}
}
