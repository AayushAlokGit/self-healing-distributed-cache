// Package vclock implements vector clocks: per-key version stamps that tell a
// genuinely newer write apart from two writes that never saw each other.
//
// A Clock is one counter per node that has coordinated a write to the key; a missing
// node is an implicit zero. The write rule is MERGE, THEN BUMP: absorb (element-wise
// max) the clock of every value replaced, then increment your own slot. Bumping alone
// is correct only with a single predecessor and wrong the moment there are two — which
// is exactly resolution, where the merged clock must dominate every sibling.
package vclock

// Clock is a vector clock keyed by node id.
//
// ⚠️ Treat it as immutable — every operation returns a new Clock. A Clock is a map, so
// an Entry copied out of the cache shares this exact map; an in-place bump would rewrite
// the version of the live entry still in the cache. Clone is what prevents that.
type Clock map[string]uint64

func (c Clock) Clone() Clock {
	out := make(Clock, len(c))
	for id, n := range c {
		out[id] = n
	}
	return out
}

// Bump increments id's counter — the "bump your own slot" half of the write rule, on a
// clock that has already merged its predecessors. Clones, so nil is fine (yields {id:1}).
func (c Clock) Bump(id string) Clock {
	out := c.Clone()
	out[id]++
	return out
}

// Merge is the element-wise maximum: a clock that has seen every write either input had.
// The result dominates both, so a resolution stamped Merge(siblings...).Bump(self)
// collapses the conflict instead of letting a loser return.
func Merge(a, b Clock) Clock {
	out := a.Clone()
	for id, n := range b {
		if n > out[id] {
			out[id] = n
		}
	}
	return out
}

// Ordering is how two clocks relate in the happened-before partial order.
type Ordering int

const (
	Equal Ordering = iota
	Before
	After
	Concurrent // neither dominates: the writes never saw each other
)

func (o Ordering) String() string {
	switch o {
	case Equal:
		return "equal"
	case Before:
		return "before"
	case After:
		return "after"
	default:
		return "concurrent"
	}
}

// Compare reports how a relates to b: After when a dominates (a[i] >= b[i] everywhere,
// strictly greater somewhere), Before the mirror, Equal when identical, else Concurrent.
// Missing keys are implicit zeros, so the two loops cover the union of node ids.
func Compare(a, b Clock) Ordering {
	var aGreater, bGreater bool

	for id, av := range a {
		if av > b[id] {
			aGreater = true
		}
	}
	for id, bv := range b {
		if bv > a[id] {
			bGreater = true
		}
	}

	switch {
	case aGreater && bGreater:
		return Concurrent
	case aGreater:
		return After
	case bGreater:
		return Before
	default:
		return Equal
	}
}
