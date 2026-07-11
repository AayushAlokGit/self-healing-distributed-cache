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
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
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

	// A death is re-routed immediately (cheap, reversible), but re-replication
	// waits this long before committing to the expensive copy, then rechecks: a
	// brief false positive (a GC pause) recovers inside the window and is skipped
	// entirely. The price is the universal one — a genuine death sits
	// under-replicated this much longer before it heals. Decoupling the two
	// reactions is the point: convict cheaply on suspicion, copy only on conviction.
	defaultHealGracePeriod = 1 * time.Second

	// Heal records held before the oldest are dropped. Only matters if nothing is
	// draining them (no dashboard attached); the log is for display, not durability.
	maxHealLog = 64
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

	// log is swapped in by whoever runs the node (the cluster), and discards until
	// then: a library that owns a logger cannot be silenced, and these logs would
	// otherwise spray heartbeat noise through every `go test` run.
	//
	// atomic.Pointer, not a plain field: the heartbeat and heal goroutines read this
	// while SetLogger may still be writing it, which is a data race — and a plain
	// mutex here would mean taking a lock on a path whose only job is to log.
	log atomic.Pointer[slog.Logger]

	replicationFactor int
	writeQuorum       int
	ringReplicas      int // virtual points per node; 0 uses the ring's default (150)

	heartbeatInterval time.Duration
	failureTimeout    time.Duration
	healGracePeriod   time.Duration // guarded by mu; wait before the expensive heal
	healthClient      *http.Client  // short timeout; a slow ping is a missed beat
	done              chan struct{}
	wg                sync.WaitGroup

	// healTrigger is a coalescing signal, not a queue: buffered to 1 and sent to
	// non-blocking, so a burst of membership changes schedules exactly one heal
	// pass (which re-asserts the whole replication invariant anyway).
	healTrigger chan struct{}

	// healCopies counts key copies pushed during heals, cumulative. It climbing
	// while nothing actually died is the re-replication storm, made countable.
	healCopies atomic.Int64

	// healLog records what each heal pass actually moved, so the dashboard can show
	// "n1 → n4: key:3, key:7" rather than just a copy count. The node keeps its own
	// record and the manager drains it: a callback into the manager would deadlock,
	// since Kill holds the manager's lock while Close waits for this goroutine.
	// It is also the honest shape — a real node knows what it copied and would emit
	// exactly this; the manager is only a collector.
	healLogMu  sync.Mutex
	healLog    []HealCopy
	healCauses []string // membership changes seen since the last heal pass

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
		healGracePeriod:   defaultHealGracePeriod,
		healthClient:      &http.Client{Timeout: defaultFailureTimeout},
		done:              make(chan struct{}),
		healTrigger:       make(chan struct{}, 1),
	}
	n.SetLogger(nil) // discard until someone wires a real one

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

// SetLogger installs the logger this node writes to. Every record it emits is
// tagged node=<id>, so one file holding all five nodes' logs can still be read one
// node at a time. A nil logger discards, which is the default.
func (n *Node) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	n.log.Store(l.With("node", n.id))
}

// logger is the current logger. Never nil — New installs a discarding one.
func (n *Node) logger() *slog.Logger { return n.log.Load() }

// SetMembership installs the set of known peers (id -> address, including self).
// Everyone starts believed alive and on the ring; the heartbeat loop demotes the
// silent. Safe to call while serving.
func (n *Node) SetMembership(peers map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	// Clone, don't alias: the caller (the cluster) hands the same map to every
	// node, and SetPeerAddr mutates it. Each node's peers must be its own, guarded
	// only by its own mutex, or one node's write races another's heartbeat read.
	n.peers = maps.Clone(peers)
	n.lastSeen = make(map[string]time.Time, len(peers))
	n.alive = make(map[string]bool, len(peers))
	r := n.newRingLocked()
	for id := range peers {
		n.lastSeen[id] = now
		n.alive[id] = true
		r.Add(id)
	}
	n.alive[n.id] = true // a node never suspects itself
	n.ring = r
}

// newRingLocked builds a ring using this node's configured virtual-point count,
// or the ring package default when unset. Caller holds n.mu.
func (n *Node) newRingLocked() *ring.Ring {
	if n.ringReplicas > 0 {
		return ring.NewWithReplicas(n.ringReplicas)
	}
	return ring.New()
}

