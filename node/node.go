// Package node is a storage node: a cache behind an HTTP server.
//
// A node is deliberately dumb. It stores what it is told and serves what it has,
// and knows nothing about the ring, replication, or the other nodes. All routing
// lives in the coordinator (Phase 3, next step), so the nodes stay simple and the
// placement logic lives in exactly one place.
package node

import (
	"context"
	"io"
	"net"
	"net/http"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cache"
)

// Node wraps a cache in an HTTP server on a localhost port.
type Node struct {
	id    string
	cache *cache.Cache
	srv   *http.Server
	addr  string // the real bound address, known only after Start
}

// New creates a node with the given id and capacity. addr may end in ":0" to let
// the OS pick a free port, which Addr reports back after Start. Call Close.
func New(id, addr string, capacity int) *Node {
	n := &Node{
		id:    id,
		cache: cache.New(capacity),
		addr:  addr,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /kv/{key}", n.handleGet)
	mux.HandleFunc("PUT /kv/{key}", n.handlePut)
	n.srv = &http.Server{Handler: mux}

	return n
}

// Start binds the port and serves in the background. Split from New so the port
// is live before any request is routed, and so Addr is knowable immediately.
func (n *Node) Start() error {
	ln, err := net.Listen("tcp", n.addr)
	if err != nil {
		return err
	}
	n.addr = ln.Addr().String() // resolve ":0" to the real port

	go n.srv.Serve(ln)
	return nil
}

// Addr is the node's bound address, e.g. "127.0.0.1:53187".
func (n *Node) Addr() string { return n.addr }

// ID is the node's ring identity.
func (n *Node) ID() string { return n.id }

// Close stops the server and the cache's sweeper. "Stop the users, then stop the
// thing they use": shut the HTTP server down first so no handler is mid-flight,
// then close the cache.
func (n *Node) Close() error {
	err := n.srv.Shutdown(context.Background())
	n.cache.Close()
	return err
}

// handleGet returns the value for {key}, or 404 if absent or expired. A cache
// miss and a missing key are the same 404: the caller cannot tell them apart.
func (n *Node) handleGet(w http.ResponseWriter, r *http.Request) {
	value, ok := n.cache.Get(r.PathValue("key"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	io.WriteString(w, value)
}

// handlePut stores the request body as the value for {key}. No TTL over the wire
// yet; entries are permanent until evicted.
func (n *Node) handlePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	n.cache.Set(r.PathValue("key"), string(body), 0)
	w.WriteHeader(http.StatusNoContent)
}
