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
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cache"
	"github.com/AayushAlokGit/self-healing-distributed-cache/ring"
)

// forwardTimeout bounds a node-to-node call. A dead owner must fail fast so the
// coordinating node can answer (or fall back to a replica) rather than hang.
const forwardTimeout = 2 * time.Second

const (
	// Copies per key: the primary plus the next R-1 distinct nodes clockwise.
	defaultReplicationFactor = 3

	// Acks required before a write returns to the client. A knob, not consensus:
	// W=1 favors availability (fast, may lose a write if that one node dies);
	// larger W trades latency and availability for durability. W>R is impossible.
	defaultWriteQuorum = 1

	// How often a node pings every peer's /health.
	defaultHeartbeatInterval = 100 * time.Millisecond

	// Silence longer than this makes a peer suspected dead and dropped from the
	// ring. The core knob: shorter = faster detection but more false positives
	// (a GC pause looks like death); longer = fewer false positives but the ring
	// routes to a corpse for longer. 5 missed beats.
	defaultFailureTimeout = 500 * time.Millisecond
)

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

	replicationFactor int
	writeQuorum       int

	heartbeatInterval time.Duration
	failureTimeout    time.Duration
	healthClient      *http.Client // short timeout; a slow ping is a missed beat
	done              chan struct{}
	wg                sync.WaitGroup

	// healTrigger is a coalescing signal, not a queue: buffered to 1 and sent to
	// non-blocking, so a burst of membership changes schedules exactly one heal
	// pass (which re-asserts the whole replication invariant anyway).
	healTrigger chan struct{}

	// healCopies counts key copies pushed during heals, cumulative. It climbing
	// while nothing actually died is the re-replication storm, made countable.
	healCopies atomic.Int64

	// healthPaused stalls this node's /health responses without stopping the rest
	// of it: a stand-in for a GC pause so a demo can show a live node being falsely
	// declared dead. Atomic, not mutex-guarded, so it stays off the hot read path.
	healthPaused atomic.Bool

	// membership is this node's own view. peers is every node ever known (static
	// for now, gossiped in future); the ring holds only those currently believed
	// alive, so routing never targets a node this view thinks is dead. Guarded
	// because handlers and the heartbeat loop touch it from many goroutines.
	mu       sync.RWMutex
	ring     *ring.Ring
	peers    map[string]string    // node id -> HTTP address (all known)
	lastSeen map[string]time.Time // last successful health ping
	alive    map[string]bool      // this node's current view
}