// SetRingReplicas sets how many virtual points each node contributes to this
// node's ring. The demo turns this down so the ring's arcs are big enough to see;
// the default (ring.New, ~150) stays optimal for real balance. Call before
// SetMembership, which is what actually builds the ring.
func (n *Node) SetRingReplicas(k int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ringReplicas = k
}

// SetPeerAddr updates the address for one peer without resetting liveness or the
// ring (unlike SetMembership, which rebuilds the whole view). A revived node comes
// back on a fresh port; its peers call this so the next heartbeat can reach it and
// re-admit it. An unknown id is simply added to the known-peers map.
func (n *Node) SetPeerAddr(id, addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.peers[id] = addr
}

// SetReplication overrides the replication factor and write quorum. For tests
// that want R and W different from the defaults.
func (n *Node) SetReplication(replicationFactor, writeQuorum int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.replicationFactor = replicationFactor
	n.writeQuorum = writeQuorum
}

// SetHealGracePeriod overrides how long a detected death waits before the node
// re-replicates. Zero heals immediately (the naive behavior). For tests that want
// to show the storm (0) versus the fix (a real grace period).
func (n *Node) SetHealGracePeriod(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.healGracePeriod = d
}

// gracePeriod reads the heal grace period under the lock, since a setter may
// change it while healLoop is running.
func (n *Node) gracePeriod() time.Duration {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.healGracePeriod
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

	n.logger().Info("node started",
		"addr", n.addr,
		"heartbeat", n.heartbeatInterval,
		"failure_timeout", n.failureTimeout,
		"heal_grace", n.gracePeriod(),
	)
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
		silence := now.Sub(n.lastSeen[id])
		isAlive := silence <= n.failureTimeout
		switch {
		case n.alive[id] && !isAlive:
			n.alive[id] = false
			n.ring.Remove(id) // stop routing to the corpse — cheap, reversible
			membershipChanged = true
			n.noteHealCause(id + " went silent")
			n.logger().Warn("peer suspected dead, removed from ring",
				"peer", id,
				"silent_for", silence.Round(time.Millisecond),
				"failure_timeout", n.failureTimeout,
			)
		case !n.alive[id] && isAlive:
			n.alive[id] = true
			n.ring.Add(id) // it came back — reroute, and repopulate it (see below)
			membershipChanged = true
			n.noteHealCause(id + " came back")
			n.logger().Info("peer recovered, re-added to ring", "peer", id)
		}
	}

	// Any membership change may leave a key's owners out of sync with its holders:
	// a death promotes a new owner that lacks the data, a recovery re-admits a node
	// that came back empty. Either way, kick the heal. It is safe to fire on a
	// recovery too because the heal is check-first (see heal): it copies only what
	// an owner is actually missing, so a node that merely flapped — and still holds
	// all its data — costs zero copies. Non-blocking so the heartbeat loop never
	// stalls; coalescing so a burst schedules one pass; grace-delayed in healLoop.
	if membershipChanged {
		select {
		case n.healTrigger <- struct{}{}:
		default:
			// The trigger already held a pending signal, so a heal is coming anyway.
			n.logger().Debug("heal already pending, coalescing")
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
			// The re-route already happened (cheap, reversible). Wait out a grace
			// period before the expensive re-replication so a brief false positive
			// (a GC pause) recovers first — by the time this fires, a flapped node
			// is back in the ring holding all its data, so the check-first heal
			// finds nothing missing and copies nothing.
			grace := n.gracePeriod()
			n.logger().Info("heal scheduled, waiting out grace period", "grace", grace)
			select {
			case <-n.done:
				return
			case <-time.After(grace):
			}
			n.heal()
		}
	}
}

