package node

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/AayushAlokGit/self-healing-distributed-cache/ring"
)

// startCluster brings up len(ids) nodes, each with its own independent ring built
// from all the ids.
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
		n.SetMembership(peers)
	}
	return nodes
}

func setReplication(nodes map[string]*Node, rf, wq int) {
	for _, n := range nodes {
		n.SetReplication(rf, wq)
	}
}

// ownersOf computes a key's R owners the way the cluster does, primary first.
func ownersOf(ids []string, key string, rf int) []string {
	r := ring.New()
	for _, id := range ids {
		r.Add(id)
	}
	return r.GetClockwiseN(key, rf)
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

// ownerOf computes the owner id the way the cluster does, so a test can kill the
// exact node holding a key.
func ownerOf(ids []string, key string) string {
	r := ring.New()
	for _, id := range ids {
		r.Add(id)
	}
	return r.Get(key)
}

// A client can hit any node with any key and get the right answer: the node it hit
// routes to the owner. Written through one node, read back through every node.
func TestAnyNodeRoutesToOwner(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, 1, 1) // one owner per key, so reads truly forward
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

// The failure that earns replication: at R=1 a key lives on one node, so killing it
// loses the key. No copy exists to fall back to.
func TestKillingOwnerLosesDataAtR1(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, 1, 1)

	const key, value = "user:42", "alice"
	clientSet(t, nodes["n0"], key, value)

	if v, code := clientGet(t, nodes["n1"], key); code != http.StatusOK || v != value {
		t.Fatalf("precondition: key should be reachable, got (%q, %d)", v, code)
	}

	owner := ownerOf(ids, key)
	nodes[owner].Close()

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

// At R=3 reads keep serving as owners die: the coordinator falls back down the replica
// list, and the key is lost only when the last of the three is gone. R copies tolerate
// R-1 deaths.
func TestReadFallbackSurvivesNodeDeaths(t *testing.T) {
	const rf = 3
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	nodes := startCluster(t, ids...)
	setReplication(nodes, rf, 1)

	const key, value = "user:42", "alice"
	clientSet(t, nodes["n0"], key, value)

	owners := ownersOf(ids, key, rf)
	ownerSet := map[string]bool{}
	for _, o := range owners {
		ownerSet[o] = true
	}

	// Read through a node that owns nothing, so the coordinator survives every kill and
	// every read is a real fallback across the network.
	var coord *Node
	for id, n := range nodes {
		if !ownerSet[id] {
			coord = n
			break
		}
	}

	for killed := range owners {
		if v, code := clientGet(t, coord, key); code != http.StatusOK || v != value {
			t.Fatalf("with %d of %d owners dead, read failed: (%q, %d)", killed, len(owners), v, code)
		}
		nodes[owners[killed]].Close()
	}

	if _, code := clientGet(t, coord, key); code == http.StatusOK {
		t.Fatalf("all %d owners dead, but the key still came back", len(owners))
	}
	t.Logf("R=%d: reads survived %d owner deaths; key lost only when all %d owners were gone",
		rf, len(owners)-1, len(owners))
}
