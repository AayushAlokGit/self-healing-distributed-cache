// Package node is a cluster peer: a cache behind an HTTP server that also holds its
// own view of the ring and routes requests. There is no central coordinator; every
// node accepts any client key on /get, /set and /del, and forwards to its peers' /kv.
//
// /get and /set address the key's owners — the R nodes the ring names. /del addresses
// every known peer instead, owner or not; see handleClientDelete for why the difference
// is load-bearing.
package node

import (
	"context"
	"encoding/json"
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
	"github.com/AayushAlokGit/self-healing-distributed-cache/vclock"
)

// forwardTimeout bounds a node-to-node call, so a dead owner fails fast instead of
// hanging the coordinating node.
const forwardTimeout = 2 * time.Second

const (
	// Copies per key: the primary plus the next R-1 distinct nodes clockwise.
	defaultReplicationFactor = 3

	// Acks required before a write returns to the client. W=1 favors availability;
	// larger W trades latency for durability. W>R is impossible.
	defaultWriteQuorum = 1

	// How often a node pings every peer's /health.
	defaultHeartbeatInterval = 100 * time.Millisecond

	// Silence longer than this makes a peer suspected dead and dropped from the ring.
	// Shorter = faster detection but more false positives (a GC pause looks like
	// death); longer = the ring routes to a corpse for longer. 5 missed beats.
	defaultFailureTimeout = 500 * time.Millisecond

	// The cheap, reversible re-route happens immediately on suspicion; the expensive,
	// irreversible copying waits this long and then rechecks, so a false positive that
	// recovers inside the window costs nothing. The price: a genuine death sits
	// under-replicated this much longer.
	defaultHealGracePeriod = 1 * time.Second

	// Heal records held before the oldest are dropped. The log is for display, not
	// durability.
	maxHealLog = 64
)

// Node is a peer in the cluster: a cache, an HTTP server, and its own view of the
// ring and peer addresses. Any node can coordinate any client key.
type Node struct {
	id        string
	cache     *cache.Cache
	srv       *http.Server
	addr      string // the real bound address, known only after Start
	client    *http.Client
	closeOnce sync.Once

	// gate sits under both HTTP clients: a partition fault (Cluster.Cut) fills it with the
	// addresses on the far side of a cut, and every request to them fails fast. It lives
	// under the clients, not in the alive/ring view, because a partition is a property of
	// the network, not a node (CAP.md §1). See partition.go.
	gate *gate

	// log is installed by whoever runs the node and discards until then. Atomic
	// because the heartbeat and heal goroutines read it while SetLogger may write it.
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

	// healTrigger coalesces: buffered to 1 and sent to non-blocking, so a burst of
	// membership changes schedules exactly one heal pass.
	healTrigger chan struct{}

	// healCopies counts key copies pushed during heals, cumulative. cleanupDropped is its
	// mirror image: surplus copies discarded, the only thing that stops heal being a ratchet.
	healCopies     atomic.Int64
	cleanupDropped atomic.Int64

	// healLog records what each heal pass moved; the manager drains it. The heal
	// goroutine must not call back up into the manager: Kill holds the manager's lock
	// while Close waits for this goroutine, so a callback would invert lock order and
	// deadlock.
	//
	// cleanupLog is the same contract for the cleanup pass, and shares healLogMu: both are
	// written only by the heal goroutine and drained only by the manager.
	healLogMu  sync.Mutex
	healLog    []HealCopy
	cleanupLog [][]string // one entry per pass: the keys it dropped
	healCauses []string   // membership changes seen since the last heal pass

	// healthPaused stalls this node's /health responses without stopping the rest of
	// it: a live node that looks dead to peers, for the false-positive demo.
	healthPaused atomic.Bool

	// membership is this node's own view. peers is every node ever known; the ring
	// holds only those currently believed alive, so routing never targets a node this
	// view thinks is dead.
	mu       sync.RWMutex
	ring     *ring.Ring
	peers    map[string]string    // node id -> HTTP address (all known)
	lastSeen map[string]time.Time // last successful health ping
	alive    map[string]bool      // this node's current view
}

