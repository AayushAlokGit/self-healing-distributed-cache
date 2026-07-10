package node

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/AayushAlokGit/self-healing-distributed-cache/ring"
)

// startCluster brings up len(ids) nodes and gives each its own ring built from
// all the ids plus the shared peer map. Each node holds an independent ring, as
// it will once gossip (Phase 4) maintains per-node views.
func startCluster(t *testing.T, ids ...string) map[string]*Node {
	t.Helper()
	nodes := make(map[string]*Node, len(ids))
	peers := make(map[string]string, len(ids))
	for _, id := range ids {
		n := start(t, id)
		nodes[id] = n
		peers[id] = n.Addr()
	}
	for _, n := range nodes {
		r := ring.New()
		for _, id := range ids {
			r.Add(id)
		}
		n.SetMembership(r, peers)
	}
	return nodes
}

func clientSet(t *testing.T, n *Node, key, value string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, "http://"+n.Addr()+"/set/"+key, strings.NewReader(value))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client set %s via %s: %v", key, n.ID(), err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func clientGet(t *testing.T, n *Node, key string) (string, int) {
	t.Helper()
	resp, err := http.Get("http://" + n.Addr() + "/get/" + key)
	if err != nil {
		t.Fatalf("client get %s via %s: %v", key, n.ID(), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

// ownerOf computes the owner id the way the cluster does, so tests can find and
// kill the exact node holding a key.
func ownerOf(ids []string, key string) string {
	r := ring.New()
	for _, id := range ids {
		r.Add(id)
	}
	return r.Get(key)
}

// The point of the coordinating role: a client can hit ANY node with ANY key and
// get the right answer, because that node routes to the owner. Written through
// one node, read back through every node.
func TestAnyNodeRoutesToOwner(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)
	const keys = 300

	for i := range keys {
		if code := clientSet(t, nodes["n0"], "key:"+strconv.Itoa(i), "v"+strconv.Itoa(i)); code != http.StatusNoContent {
			t.Fatalf("set key %d returned %d", i, code)
		}
	}
	for i := range keys {
		via := nodes[ids[i%len(ids)]]
		key, want := "key:"+strconv.Itoa(i), "v"+strconv.Itoa(i)
		if v, code := clientGet(t, via, key); code != http.StatusOK || v != want {
			t.Fatalf("get %q via %s = (%q, %d), want (%q, 200)", key, via.ID(), v, code, want)
		}
	}
}

// The failure that earns replication. At R=1 a key lives on exactly one node, so
// killing that node loses the key — no copy exists to fall back to. Any survivor
// still routes there and gets a 502, not the value.
func TestKillingOwnerLosesDataAtR1(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)

	const key, value = "user:42", "alice"
	clientSet(t, nodes["n0"], key, value)

	if v, code := clientGet(t, nodes["n1"], key); code != http.StatusOK || v != value {
		t.Fatalf("precondition: key should be reachable, got (%q, %d)", v, code)
	}

	owner := ownerOf(ids, key)
	nodes[owner].Close() // kill it

	var survivor *Node
	for id, n := range nodes {
		if id != owner {
			survivor = n
			break
		}
	}

	_, code := clientGet(t, survivor, key)
	if code == http.StatusOK {
		t.Fatalf("expected data loss at R=1 after killing owner %s, but the key came back", owner)
	}
	t.Logf("R=1: killed owner %s of %q -> survivor returns %d, data is gone. This is what replication fixes.",
		owner, key, code)
}