// New creates a node with the given id and capacity. addr may end in ":0" to let
// the OS pick a free port, which Addr reports back after Start. Call Close.
func New(id, addr string, capacity int) *Node {
	n := &Node{
		id:                id,
		cache:             cache.New(capacity),
		addr:              addr,
		client:            &http.Client{Timeout: forwardTimeout},
		peers:             map[string]string{},
		lastSeen:          map[string]time.Time{},
		alive:             map[string]bool{},
		replicationFactor: defaultReplicationFactor,
		writeQuorum:       defaultWriteQuorum,
		heartbeatInterval: defaultHeartbeatInterval,
		failureTimeout:    defaultFailureTimeout,
		healthClient:      &http.Client{Timeout: defaultFailureTimeout},
		done:              make(chan struct{}),
		healTrigger:       make(chan struct{}, 1),
	}

	mux := http.NewServeMux()
	// Internal: node-to-node storage and liveness.
	mux.HandleFunc("GET /kv/{key}", n.handleGet)
	mux.HandleFunc("PUT /kv/{key}", n.handlePut)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		if n.healthPaused.Load() {
			// Stall past any pinger's timeout so the ping fails, but wake on Close
			// so Shutdown is never blocked by a sleeping handler.
			select {
			case <-n.done:
			case <-time.After(3 * n.failureTimeout):
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	// Client-facing: any node coordinates.
	mux.HandleFunc("GET /get/{key}", n.handleClientGet)
	mux.HandleFunc("PUT /set/{key}", n.handleClientSet)
	n.srv = &http.Server{Handler: mux}

	return n
}

// SetMembership installs the set of known peers (id -> address, including self).
// Everyone starts believed alive and on the ring; the heartbeat loop demotes the
// silent. Safe to call while serving.
func (n *Node) SetMembership(peers map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	n.peers = peers
	n.lastSeen = make(map[string]time.Time, len(peers))
	n.alive = make(map[string]bool, len(peers))
	r := ring.New()
	for id := range peers {
		n.lastSeen[id] = now
		n.alive[id] = true
		r.Add(id)
	}
	n.alive[n.id] = true // a node never suspects itself
	n.ring = r
}

// SetReplication overrides the replication factor and write quorum. For tests
// that want R and W different from the defaults.
func (n *Node) SetReplication(replicationFactor, writeQuorum int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.replicationFactor = replicationFactor
	n.writeQuorum = writeQuorum
}

type owner struct{ id, addr string }

// ownersFor returns the key's R owners in ring order (primary first), per this
// node's current view. Nil if the ring is unset.
func (n *Node) ownersFor(key string) []owner {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.ring == nil {
		return nil
	}
	ids := n.ring.GetClockwiseN(key, n.replicationFactor)
	owners := make([]owner, len(ids))
	for i, id := range ids {
		owners[i] = owner{id: id, addr: n.peers[id]}
	}
	return owners
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
	n.wg.Go(n.heartbeatLoop)
	n.wg.Go(n.healLoop)
	return nil
}

// heartbeatLoop pings every peer on a tick and updates this node's alive view,
// until Close. Ticker, not Sleep, so Close is not blocked for a full interval.
func (n *Node) heartbeatLoop() {
	ticker := time.NewTicker(n.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-n.done:
			return
		case <-ticker.C:
			n.heartbeatRound()
		}
	}
}

// heartbeatRound pings all peers outside the lock (network I/O), then takes the
// lock once to record who answered and reconcile the alive view against the
// failure timeout. A transition flips the peer's ring membership, so ownership
// recomputes to route around the dead (and back to the recovered).
func (n *Node) heartbeatRound() {
	n.mu.RLock()
	targets := make(map[string]string, len(n.peers))
	for id, addr := range n.peers {
		if id != n.id {
			targets[id] = addr
		}
	}
	n.mu.RUnlock()

	answered := make(map[string]bool, len(targets))
	for id, addr := range targets {
		if n.pingHealth(addr) {
			answered[id] = true
		}
	}

	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	for id := range answered {
		n.lastSeen[id] = now
	}
	membershipChanged := false
	for id := range n.peers {
		if id == n.id {
			continue
		}
		isAlive := now.Sub(n.lastSeen[id]) <= n.failureTimeout
		switch {
		case n.alive[id] && !isAlive:
			n.alive[id] = false
			n.ring.Remove(id) // stop routing to the corpse
			membershipChanged = true
		case !n.alive[id] && isAlive:
			n.alive[id] = true
			n.ring.Add(id) // it came back
			membershipChanged = true
		}
	}

	// Ownership just shifted, so some ranges may be under-replicated (a death) or
	// owe a copy to a returned node (a recovery). Kick the heal — either way the
	// fix is the same: re-assert the replication invariant. Non-blocking so the
	// heartbeat loop never stalls on it; coalescing so a burst schedules one pass.
	if membershipChanged {
		select {
		case n.healTrigger <- struct{}{}:
		default:
		}
	}
}

// pingHealth reports whether addr answered its /health within the health client's
// timeout. A slow answer is a missed beat, indistinguishable from death.
func (n *Node) pingHealth(addr string) bool {
	resp, err := n.healthClient.Get("http://" + addr + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// healLoop waits for a membership change to signal that ownership shifted, then
// re-replicates. Separate from the heartbeat loop on purpose: a heal copies data
// over the network and can be slow, and the heartbeat must keep pinging or it
// would start declaring more false deaths while stuck healing.
func (n *Node) healLoop() {
	for {
		select {
		case <-n.done:
			return
		case <-n.healTrigger:
			n.heal()
		}
	}
}

// heal re-asserts the replication invariant: for every key this node is the
// primary owner of, push a copy to that key's other current owners. When a death
// promotes a new owner, this is what actually moves the bytes so the range is
// back to R live copies. Idempotent — a co-owner that already has the key just
// overwrites it.
//
// Naive on purpose (Phase 5 step 1): it re-pushes every key it is primary of, not
// only those the dead node held, and pushes to co-owners that already have a copy.
// Both are wasted sends — the re-replication storm step 2 will measure.
func (n *Node) heal() {
	for key, value := range n.cache.Snapshot() {
		select {
		case <-n.done:
			return // stop promptly on Close rather than finish a long scan
		default:
		}

		owners := n.ownersFor(key)
		// Only the primary coordinates a key's heal, so co-owners don't all push
		// the same key to the same target.
		if len(owners) == 0 || owners[0].id != n.id {
			continue
		}
		for _, o := range owners[1:] {
			n.storeOn(o.addr, key, value) // naive: no retry; a failed copy waits for the next heal
			n.healCopies.Add(1)
		}
	}
}

// PauseHealth stalls (or resumes) this node's /health responses. The node keeps
// serving everything else — it is a live node that merely looks silent, so peers
// with a short timeout will falsely declare it dead. For the false-positive demo.
func (n *Node) PauseHealth(paused bool) { n.healthPaused.Store(paused) }

// AlivePeers is this node's current view of who is up, for tests and the
// eventual dashboard.
func (n *Node) AlivePeers() map[string]bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	view := make(map[string]bool, len(n.alive))
	maps.Copy(view, n.alive)
	return view
}

// HealCopies is the cumulative number of key copies this node has pushed during
// heals, for tests and the dashboard. A climbing count with no real death is the
// re-replication storm — the cost of the detector guessing wrong.
func (n *Node) HealCopies() int64 { return n.healCopies.Load() }

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
		close(n.done) // stop the heartbeat loop first
		n.wg.Wait()
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

// handleClientGet coordinates a read: try the key's owners in ring order and
// return the first reachable hit. An unreachable owner is skipped — that is the
// fallback that keeps reads serving after a node dies. Only when every owner is
// unreachable do we 502; a reachable miss is an honest 404.
func (n *Node) handleClientGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	owners := n.ownersFor(key)
	if len(owners) == 0 {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	reachedNone := true
	for _, o := range owners {
		var (
			v   string
			ok  bool
			err error
		)
		if o.id == n.id {
			v, ok = n.cache.Get(key) // local read never "fails to reach"
		} else {
			v, ok, err = n.fetchFrom(o.addr, key)
		}
		if err != nil {
			continue // owner unreachable: fall back to the next
		}
		reachedNone = false
		if ok {
			io.WriteString(w, v)
			return
		}
	}

	if reachedNone {
		http.Error(w, "all owners unreachable", http.StatusBadGateway)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// handleClientSet writes to all R owners and acks once writeQuorum of them
// succeed. Writing to every owner is what puts the copies in place for the read
// fallback; the quorum only decides when to answer the client.
func (n *Node) handleClientSet(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key, value := r.PathValue("key"), string(body)

	owners := n.ownersFor(key)
	if len(owners) == 0 {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	acks := 0
	for _, o := range owners {
		if o.id == n.id {
			n.cache.Set(key, value, 0)
			acks++
			continue
		}
		if err := n.storeOn(o.addr, key, value); err == nil {
			acks++
		}
	}

	if acks < n.writeQuorum {
		http.Error(w, "write quorum not met", http.StatusBadGateway)
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
