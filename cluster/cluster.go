// Package cluster runs N cache nodes as goroutines in one process
// (cluster-in-a-box) and gives the dashboard a god's-eye view plus the
// failure-injection controls that drive the demo: kill a node, pause its health
// (a GC-pause stand-in), revive it, and watch the cluster re-replicate and keep
// serving.
//
// The manager holds ground truth (which nodes it has killed); the nodes still
// discover each other's deaths on their own via heartbeat, exactly as in the
// tests. The gap between "killed" (instant) and "re-replicated" (a heartbeat +
// grace period later) is the money moment made visible.
package cluster

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/node"
)

const (
	nodeCapacity = 10000

	// Activity-log entries kept, heals included. There IS a cap, deliberately: this
	// is a hosted demo anyone can click Kill on forever, and an append-only list
	// nothing ever trims is the Phase-1 leak wearing a new hat — reachable, growing,
	// uncollectable. 300 is chosen to hold many kills' worth of heals so the kill
	// that caused a heal is still on screen above it, which 40 was not.
	maxEvents = 300

	// Virtual points per node, demo-only. The library default (~150) gives the
	// smoothest load balance, but its 750 ring points render as an illegible haze.
	// The demo trades balance for a ring whose arcs are big enough to see — the
	// engineering rigor stays in the tests, which use the default.
	demoRingReplicas = 8
)

// Cluster owns the nodes and the authoritative membership.
type Cluster struct {
	mu           sync.Mutex
	ids          []string
	rf, wq       int
	grace        time.Duration
	ringReplicas int
	nodes        map[string]*node.Node // live nodes only; a killed id is absent
	addrs        map[string]string     // last address per id (survives a kill/revive)
	client       *http.Client
	events       []Event // kills, revives, writes AND heals, in the order they happened
	nextTick     uint64  // monotonic event id, so the UI can dedupe without a clock
	seeded       int     // demo keys issued so far, so Seed appends instead of rewriting

	// log is the server log, distinct from events above: events are the ~40 lines
	// the dashboard shows a viewer, this is the durable record on stdout and disk.
	// Discards until SetLogger, so tests stay quiet. Atomic because Set and Get read
	// it without holding c.mu.
	log atomic.Pointer[slog.Logger]
}

// Event is one entry in the dashboard's activity log — heals included. Heals live
// in the SAME list as the kills rather than a log of their own, because the
// question a viewer actually has is "which kill caused which copies," and that is
// a question about ORDER. One list and one counter, appended to at the moment each
// thing happens, answers it for free: a heal entry lands after the kill that caused
// it because that is when it happened.
//
// From/To/Keys/Cause are set on heal events only, and omitted from the JSON
// otherwise.
type Event struct {
	ID   uint64 `json:"id"`
	Kind string `json:"kind"` // kill | revive | pause | resume | set | seed | info | heal
	Msg  string `json:"msg"`

	From  string   `json:"from,omitempty"`  // heal: the node that sent the copies
	To    string   `json:"to,omitempty"`    // heal: the node that received them
	Keys  []string `json:"keys,omitempty"`  // heal: exactly which keys moved
	Cause string   `json:"cause,omitempty"` // heal: what the SENDER saw that made it heal
}

// New builds (but does not start) a cluster of the given node ids.
func New(rf, wq int, grace time.Duration, ids ...string) *Cluster {
	c := &Cluster{
		ids:          ids,
		rf:           rf,
		wq:           wq,
		grace:        grace,
		ringReplicas: demoRingReplicas,
		nodes:        make(map[string]*node.Node, len(ids)),
		addrs:        make(map[string]string, len(ids)),
		client:       &http.Client{Timeout: 2 * time.Second},
	}
	c.SetLogger(nil) // discard until someone wires a real one
	return c
}

// SetLogger installs the logger the cluster and every node it owns writes to. Call
// it before Start; nodes created later (a Revive) inherit it. A nil logger
// discards, which is the default — see node.SetLogger for why a library defaults
// to silence.
func (c *Cluster) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	c.log.Store(l)

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.nodes { // nodes already running, if any
		n.SetLogger(l)
	}
}

// logger is the current logger. Never nil — New installs a discarding one.
func (c *Cluster) logger() *slog.Logger { return c.log.Load() }

// Start brings every node up and wires membership so all peers know each other.
func (c *Cluster) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	log := c.logger()
	for _, id := range c.ids {
		n := node.New(id, "127.0.0.1:0", nodeCapacity)
		n.SetLogger(c.logger()) // before Start, so the node's own startup log is captured
		if err := n.Start(); err != nil {
			log.Error("node failed to start, cluster cannot come up", "node", id, "err", err)
			return fmt.Errorf("start %s: %w", id, err)
		}
		c.nodes[id] = n
		c.addrs[id] = n.Addr()
	}
	c.wireAll()
	c.logf("info", "cluster up: %d nodes, R=%d, W=%d, grace=%v", len(c.ids), c.rf, c.wq, c.grace)
	log.Info("cluster up",
		"nodes", len(c.ids),
		"replication_factor", c.rf,
		"write_quorum", c.wq,
		"grace", c.grace,
	)
	return nil
}