// New creates a node with the given id and capacity. addr may end in ":0" to let
// the OS pick a free port, which Addr reports back after Start. Call Close.
func New(id, addr string, capacity int) *Node {
	// One gate shared by both clients: cut the network and data AND heartbeats stop
	// together, which is what makes each side of a partition convict the other (CAP.md §2).
	g := &gate{base: http.DefaultTransport}
	n := &Node{
		id:                id,
		cache:             cache.New(capacity),
		addr:              addr,
		gate:              g,
		client:            &http.Client{Timeout: forwardTimeout, Transport: g},
		peers:             map[string]string{},
		lastSeen:          map[string]time.Time{},
		alive:             map[string]bool{},
		replicationFactor: defaultReplicationFactor,
		writeQuorum:       defaultWriteQuorum,
		heartbeatInterval: defaultHeartbeatInterval,
		failureTimeout:    defaultFailureTimeout,
		healGracePeriod:   defaultHealGracePeriod,
		healthClient:      &http.Client{Timeout: defaultFailureTimeout, Transport: g},
		done:              make(chan struct{}),
		healTrigger:       make(chan struct{}, 1),
	}
	n.SetLogger(nil) // discard until someone wires a real one

	mux := http.NewServeMux()
	// Internal: node-to-node storage and liveness.
	mux.HandleFunc("GET /kv/{key}", n.handleGet)
	mux.HandleFunc("PUT /kv/{key}", n.handlePut)
	mux.HandleFunc("DELETE /kv/{key}", n.handleDelete)
	mux.HandleFunc("DELETE /kv", n.handleClear)
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
	mux.HandleFunc("DELETE /del/{key}", n.handleClientDelete)
	mux.HandleFunc("DELETE /del", n.handleClientClear)
	n.srv = &http.Server{Handler: mux}

	return n
}

// SetLogger installs the logger this node writes to; every record it emits is tagged
// node=<id>. A nil logger discards, which is the default.
func (n *Node) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	n.log.Store(l.With("node", n.id))
}

// logger is never nil: New installs a discarding one.
func (n *Node) logger() *slog.Logger { return n.log.Load() }

