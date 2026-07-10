// Package node is a cluster peer: a cache behind an HTTP server that also holds
// its own view of the ring and routes requests.
//
// There is no central coordinator (HLD §3: no single point of failure, all peers
// equal — a coordinator would need consensus to be fault-tolerant, which is CP;
// we are AP). Instead every node accepts any client key on /get and /set and
// coordinates that one request, forwarding to the owner's internal /kv endpoint.
// Each node's membership view is its own, injected for now and gossiped in Phase 4.
package node

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cache"
	"github.com/AayushAlokGit/self-healing-distributed-cache/ring"
)

// forwardTimeout bounds a node-to-node call. A dead owner must fail fast so the
// coordinating node can answer (or, once replicated, fall back) rather than hang.
const forwardTimeout = 2 * time.Second

// Node is a peer in the cluster: a cache (Store Engine), an HTTP server, and its
// own view of the ring and peer addresses. Any node accepts any client key and
// coordinates that one request — there is no central coordinator (HLD §3).
type Node struct {
	id        string
	cache     *cache.Cache
	srv       *http.Server
	addr      string // the real bound address, known only after Start
	client    *http.Client
	closeOnce sync.Once

	// membership is this node's own view, injected at setup and (Phase 4)
	// updated by gossip. Guarded because handlers read it from many goroutines.
	mu    sync.RWMutex
	ring  *ring.Ring
	peers map[string]string // node id -> HTTP address
}

// New creates a node with the given id and capacity. addr may end in ":0" to let
// the OS pick a free port, which Addr reports back after Start. Call Close.
func New(id, addr string, capacity int) *Node {
	n := &Node{
		id:     id,
		cache:  cache.New(capacity),
		addr:   addr,
		client: &http.Client{Timeout: forwardTimeout},
		peers:  map[string]string{},
	}

	mux := http.NewServeMux()
	// Internal: node-to-node storage.
	mux.HandleFunc("GET /kv/{key}", n.handleGet)
	mux.HandleFunc("PUT /kv/{key}", n.handlePut)
	// Client-facing: any node coordinates.
	mux.HandleFunc("GET /get/{key}", n.handleClientGet)
	mux.HandleFunc("PUT /set/{key}", n.handleClientSet)
	n.srv = &http.Server{Handler: mux}

	return n
}

// SetMembership installs this node's ring and peer map. Static for now; Phase 4's
// gossip replaces the whole call. Safe to call while the node is serving.
func (n *Node) SetMembership(r *ring.Ring, peers map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ring = r
	n.peers = peers
}

// ownerFor returns the id and address of the node that owns key, per this node's
// current view. addr is "" if the owner is unknown (missing from the peer map).
func (n *Node) ownerFor(key string) (id, addr string) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.ring == nil {
		return "", ""
	}
	id = n.ring.Get(key)
	return id, n.peers[id]
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
	var err error
	n.closeOnce.Do(func() {
		err = n.srv.Shutdown(context.Background())
		n.cache.Close()
	})
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

// handleClientGet coordinates a read: find the owner and fetch from it, reading
// this node's own cache directly when it is the owner. R=1 for now, so a dead
// owner means the key is unreachable — the failure that earns replication.
func (n *Node) handleClientGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	id, addr := n.ownerFor(key)
	if id == "" {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	if id == n.id {
		if v, ok := n.cache.Get(key); ok {
			io.WriteString(w, v)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	v, ok, err := n.fetchFrom(addr, key)
	if err != nil {
		http.Error(w, "owner unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	io.WriteString(w, v)
}

// handleClientSet coordinates a write to the key's owner.
func (n *Node) handleClientSet(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key, value := r.PathValue("key"), string(body)

	id, addr := n.ownerFor(key)
	if id == "" {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	if id == n.id {
		n.cache.Set(key, value, 0)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := n.storeOn(addr, key, value); err != nil {
		http.Error(w, "owner unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fetchFrom GETs key from another node's internal /kv endpoint. ok is false on a
// clean 404 (miss); err is non-nil only when the node could not be reached.
func (n *Node) fetchFrom(addr, key string) (value string, ok bool, err error) {
	resp, err := n.client.Get("http://" + addr + "/kv/" + key)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}
	return string(body), true, nil
}

// storeOn PUTs key=value to another node's internal /kv endpoint.
func (n *Node) storeOn(addr, key, value string) error {
	req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/kv/"+key, strings.NewReader(value))
	if err != nil {
		return err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
