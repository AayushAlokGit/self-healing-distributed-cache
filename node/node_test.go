package node

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// start brings up a node on an OS-chosen port and returns it, failing the test
// if the port cannot be bound.
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

func get(t *testing.T, n *Node, key string) (string, int) {
	t.Helper()
	resp, err := http.Get("http://" + n.Addr() + "/kv/" + key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
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

// A node's address is live the moment Start returns, so a coordinator can route
// to it without a race against the server goroutine.
func TestAddrIsBoundAfterStart(t *testing.T) {
	n := start(t, "n0")
	if n.Addr() == "127.0.0.1:0" || !strings.HasPrefix(n.Addr(), "127.0.0.1:") {
		t.Fatalf("Addr not resolved to a real port: %q", n.Addr())
	}
}
