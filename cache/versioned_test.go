package cache

import (
	"slices"
	"testing"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/vclock"
)

// The running example: alice via n0, then bob (via n0) and carol (via n4) diverge.
var (
	vcAlice = vclock.Clock{"n0": 1}
	vcBob   = vcAlice.Bump("n0")                      // {n0:2}
	vcCarol = vcAlice.Bump("n4")                      // {n0:1, n4:1} — concurrent with bob
	vcDave  = vcBob.Bump("n1")                        // {n0:2, n1:1} — dominates bob
	vcMerge = vclock.Merge(vcBob, vcCarol).Bump("n0") // {n0:3, n4:1} — dominates both
)

var never time.Time // the zero instant: never expires

func values(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Value
	}
	slices.Sort(out)
	return out
}

func TestConcurrentWritesBothSurvive(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)
	c.SetVersioned("k", "carol", vcCarol, never)

	got, ok := c.GetEntries("k")
	if !ok {
		t.Fatal("GetEntries miss after two writes")
	}
	if want := []string{"bob", "carol"}; !slices.Equal(values(got), want) {
		t.Fatalf("concurrent writes = %v, want both %v", values(got), want)
	}
}

func TestDominatingWriteCollapsesTheConflict(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)
	c.SetVersioned("k", "carol", vcCarol, never)
	// A resolution whose clock dominates both siblings must leave exactly one value.
	c.SetVersioned("k", "resolved", vcMerge, never)

	got, _ := c.GetEntries("k")
	if want := []string{"resolved"}; !slices.Equal(values(got), want) {
		t.Fatalf("after dominating write = %v, want %v", values(got), want)
	}
}

func TestStaleReplicaWriteIsIgnored(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "dave", vcDave, never) // {n0:2, n1:1}
	c.SetVersioned("k", "bob", vcBob, never)   // {n0:2} — dave dominates it, so it's stale

	got, _ := c.GetEntries("k")
	if want := []string{"dave"}; !slices.Equal(values(got), want) {
		t.Fatalf("stale write survived: %v, want %v", values(got), want)
	}
}

func TestEqualVersionDoesNotDuplicate(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)
	c.SetVersioned("k", "bob-again", vcBob, never) // same clock

	got, _ := c.GetEntries("k")
	if len(got) != 1 {
		t.Fatalf("equal-version rewrite = %d entries, want 1: %v", len(got), values(got))
	}
}

func TestGetReturnsOneOfTheConflictingValues(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)
	c.SetVersioned("k", "carol", vcCarol, never)

	// Legacy Get collapses a conflict to a single value: it must be one of them, never empty.
	v, ok := c.Get("k")
	if !ok || (v != "bob" && v != "carol") {
		t.Fatalf("Get on a conflict = %q, %v; want one of bob/carol", v, ok)
	}
}

func TestSnapshotAllSeesTheConflictSnapshotDoesNot(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)
	c.SetVersioned("k", "carol", vcCarol, never)

	if all := c.SnapshotAll()["k"]; len(all) != 2 {
		t.Fatalf("SnapshotAll = %d entries, want 2", len(all))
	}
	if one := c.Snapshot(); one["k"].Value != "bob" && one["k"].Value != "carol" {
		t.Fatalf("Snapshot = %q, want one of bob/carol", one["k"].Value)
	}
}

func TestExpiredVersionIsPrunedButLiveSiblingSurvives(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)                          // permanent
	c.SetVersioned("k", "carol", vcCarol, time.Now().Add(-time.Hour)) // already expired, concurrent

	got, ok := c.GetEntries("k")
	if !ok {
		t.Fatal("key went fully dead though bob is permanent")
	}
	if want := []string{"bob"}; !slices.Equal(values(got), want) {
		t.Fatalf("expired sibling not pruned: %v, want %v", values(got), want)
	}
}

func TestLegacySetAtReplacesTheWholeSet(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	c.SetVersioned("k", "bob", vcBob, never)
	c.SetVersioned("k", "carol", vcCarol, never) // two concurrent versions
	c.SetAt("k", "override", never)              // unversioned replace

	got, _ := c.GetEntries("k")
	if want := []string{"override"}; !slices.Equal(values(got), want) {
		t.Fatalf("SetAt did not replace the set: %v, want %v", values(got), want)
	}
}
