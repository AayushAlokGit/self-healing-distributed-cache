package cluster

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitUntil polls f until true or the deadline, for events whose timing is bounded but
// not exact.
func waitUntil(t *testing.T, within time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for: %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// keyState finds a key in a state snapshot.
func keyState(st State, key string) (KeyState, bool) {
	for _, k := range st.Keys {
		if k.Key == key {
			return k, true
		}
	}
	return KeyState{}, false
}

// outcomes renders a read path as "n0:hit,n4:skipped,n2:skipped" for failure messages.
func outcomes(res ReadResult) string {
	var b []string
	for _, h := range res.Path {
		b = append(b, h.Node+":"+h.Outcome)
	}
	return strings.Join(b, ",")
}

// The read trace must get three facts right:
//
//  1. A conflict-aware read asks EVERY owner — it cannot stop at the first hit without
//     risking an unseen concurrent sibling — so every owner that holds the key hits, and
//     the primary (rank 0), holding the winning version, is the servedBy. (When the R_read
//     dial lands in 7B, owners past R_read go back to "skipped".)
//  2. A dead owner is "unreachable", never "miss" — a miss says the node answered.
//  3. An unwritten key is a miss at every owner. Only the trace tells that apart from
//     (2); the value is absent either way.
func TestReadPathNamesEveryOwnerAndWhatItSaid(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(6); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const key = "key:3"

	// (1) Healthy: every owner holds the key, so a gather-all read hits all R; the primary
	// holds the winning version and is the servedBy.
	res, err := c.Get(key)
	if err != nil || !res.Found {
		t.Fatalf("get %q on a healthy cluster: (%+v, %v)", key, res, err)
	}
	if res.Coordinator == "" {
		t.Fatalf("get %q did not report which node coordinated it: %+v", key, res)
	}
	if len(res.Path) != 3 {
		t.Fatalf("get %q should trace all R=3 owners, traced %d: %s", key, len(res.Path), outcomes(res))
	}
	if res.Path[0].Role != "primary" || res.Path[0].Outcome != "hit" || res.ServedBy != res.Path[0].Node {
		t.Fatalf("get %q on a healthy cluster should be served by its primary, got servedBy=%s path=%s",
			key, res.ServedBy, outcomes(res))
	}
	for _, h := range res.Path {
		if h.Outcome != "hit" {
			t.Fatalf("get %q: every owner holds it, so a gather-all read hits them all; %s said %q; path=%s",
				key, h.Node, h.Outcome, outcomes(res))
		}
	}

	// (2) A key nobody wrote: every owner is alive, answers, and has no copy.
	//
	// Must run BEFORE the kill: afterwards an owner of this key could be the dead node,
	// which reports unreachable, and the assertion would pass or fail on a hash.
	miss, err := c.Get("key:never-written")
	if err != nil {
		t.Fatalf("get on a missing key should be a clean miss, not an error: %v", err)
	}
	if miss.Found || len(miss.Path) == 0 {
		t.Fatalf("a miss should still trace its owners, got found=%v path=%s", miss.Found, outcomes(miss))
	}
	for _, h := range miss.Path {
		if h.Outcome != "miss" {
			t.Fatalf("every live owner of an unwritten key should MISS (answered, no copy), %s said %q; path=%s",
				h.Node, h.Outcome, outcomes(miss))
		}
	}

	// (3) Kill the primary: it goes unreachable, but the surviving owners still hold the key,
	// so a gather-all read hits them and is served by a live replica — a fallback, never the
	// dead node.
	primary := res.Path[0].Node
	if err := c.Kill(primary); err != nil {
		t.Fatalf("kill %s: %v", primary, err)
	}
	res, err = c.Get(key)
	if err != nil || !res.Found {
		t.Fatalf("get %q after killing its primary should still serve: (%+v, %v)", key, res, err)
	}
	// The coordinator's ring drops a silent peer within a heartbeat, after which the dead
	// node leaves the path entirely. Both states are correct, so assert only what holds
	// in either: a dead node never answers. Pinning "path[0] is unreachable" pins a race.
	for _, h := range res.Path {
		if h.Node == primary && h.Outcome != "unreachable" {
			t.Fatalf("killed node %s reported %q — a dead node cannot answer; path=%s",
				primary, h.Outcome, outcomes(res))
		}
	}
	// A found read is served by an owner that hit, and after the primary died that owner is
	// a live replica, not the corpse.
	hits := map[string]bool{}
	for _, h := range res.Path {
		if h.Outcome == "hit" {
			hits[h.Node] = true
		}
	}
	if len(hits) == 0 {
		t.Fatalf("a found read must hit at least one owner; path=%s", outcomes(res))
	}
	if !hits[res.ServedBy] {
		t.Fatalf("servedBy %s is not among the owners that hit; path=%s", res.ServedBy, outcomes(res))
	}
	if res.ServedBy == primary {
		t.Fatalf("primary %s is dead; it cannot be servedBy; path=%s", primary, outcomes(res))
	}
}

// The full loop through the manager: seed, snapshot, kill an owner, watch the key drop
// to under-replicated then heal back to R, all while reads keep serving.
func TestClusterDemoFlow(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Every seeded key should live on R=3 holders.
	st := c.State()
	if len(st.Keys) != 12 {
		t.Fatalf("want 12 keys in state, got %d", len(st.Keys))
	}
	var victimKey string
	for _, k := range st.Keys {
		if len(k.Holders) != 3 {
			t.Fatalf("key %q should have 3 holders, got %v", k.Key, k.Holders)
		}
		if victimKey == "" {
			victimKey = k.Key
		}
	}

	// Read a key back through the cluster. With every node up, its primary answers.
	res, err := c.Get(victimKey)
	if err != nil || !res.Found {
		t.Fatalf("get %q before kill: (%+v, %v)", victimKey, res, err)
	}
	if res.Fallback() {
		t.Fatalf("get %q with a healthy cluster should be served by its primary, not a fallback: %+v", victimKey, res)
	}

	// Kill the primary owner of the victim key.
	ks, _ := keyState(st, victimKey)
	primary := ks.Owners[0]
	if err := c.Kill(primary); err != nil {
		t.Fatalf("kill %s: %v", primary, err)
	}

	// Reads keep serving immediately via fallback, even while under-replicated. Assert
	// who answered, not just that a value came back.
	res, err = c.Get(victimKey)
	if err != nil || !res.Found {
		t.Fatalf("get %q right after kill should still serve via fallback: (%+v, %v)", victimKey, res, err)
	}
	if res.ServedBy == primary {
		t.Fatalf("get %q was served by the node we just killed (%s)", victimKey, primary)
	}
	if !res.Fallback() {
		t.Fatalf("get %q after killing its primary %s should report a fallback, got %+v", victimKey, primary, res)
	}

	// The heal restores R: the key returns to 3 holders, and none is the dead node.
	waitUntil(t, 4*time.Second, "victim key heals back to 3 holders", func() bool {
		st := c.State()
		k, ok := keyState(st, victimKey)
		if !ok {
			return false
		}
		for _, h := range k.Holders {
			if h == primary {
				return false // dead node must not be counted a holder
			}
		}
		return len(k.Holders) == 3
	})
	t.Logf("heal restored R=3 for %q after killing its primary %s", victimKey, primary)

	if st := c.State(); st.TotalHealCopies == 0 {
		t.Fatalf("expected heal copies after a kill, got 0")
	}
	if st := c.State(); st.AliveCount != 4 {
		t.Fatalf("want 4 alive nodes after one kill, got %d", st.AliveCount)
	}
}

// Guards the seed-appends bug: Seed must ADD keys, not rewrite key:0..key:n-1 on every
// call, or the dashboard's repeated "seed more" clicks are silent no-ops. Only shows up
// on the second call, which TestClusterDemoFlow never makes.
func TestSeedAppendsRatherThanRewrites(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Seed(12); err != nil { // what the server does at startup
		t.Fatalf("seed: %v", err)
	}
	if err := c.Seed(8); err != nil { // what the dashboard button does, repeatedly
		t.Fatalf("seed: %v", err)
	}

	st := c.State()
	if len(st.Keys) != 20 {
		t.Fatalf("12 + 8 seeded: want 20 distinct keys, got %d (Seed is rewriting, not appending)", len(st.Keys))
	}
	for _, k := range st.Keys {
		if len(k.Holders) != 3 {
			t.Errorf("key %q: want 3 holders, got %v", k.Key, k.Holders)
		}
	}
	// The second batch must be the *new* numbers, not a rerun of the first.
	if _, ok := keyState(st, "key:19"); !ok {
		t.Errorf("key:19 missing: the second Seed(8) did not continue past the first batch")
	}
}

// The heal log must name what actually moved: which keys, from which node, to which.
// The copy counter alone can only say "24 copies happened".
func TestHealLogNamesTheKeysThatMoved(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A healthy cluster logs no heals: a heal that copies nothing must file no record.
	if h := healEvents(c.State()); len(h) != 0 {
		t.Fatalf("no death yet, want no heal events, got %v", h)
	}

	killID := lastEventID(c.State())
	if err := c.Kill("n2"); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Detection (~500ms) + grace (500ms) + the copies. State() is what drains the nodes'
	// records, so polling it is also what builds the log.
	waitUntil(t, 5*time.Second, "a heal event to be logged", func() bool {
		return len(healEvents(c.State())) > 0
	})

	st := c.State()
	live := map[string]bool{"n0": true, "n1": true, "n3": true, "n4": true}
	seen := map[string]bool{}
	for _, h := range healEvents(st) {
		// A heal must be logged AFTER the kill that caused it: the log's order is the
		// only thing that makes it causal.
		if h.ID <= killID {
			t.Errorf("heal %d was logged before the kill (id %d) that caused it — the log's order is not causal", h.ID, killID)
		}
		if !live[h.From] || !live[h.To] {
			t.Errorf("heal %d: %s -> %s, but the dead node cannot send or receive", h.ID, h.From, h.To)
		}
		if h.From == h.To {
			t.Errorf("heal %d: node %s copied to itself", h.ID, h.From)
		}
		if len(h.Keys) == 0 {
			t.Errorf("heal %d: %s -> %s recorded with no keys", h.ID, h.From, h.To)
		}
		// The sender reports what IT saw, not what the manager did. Only the node knows.
		if !strings.Contains(h.Cause, "n2") {
			t.Errorf("heal %d: cause is %q, want it to name the peer (n2) whose silence the sender observed", h.ID, h.Cause)
		}
		for _, k := range h.Keys {
			seen[k] = true
			// The receiver must actually hold what the log claims it was sent.
			ks, ok := keyState(st, k)
			if !ok {
				t.Errorf("heal %d names key %q, which is not in the cluster", h.ID, k)
				continue
			}
			if !slices.Contains(ks.Holders, h.To) {
				t.Errorf("heal %d claims %s got %q, but its holders are %v", h.ID, h.To, k, ks.Holders)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("heal log recorded no keys at all")
	}
	t.Logf("heal log: %d events covering %d distinct keys", len(healEvents(st)), len(seen))
}

// healEvents pulls just the heal entries out of the unified activity log.
func healEvents(st State) []Event {
	var out []Event
	for _, e := range st.Events {
		if e.Kind == "heal" {
			out = append(out, e)
		}
	}
	return out
}

// lastEventID is the newest event id.
func lastEventID(st State) uint64 {
	if len(st.Events) == 0 {
		return 0
	}
	return st.Events[len(st.Events)-1].ID
}

// notOnItsOwners names every key that some OWNER of it does not hold. This is the real
// replication invariant, and it is stronger than UnderReplicated (holders < R):
// leftover copies on non-owners pad the holder count while a true owner sits empty.
// Waiting on UnderReplicated instead lets a test proceed before the heal converges.
func notOnItsOwners(st State) []string {
	var out []string
	for _, k := range st.Keys {
		for _, o := range k.Owners {
			if !slices.Contains(k.Holders, o) {
				out = append(out, k.Key+"/"+o)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Guards the stranded-key bug: if only a key's PRIMARY may push it, a revived node
// promoted back to primary while holding nothing has nothing to send, the nodes that do
// hold the key stand down, and it stays under-replicated forever. So the healer is the
// first owner in ring order that actually HOLDS the key: exactly one sender, always.
func TestReviveRestoresFullReplication(t *testing.T) {
	c := New(3, 1, 300*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(20); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Down to two nodes: with R=3 and 2 alive, the survivors hold every key, including
	// many they will not own once the others return.
	for _, victim := range []string{"n1", "n2", "n3"} {
		if err := c.Kill(victim); err != nil {
			t.Fatalf("kill %s: %v", victim, err)
		}
		time.Sleep(1200 * time.Millisecond) // let detection + grace + heal settle
	}
	if got := c.State().AliveCount; got != 2 {
		t.Fatalf("want 2 nodes alive, got %d", got)
	}

	for _, id := range []string{"n1", "n2", "n3"} {
		if err := c.Revive(id); err != nil {
			t.Fatalf("revive %s: %v", id, err)
		}
	}

	// Every key must get back onto all R of its owners with no client writes. Wait on the
	// same invariant the assertions below check: a weaker predicate exits early on
	// leftover copies and the test flakes.
	waitUntil(t, 20*time.Second, "every key to land on all of its owners after the revives", func() bool {
		return len(notOnItsOwners(c.State())) == 0
	})

	st := c.State()
	if got := st.AliveCount; got != 5 {
		t.Fatalf("want 5 nodes alive, got %d", got)
	}
	for _, k := range st.Keys {
		if len(k.Holders) < 3 {
			t.Errorf("key %q: %d holders %v, want 3 (owners %v)", k.Key, len(k.Holders), k.Holders, k.Owners)
		}
		// Owners must be holders: a key parked only on leftover non-owners is replicated
		// by accident, and the next kill loses it.
		for _, o := range k.Owners {
			if !slices.Contains(k.Holders, o) {
				t.Errorf("key %q: owner %s does not hold it (holders %v) — stranded", k.Key, o, k.Holders)
			}
		}
	}
	t.Logf("all %d keys back to R=3 on their true owners, by heal alone", len(st.Keys))
}

// nodeKeyCount reads a node's key count from a snapshot.
func nodeKeyCount(st State, id string) int {
	for _, n := range st.Nodes {
		if n.ID == id {
			return n.KeyCount
		}
	}
	return -1
}

// A revived node comes back empty, and the check-first heal must repopulate it. Without
// that, a returned node stays empty until new writes happen to land on it.
func TestRevivedNodeRepopulates(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(15); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const victim = "n2"
	if kc := nodeKeyCount(c.State(), victim); kc <= 0 {
		t.Fatalf("precondition: %s should hold keys, got %d", victim, kc)
	}

	if err := c.Kill(victim); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// Let the death heal settle so its keys live on the survivors.
	time.Sleep(2 * time.Second)

	if err := c.Revive(victim); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if kc := nodeKeyCount(c.State(), victim); kc != 0 {
		t.Fatalf("a revived node should return empty, got %d keys", kc)
	}
	waitUntil(t, 6*time.Second, "revived node repopulates via the check-first heal", func() bool {
		return nodeKeyCount(c.State(), victim) > 0
	})
	t.Logf("revived %s repopulated to %d keys with no client writes", victim, nodeKeyCount(c.State(), victim))
}

// A false positive with a grace period costs no heal copies: pause a node's
// health, let peers suspect it, resume before the grace period, and confirm the
// cluster did not re-replicate.
func TestClusterGraceAbsorbsFalsePositive(t *testing.T) {
	const grace = 2 * time.Second
	c := New(3, 1, grace, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.Pause("n1", true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitUntil(t, 3*time.Second, "n1 shows paused in state", func() bool {
		for _, n := range c.State().Nodes {
			if n.ID == "n1" {
				return n.Paused
			}
		}
		return false
	})

	// Resume within the grace window.
	if err := c.Pause("n1", false); err != nil {
		t.Fatalf("resume: %v", err)
	}

	time.Sleep(grace + 700*time.Millisecond)
	if st := c.State(); st.TotalHealCopies != 0 {
		t.Fatalf("grace period should have absorbed the false positive, got %d heal copies", st.TotalHealCopies)
	}
	t.Logf("grace period absorbed the false positive: 0 heal copies")
}

// A TTL'd key must die on schedule even if the cluster re-replicated it meanwhile. This
// is what the absolute-deadline wire format buys: if a heal copy carried a *duration*
// instead, the fresh copy would restart the key's life, and a read falling back to that
// replica would serve a value that should be gone.
func TestHealDoesNotResurrectAnExpiringKey(t *testing.T) {
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	const key, ttl = "session:abc", 3 * time.Second
	writtenAt := time.Now()
	if err := c.Set(key, "aayush", ttl); err != nil {
		t.Fatalf("set: %v", err)
	}

	ks, ok := keyState(c.State(), key)
	if !ok {
		t.Fatalf("key %q missing from state right after the write", key)
	}
	if ks.TTLMs <= 0 {
		t.Fatalf("key %q should report remaining life, got ttlMs=%d", key, ks.TTLMs)
	}
	primary := ks.Owners[0]

	// Kill the primary: the survivors must re-replicate the key to a new owner.
	if err := c.Kill(primary); err != nil {
		t.Fatalf("kill %s: %v", primary, err)
	}

	// Wait for the heal to place a copy on a node that did not have one, so this tests a
	// *healed* copy and not just the originals.
	before := map[string]bool{}
	for _, h := range ks.Holders {
		before[h] = true
	}
	waitUntil(t, 2*time.Second, "the heal copies the expiring key to a new holder", func() bool {
		k, ok := keyState(c.State(), key)
		if !ok {
			return false
		}
		for _, h := range k.Holders {
			if !before[h] {
				return true // a node that never had this key now does: a healed copy
			}
		}
		return false
	})

	// Wait out the ORIGINAL deadline. A healed copy given a fresh TTL is still alive here.
	time.Sleep(time.Until(writtenAt.Add(ttl)) + 750*time.Millisecond)

	res, err := c.Get(key)
	if err != nil {
		t.Fatalf("get after expiry: %v", err)
	}
	if res.Found {
		t.Fatalf("key %q outlived its %s TTL: the heal handed the copy on %s a new lease on life (value %q)",
			key, ttl, res.ServedBy, res.Value)
	}
	if k, ok := keyState(c.State(), key); ok {
		t.Fatalf("expired key %q is still held by %v", key, k.Holders)
	}
}

// The counterpart: a key written with no TTL is not accidentally given one.
func TestKeyWithoutTTLNeverExpires(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Set("permanent", "v", 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	ks, ok := keyState(c.State(), "permanent")
	if !ok {
		t.Fatalf("key missing from state")
	}
	if ks.TTLMs != -1 {
		t.Fatalf("a key with no TTL should report ttlMs=-1 (never expires), got %d", ks.TTLMs)
	}
}

// eventsOfKind pulls one kind out of a snapshot's activity log.
func eventsOfKind(st State, kind string) []Event {
	var out []Event
	for _, e := range st.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// The expiry event must be about the KEY, not about the copies.
//
// A key with R=3 lives on three nodes, and each frees the memory at its own arbitrary
// later moment — lazily if a read lands on it, otherwise whenever the sampler happens to
// draw it. Reporting the deletions would tell the viewer the replicas disagreed about
// when the key died. They did not: SetAt gave all three the same absolute deadline, so
// the key dies once, everywhere, and that is the one event worth showing.
//
// Polling State() is what detects it, so this drives State() in the wait loop rather
// than checking once at the end.
func TestExpiryIsOneEventPerKeyNotPerReplica(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	const key = "doomed"
	if err := c.Set(key, "v", 150*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	// It really is on three nodes: without that, "one event per key" proves nothing.
	st := c.State()
	ks, ok := keyState(st, key)
	if !ok || len(ks.Holders) != 3 {
		t.Fatalf("%q should be held by R=3 nodes before it expires, got %v", key, ks.Holders)
	}

	waitUntil(t, 3*time.Second, "the key to expire", func() bool {
		return len(eventsOfKind(c.State(), "expire")) > 0
	})

	// Keep polling well past the deadline: a detector that re-fires would do it here.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		c.State()
		time.Sleep(20 * time.Millisecond)
	}

	exp := eventsOfKind(c.State(), "expire")
	if len(exp) != 1 {
		t.Fatalf("one key expiring on 3 replicas must be exactly 1 expire event, got %d: %+v", len(exp), exp)
	}
	if !slices.Contains(exp[0].Keys, key) {
		t.Fatalf("the expire event should name the key that died, got keys=%v", exp[0].Keys)
	}
	if exp[0].Cause != "" {
		t.Fatalf("an expiry is caused by the clock, not by a fault; it must carry no cause, got %q", exp[0].Cause)
	}

	// And the client agrees: gone means gone, whoever still has the bytes.
	if res, err := c.Get(key); err != nil || res.Found {
		t.Fatalf("an expired key must not be readable: (%+v, %v)", res, err)
	}
}

// The bug the deadline check exists to prevent.
//
// State() builds its key list from the ALIVE nodes only, so killing a node makes its
// keys vanish from that list. A detector that called "gone from the snapshot" an expiry
// would fire on every kill — the demo's headline action would spray fake expiry events
// for keys that are alive, well within their TTL, and still held by two other replicas.
//
// Gone-and-past-its-deadline is an expiry. Gone-and-not-yet-due is a node that died
// holding it. The remembered deadline is the only thing that can tell them apart.
//
// ⚠️ R=3 over exactly THREE nodes, so every key is on every node and killing all three
// strips every key of every holder. Killing one node out of five proves nothing — the key
// still has two live replicas, so it never leaves the snapshot and the naive detector
// stays silent for the wrong reason. (It passed against the broken code. That is how this
// version came to exist.) Killing 3-of-5 would depend on which owners the hash handed
// out, and a test whose result depends on a hash will flake on somebody else's machine.
func TestKilledNodesKeysAreNotReportedAsExpired(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	// A long TTL: nothing here is allowed to expire during the test.
	const keys = 6
	for i := range keys {
		if err := c.Set("live:"+strconv.Itoa(i), "v", time.Hour); err != nil {
			t.Fatalf("set: %v", err)
		}
	}

	st := c.State() // records the deadlines, and proves the keys were really there
	if len(st.Keys) != keys {
		t.Fatalf("want %d keys held before the kills, got %d", keys, len(st.Keys))
	}

	for _, id := range []string{"n0", "n1", "n2"} {
		if err := c.Kill(id); err != nil {
			t.Fatalf("kill %s: %v", id, err)
		}
	}

	// Every key has now lost every holder — exactly the snapshot a naive detector reads
	// as "6 keys expired".
	st = c.State()
	if len(st.Keys) != 0 {
		t.Fatalf("with every node dead, no key can be held; got %d", len(st.Keys))
	}
	if exp := eventsOfKind(st, "expire"); len(exp) > 0 {
		t.Fatalf("keys lost with the whole cluster must not be reported as expired — they are an "+
			"hour from their deadline, so this is data loss, and calling it expiry hides it behind "+
			"a routine event. Got %d expire event(s): %+v", len(exp), exp)
	}
}

// Reclamation is a different fact from expiry, and the cache is the only thing that can
// report it: the key is dead to every reader the moment its deadline passes, but the
// bytes sit in the map until a Get lands on them or the sampler draws them.
func TestReclaimEventNamesTheNodeAndTheReason(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Set("doomed", "v", 100*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	// The cache's sweeper runs on a 1s tick, so give it room.
	waitUntil(t, 6*time.Second, "the sweeper to free the expired key", func() bool {
		return len(eventsOfKind(c.State(), "reclaim")) > 0
	})

	for _, e := range eventsOfKind(c.State(), "reclaim") {
		if e.From == "" {
			t.Fatalf("a reclaim must name the node whose memory was freed: %+v", e)
		}
		if len(e.Keys) == 0 {
			t.Fatalf("a reclaim must name the keys it freed: %+v", e)
		}
		if e.Cause != "" {
			t.Fatalf("a reclaim is caused by the clock, not a fault. A cause here would nest it "+
				"under the last kill and tell the viewer that killing a node destroyed data. Got %q", e.Cause)
		}
	}
}

// A key can be born and die between two polls, and its expiry must still be reported.
//
// The detector notices a death by missing a key it saw alive last time — so a key it
// never saw alive at all is one whose death it can never notice. Every TTL shorter than
// the dashboard's poll interval would expire in silence. Set records the deadline when it
// writes the key, rather than waiting to observe it, which is why this works.
//
// No State() call between the write and the deadline: that gap is the whole test.
func TestAKeyThatDiesBetweenTwoPollsStillReportsItsExpiry(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Set("blink", "v", 100*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // it lives and dies with nobody watching

	exp := eventsOfKind(c.State(), "expire")
	if len(exp) != 1 {
		t.Fatalf("a key whose whole life fell between two polls must still report exactly one "+
			"expiry, got %d: %+v", len(exp), exp)
	}
	if !slices.Contains(exp[0].Keys, "blink") {
		t.Fatalf("the expire event should name the key that died, got keys=%v", exp[0].Keys)
	}
}

// copiesStored counts every copy of every key the cluster physically holds.
func copiesStored(st State) int {
	n := 0
	for _, k := range st.Keys {
		n += len(k.Holders)
	}
	return n
}

// Without cleanup, heal is a ratchet: it only ever COPIES. Kill a node and its keys are
// re-replicated onto whoever owns them now; revive it and the ring snaps back, but those
// copies stay put. Nothing removes them, so every kill/revive cycle permanently raises the
// copy count and R creeps toward N — giving away the sharding one outage at a time.
//
// So after the ring is whole again, every key must be held by exactly its owners, and the
// total copies must be back to keys × R. This test fails against a heal with no cleanup.
func TestCleanupDropsTheCopiesTheRingNoLongerOwns(t *testing.T) {
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	want := 12 * 3
	if got := copiesStored(c.State()); got != want {
		t.Fatalf("precondition: a fresh cluster should hold %d copies, holds %d", want, got)
	}

	// The outage: n2's keys land on nodes that will not own them once n2 is back.
	if err := c.Kill("n2"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitUntil(t, 6*time.Second, "the heal to re-replicate n2's keys", func() bool {
		return c.State().TotalHealCopies > 0
	})
	if err := c.Revive("n2"); err != nil {
		t.Fatalf("revive: %v", err)
	}

	// Converge: every key back on all its owners (heal), and no key anywhere else (cleanup).
	waitUntil(t, 15*time.Second, "the ring to return to exactly keys × R copies", func() bool {
		st := c.State()
		return len(notOnItsOwners(st)) == 0 && copiesStored(st) == want
	})

	st := c.State()
	for _, k := range st.Keys {
		holders := append([]string(nil), k.Holders...)
		owners := append([]string(nil), k.Owners...)
		sort.Strings(holders)
		sort.Strings(owners)
		if !slices.Equal(holders, owners) {
			t.Errorf("key %q: held by %v but owned by %v — a surplus copy survived the cleanup",
				k.Key, k.Holders, k.Owners)
		}
	}
	if got := copiesStored(st); got != want {
		t.Errorf("after a kill+revive the cluster should hold %d copies again, holds %d", want, got)
	}
	t.Logf("kill+revive settled back to %d copies (= %d keys × R=3), no leftovers", copiesStored(st), len(st.Keys))
}

// Cleanup deletes data, so the end-to-end question is whether a shrinking cluster still has
// every key when the dust settles. Squeeze to two live nodes and then to one, and the last
// node standing must hold everything — cleanup must never eat the copies keeping the cluster
// alive.
//
// ⚠️ This does NOT prove the confirm-before-drop rule, and a version of cleanup that deletes
// without asking anybody still passes it. The reason is worth knowing: below R=3 live nodes
// every survivor is an owner of every key, so cleanup returns at the ownership check and the
// confirm path never runs. The rule itself is guarded in node/cleanup_test.go, which drives
// cleanup directly and controls what each owner answers.
func TestShrinkingClusterKeepsEveryKey(t *testing.T) {
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(8); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := len(c.State().Keys)

	for _, victim := range []string{"n1", "n2", "n3"} {
		if err := c.Kill(victim); err != nil {
			t.Fatalf("kill %s: %v", victim, err)
		}
		time.Sleep(1200 * time.Millisecond) // detection + grace + heal + cleanup
	}

	st := c.State()
	if st.AliveCount != 2 {
		t.Fatalf("want 2 nodes alive, got %d", st.AliveCount)
	}
	if len(st.Keys) != before {
		t.Fatalf("cleanup lost keys while the cluster shrank: %d keys, want %d", len(st.Keys), before)
	}

	// Down to one node: it is now the sole owner and sole holder of everything. A cleanup
	// that dropped what it could not confirm would empty the cluster here.
	if err := c.Kill("n4"); err != nil {
		t.Fatalf("kill n4: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	st = c.State()
	if st.AliveCount != 1 {
		t.Fatalf("want 1 node alive, got %d", st.AliveCount)
	}
	if len(st.Keys) != before {
		t.Fatalf("the last node standing must still hold every key: has %d, want %d — cleanup deleted "+
			"copies it could not confirm anywhere else", len(st.Keys), before)
	}
	for _, k := range st.Keys {
		if len(k.Holders) != 1 {
			t.Errorf("key %q: want exactly 1 holder on a 1-node cluster, got %v", k.Key, k.Holders)
		}
	}
	if res, err := c.Get("key:0"); err != nil || !res.Found {
		t.Fatalf("key:0 must still be readable from the last node: (%+v, %v)", res, err)
	}
	t.Logf("survived down to 1 node with all %d keys intact — cleanup dropped nothing it could not confirm", before)
}

// A delete must take every copy, and it must STAY taken: the heal that repairs a kill
// must not read a deleted key as a key that lost its replicas.
func TestDeleteRemovesEveryCopyAndStaysDeleted(t *testing.T) {
	c := New(3, 1, 300*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const key = "key:5"
	ks, ok := keyState(c.State(), key)
	if !ok || len(ks.Holders) != 3 {
		t.Fatalf("precondition: %q should be on 3 holders, got %v", key, ks.Holders)
	}

	dropped, err := c.Delete(key)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	sort.Strings(dropped)
	if !slices.Equal(dropped, ks.Holders) {
		t.Errorf("delete should report every node that held %q: holders %v, dropped %v", key, ks.Holders, dropped)
	}

	if _, still := keyState(c.State(), key); still {
		t.Fatalf("%q is still on the ring right after the delete", key)
	}
	if res, err := c.Get(key); err != nil || res.Found {
		t.Fatalf("a deleted key must not be readable: (%+v, %v)", res, err)
	}

	// The other 11 are untouched: a delete is not a clear.
	if st := c.State(); len(st.Keys) != 11 {
		t.Errorf("deleting 1 of 12 keys should leave 11, got %d", len(st.Keys))
	}

	// And it does not come back. Give heal a kill to react to, so this is not just an idle
	// cluster sitting still: the survivors re-replicate, and the deleted key must not be
	// among what they copy.
	if err := c.Kill(ks.Holders[0]); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitUntil(t, 6*time.Second, "the heal to run after the kill", func() bool {
		return c.State().TotalHealCopies > 0
	})
	if k, back := keyState(c.State(), key); back {
		t.Fatalf("the heal resurrected deleted key %q onto %v", key, k.Holders)
	}

	// Deleting what is not there is a no-op, not an error: the caller asked for the key to
	// be gone, and it is.
	again, err := c.Delete(key)
	if err != nil {
		t.Fatalf("re-deleting an absent key should not error: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("re-deleting an absent key should report no holders, got %v", again)
	}
}

// The dashboard-reachable half of the same bug, using only Kill and Revive.
//
// Nothing in this system ever deletes a surplus copy. Kill a node and the heal re-replicates
// its keys onto whoever is an owner *now*; revive it and the ring snaps back, but the copies
// made in the meantime stay exactly where they are — on nodes that are no longer owners of
// those keys. So "the R nodes the ring names" is a strict subset of "the nodes actually
// holding this key", and a delete aimed at the owners leaves the leftovers behind: the key
// keeps its holders, and so it never even leaves the dashboard.
func TestDeleteFindsCopiesTheRingNoLongerNames(t *testing.T) {
	c := New(3, 1, 200*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Kill, let the heal scatter copies, revive, let the ring snap back.
	if err := c.Kill("n2"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitUntil(t, 6*time.Second, "the heal to re-replicate n2's keys", func() bool {
		return c.State().TotalHealCopies > 0
	})
	if err := c.Revive("n2"); err != nil {
		t.Fatalf("revive: %v", err)
	}
	waitUntil(t, 10*time.Second, "every key back on all of its owners", func() bool {
		return len(notOnItsOwners(c.State())) == 0
	})

	// Find a key now held by more nodes than own it: those extra holders are the leftovers,
	// and they are invisible to a delete that asks the ring who owns the key.
	var victim KeyState
	for _, k := range c.State().Keys {
		if len(k.Holders) > len(k.Owners) {
			victim = k
			break
		}
	}
	if victim.Key == "" {
		t.Skip("no key ended up with a leftover copy this run; nothing to prove here")
	}
	t.Logf("%q is owned by %v but held by %v — %d leftover copies the ring does not name",
		victim.Key, victim.Owners, victim.Holders, len(victim.Holders)-len(victim.Owners))

	dropped, err := c.Delete(victim.Key)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	sort.Strings(dropped)
	if !slices.Equal(dropped, victim.Holders) {
		t.Errorf("delete should reach every holder of %q, not just its owners: holders %v, dropped %v",
			victim.Key, victim.Holders, dropped)
	}
	if k, still := keyState(c.State(), victim.Key); still {
		t.Fatalf("%q survived its delete on %v — those are holders the ring does not name as owners",
			victim.Key, k.Holders)
	}
}

// Guards the delete-resurrection bug, and it is the entire reason a delete is broadcast to
// every peer instead of to the key's R owners.
//
// Pause is no longer wired to a dashboard button, but the API keeps it, and this is the
// sharpest form of the hazard: not just a copy the delete misses, but one that actively
// pushes the key back afterwards.
//
// A health-paused node is alive and still holding its keys, but its peers have convicted it
// and dropped it from THEIR ring — so ownersFor no longer names it. An owners-only delete
// therefore never reaches it, and it keeps its copy. Resume it: heal fires, the node sees a
// key that no owner holds, appoints itself the healer (heal follows the data, not the ring)
// and pushes the key back across the cluster. The delete undoes itself, disguised as a heal.
//
// This test fails against a delete that asks the ring who owns the key.
func TestDeleteIsNotUndoneByAPausedHolderComingBack(t *testing.T) {
	const grace = 200 * time.Millisecond
	c := New(3, 1, grace, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(6); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const key = "key:3"
	ks, ok := keyState(c.State(), key)
	if !ok || len(ks.Holders) == 0 {
		t.Fatalf("precondition: %q should be held by someone, got %v", key, ks.Holders)
	}
	victim := ks.Holders[0]

	// Pause a holder and wait past the failure timeout (500ms) plus the grace, so its peers
	// have convicted it, dropped it from their ring, and healed around it. It is now a live
	// node, holding the key, that the ring does not consider an owner of it.
	if err := c.Pause(victim, true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	if k, ok := keyState(c.State(), key); !ok || !slices.Contains(k.Holders, victim) {
		t.Fatalf("precondition: paused %s should still be holding %q (holders %v) — "+
			"without that there is no hazard to test", victim, key, k.Holders)
	}

	dropped, err := c.Delete(key)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !slices.Contains(dropped, victim) {
		t.Fatalf("the delete never reached paused holder %s (dropped %v): it is out of the ring, "+
			"so only a broadcast finds it — it will resurrect %q the moment it resumes", victim, dropped, key)
	}
	if _, still := keyState(c.State(), key); still {
		t.Fatalf("%q still on the ring right after the delete", key)
	}

	// Resume: the node rejoins, heal fires, and must find nothing to push back.
	if err := c.Pause(victim, false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second) // well past detection + grace + heal
	for time.Now().Before(deadline) {
		if k, back := keyState(c.State(), key); back {
			t.Fatalf("%q came back after %s resumed, now held by %v — the heal undid the delete",
				key, victim, k.Holders)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("%q stayed deleted through %s's resume and the heal that followed", key, victim)
}

// Clear empties every node, and the demo key numbering restarts so that "clear, then seed"
// lands on the same ring twice.
func TestClearEmptiesEveryNodeAndResetsSeeding(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	gone, err := c.Clear()
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	// Keys, not copies: 12 keys at R=3 is 36 copies, and reporting 36 would just be leaking
	// the replication factor into the UI.
	if gone != 12 {
		t.Fatalf("clear should report 12 distinct keys dropped, got %d", gone)
	}

	st := c.State()
	if len(st.Keys) != 0 {
		t.Fatalf("cleared cluster should hold no keys, holds %d", len(st.Keys))
	}
	for _, n := range st.Nodes {
		if n.KeyCount != 0 {
			t.Errorf("node %s still holds %d keys after a clear", n.ID, n.KeyCount)
		}
	}

	// Seeding after a clear starts at key:0 again — nothing is left for it to overwrite.
	if err := c.Seed(3); err != nil {
		t.Fatalf("seed after clear: %v", err)
	}
	for _, want := range []string{"key:0", "key:1", "key:2"} {
		if _, ok := keyState(c.State(), want); !ok {
			t.Errorf("after a clear, seeding should restart the numbering at key:0 — %q missing", want)
		}
	}
}
