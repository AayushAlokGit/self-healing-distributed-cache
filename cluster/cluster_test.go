package cluster

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// waitUntil polls f until true or the deadline, so the test waits on an event
// whose timing is only bounded, not exact.
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

// outcomes reads a read path back as "n0:hit,n4:skipped,n2:skipped" for a failure
// message you can actually diagnose from.
func outcomes(res ReadResult) string {
	var b []string
	for _, h := range res.Path {
		b = append(b, h.Node+":"+h.Outcome)
	}
	return strings.Join(b, ",")
}

// A read reports not just WHICH node answered but what that node was to the key and
// what every other owner said. The three facts the trace has to get right:
//
//  1. A healthy read stops at the primary. The other owners are never asked — R=3 is
//     how many copies exist, not how many nodes a read touches.
//  2. A dead owner is UNREACHABLE. Never "miss": that would say the node answered.
//  3. A key nobody ever wrote is a miss at EVERY owner — all alive, none holding it.
//     Told apart from (2) only by the trace; the value is absent either way.
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

	// (1) Healthy: the primary hits, and the two replicas are never asked.
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
	for _, h := range res.Path[1:] {
		if h.Role != "replica" || h.Outcome != "skipped" {
			t.Fatalf("get %q: the primary answered, so replica %s should never have been asked; path=%s",
				key, h.Node, outcomes(res))
		}
	}

	// (2) A key nobody wrote: every owner is alive and says so, and none has it. This is
	// the case the value alone cannot distinguish from "everyone is dead".
	//
	// Checked BEFORE the kill, deliberately: afterwards, an owner of THIS key may happen
	// to be the node we killed, and it would report unreachable rather than miss. The
	// assertion would then fail or pass on which key the ring handed us — a test whose
	// result depends on a hash is a test that will flake on somebody else's machine.
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

	// (3) Kill the primary. It must now show as unreachable — the read walks past it to
	// a replica, and the trace is what proves the walk happened rather than merely that
	// a value came back.
	primary := res.Path[0].Node
	if err := c.Kill(primary); err != nil {
		t.Fatalf("kill %s: %v", primary, err)
	}
	res, err = c.Get(key)
	if err != nil || !res.Found {
		t.Fatalf("get %q after killing its primary should still serve: (%+v, %v)", key, res, err)
	}
	// The coordinator's ring drops a silent peer within a heartbeat or so, after which
	// the dead node stops being an owner at all and leaves the path entirely. Both
	// states are correct, so assert what is true in EITHER: a dead node never answers.
	// Pinning "path[0] is unreachable" would be pinning a race.
	for _, h := range res.Path {
		if h.Node == primary && h.Outcome != "unreachable" {
			t.Fatalf("killed node %s reported %q — a dead node cannot answer; path=%s",
				primary, h.Outcome, outcomes(res))
		}
	}
	hit := 0
	for _, h := range res.Path {
		if h.Outcome == "hit" {
			hit++
			if h.Node != res.ServedBy {
				t.Fatalf("path says %s hit but servedBy says %s; path=%s", h.Node, res.ServedBy, outcomes(res))
			}
		}
	}
	if hit != 1 {
		t.Fatalf("a found read must hit exactly one owner, got %d; path=%s", hit, outcomes(res))
	}
}

// The whole demo loop through the manager: seed keys, snapshot god's-eye state,
// kill an owner, watch the key drop to under-replicated then heal back to R, all
// while reads keep serving.
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

	// Reads keep serving immediately (fallback), even while under-replicated. Now that
	// a read reports who answered, assert the fallback actually happened rather than
	// inferring it from the value coming back: a read served by the dead node's
	// replacement is the whole claim, and it used to be untestable from out here.
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

	// Total heal copies climbed — the manager saw the re-replication happen.
	if st := c.State(); st.TotalHealCopies == 0 {
		t.Fatalf("expected heal copies after a kill, got 0")
	}
	if st := c.State(); st.AliveCount != 4 {
		t.Fatalf("want 4 alive nodes after one kill, got %d", st.AliveCount)
	}
}

