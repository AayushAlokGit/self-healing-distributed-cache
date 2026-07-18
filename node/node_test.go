package node

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// start brings up a node on an OS-chosen port, failing the test if it cannot bind.
func start(t *testing.T, id string) *Node {
	t.Helper()
	n := New(id, "127.0.0.1:0", 1000)
	if err := n.Start(); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func put(t *testing.T, n *Node, key, value string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, "http://"+n.Addr()+"/kv/"+key, strings.NewReader(value))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// get reads /kv, which now returns a JSON array of versions. It returns the first value,
// which is all the single-value tests need.
func get(t *testing.T, n *Node, key string) (string, int) {
	t.Helper()
	resp, err := http.Get("http://" + n.Addr() + "/kv/" + key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode
	}
	var wires []wireEntry
	if err := json.NewDecoder(resp.Body).Decode(&wires); err != nil {
		t.Fatalf("get %s: decode: %v", key, err)
	}
	if len(wires) == 0 {
		return "", resp.StatusCode
	}
	return wires[0].Value, resp.StatusCode
}

func TestPutThenGet(t *testing.T) {
	n := start(t, "n0")

	if code := put(t, n, "user:42", "alice"); code != http.StatusNoContent {
		t.Fatalf("PUT returned %d, want 204", code)
	}
	if v, code := get(t, n, "user:42"); code != http.StatusOK || v != "alice" {
		t.Fatalf("GET returned (%q, %d), want (\"alice\", 200)", v, code)
	}
}

func TestMissingKeyIs404(t *testing.T) {
	n := start(t, "n0")
	if _, code := get(t, n, "ghost"); code != http.StatusNotFound {
		t.Fatalf("GET of absent key returned %d, want 404", code)
	}
}

func TestOverwrite(t *testing.T) {
	n := start(t, "n0")
	put(t, n, "k", "v1")
	put(t, n, "k", "v2")
	if v, _ := get(t, n, "k"); v != "v2" {
		t.Fatalf("after overwrite got %q, want v2", v)
	}
}

// Addr must be live the moment Start returns, so a coordinator can route to it
// without racing the server goroutine.
func TestAddrIsBoundAfterStart(t *testing.T) {
	n := start(t, "n0")
	if n.Addr() == "127.0.0.1:0" || !strings.HasPrefix(n.Addr(), "127.0.0.1:") {
		t.Fatalf("Addr not resolved to a real port: %q", n.Addr())
	}
}
