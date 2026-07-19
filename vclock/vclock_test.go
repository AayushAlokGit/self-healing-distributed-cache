package vclock

import "testing"

// user:1 = alice (written once via n0); a cut lets bob (via n0) and carol (via n4) diverge.
var (
	alice = Clock{"n0": 1}
	bob   = alice.Bump("n0") // {n0:2}
	carol = alice.Bump("n4") // {n0:1, n4:1}
)

func TestBumpFromNilStartsAtOne(t *testing.T) {
	var empty Clock
	if got := empty.Bump("n0"); got["n0"] != 1 {
		t.Fatalf("first write via n0 = %v, want {n0:1}", got)
	}
}

func TestBumpDoesNotMutateTheReceiver(t *testing.T) {
	// Bump clones, so a discarded result must leave the original untouched. Catches a
	// "simplification" to an in-place out[id]++ that would write through the shared map.
	original := Clock{"n0": 2}
	original.Bump("n0")
	if original["n0"] != 2 {
		t.Fatalf("Bump mutated the receiver: %v", original)
	}
}

func TestConcurrentWritesAcrossACutAreDetected(t *testing.T) {
	if got := Compare(bob, carol); got != Concurrent {
		t.Fatalf("Compare(bob, carol) = %v, want Concurrent", got)
	}
	if got := Compare(carol, bob); got != Concurrent {
		t.Fatalf("Compare is not symmetric for concurrency: %v", got)
	}
}

func TestASequentialWriteDominates(t *testing.T) {
	// dave read bob then wrote via n1, so it has seen everything bob had: a genuine later.
	dave := bob.Bump("n1") // {n0:2, n1:1}
	if got := Compare(dave, bob); got != After {
		t.Fatalf("Compare(dave, bob) = %v, want After", got)
	}
	if got := Compare(bob, dave); got != Before {
		t.Fatalf("Compare(bob, dave) = %v, want Before", got)
	}
}

func TestResolutionMustMergeToDominateEverySibling(t *testing.T) {
	resolved := Merge(bob, carol).Bump("n0") // {n0:3, n4:1}
	if got := Compare(resolved, bob); got != After {
		t.Fatalf("resolved vs bob = %v, want After", got)
	}
	if got := Compare(resolved, carol); got != After {
		t.Fatalf("resolved vs carol = %v, want After", got)
	}
}

func TestBumpOnlyResolutionLeavesTheLoserConcurrent(t *testing.T) {
	// Skipping the merge: bob.Bump("n0") = {n0:3} does not dominate carol (n4: 0 < 1), so
	// carol resurfaces forever. Fails loudly if resolution is ever "simplified" to a Bump.
	buggy := bob.Bump("n0")
	if got := Compare(buggy, carol); got != Concurrent {
		t.Fatalf("bump-only resolution vs carol = %v; the merge is load-bearing, want Concurrent", got)
	}
}

func TestEqualClocksAreEqual(t *testing.T) {
	if got := Compare(Clock{"n0": 2, "n1": 1}, Clock{"n1": 1, "n0": 2}); got != Equal {
		t.Fatalf("identical clocks compared %v, want Equal", got)
	}
	if got := Compare(nil, nil); got != Equal {
		t.Fatalf("two empty clocks compared %v, want Equal", got)
	}
}

func TestMergeTakesTheElementwiseMax(t *testing.T) {
	got := Merge(Clock{"n0": 3, "n1": 1}, Clock{"n0": 1, "n2": 5})
	want := Clock{"n0": 3, "n1": 1, "n2": 5}
	if len(got) != len(want) {
		t.Fatalf("Merge = %v, want %v", got, want)
	}
	for id, n := range want {
		if got[id] != n {
			t.Fatalf("Merge[%s] = %d, want %d (full: %v)", id, got[id], n, got)
		}
	}
}

func TestCloneIsIndependent(t *testing.T) {
	original := Clock{"n0": 1}
	clone := original.Clone()
	clone["n0"] = 99
	clone["n4"] = 7
	if original["n0"] != 1 || len(original) != 1 {
		t.Fatalf("Clone aliased the original: %v", original)
	}
}