// wireAll gives every live node the full peer map and the demo's knobs. Caller
// holds c.mu.
func (c *Cluster) wireAll() {
	peers := make(map[string]string, len(c.addrs))
	maps.Copy(peers, c.addrs)
	for _, n := range c.nodes {
		n.SetRingReplicas(c.ringReplicas) // before SetMembership, which builds the ring
		n.SetMembership(peers)
		n.SetReplication(c.rf, c.wq)
		n.SetHealGracePeriod(c.grace)
	}
}

// Kill closes a node. Its peers keep it in their known-peer map and discover the
// death themselves via heartbeat — the manager does not tell them. That delay is
// the point: the ring reroutes, then the heal restores R a grace period later.
func (c *Cluster) Kill(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[id]
	if !ok {
		c.logger().Warn("kill: node is not running", "node", id)
		return fmt.Errorf("node %s is not running", id)
	}
	// Peers are not told — they must notice the silence via heartbeat themselves.
	c.logger().Warn("node killed (fault injected)",
		"node", id,
		"keys_held", len(n.HeldKeys()),
		"nodes_left", len(c.nodes)-1,
		"heal_expected_in", c.grace,
	)
	n.Close()
	delete(c.nodes, id)
	c.logf("kill", "killed %s — peers will detect the silence and re-replicate its keys", id)
	return nil
}

// Revive starts a fresh node for a killed id on a new port and tells the live
// peers its new address, so the next heartbeat re-admits it. It comes back empty,
// and the heal repopulates it — including the keys it is now the *primary* of,
// which the old primary-only heal could never deliver (see node.heal). Reads keep
// serving throughout, from the copies the heal already placed elsewhere.
func (c *Cluster) Revive(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.nodes[id]; ok {
		c.logger().Warn("revive: node is already running", "node", id)
		return fmt.Errorf("node %s is already running", id)
	}
	if !slices.Contains(c.ids, id) {
		c.logger().Warn("revive: unknown node id", "node", id, "known", strings.Join(c.ids, ","))
		return fmt.Errorf("unknown node id %s", id)
	}

	n := node.New(id, "127.0.0.1:0", nodeCapacity)
	n.SetLogger(c.logger())
	if err := n.Start(); err != nil {
		c.logger().Error("revive failed: could not bind a port", "node", id, "err", err)
		return fmt.Errorf("revive %s: %w", id, err)
	}
	c.nodes[id] = n
	c.addrs[id] = n.Addr()

	// The fresh node gets the whole current view; the peers just get its new addr,
	// which avoids resetting their liveness (which would wrongly re-admit any other
	// killed node instantly).
	peers := make(map[string]string, len(c.addrs))
	maps.Copy(peers, c.addrs)
	n.SetRingReplicas(c.ringReplicas)
	n.SetMembership(peers)
	n.SetReplication(c.rf, c.wq)
	n.SetHealGracePeriod(c.grace)
	for pid, peer := range c.nodes {
		if pid != id {
			peer.SetPeerAddr(id, n.Addr())
		}
	}
	c.logf("revive", "revived %s on a fresh port — it returns empty; reads still serve from replicas", id)
	// Comes back with an empty cache; the heal repopulates it.
	c.logger().Info("node revived (empty, heal will repopulate)",
		"node", id,
		"addr", n.Addr(),
		"nodes_alive", len(c.nodes),
	)
	return nil
}

// Pause stalls (or resumes) a node's health replies without stopping it: a live
// node that merely looks silent, so its peers falsely convict it. With a grace
// period the heal is withheld until the node is confirmed dead — resume it in
// time and no needless re-replication happens.
func (c *Cluster) Pause(id string, paused bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[id]
	if !ok {
		c.logger().Warn("pause: node is not running", "node", id, "paused", paused)
		return fmt.Errorf("node %s is not running", id)
	}
	// n.PauseHealth logs the fault itself (it is the node's own event) — no second
	// line for it here.
	n.PauseHealth(paused)
	if paused {
		c.logf("pause", "paused %s's health — a GC-pause stand-in; peers may falsely suspect it", id)
	} else {
		c.logf("resume", "resumed %s's health — if it beat the grace period, no heal storm", id)
	}
	return nil
}