// Seed must ADD keys, not rewrite key:0..key:n-1 every call. The dashboard's
// "seed 8 more keys" button calls Seed(8) repeatedly, and when Seed numbered from
// zero each click silently overwrote keys that already existed — the button was a
// no-op and the ring never changed. TestClusterDemoFlow could not catch this: it
// calls Seed exactly once, and the bug only appears on the second call.
func TestSeedAppendsRatherThanRewrites(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)

	if err := c.Seed(12); err != nil { // what the server does at startup
		t.Fatalf("seed: %v", err)
	}
	if err := c.Seed(8); err != nil { // what the dashboard button does
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

// The dashboard's heal log must name what actually moved: which keys, from which
// node, to which node. The copy *counter* can only say "24 copies happened"; this
// is the evidence behind that number, and it is the money moment made legible.
func TestHealLogNamesTheKeysThatMoved(t *testing.T) {
	c := New(3, 1, 500*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(12); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Nothing has died, so nothing should have moved: a healthy cluster logs no heals.
	// (A heal that copies nothing must not file a record.)
	if h := healEvents(c.State()); len(h) != 0 {
		t.Fatalf("no death yet, want no heal events, got %v", h)
	}

	killID := lastEventID(c.State()) // everything after this is a consequence of...
	if err := c.Kill("n2"); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Detection (~500ms) + grace (500ms) + the copies themselves. State() is what
	// drains the nodes' records, so polling it is also what builds the log.
	waitUntil(t, 5*time.Second, "a heal event to be logged", func() bool {
		return len(healEvents(c.State())) > 0
	})

	st := c.State()
	live := map[string]bool{"n0": true, "n1": true, "n3": true, "n4": true}
	seen := map[string]bool{}
	for _, h := range healEvents(st) {
		// Ordering is the whole point of merging heals into the activity log: a heal
		// must be logged AFTER the kill that caused it, so a viewer reading top-down
		// sees cause then effect without the UI having to reconstruct anything.
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
		// The sender must say what IT saw that made it heal — not what the manager
		// did. Those are different facts, and only the node can report the first.
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

// lastEventID is the newest event id, so a test can assert that later events came
// after it.
func lastEventID(st State) uint64 {
	if len(st.Events) == 0 {
		return 0
	}
	return st.Events[len(st.Events)-1].ID
}

// notOnItsOwners names every key that some OWNER of it does not hold.
//
// This is the real replication invariant, and it is strictly stronger than
// UnderReplicated (holders < R). A key can have R holders and still be broken: after
// a kill/revive cycle the survivors keep leftover copies of keys they no longer own,
// and those leftovers pad the holder count while a genuine owner sits empty. Waiting
// on UnderReplicated alone therefore lets a test proceed before the heal has
// converged — which is exactly the flake this replaced.
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

// THE STRANDED-KEY BUG. The heal used to say "only the PRIMARY of a key pushes it,"
// which quietly requires one node to be both the primary AND a holder. Kill enough
// nodes that the survivors end up holding keys they do not own, then revive
// everything: the revived nodes are promoted straight back to primary of their own
// arcs while holding NOTHING. The primary then has nothing to send, and the nodes
// that do have the key are not the primary, so they stand down. Nobody is both, and
// the key stays under-replicated forever — no further membership change is coming
// to retrigger anything.
//
// The fix ties the right to push to the DATA rather than to the ring position: the
// healer is the first owner, in ring order, that actually holds the key. Exactly one
// sender (no duplicate copies) and a sender always exists (nothing is stranded).
func TestReviveRestoresFullReplication(t *testing.T) {
	c := New(3, 1, 300*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.Seed(20); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Down to two nodes: with R=3 and 2 alive, the survivors end up holding every
	// key, including many they are not owners of once the others return.
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

	// Every key must get back onto all R of its owners with NO client writes — the
	// heal alone. Wait on the same invariant the assertions below check, or the wait
	// can exit early on leftover copies and the test flakes.
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
		// And the owners must be the holders: a key parked on two leftover nodes that
		// do not own it is "replicated" only by accident, and the next kill loses it.
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

// A revived node comes back empty, but the check-first heal repopulates it: once
// it rejoins the ring as an owner of some ranges, the primaries of those ranges
// notice it is missing the keys and push them over. Without this, a returned node
// stays empty until new writes happen to land on it.
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
	// It returns empty…
	if kc := nodeKeyCount(c.State(), victim); kc != 0 {
		t.Fatalf("a revived node should return empty, got %d keys", kc)
	}
	// …then the heal repopulates it as peers notice it owns keys it lacks.
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

// A TTL'd key must die on schedule even if the cluster re-replicated it in the
// meantime. This is the bug the whole absolute-deadline design exists to prevent:
// the heal copies a key by reading it from a holder and writing it to a new owner,
// and if that write carried a *duration* (or no deadline at all, which is what the
// wire format used to do) the fresh copy would start its life over. The key would
// then outlive its own expiry on the very replica that rescued it — and a read,
// falling back to that replica, would serve a value that should have been gone.
//
// So: write a key with a short TTL, kill its primary to force a heal, let the heal
// copy it, then wait out the ORIGINAL deadline and demand the key be gone from
// everywhere. The heal must not have extended its life.
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

	// Wait for the heal to actually place a copy on a node that did not have one,
	// so we are genuinely testing a *healed* copy and not just the originals.
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

	// Now wait out the ORIGINAL deadline. If the healed copy was given a fresh TTL,
	// it is still alive here and the read below will happily serve it.
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