// heal re-asserts the replication invariant: for every key this node holds, make
// sure each of that key's current owners has a copy. When a death promotes a new
// owner, or a recovery re-admits an empty node, this is what moves the bytes.
//
// Check-first: before copying, it asks whether the owner already has the key and
// skips it if so. That does two jobs at once — a node that merely flapped (still
// holding its data) costs zero copies, and a genuinely returned node is
// repopulated — without any special-casing of death vs. recovery.
//
// WHO does the pushing is the subtle part, and "the primary does it" — the rule
// this used to have — is WRONG, in a way that strands keys forever:
//
//	A key can only be healed by a node that BOTH holds it AND is allowed to push.
//	Tie the permission to being the primary, and a revived node that comes back
//	empty and is promoted straight back to primary of its own arcs can never be
//	repopulated: the primary has nothing to send, and the nodes that do have the
//	key are not the primary, so they stand down. Nobody is both. The key sits
//	under-replicated until a client happens to rewrite it.
//
// The fix: permission follows the DATA, not the position. The healer for a key is
// the first owner, in ring order, that actually holds it — so exactly one node
// pushes (no duplicate sends), and a pusher exists whenever anybody has the data.
// A node ranked below a holder stands down; a node ranked above one, or holding a
// key no owner has at all (a leftover from an older ring), steps up.
func (n *Node) heal() {
	// Batched by target: one pass that pushes 8 keys to n4 is one record, not
	// eight. A per-key record would flood the dashboard's log and push the kill
	// event that caused the heal out of view.
	moved := map[string][]string{}

	start := time.Now()
	snapshot := n.cache.Snapshot()
	causes := n.drainHealCauses() // what my heartbeat saw that made this pass happen
	var healerFor, deferred, alreadyPresent, unreachable, failed int

	log := n.logger()
	log.Debug("heal started", "keys_held", len(snapshot), "cause", strings.Join(causes, "; "))

	for key, entry := range snapshot {
		select {
		case <-n.done:
			log.Debug("heal aborted: node closing", "copies", len(moved))
			return // stop promptly on Close rather than finish a long scan
		default:
		}

		owners := n.ownersFor(key)
		if len(owners) == 0 {
			continue // no live owner at all: nowhere to send it
		}

		// My rank among this key's owners, or last if I am not an owner — a holder
		// left over from an older ring still has to be able to heal, or a key whose
		// owners ALL lack it would have no sender at all.
		rank := len(owners)
		for i, o := range owners {
			if o.id == n.id {
				rank = i
				break
			}
		}

		// Stand down if any owner ahead of me holds the key: that node is the healer,
		// and two senders would mean duplicate copies of every key.
		standDown := false
		for _, o := range owners[:min(rank, len(owners))] {
			if _, has, err := n.fetchFrom(o.addr, key); err == nil && has {
				standDown = true
				break
			}
		}
		if standDown {
			deferred++
			log.Debug("heal: stood down, an owner ahead of me holds this key", "key", key)
			continue
		}

		healerFor++
		for _, o := range owners {
			if o.id == n.id {
				continue // I have it; that's why I'm the healer
			}
			_, has, err := n.fetchFrom(o.addr, key)
			switch {
			case err != nil:
				unreachable++ // can't copy to a node we can't reach; a later pass retries
				log.Debug("heal: owner unreachable, key stays under-replicated", "key", key, "owner", o.id, "err", err)
				continue
			case has:
				alreadyPresent++ // check-first paying off: no copy needed
				log.Debug("heal: owner already has key, no copy", "key", key, "owner", o.id)
				continue
			}
			// entry.Expires, not a fresh TTL: the copy inherits the deadline the key
			// already had, so a healed replica dies at the same instant as the original
			// rather than being handed a new lease on life by its own rescue.
			if err := n.storeOn(o.addr, key, entry.Value, entry.Expires); err != nil {
				failed++
				log.Warn("heal: copy failed, key still under-replicated", "key", key, "owner", o.id, "err", err)
				continue // record only copies that actually landed
			}
			n.healCopies.Add(1)
			moved[o.id] = append(moved[o.id], key)
			log.Debug("heal: copied key", "key", key, "to", o.id)
		}
	}

	copies := 0
	for _, keys := range moved {
		copies += len(keys)
	}

	// A pass that copied nothing is the healthy case — a flap the grace period
	// absorbed, or nothing owed. It is not news, so it stays at Debug; only a pass
	// that actually moved bytes gets an Info line.
	attrs := []any{
		"copies", copies,
		"targets", len(moved),
		"healer_for", healerFor,
		"stood_down", deferred,
		"already_present", alreadyPresent,
		"unreachable", unreachable,
		"failed", failed,
		"took", time.Since(start).Round(time.Millisecond),
		"heal_copies_total", n.healCopies.Load(),
	}
	if copies == 0 {
		log.Debug("heal complete: no copies needed", attrs...)
	} else {
		for to, keys := range moved {
			slices.Sort(keys)
			log.Info("heal: re-replicated keys", "to", to, "count", len(keys), "keys", strings.Join(keys, ","))
		}
		log.Info("heal complete: replication restored", attrs...)
	}

	n.recordHeal(moved, causes)
}

