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
//	per-node [378 5609 6757 7652 8848 10841 11310 11647 12499 24459]
//	busiest 2.4x fair share, quietest 0.04x  -> 65x span
//
// Ideal is 10,000 keys each. No hash fixes this; virtual nodes do, by giving
// each node many points so the arc sizes average out.
func TestNaiveRingIsLumpy(t *testing.T) {
	const (
		nodes = 10
		keys  = 100_000
	)

	r := NewWithReplicas(1) // the naive ring: one point per node
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
	t.Logf("busiest %.2fx fair share, quietest %.2fx  (%.1fx span)", hi, lo, hi/lo)
}

// The payoff: each physical node owns many small arcs, so its total load is the
// sum of many random pieces and concentrates around the average. The span
// collapses as replicas rise. Recorded run, 10 nodes, 100k keys:
//
//	  1 replica    busiest 2.45x  quietest 0.04x   64.7x span
//	 10 replicas   busiest 1.75x  quietest 0.46x    3.8x span
//	 50 replicas   busiest 1.24x  quietest 0.83x    1.5x span
//	150 replicas   busiest 1.23x  quietest 0.85x    1.4x span
//
// The excess over fair share shrinks ~1/sqrt(replicas) (the standard error of a
// mean) in the steep part, then plateaus: 50 -> 150 barely moves (1.24 -> 1.23),
// because at 1500 points the residual is the finite-keyspace sampling, not arc
// variance. So ~50 points already gets nearly all the benefit for 10 nodes;
// more just costs storage. 150 leaves headroom for larger clusters.
func TestVirtualNodesFlattenLoad(t *testing.T) {
	const (
		nodes = 10
		keys  = 100_000
	)

	var last float64
	for _, replicas := range []int{1, 10, 50, 150} {
		r := NewWithReplicas(replicas)
		for i := range nodes {
			r.Add("node" + strconv.Itoa(i))
		}

		hi, lo := spread(distribution(r, keys), nodes, keys)
		t.Logf("%3d replicas   busiest %.2fx  quietest %.2fx   %.1fx span", replicas, hi, lo, hi/lo)
		last = hi
	}

	// 150 replicas should hold the busiest node near its fair share.
	if last > 1.25 {
		t.Fatalf("virtual nodes failed to balance: busiest still %.2fx fair share", last)
	}
}

// The second reason for virtual nodes (Q8): a naive node's whole arc dumps onto
// its one clockwise neighbour, risking a cascade. With many scattered points,
// the dead node's keys spread across (almost) every survivor. Recorded run:
//
//	naive:  node3's keys landed on 1 survivor,  which took 100% of them
//	vnodes: node3's keys landed on 9 survivors, busiest took 19%
//
// Hitting all N-1 is not guaranteed — the ceiling is min(replicas, N-1) and a
// survivor is missed with probability ~(1-1/(N-1))^replicas — so the test only
// asserts a wide spread, not the exact count.
func TestFailureSpreadsLoadAcrossSurvivors(t *testing.T) {
	const (
		nodes = 10
		keys  = 100_000
	)

	measure := func(replicas int) (survivors int, busiestShare float64) {
		r := NewWithReplicas(replicas)
		for i := range nodes {
			r.Add("node" + strconv.Itoa(i))
		}
		before := make([]string, keys)
		for i := range keys {
			before[i] = r.Get("key:" + strconv.Itoa(i))
		}

		r.Remove("node3")

		absorbed := map[string]int{}
		moved := 0
		for i := range keys {
			if before[i] == "node3" {
				absorbed[r.Get("key:"+strconv.Itoa(i))]++
				moved++
			}
		}
		busiest := 0
		for _, c := range absorbed {
			if c > busiest {
				busiest = c
			}
		}
		return len(absorbed), float64(busiest) / float64(moved)
	}

	n1, share1 := measure(1)
	nV, shareV := measure(defaultReplicas)
	t.Logf("naive:  node3's keys landed on %d survivor(s), busiest took %.0f%%", n1, share1*100)
	t.Logf("vnodes: node3's keys landed on %d survivor(s), busiest took %.0f%%", nV, shareV*100)

	if n1 != 1 {
		t.Fatalf("naive ring should dump a dead node's whole arc on one neighbour, hit %d", n1)
	}
	if nV < nodes/2 {
		t.Fatalf("vnodes should spread across a majority of the %d survivors, hit only %d", nodes-1, nV)
	}
	if shareV > 0.25 {
		t.Fatalf("one survivor absorbed %.0f%% of the dead node's load", shareV*100)
	}
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