// SetMembership installs the set of known peers (id -> address, including self).
// Everyone starts believed alive and on the ring; the heartbeat loop demotes the
// silent. Safe to call while serving.
func (n *Node) SetMembership(peers map[string]string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	// Clone, don't alias: the caller hands the same map to every node and SetPeerAddr
	// mutates it. Each node's peers must be its own, or one node's write races
	// another's heartbeat read.
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

// SetRingReplicas sets how many virtual points each node contributes to this node's
// ring. Call before SetMembership, which is what actually builds the ring.
func (n *Node) SetRingReplicas(k int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ringReplicas = k
}

// SetPeerAddr updates one peer's address without resetting liveness or the ring
// (unlike SetMembership, which rebuilds the whole view). A revived node comes back on
// a fresh port, so its peers call this to reach it again. Unknown ids are added.
func (n *Node) SetPeerAddr(id, addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.peers[id] = addr
}

// SetReplication overrides the replication factor and write quorum.
func (n *Node) SetReplication(replicationFactor, writeQuorum int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.replicationFactor = replicationFactor
	n.writeQuorum = writeQuorum
}

// SetHealGracePeriod overrides how long a detected death waits before the node
// re-replicates. Zero heals immediately (the naive behavior).
func (n *Node) SetHealGracePeriod(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.healGracePeriod = d
}

// gracePeriod reads the heal grace period under the lock: a setter may change it
// while healLoop is running.
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

// allPeers returns every node this one has ever heard of, itself included, sorted by id.
//
// The counterpart to ownersFor, and the difference is the point: the ring holds only the
// nodes currently believed alive, so ownersFor cannot name a node this view has convicted
// — even though a convicted node may be perfectly alive and still holding keys. A delete
// has to reach those, so it addresses peers, not owners.
func (n *Node) allPeers() []owner {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peers := make([]owner, 0, len(n.peers))
	for id, addr := range n.peers {
		peers = append(peers, owner{id: id, addr: addr})
	}
	slices.SortFunc(peers, func(a, b owner) int { return strings.Compare(a.id, b.id) })
	return peers
}

// Start binds the port and serves in the background. Split from New so the port is
// live, and Addr knowable, before any request is routed.
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

// heartbeatLoop pings every peer on a tick and updates this node's alive view, until
// Close.
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

// heartbeatRound pings all peers outside the lock, then takes the lock once to record
// who answered and reconcile the alive view against the failure timeout. A transition
// flips the peer's ring membership, so ownership recomputes.
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
			n.ring.Add(id) // back in the ring; the heal repopulates it
			membershipChanged = true
			n.noteHealCause(id + " came back")
			n.logger().Info("peer recovered, re-added to ring", "peer", id)
		}
	}

	// Any membership change can leave a key's owners out of sync with its holders: a
	// death promotes an owner that lacks the data, a recovery re-admits an empty node.
	// Firing on a recovery is safe because the heal is check-first, so a node that
	// merely flapped costs zero copies.
	if membershipChanged {
		select {
		case n.healTrigger <- struct{}{}:
		default:
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

// healLoop waits for a membership change, waits out the grace period, then heals.
// Separate from the heartbeat loop: a heal copies data over the network and can be
// slow, and a heartbeat stuck behind it would start declaring more false deaths.
func (n *Node) healLoop() {
	for {
		select {
		case <-n.done:
			return
		case <-n.healTrigger:
			// The cheap re-route already happened; the expensive copying waits, so a
			// false positive that recovers inside the window costs nothing.
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

// covered reports whether entries already hold v or a version that dominates it, so pushing
// v would add nothing. Concurrent siblings are NOT covered — preserving them is the point.
func covered(entries []cache.Entry, v cache.Entry) bool {
	for _, e := range entries {
		switch vclock.Compare(v.Version, e.Version) {
		case vclock.Before, vclock.Equal:
			return true
		}
	}
	return false
}

// coveredAhead reports whether some owner ranked ahead of rank demonstrably holds v or a
// dominator of it — making that owner v's healer (or marking v stale), so this node stands
// down for v. An unreachable owner ahead does not count: unconfirmed is not deferred-to.
func coveredAhead(ownerEntries [][]cache.Entry, reachable []bool, rank int, v cache.Entry) bool {
	for i := 0; i < rank && i < len(ownerEntries); i++ {
		if reachable[i] && covered(ownerEntries[i], v) {
			return true
		}
	}
	return false
}

// heal re-asserts the replication invariant: every current owner must hold every version
// this node holds. Check-first — ask before copying — so a flapped node costs zero copies
// and a revived empty node is repopulated.
//
// The healer for a VERSION is the first owner, in ring order, that holds it (or a dominator):
// permission to push follows the data, not the ring position, and it is decided per-version,
// not per-key. Two concurrent siblings on different owners each get their own healer, so a
// stranded carol propagates instead of living forever on one node. A version an owner ahead
// already covers is stood down; so is a stale local version a dominator elsewhere will replace.
func (n *Node) heal() {
	// Batched by target: one pass pushing 8 keys to n4 is one record, not eight.
	moved := map[string][]string{}

	start := time.Now()
	snapshot := n.cache.SnapshotAll() // every version of every key I hold, not just one
	causes := n.drainHealCauses()     // what my heartbeat saw that made this pass happen
	var healerFor, deferred, alreadyPresent, unreachable, failed int

	log := n.logger()
	log.Debug("heal started", "keys_held", len(snapshot), "cause", strings.Join(causes, "; "))

	for key, versions := range snapshot {
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

		// My rank among this key's owners, or last if I am a leftover holder from an older
		// ring — such a holder must still be able to heal, or a version no owner has yet
		// would have no sender at all.
		rank := len(owners)
		for i, o := range owners {
			if o.id == n.id {
				rank = i
				break
			}
		}

		// Fetch every owner's version set once and reuse it across my local versions. An
		// owner I cannot reach stays unreachable and is retried on a later pass.
		ownerEntries := make([][]cache.Entry, len(owners))
		reachable := make([]bool, len(owners))
		for i, o := range owners {
			if o.id == n.id {
				ownerEntries[i], reachable[i] = versions, true
				continue
			}
			es, _, err := n.fetchFrom(o.addr, key)
			if err != nil {
				continue // reachable[i] stays false: a later pass retries
			}
			ownerEntries[i], reachable[i] = es, true
		}

		// Heal each version independently: presence != version reaches the heal's own
		// sender-selection. I heal v only if no owner ahead of me already covers it; else
		// that owner is v's healer, or v is a stale local copy a dominator will replace.
		healed := false
		for _, v := range versions {
			if coveredAhead(ownerEntries, reachable, rank, v) {
				continue
			}
			healed = true
			for i, o := range owners {
				if o.id == n.id {
					continue // I have it; that's why I'm the healer
				}
				switch {
				case !reachable[i]:
					unreachable++ // can't copy to a node we can't reach; a later pass retries
				case covered(ownerEntries[i], v):
					alreadyPresent++ // owner already holds v or a dominator
				default:
					// v.Expires, not a fresh TTL: the copy inherits the deadline the key
					// already had, or its rescue would hand it a new lease on life.
					if err := n.storeOn(o.addr, key, v.Value, v.Version, v.Expires); err != nil {
						failed++
						log.Warn("heal: copy failed, version still under-replicated", "key", key, "owner", o.id, "err", err)
						continue // record only copies that actually landed
					}
					n.healCopies.Add(1)
					moved[o.id] = append(moved[o.id], key)
					// Keep the owner's view current so a later local version's covered-check
					// sees what we just pushed and does not re-send it.
					ownerEntries[i] = cache.MergeVersions(ownerEntries[i], []cache.Entry{v})
					log.Debug("heal: copied version", "key", key, "to", o.id)
				}
			}
		}
		if healed {
			healerFor++
		} else {
			deferred++
		}
	}

	copies := 0
	for _, keys := range moved {
		copies += len(keys)
	}

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

	// After the copying, never before: cleanup asks the owners whether they hold a key, and
	// a key this pass is about to copy to them is one they do not hold *yet*.
	n.cleanup()
}

// cleanup drops the copies this node holds but no longer owns — the counterweight to heal,
// which only ever COPIES. Without it every kill/revive leaves surplus copies behind for good
// and R creeps toward N: the sharding given away one outage at a time.
//
// ⚠️ Confirm, THEN drop. A copy goes only if every one of the key's R owners answers that it
// holds the key; an owner that says no, or cannot be reached, ends the matter. A surplus copy
// and the last copy alive look identical from here — asking is the only thing that tells them
// apart, and reversing the order makes this a data-loss bug.
// See TestCleanupDropsOnlyWhatEveryOwnerConfirms.
//
// Two non-owners dropping the same key concurrently is safe: neither is an owner and each
// drops only after all R owners confirm, so the count cannot fall below R however they
// interleave. An owner never reaches the drop, so the owners cannot clean each other up.
//
// Version-aware: a copy is dropped only if every owner covers each version it carries — holds
// that version, or a dominator of it. A version no owner covers is a stranded sibling (a write
// a down owner missed, or one side of a cut): kept, not dropped, and the heal re-armed to
// propagate it. So cleanup can never discard an acked write the owners have not yet seen — the
// presence≠version hole the old has-the-key check left open. See TestCleanupKeepsAStrandedSibling.
func (n *Node) cleanup() {
	start := time.Now()
	log := n.logger()

	dropped := make([]string, 0, 8)
	var kept int

	for key, versions := range n.cache.SnapshotAll() {
		select {
		case <-n.done:
			return
		default:
		}

		owners := n.ownersFor(key)
		if len(owners) == 0 {
			continue // no ring, or nobody alive: not the moment to be discarding data
		}
		if slices.ContainsFunc(owners, func(o owner) bool { return o.id == n.id }) {
			continue // I own this key. Holding it is the point.
		}

		// I am not an owner. I let go only once every owner covers every version I hold. An
		// owner that cannot be reached, or one missing a version I have (a stranded sibling),
		// ends the matter: dropping then would lose a write the owners do not have.
		confirmed := true
		for _, o := range owners {
			ownerEntries, _, err := n.fetchFrom(o.addr, key)
			if err != nil {
				confirmed = false
				log.Debug("cleanup: keeping a key I do not own — an owner is unreachable",
					"key", key, "owner", o.id)
				break
			}
			for _, v := range versions {
				if !covered(ownerEntries, v) {
					confirmed = false
					log.Debug("cleanup: keeping a version no owner holds — a stranded sibling",
						"key", key, "owner", o.id)
					break
				}
			}
			if !confirmed {
				break
			}
		}
		if !confirmed {
			kept++
			continue
		}

		if n.cache.Delete(key) {
			dropped = append(dropped, key)
		}
	}

	// A key kept because an owner could not confirm it is not settled, just deferred — and
	// nothing else will come back for it, since cleanup only runs inside a heal and heals
	// only run on a membership change. Without this, a copy whose owner was still being
	// repopulated at the moment we asked stays stranded until the next kill. Re-arming is
	// self-limiting: the retry that confirms it leaves kept == 0 and the loop stops.
	if kept > 0 {
		select {
		case n.healTrigger <- struct{}{}:
			log.Debug("cleanup: re-arming, some copies could not be confirmed yet", "kept_unconfirmed", kept)
		default:
		}
	}

	if len(dropped) == 0 {
		log.Debug("cleanup: nothing to drop", "kept_unconfirmed", kept, "took", time.Since(start).Round(time.Millisecond))
		return
	}

	slices.Sort(dropped)
	n.cleanupDropped.Add(int64(len(dropped)))
	n.recordCleanup(dropped)
	log.Info("cleanup: dropped copies this node no longer owns",
		"count", len(dropped),
		"keys", strings.Join(dropped, ","),
		"kept_unconfirmed", kept,
		"cleanup_dropped_total", n.cleanupDropped.Load(),
		"took", time.Since(start).Round(time.Millisecond),
	)
}

// recordCleanup files what a pass dropped. Bounded, like the heal log.
func (n *Node) recordCleanup(keys []string) {
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	n.cleanupLog = append(n.cleanupLog, keys)
	if over := len(n.cleanupLog) - maxHealLog; over > 0 {
		n.cleanupLog = slices.Delete(n.cleanupLog, 0, over)
	}
}

// DrainCleanupLog returns the key batches dropped since the last drain, and clears it.
func (n *Node) DrainCleanupLog() [][]string {
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	if len(n.cleanupLog) == 0 {
		return nil
	}
	out := n.cleanupLog
	n.cleanupLog = nil
	return out
}

// CleanupDropped is how many surplus copies this node has discarded, cumulative.
func (n *Node) CleanupDropped() int64 { return n.cleanupDropped.Load() }

// HealCopy is one heal pass's worth of keys pushed to a single target, for the
// dashboard.
type HealCopy struct {
	To    string
	Keys  []string
	Cause string // the membership change THIS node saw that triggered the pass
}

// recordHeal files what a heal pass moved. Bounded: a dashboard that stops draining
// must not grow this without limit.
func (n *Node) recordHeal(moved map[string][]string, causes []string) {
	if len(moved) == 0 {
		return // a heal that copied nothing is the healthy case, not news
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

// noteHealCause records a membership change this node observed, to be attached to the
// heal pass it triggers. Each heal is attributed to the observation that caused it:
// two nodes can disagree about a death, and a false positive is exactly that.
//
// Caller holds n.mu. Takes healLogMu, never the reverse: no lock-order inversion.
func (n *Node) noteHealCause(cause string) {
	n.healLogMu.Lock()
	defer n.healLogMu.Unlock()
	if !slices.Contains(n.healCauses, cause) { // a flap can report the same thing twice
		n.healCauses = append(n.healCauses, cause)
	}
}

// drainHealCauses takes all membership changes observed since the last heal pass:
// coalescing means one pass can answer several at once.
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
// serving everything else: it is alive but looks silent, so peers falsely declare it
// dead. For the false-positive demo.
func (n *Node) PauseHealth(paused bool) {
	n.healthPaused.Store(paused)
	if paused {
		n.logger().Warn("health replies stalled (fault injected): node is alive but will look dead to peers")
	} else {
		n.logger().Info("health replies resumed")
	}
}

// HealthPaused reports whether this node's health replies are currently stalled.
func (n *Node) HealthPaused() bool { return n.healthPaused.Load() }

// AlivePeers is this node's current view of who is up.
func (n *Node) AlivePeers() map[string]bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	view := make(map[string]bool, len(n.alive))
	maps.Copy(view, n.alive)
	return view
}

// HealCopies is the cumulative number of key copies this node has pushed during
// heals. A climbing count with no real death is a re-replication storm.
func (n *Node) HealCopies() int64 { return n.healCopies.Load() }

// HeldKeys returns the keys this node physically holds right now, as opposed to where
// the ring says they should live.
func (n *Node) HeldKeys() []string {
	snap := n.cache.Snapshot()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	return keys
}

// HeldEntries is HeldKeys with each key's deadline attached. A zero Expires never
// dies.
func (n *Node) HeldEntries() map[string]cache.Entry { return n.cache.Snapshot() }

// Reclaim is an expired entry whose memory this node has freed. Aliased, not
// wrapped, so the manager can read it without importing the cache package.
type Reclaim = cache.Reclaim

// Reasons a Reclaim can carry, re-exported for the same reason.
const (
	ReclaimLazy  = cache.ReclaimLazy
	ReclaimSweep = cache.ReclaimSweep
	ReclaimEvict = cache.ReclaimEvict
)

// DrainReclaimed returns the expired entries this node's cache has freed since the
// last drain, and clears them. The manager polls it the same way it polls the heal
// log — the node reports what it did, and never calls up to say so.
func (n *Node) DrainReclaimed() []Reclaim { return n.cache.DrainReclaimed() }

// Addr is the node's bound address, e.g. "127.0.0.1:53187".
func (n *Node) Addr() string { return n.addr }

// ID is the node's ring identity.
func (n *Node) ID() string { return n.id }

// Close stops the background loops, then the HTTP server, then the cache, so nothing
// is still using the cache when it goes away. Idempotent.
func (n *Node) Close() error {
	var err error
	n.closeOnce.Do(func() {
		log := n.logger()
		keys := len(n.HeldKeys())
		close(n.done) // stop the heartbeat and heal loops
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

// wireEntry is one version of a key as it crosses the network: value, vector clock, and
// deadline as Unix milliseconds (0 = never). The internal /kv endpoints speak a JSON array
// of these, so a node can hand a peer every concurrent version it holds, not just one.
type wireEntry struct {
	Value     string       `json:"v"`
	Version   vclock.Clock `json:"vc,omitempty"`
	ExpiresMS int64        `json:"e,omitempty"`
}

func toWire(e cache.Entry) wireEntry {
	w := wireEntry{Value: e.Value, Version: e.Version}
	if !e.Expires.IsZero() {
		w.ExpiresMS = e.Expires.UnixMilli()
	}
	return w
}

func (w wireEntry) toEntry() cache.Entry {
	var expires time.Time
	if w.ExpiresMS > 0 {
		expires = time.UnixMilli(w.ExpiresMS)
	}
	return cache.Entry{Value: w.Value, Version: w.Version, Expires: expires}
}

// handleGet returns every live version of {key} as a JSON array, or 404 if absent or
// expired. An expiry and a missing key are the same 404: the caller cannot tell them apart.
func (n *Node) handleGet(w http.ResponseWriter, r *http.Request) {
	entries, ok := n.cache.GetEntries(r.PathValue("key"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	wires := make([]wireEntry, len(entries))
	for i, e := range entries {
		wires[i] = toWire(e)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wires)
}

// expiresHeader carries a key's deadline between nodes as an absolute instant: Unix
// milliseconds, or "0"/absent for "never expires". Absolute, not a duration —
// re-deriving it from the receiver's clock would give a healed copy a fresh full
// lifetime and let it outlive the original. See cache.SetAt.
const expiresHeader = "X-Expires-At"

// Headers a client read carries back: the node that coordinated it (any live node
// can, since coordinating needs no local copy), the node that served the value, and
// what happened at each of the key's R owners in ring order (see FormatReadPath).
const (
	ServedByHeader    = "X-Served-By"
	CoordinatorHeader = "X-Coordinator"
	ReadPathHeader    = "X-Read-Path"
	// ConflictHeader is the number of concurrent siblings a read reconciled to, set only
	// when that number is >1. Its presence tells a client the body is a JSON array of the
	// sibling values rather than a single plain value.
	ConflictHeader = "X-Conflict"
)

// DroppedHeader carries what a delete actually removed: from /del/{key}, the comma-
// separated ids of the nodes that were holding the key ("n0,n2,n4" — empty if none were);
// from /del, the number of entries dropped cluster-wide.
const DroppedHeader = "X-Dropped"

// What happened at one owner during a read. Miss (answered, has no copy) and
// Unreachable (never answered) are different facts and must not be conflated: a
// revived node comes back empty, so it misses while being perfectly alive.
const (
	OutcomeHit         = "hit"         // answered, and had the key
	OutcomeMiss        = "miss"        // answered, and did not have the key
	OutcomeUnreachable = "unreachable" // did not answer
	OutcomeSkipped     = "skipped"     // never asked: an earlier owner already served it
)

// FormatReadPath encodes one outcome per owner as "n0:unreachable,n4:miss,n2:hit",
// ordered by rank, so index 0 is the primary. Every owner appears, including the ones
// never asked: the read stops at the first hit, so a healthy read leaves the rest
// skipped. Safe as a header value: node ids contain neither ':' nor ','.
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

// versionHeader carries a value's vector clock between nodes, JSON-encoded. Absent means an
// unversioned write (the legacy path); the receiver reconciles by dominance either way.
const versionHeader = "X-Version"

func putVersion(h http.Header, v vclock.Clock) {
	if len(v) == 0 {
		return
	}
	if b, err := json.Marshal(v); err == nil {
		h.Set(versionHeader, string(b))
	}
}

func readVersion(h http.Header) (vclock.Clock, error) {
	s := h.Get(versionHeader)
	if s == "" {
		return nil, nil
	}
	var v vclock.Clock
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// handlePut reconciles the request body into {key}, honouring the deadline and vector clock
// the sender stamped on it. The internal replica write: SetVersioned keeps a concurrent
// value rather than clobbering it, so a replica that already holds a conflicting version
// ends up with both.
func (n *Node) handlePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	version, err := readVersion(r.Header)
	if err != nil {
		http.Error(w, "bad version: "+err.Error(), http.StatusBadRequest)
		return
	}
	n.cache.SetVersioned(r.PathValue("key"), string(body), version, readExpires(r.Header))
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete drops {key} from this node's cache. The internal replica delete. 204 if
// this node was holding it, 404 if it was not — the sender counts the 204s to report
// which nodes actually had a copy.
func (n *Node) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !n.cache.Delete(r.PathValue("key")) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleClear empties this node's cache, reporting how many entries it dropped.
func (n *Node) handleClear(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(DroppedHeader, strconv.Itoa(n.cache.Clear()))
	w.WriteHeader(http.StatusNoContent)
}

// handleClientGet coordinates a read: gather every version the key's reachable owners
// hold and reconcile them into one maximal set under vector-clock dominance. One survivor
// is a plain value; two or more are concurrent siblings — a detected conflict — returned
// as a JSON array with the X-Conflict header. Only when every owner is unreachable is it a
// 502; a reachable miss is an honest 404.
//
// Unlike the pre-versioning read, it cannot stop at the first hit: a concurrent sibling on
// a later owner would go unseen, and an unseen conflict is a silently-picked winner. When
// the R_read dial lands (Phase 7B) it asks the first R_read owners instead of all; the
// gather-and-reconcile below is unchanged.
func (n *Node) handleClientGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	owners := n.ownersFor(key)
	if len(owners) == 0 {
		http.Error(w, "no owner for key", http.StatusServiceUnavailable)
		return
	}

	// One outcome per owner, in ring order, plus what each one held so provenance can be
	// recovered after reconciliation. Owners never reached stay "skipped".
	ids := make([]string, len(owners))
	outcomes := make([]string, len(owners))
	held := make([][]cache.Entry, len(owners))
	for i, o := range owners {
		ids[i] = o.id
		outcomes[i] = OutcomeSkipped
	}

	log := n.logger()
	reachedNone := true
	for i, o := range owners {
		var (
			entries []cache.Entry
			ok      bool
			err     error
		)
		if o.id == n.id {
			entries, ok = n.cache.GetEntries(key) // local read never "fails to reach"
		} else {
			entries, ok, err = n.fetchFrom(o.addr, key)
		}
		switch {
		case err != nil:
			outcomes[i] = OutcomeUnreachable
			log.Warn("read: owner unreachable",
				"key", key, "owner", o.id, "rank", i, "of", len(owners), "err", err)
			continue
		case !ok:
			outcomes[i] = OutcomeMiss
			reachedNone = false // it answered, it just has no copy
			continue
		}
		outcomes[i] = OutcomeHit
		reachedNone = false
		held[i] = entries
	}

	merged := cache.MergeVersions(held...)

	// servedBy is the first owner, in ring order, that actually holds a surviving version —
	// not merely the first that answered. A node returning only a stale, dominated copy did
	// not serve the value the client gets back (presence != version, at the header level).
	var servedBy string
	for i, o := range owners {
		if outcomes[i] == OutcomeHit && holdsAny(held[i], merged) {
			servedBy = o.id
			break
		}
	}

	// Headers before the body, always: the first Write (and http.Error, which writes one
	// itself) flushes the header block, so anything set after it goes nowhere. The trace is
	// set on every exit — a 404 (all owners answered, none had it) and a 502 (nobody
	// answered) are different facts the empty value cannot distinguish.
	w.Header().Set(CoordinatorHeader, n.id)
	w.Header().Set(ReadPathHeader, FormatReadPath(ids, outcomes))

	switch {
	case len(merged) == 1:
		w.Header().Set(ServedByHeader, servedBy)
		log.Debug("read hit", "key", key, "served_by", servedBy)
		io.WriteString(w, merged[0].Value)
	case len(merged) > 1:
		// Concurrent siblings: none dominates, so we cannot pick one without destroying an
		// acked write. Hand the client all of them and let it (or a human) resolve.
		vals := make([]string, len(merged))
		for i, e := range merged {
			vals[i] = e.Value
		}
		w.Header().Set(ServedByHeader, servedBy)
		w.Header().Set(ConflictHeader, strconv.Itoa(len(merged)))
		w.Header().Set("Content-Type", "application/json")
		log.Info("read returned conflicting siblings", "key", key, "versions", len(merged))
		json.NewEncoder(w).Encode(vals)
	case reachedNone:
		log.Error("read failed: all owners unreachable", "key", key, "owners", len(owners))
		http.Error(w, "all owners unreachable", http.StatusBadGateway)
	default:
		log.Debug("read miss", "key", key)
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// holdsAny reports whether have contains a version whose clock equals one in want. Used to
// find which owner supplied a surviving version, so servedBy names a node that holds the
// value actually returned, not one whose copy was dropped as stale.
func holdsAny(have, want []cache.Entry) bool {
	for _, h := range have {
		for _, w := range want {
			if vclock.Compare(h.Version, w.Version) == vclock.Equal {
				return true
			}
		}
	}
	return false
}

// handleClientSet writes to all R owners and acks once writeQuorum of them succeed.
// Every owner is written to regardless; the quorum only decides when to answer.
func (n *Node) handleClientSet(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key, value := r.PathValue("key"), string(body)

	// The client sends a duration; the coordinator turns it into an instant once and
	// hands every replica that same instant. Converting per-owner would leave the
	// replicas disagreeing by their clock skew.
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

	// Read-before-write: merge every version currently on the reachable owners, then bump
	// this coordinator's slot. The new value therefore dominates everything it could see —
	// but two coordinators that cannot see each other (a cut) each bump a different slot, so
	// their writes come out concurrent and are both kept. Best-effort: an unreachable owner
	// is skipped, not waited on.
	base := n.currentVersion(key, owners)
	version := base.Bump(n.id)

	acks, ownerIDs := 0, make([]string, 0, len(owners))
	for _, o := range owners {
		ownerIDs = append(ownerIDs, o.id)
		if o.id == n.id {
			n.cache.SetVersioned(key, value, version, expires)
			acks++
			continue
		}
		if err := n.storeOn(o.addr, key, value, version, expires); err != nil {
			// Not fatal: the write still succeeds if enough others ack, and a later heal
			// repairs this replica.
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

// handleClientDelete removes a key from EVERY known node, not just the R owners the ring
// names, and reports the ids that were holding a copy in DroppedHeader.
//
// The broadcast is the correctness argument. The ring names where a copy SHOULD be; a
// delete must erase it wherever one IS, and the two drift apart because nothing here ever
// removes a surplus copy. Two ways an owners-only delete leaks:
//
//   - Leftovers. A heal re-replicates a dead node's keys onto whoever owns them now; revive
//     it and the ring snaps back, but those copies stay on nodes that no longer own them. A
//     delete aimed at the owners walks past them and the key never leaves the dashboard.
//     Kill and Revive alone produce this — see TestDeleteFindsCopiesTheRingNoLongerNames.
//   - Resurrection. A health-paused node is alive and still serving /kv, but its peers have
//     convicted it and dropped it from their ring, so it is not an owner and never gets the
//     delete. Resume it: heal finds a key no owner holds, appoints it the healer (heal
//     follows the data, not the ring), and pushes the key back. The delete reverts, wearing
//     a heal's clothes.
//
// Real systems need a tombstone here — a "deleted at T" marker that replicates like a value,
// so heal sees DELETED rather than MISSING. We can skip it only because a dead node is
// destroyed and revives empty (cluster.Revive): unreachable means nothing left to resurrect.
// Give the nodes durable storage and that argument collapses.
func (n *Node) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	log := n.logger()

	dropped := make([]string, 0, 4)
	for _, p := range n.allPeers() {
		if p.id == n.id {
			if n.cache.Delete(key) {
				dropped = append(dropped, p.id)
			}
			continue
		}
		had, err := n.deleteOn(p.addr, key)
		if err != nil {
			// Not a failed delete: unreachable means down, and a node revives empty.
			log.Warn("delete: peer unreachable, assuming it holds nothing", "key", key, "peer", p.id, "err", err)
			continue
		}
		if had {
			dropped = append(dropped, p.id)
		}
	}

	log.Debug("delete ok", "key", key, "dropped_by", strings.Join(dropped, ","))
	w.Header().Set(DroppedHeader, strings.Join(dropped, ","))
	w.WriteHeader(http.StatusNoContent)
}

// handleClientClear empties every node this one knows of, and reports the total number of
// entries dropped — copies, not distinct keys, so a key held by 3 nodes counts 3 times.
// Broadcast for the same reason as handleClientDelete.
func (n *Node) handleClientClear(w http.ResponseWriter, _ *http.Request) {
	log := n.logger()

	total := 0
	for _, p := range n.allPeers() {
		if p.id == n.id {
			total += n.cache.Clear()
			continue
		}
		dropped, err := n.clearOn(p.addr)
		if err != nil {
			log.Warn("clear: peer unreachable, assuming it holds nothing", "peer", p.id, "err", err)
			continue
		}
		total += dropped
	}

	log.Debug("clear ok", "copies_dropped", total)
	w.Header().Set(DroppedHeader, strconv.Itoa(total))
	w.WriteHeader(http.StatusNoContent)
}

// fetchFrom GETs every version of key from another node's internal /kv endpoint. ok is
// false on a clean 404 (the node answered, holds nothing); err is non-nil only when the
// node could not be reached. Normally one entry; several when the node holds a conflict.
func (n *Node) fetchFrom(addr, key string) (entries []cache.Entry, ok bool, err error) {
	resp, err := n.client.Get("http://" + addr + "/kv/" + key)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	var wires []wireEntry
	if err := json.NewDecoder(resp.Body).Decode(&wires); err != nil {
		return nil, false, err
	}
	entries = make([]cache.Entry, len(wires))
	for i, wv := range wires {
		entries[i] = wv.toEntry()
	}
	return entries, len(entries) > 0, nil
}

// storeOn PUTs one versioned value to another node's internal /kv endpoint, stamped with the
// deadline and vector clock the value already carries. A zero expires means it never expires;
// a nil version is an unversioned write.
func (n *Node) storeOn(addr, key, value string, version vclock.Clock, expires time.Time) error {
	req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/kv/"+key, strings.NewReader(value))
	if err != nil {
		return err
	}
	putExpires(req.Header, expires)
	putVersion(req.Header, version)
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// currentVersion merges the vector clocks of every version the key's reachable owners hold
// — the base a new write bumps from. Unreachable owners are skipped: the write proceeds
// against what it can see, which is exactly how a cut produces concurrent writes.
func (n *Node) currentVersion(key string, owners []owner) vclock.Clock {
	var base vclock.Clock
	for _, o := range owners {
		var entries []cache.Entry
		if o.id == n.id {
			entries, _ = n.cache.GetEntries(key)
		} else {
			entries, _, _ = n.fetchFrom(o.addr, key)
		}
		for _, e := range entries {
			base = vclock.Merge(base, e.Version)
		}
	}
	return base
}

// deleteOn DELETEs key on another node's internal /kv endpoint. had reports whether that
// node was holding it; err is non-nil only when the node could not be reached.
func (n *Node) deleteOn(addr, key string) (had bool, err error) {
	req, err := http.NewRequest(http.MethodDelete, "http://"+addr+"/kv/"+key, nil)
	if err != nil {
		return false, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusNoContent, nil
}

// clearOn empties another node's cache and returns how many entries it dropped.
func (n *Node) clearOn(addr string) (dropped int, err error) {
	req, err := http.NewRequest(http.MethodDelete, "http://"+addr+"/kv", nil)
	if err != nil {
		return 0, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	// A peer that answers without the header dropped nothing we can prove; count 0
	// rather than guessing.
	dropped, _ = strconv.Atoi(resp.Header.Get(DroppedHeader))
	return dropped, nil
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