// HealCopy is one heal pass's worth of keys pushed to a single target, for the
// dashboard: "this node sent these keys to that node, because of this."
type HealCopy struct {
	To    string
	Keys  []string
	Cause string // the membership change THIS node saw that triggered the pass
}

// recordHeal files what a heal pass moved. Bounded: a dashboard that stops
// draining must not grow this without limit.
func (n *Node) recordHeal(moved map[string][]string, causes []string) {
	if len(moved) == 0 {
		return // a heal that copied nothing (the healthy case) is not news
	}
	cause := strings.Join(causes, " and ")
	targets := slices.Sorted(maps.Keys(moved)) // stable order for a stable render
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	for _, to := range targets {
		keys := moved[to]
		slices.Sort(keys)
		n.healLog = append(n.healLog, HealCopy{To: to, Keys: keys, Cause: cause})
	}
	if over := len(n.healLog) - maxHealLog; over > 0 {
		n.healLog = slices.Delete(n.healLog, 0, over) // drop oldest
	}
}

// noteHealCause records a membership change this node observed, to be attached to
// the heal pass it triggers. The node is the only one that can say this honestly:
// the manager knows it killed n2, but what makes THIS node heal is that ITS OWN
// heartbeat stopped hearing from n2 — and two nodes can disagree about that (a
// false positive is exactly one node seeing a death nobody else sees). So the
// dashboard shows each heal attributed to the observation that actually caused it.
//
// Caller holds n.mu (heartbeatRound does). Takes healLogMu, never the reverse, so
// there is no lock-order inversion.
func (n *Node) noteHealCause(cause string) {
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	if !slices.Contains(n.healCauses, cause) { // a flap can report the same thing twice
		n.healCauses = append(n.healCauses, cause)
	}
}

// drainHealCauses takes the membership changes observed since the last heal pass.
// Coalescing means one pass can answer several changes at once, so this returns
// all of them.
func (n *Node) drainHealCauses() []string {
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	out := n.healCauses
	n.healCauses = nil
	return out
}

// DrainHealLog returns everything this node has copied since the last drain, and
// clears it. The manager polls this to build the dashboard's heal log.
func (n *Node) DrainHealLog() []HealCopy {
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	if len(n.healLog) == 0 {
		return nil
	}
	out := n.healLog
	n.healLog = nil
	return out
}

// PauseHealth stalls (or resumes) this node's /health responses. The node keeps
// serving everything else — it is a live node that merely looks silent, so peers
// with a short timeout will falsely declare it dead. For the false-positive demo.
func (n *Node) PauseHealth(paused bool) {
	n.healthPaused.Store(paused)
	if paused {
		n.logger().Warn("health replies stalled (fault injected): node is alive but will look dead to peers")
	} else {
		n.logger().Info("health replies resumed")
	}
}

// HealthPaused reports whether this node's health replies are currently stalled,
// for the dashboard to show the false-positive injection state.
func (n *Node) HealthPaused() bool { return n.healthPaused.Load() }

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

// HeldKeys returns the keys this node physically holds right now, for the
// dashboard to show where data actually lives (as opposed to where the ring says
// it should). Watching a key appear on a new node here is the heal, made visible.
func (n *Node) HeldKeys() []string {
	snap := n.cache.Snapshot()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	return keys
}

// HeldEntries is HeldKeys with each key's deadline attached, so the dashboard can
// show a key's remaining life alongside where it lives. A zero Expires never dies.
func (n *Node) HeldEntries() map[string]cache.Entry { return n.cache.Snapshot() }

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
		log := n.logger()
		keys := len(n.HeldKeys())
		close(n.done) // stop the heartbeat loop first
		n.wg.Wait()
		err = n.srv.Shutdown(context.Background())
		n.cache.Close()
		if err != nil {
			log.Error("node stopped with an unclean HTTP shutdown", "err", err)
			return
		}
		log.Info("node stopped", "keys_held", keys, "heal_copies_total", n.healCopies.Load())
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

