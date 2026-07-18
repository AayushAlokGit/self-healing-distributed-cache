package node

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cache"
	"github.com/AayushAlokGit/self-healing-distributed-cache/vclock"
)

// kvEntries reads every version a node holds for key straight off its internal /kv endpoint.
func kvEntries(t *testing.T, n *Node, key string) []cache.Entry {
	t.Helper()
	resp, err := http.Get("http://" + n.Addr() + "/kv/" + key)
	if err != nil {
		t.Fatalf("kv get %s: %v", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	var wires []wireEntry
	if err := json.NewDecoder(resp.Body).Decode(&wires); err != nil {
		t.Fatalf("kv get %s decode: %v", key, err)
	}
	out := make([]cache.Entry, len(wires))
	for i, wv := range wires {
		out[i] = wv.toEntry()
	}
	return out
}

// putVersioned PUTs one versioned value straight to a node's internal /kv endpoint, stamping
// the vector clock in the X-Version header the way storeOn does.
func putVersioned(t *testing.T, n *Node, key, value string, version vclock.Clock) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, "http://"+n.Addr()+"/kv/"+key, strings.NewReader(value))
	putVersion(req.Header, version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put %s = %d, want 204", key, resp.StatusCode)
	}
}

// Two concurrent versions PUT to the same node over HTTP must both survive: the X-Version
// header round-trips and handlePut reconciles instead of clobbering. A dominating write then
// collapses them back to one. This is the wire-level counterpart to the cache reconcile tests.
func TestConcurrentPutsOverHTTPKeepBothThenCollapse(t *testing.T) {
	n := start(t, "n0")

	base := vclock.Clock{"n0": 1}
	bob := base.Bump("n0")                        // {n0:2}
	carol := base.Bump("n4")                      // {n0:1, n4:1} — concurrent with bob
	winner := vclock.Merge(bob, carol).Bump("n0") // dominates both

	putVersioned(t, n, "k", "bob", bob)
	putVersioned(t, n, "k", "carol", carol)

	got := kvEntries(t, n, "k")
	values := func(es []cache.Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Value
		}
		slices.Sort(out)
		return out
	}
	if want := []string{"bob", "carol"}; !slices.Equal(values(got), want) {
		t.Fatalf("concurrent PUTs = %v, want both %v", values(got), want)
	}

	putVersioned(t, n, "k", "resolved", winner)
	if got := kvEntries(t, n, "k"); len(got) != 1 || got[0].Value != "resolved" {
		t.Fatalf("after dominating PUT = %+v, want single resolved", got)
	}
}

// A write reads the current version off the owners, bumps the coordinator's slot, and stamps
// the result. Sequential writes therefore dominate and collapse to one value — no conflict,
// because each writer saw the last write before making its own.
func TestWritesCarryIncrementingVectorClocks(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, 3, 1)

	primary := nodes[ownerOf(ids, "k")]

	// First write via n0: bumps its own slot up from nothing.
	if code := clientSet(t, nodes["n0"], "k", "v1"); code != http.StatusNoContent {
		t.Fatalf("write v1 = %d", code)
	}
	if got := kvEntries(t, primary, "k"); len(got) != 1 || got[0].Value != "v1" || got[0].Version["n0"] != 1 {
		t.Fatalf("after v1: %+v, want one {v1, n0:1}", got)
	}

	// Second write via n0: reads {n0:1}, bumps to {n0:2}, dominates, stays a single value.
	clientSet(t, nodes["n0"], "k", "v2")
	if got := kvEntries(t, primary, "k"); len(got) != 1 || got[0].Value != "v2" || got[0].Version["n0"] != 2 {
		t.Fatalf("after v2: %+v, want one {v2, n0:2}", got)
	}

	// Third write via a different coordinator n1: it reads {n0:2} first, so its write is a
	// descendant, not a conflict — {n0:2, n1:1} dominates and stays one value.
	clientSet(t, nodes["n1"], "k", "v3")
	got := kvEntries(t, primary, "k")
	if len(got) != 1 || got[0].Value != "v3" {
		t.Fatalf("after v3: %+v, want single v3", got)
	}
	if got[0].Version["n0"] != 2 || got[0].Version["n1"] != 1 {
		t.Fatalf("v3 version = %v, want {n0:2, n1:1}", got[0].Version)
	}
	if vclock.Compare(got[0].Version, vclock.Clock{"n0": 2}) != vclock.After {
		t.Fatalf("v3 (%v) should dominate the previous write", got[0].Version)
	}
}