// Set writes a key through a live node (the real client path: any node
// coordinates). Returns an error if no node is up.
func (c *Cluster) Set(key, value string) error {
	start := time.Now()
	c.mu.Lock()
	coord := c.anyLiveAddrLocked()
	c.mu.Unlock()
	if coord == "" {
		c.logger().Error("write dropped: no live node to coordinate it", "key", key)
		return fmt.Errorf("no live node to coordinate the write")
	}

	req, err := http.NewRequest(http.MethodPut, "http://"+coord+"/set/"+key, bytes.NewReader([]byte(value)))
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger().Error("write failed: coordinator unreachable", "key", key, "coordinator", coord, "err", err)
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		c.logger().Error("write rejected by coordinator", "key", key, "coordinator", coord, "status", resp.StatusCode)
		return fmt.Errorf("set %s: status %d", key, resp.StatusCode)
	}

	c.mu.Lock()
	c.logf("set", "wrote %q via a coordinator", key)
	c.mu.Unlock()
	c.logger().Debug("write ok", "key", key, "coordinator", coord, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// Get reads a key through a live node. found is false on a clean miss; err is set
// only when no node could serve it.
func (c *Cluster) Get(key string) (value string, found bool, err error) {
	start := time.Now()
	c.mu.Lock()
	coord := c.anyLiveAddrLocked()
	c.mu.Unlock()
	if coord == "" {
		c.logger().Error("read dropped: no live node to coordinate it", "key", key)
		return "", false, fmt.Errorf("no live node to coordinate the read")
	}

	resp, err := c.client.Get("http://" + coord + "/get/" + key)
	if err != nil {
		c.logger().Error("read failed: coordinator unreachable", "key", key, "coordinator", coord, "err", err)
		return "", false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	took := time.Since(start).Round(time.Millisecond)
	switch resp.StatusCode {
	case http.StatusOK:
		c.logger().Debug("read hit", "key", key, "took", took)
		return string(body), true, nil
	case http.StatusNotFound:
		c.logger().Debug("read miss", "key", key, "took", took)
		return "", false, nil
	default:
		c.logger().Error("read failed: all owners unreachable",
			"key", key, "coordinator", coord, "status", resp.StatusCode, "took", took)
		return "", false, fmt.Errorf("get %s: status %d", key, resp.StatusCode)
	}
}

// Seed writes n *new* demo keys, kept small so the ring stays legible. The
// cluster numbers them itself rather than taking a range from the caller: a
// dashboard that tracked "how many have I seeded" would be remembering state it
// does not own, and two browser tabs (or a page reload) would then hand out the
// same key numbers twice — the seed button would silently rewrite existing keys
// instead of adding any.
func (c *Cluster) Seed(n int) error {
	if n <= 0 {
		return nil
	}

	// Claim the numbers under the lock, then write outside it: Set does network
	// I/O to the owners, and holding c.mu across that would block the dashboard's
	// State() polls for the duration.
	c.mu.Lock()
	first := c.seeded
	c.seeded += n
	c.mu.Unlock()

	start := time.Now()
	for i := first; i < first+n; i++ {
		if err := c.Set(fmt.Sprintf("key:%d", i), fmt.Sprintf("v%d", i)); err != nil {
			c.logger().Error("seed stopped early: a write failed",
				"key", fmt.Sprintf("key:%d", i), "seeded_so_far", i-first, "err", err)
			return err
		}
	}

	c.mu.Lock()
	c.logf("seed", "seeded %d keys (key:%d..key:%d)", n, first, first+n-1)
	c.mu.Unlock()
	c.logger().Info("seeded keys",
		"count", n,
		"range", fmt.Sprintf("key:%d..key:%d", first, first+n-1),
		"total_seeded", first+n,
		"took", time.Since(start).Round(time.Millisecond),
	)
	return nil
}

// Close stops every live node.
func (c *Cluster) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	stopped := len(c.nodes)
	for _, n := range c.nodes {
		n.Close()
	}
	c.nodes = map[string]*node.Node{}
	c.logger().Info("cluster stopped", "nodes_stopped", stopped)
}

// anyLiveAddrLocked returns some live node's address, deterministically. Caller
// holds c.mu.
func (c *Cluster) anyLiveAddrLocked() string {
	live := make([]string, 0, len(c.nodes))
	for id := range c.nodes {
		live = append(live, id)
	}
	if len(live) == 0 {
		return ""
	}
	sort.Strings(live)
	return c.addrs[live[0]]
}

// logf appends an event to the dashboard's activity strip, trimming to the last
// maxEvents. Caller holds c.mu.
//
// It mirrors the event into the server log at Debug — deliberately not Info, since
// each call site already logs the same fact there with far more context. The
// mirror exists so the file can be lined up against what a viewer actually saw on
// screen (a screenshot's event strip is now findable in the log).
func (c *Cluster) logf(kind, format string, args ...any) {
	c.appendEvent(Event{Kind: kind, Msg: fmt.Sprintf(format, args...)})
}

// appendEvent stamps an event with the next id and files it. Every event — a kill,
// a write, a heal — goes through here and shares ONE counter, which is what makes
// the log's order a faithful record of what happened in what order. Caller holds
// c.mu.
func (c *Cluster) appendEvent(e Event) {
	c.nextTick++
	e.ID = c.nextTick
	c.events = append(c.events, e)
	if over := len(c.events) - maxEvents; over > 0 {
		c.events = slices.Delete(c.events, 0, over) // drop oldest
	}
	c.logger().Debug("dashboard event shown to the viewer", "kind", e.Kind, "event", e.Msg, "event_id", e.ID)
}

// plural is the "s" in "3 keys".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// angleOf maps a ring hash to degrees [0, 360).
func angleOf(h uint32) float64 { return float64(h) / 4294967296.0 * 360.0 }