// expiresHeader carries a key's deadline between nodes as an absolute instant —
// Unix milliseconds, or "0"/absent for "never expires".
//
// Absolute, not a duration, and that is the whole point. A replica write and a heal
// copy are both "someone else already decided when this key dies"; re-deriving the
// deadline from the receiver's clock would hand a healed copy a fresh full lifetime
// and let it outlive the original. See cache.SetAt.
const expiresHeader = "X-Expires-At"

// What a client read reveals about the cluster, carried back to the caller.
//
// CoordinatorHeader is the node that took the request. It is almost never the node
// that has the data: any live node can coordinate, because coordinating means
// hashing the key and asking the owners, which needs no local copy of anything.
//
// ReadPathHeader is what happened at each of the key's R owners, in ring order —
// see FormatReadPath. Until now only the server log ever saw this, and it is the
// whole self-healing story in one line.
const (
	ServedByHeader    = "X-Served-By"
	CoordinatorHeader = "X-Coordinator"
	ReadPathHeader    = "X-Read-Path"
)

// What happened at one owner during a read.
//
// Miss and Unreachable both mean "this owner did not serve the read", and the
// difference between them is the thing worth seeing: a node that is UNREACHABLE is
// gone, while a node that MISSES answered promptly and simply has no copy — which
// is precisely the state a revived node is in, since it comes back empty and the
// ring promotes it straight back to primary before the heal has refilled it. Both
// look identical to a client that is only told the value.
const (
	OutcomeHit         = "hit"         // answered, and had the key
	OutcomeMiss        = "miss"        // answered, and did not have the key
	OutcomeUnreachable = "unreachable" // did not answer
	OutcomeSkipped     = "skipped"     // never asked: an earlier owner already served it
)

// FormatReadPath encodes one outcome per owner as "n0:unreachable,n4:miss,n2:hit",
// ordered by rank, so index 0 is the primary. Safe as a header value because node
// ids are cluster-assigned (n0..nN) and contain neither ':' nor ','.
//
// Every owner appears, including the ones never asked — that absence is itself the
// point. R=3 is about how many COPIES exist, not how many nodes a read consults:
// the coordinator stops at the first owner that answers with the value, so a
// healthy read touches exactly one node and the other two show up as skipped.
func FormatReadPath(ids, outcomes []string) string {
	hops := make([]string, len(ids))
	for i, id := range ids {
		hops[i] = id + ":" + outcomes[i]
	}
	return strings.Join(hops, ",")
}

func putExpires(h http.Header, expires time.Time) {
	if expires.IsZero() {
		return // absent means never
	}
	h.Set(expiresHeader, strconv.FormatInt(expires.UnixMilli(), 10))
}

func readExpires(h http.Header) time.Time {
	ms, err := strconv.ParseInt(h.Get(expiresHeader), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{} // absent, junk, or 0: never expires
	}
	return time.UnixMilli(ms)
}

// handlePut stores the request body as the value for {key}, honouring the deadline
// the sender stamped on it (see expiresHeader). This is the internal replica write:
// the deadline was decided by whoever coordinated the original client write.
func (n *Node) handlePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	n.cache.SetAt(r.PathValue("key"), string(body), readExpires(r.Header))
	w.WriteHeader(http.StatusNoContent)
}

// handleClientGet coordinates a read: try the key's owners in ring order and
// return the first reachable hit. An unreachable owner is skipped — that is the
// fallback that keeps reads serving after a node dies. Only when every owner is
// unreachable do we 502; a reachable miss is an honest 404.
//
// Any node can do this. Coordinating means hashing the key and asking the owners,
// which needs no local copy — so the node the client talked to is usually NOT the
// node that had the data, and both are reported (see ReadPathHeader).
func (n *Node) handleClientGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	owners := n.ownersFor(key)
	if len(owners) == 0 {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	// One outcome per owner, in ring order. Owners we never ask — because someone
	// earlier already served the read — stay "skipped" rather than being left out:
	// the read stopping early is a fact about the read, not a gap in the record.
	ids := make([]string, len(owners))
	outcomes := make([]string, len(owners))
	for i, o := range owners {
		ids[i] = o.id
		outcomes[i] = OutcomeSkipped
	}

	log := n.logger()
	var (
		value       string
		servedBy    string
		reachedNone = true
	)
	for i, o := range owners {
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
		switch {
		case err != nil:
			outcomes[i] = OutcomeUnreachable
			log.Warn("read: owner unreachable, falling back to next owner",
				"key", key, "owner", o.id, "rank", i, "of", len(owners), "err", err)
			continue // owner unreachable: fall back to the next
		case !ok:
			outcomes[i] = OutcomeMiss
			reachedNone = false
			continue // owner answered, and has no copy: fall back to the next
		}

		outcomes[i] = OutcomeHit
		reachedNone = false
		value, servedBy = v, o.id
		if i > 0 {
			// The fallback earning its keep: the primary could not, a replica did.
			log.Info("read served by fallback replica", "key", key, "served_by", o.id, "rank", i)
		} else {
			log.Debug("read hit", "key", key, "served_by", o.id)
		}
		break // stop at the first hit — hence the skipped owners behind it
	}

	// Headers before the body, always: the first Write flushes the header block, and
	// http.Error writes one itself, so anything set after either goes nowhere.
	//
	// The trace is set on EVERY exit, not just the happy one. A 404 whose owners all
	// say "miss" (they answered; nobody has the key) is a completely different fact
	// from a 502 where they all say "unreachable" (nobody answered at all), and the
	// value — which in both cases is no value — cannot tell you which happened.
	w.Header().Set(CoordinatorHeader, n.id)
	w.Header().Set(ReadPathHeader, FormatReadPath(ids, outcomes))

	switch {
	case servedBy != "":
		w.Header().Set(ServedByHeader, servedBy)
		io.WriteString(w, value)
	case reachedNone:
		log.Error("read failed: all owners unreachable", "key", key, "owners", len(owners))
		http.Error(w, "all owners unreachable", http.StatusBadGateway)
	default:
		log.Debug("read miss", "key", key)
		http.Error(w, "not found", http.StatusNotFound)
	}
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

	// The client sends a duration; the coordinator turns it into an instant, ONCE,
	// and every replica is handed that same instant. If each owner converted the
	// duration itself, the replicas would already disagree by their clock skew — and
	// a heal, arriving later still, would disagree by far more.
	var expires time.Time
	if ttl, err := parseTTL(r.URL.Query().Get("ttl")); err != nil {
		http.Error(w, "bad ttl: "+err.Error(), http.StatusBadRequest)
		return
	} else if ttl > 0 {
		expires = time.Now().Add(ttl)
	}

	owners := n.ownersFor(key)
	if len(owners) == 0 {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	log := n.logger()
	acks, ownerIDs := 0, make([]string, 0, len(owners))
	for _, o := range owners {
		ownerIDs = append(ownerIDs, o.id)
		if o.id == n.id {
			n.cache.SetAt(key, value, expires)
			acks++
			continue
		}
		if err := n.storeOn(o.addr, key, value, expires); err != nil {
			// Not fatal on its own: the write still succeeds if enough others ack,
			// and a later heal repairs this replica.
			log.Warn("write: owner did not ack", "key", key, "owner", o.id, "err", err)
			continue
		}
		acks++
	}

	quorum := n.writeQuorum
	if acks < quorum {
		log.Error("write rejected: quorum not met",
			"key", key, "acks", acks, "quorum", quorum, "owners", strings.Join(ownerIDs, ","))
		http.Error(w, "write quorum not met", http.StatusBadGateway)
		return
	}
	log.Debug("write ok", "key", key, "acks", acks, "quorum", quorum)
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

// storeOn PUTs key=value to another node's internal /kv endpoint, stamped with the
// deadline the key already has. A zero expires means it never expires.
func (n *Node) storeOn(addr, key, value string, expires time.Time) error {
	req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/kv/"+key, strings.NewReader(value))
	if err != nil {
		return err
	}
	putExpires(req.Header, expires)
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// parseTTL reads the client's ttl query parameter: a Go duration ("30s", "5m") or a
// bare number of seconds. Empty or zero means the key never expires.
func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		if secs < 0 {
			return 0, fmt.Errorf("negative ttl %q", s)
		}
		return time.Duration(secs * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("not a duration or a number of seconds: %q", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("negative ttl %q", s)
	}
	return d, nil
}
