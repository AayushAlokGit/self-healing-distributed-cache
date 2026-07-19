// Package cluster runs N cache nodes as goroutines in one process
// (cluster-in-a-box) and gives the dashboard a god's-eye view plus the
// failure-injection controls: kill a node, pause its health, revive it.
//
// The manager holds ground truth about which nodes it killed; the nodes still
// discover each other's deaths on their own, via heartbeat.
package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/node"
)

const (
	nodeCapacity = 10000

	// Activity-log entries kept. The cap is required: an append-only list anyone can
	// grow by clicking Kill is an unbounded leak. 300 keeps a kill on screen above
	// the heals it caused.
	maxEvents = 300

	// Virtual points per node, demo-only: fewer, larger arcs so the ring is legible.
	// Tests use the library default (~150), which balances load better.
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

	// deadlines is the previous poll's key → deadline, and it is what makes an expiry
	// detectable at all: nothing in this system fires a timer when a key dies, so the
	// only way to notice is to remember what was alive last time and compare. Zero
	// Time means the key never expires. See State.
	deadlines map[string]time.Time

	// log is the durable server log, distinct from events above. Discards until
	// SetLogger. Atomic because Set and Get read it without holding c.mu.
	log atomic.Pointer[slog.Logger]
}

// Event is one entry in the dashboard's activity log. Kills, writes and heals share
// one list, so its order answers "which kill caused which copies".
//
// From/To/Keys/Cause are set on heal events only.
type Event struct {
	ID   uint64 `json:"id"`
	Kind string `json:"kind"` // kill | revive | pause | resume | set | read | seed | delete | clear | info | heal | cleanup | expire | reclaim
	Msg  string `json:"msg"`

	From  string   `json:"from,omitempty"`  // heal: the sender. reclaim/cleanup: the node that freed the memory
	To    string   `json:"to,omitempty"`    // heal: the node that received the copies
	Keys  []string `json:"keys,omitempty"`  // heal: the keys moved. expire/reclaim/cleanup: the keys that went
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
		deadlines:    make(map[string]time.Time),
		client:       &http.Client{Timeout: 2 * time.Second},
	}
	c.SetLogger(nil)
	return c
}

// SetLogger installs the logger the cluster and every node it owns writes to. Call
// it before Start; nodes created later (a Revive) inherit it. A nil logger discards,
// which is the default.
func (c *Cluster) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	c.log.Store(l)

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.nodes {
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

// Kill closes a node. Its peers keep it in their known-peer map and must discover
// the death themselves via heartbeat; the manager does not tell them.
func (c *Cluster) Kill(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[id]
	if !ok {
		c.logger().Warn("kill: node is not running", "node", id)
		return fmt.Errorf("node %s is not running", id)
	}
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

// Revive starts a fresh node for a killed id on a new port and tells the live peers
// its new address, so the next heartbeat re-admits it. It comes back empty and the
// heal repopulates it; reads keep serving from the copies placed elsewhere.
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

	// The fresh node gets the whole current view; the peers get only its new addr.
	// Handing them the full map would reset their liveness and wrongly re-admit any
	// other killed node instantly.
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
	c.logger().Info("node revived (empty, heal will repopulate)",
		"node", id,
		"addr", n.Addr(),
		"nodes_alive", len(c.nodes),
	)
	return nil
}

// Pause stalls (or resumes) a node's health replies without stopping it: a live node
// that merely looks silent, so its peers falsely convict it. Resume within the grace
// period and no re-replication happens.
func (c *Cluster) Pause(id string, paused bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, ok := c.nodes[id]
	if !ok {
		c.logger().Warn("pause: node is not running", "node", id, "paused", paused)
		return fmt.Errorf("node %s is not running", id)
	}
	n.PauseHealth(paused) // the node logs the fault itself
	if paused {
		c.logf("pause", "paused %s's health — a GC-pause stand-in; peers may falsely suspect it", id)
	} else {
		c.logf("resume", "resumed %s's health — if it beat the grace period, no heal storm", id)
	}
	return nil
}

// Set writes a key through a live node. via names which node coordinates: "" keeps the
// old behavior (any live node), a node id routes the write through exactly that node — the
// partition demo drives one write through n0 and another through n3, and needs to choose.
// A via that is not a live node is an error (see coordAddrLocked). A ttl of 0 means the key
// never expires. Errors if no node is up.
//
// The ttl travels as a duration only on this first hop: the coordinator turns it into
// an absolute deadline, and every replica and heal copy gets that same instant.
func (c *Cluster) Set(key, value string, ttl time.Duration, via string) error {
	start := time.Now()
	c.mu.Lock()
	coord, err := c.coordAddrLocked(via)
	c.mu.Unlock()
	if err != nil {
		c.logger().Warn("write dropped: named coordinator is not live", "key", key, "via", via, "err", err)
		return err
	}
	if coord == "" {
		c.logger().Error("write dropped: no live node to coordinate it", "key", key)
		return fmt.Errorf("no live node to coordinate the write")
	}

	url := "http://" + coord + "/set/" + key
	if ttl > 0 {
		// A duration string ("250ms", "2m0s"), not a float of seconds: exact at any scale.
		url += "?ttl=" + ttl.String()
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader([]byte(value)))
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
	if ttl > 0 {
		// Remember the deadline HERE, and not only when a poll happens to see the key
		// alive. A key whose whole life fits between two polls would otherwise never be
		// observed alive, so its death could never be noticed either — every TTL shorter
		// than the poll interval would expire in silence. The next poll overwrites this
		// with the coordinator's authoritative instant; in-process, they differ by the
		// length of one HTTP hop.
		c.deadlines[key] = time.Now().Add(ttl)
		c.logf("set", "wrote %q via a coordinator, expiring in %s", key, ttl)
	} else {
		c.logf("set", "wrote %q via a coordinator", key)
	}
	c.mu.Unlock()
	c.logger().Debug("write ok", "key", key, "coordinator", coord, "ttl", ttl, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// ReadHop is what happened at one of a key's owners during a read, in ring order:
// Rank 0 is the primary, the rest are replicas. Outcome is one of node.OutcomeHit,
// OutcomeMiss, OutcomeUnreachable or OutcomeSkipped.
type ReadHop struct {
	Node    string `json:"node"`
	Rank    int    `json:"rank"`
	Role    string `json:"role"`    // primary | replica
	Outcome string `json:"outcome"` // hit | miss | unreachable | skipped
}

// ReadResult is the value plus what the read revealed about the cluster.
//
// Coordinator is the node that took the request; ServedBy is the node the value came
// from. They are usually different: any live node can coordinate a read. Path is every
// owner of the key and what each one said.
type ReadResult struct {
	Value       string    `json:"value"`
	Found       bool      `json:"found"`
	Coordinator string    `json:"coordinator,omitempty"`
	ServedBy    string    `json:"servedBy,omitempty"`
	Path        []ReadHop `json:"path,omitempty"`
	// Conflict is set when the owners hold concurrent siblings the read could not collapse to
	// one: two writes that never saw each other, both kept. Siblings then carries every value
	// and Value is empty — there is no single answer to put there.
	Conflict bool     `json:"conflict,omitempty"`
	Siblings []string `json:"siblings,omitempty"`
}

// Primary is the node the ring says should hold this key: the first owner clockwise.
// Derived from Path, so there is one source of truth for who the owners were.
func (r ReadResult) Primary() string {
	if len(r.Path) == 0 {
		return ""
	}
	return r.Path[0].Node
}

// Fallback reports whether a replica answered because the primary could not.
//
// Two distinct causes, told apart only by Path: the primary was unreachable (dead), or
// it answered and simply lacked the key (a revived node is reachable but empty).
func (r ReadResult) Fallback() bool {
	p := r.Primary()
	return r.Found && r.ServedBy != "" && p != "" && r.ServedBy != p
}

// parseReadPath decodes the trace the coordinator stamped on the response (see
// node.FormatReadPath). A malformed hop is dropped: a successful read must not be
// reported as failed because its annotation was garbled.
func parseReadPath(s string) []ReadHop {
	if s == "" {
		return nil
	}
	var hops []ReadHop
	for i, hop := range strings.Split(s, ",") {
		id, outcome, ok := strings.Cut(hop, ":")
		if !ok || id == "" {
			continue
		}
		role := "replica"
		if i == 0 {
			role = "primary" // first owner clockwise
		}
		hops = append(hops, ReadHop{Node: id, Rank: i, Role: role, Outcome: outcome})
	}
	return hops
}

// Get reads a key through a live node. via names which node coordinates: "" picks any live
// node (the old behavior), a node id routes the read through exactly that node, so the
// reported Coordinator is deterministic — reading one side of a partition means naming the
// node on that side. A via that is not a live node is an error (see coordAddrLocked). Found
// is false on a clean miss; err is set only when no node could serve it.
func (c *Cluster) Get(key, via string) (ReadResult, error) {
	start := time.Now()
	c.mu.Lock()
	coord, err := c.coordAddrLocked(via)
	c.mu.Unlock()
	if err != nil {
		c.logger().Warn("read dropped: named coordinator is not live", "key", key, "via", via, "err", err)
		return ReadResult{}, err
	}
	if coord == "" {
		c.logger().Error("read dropped: no live node to coordinate it", "key", key)
		return ReadResult{}, fmt.Errorf("no live node to coordinate the read")
	}

	resp, err := c.client.Get("http://" + coord + "/get/" + key)
	if err != nil {
		c.logger().Error("read failed: coordinator unreachable", "key", key, "coordinator", coord, "err", err)
		return ReadResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	took := time.Since(start).Round(time.Millisecond)

	res := ReadResult{
		Coordinator: resp.Header.Get(node.CoordinatorHeader),
		ServedBy:    resp.Header.Get(node.ServedByHeader),
		Path:        parseReadPath(resp.Header.Get(node.ReadPathHeader)),
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// A conflict comes back as a JSON array of siblings under X-Conflict, not a plain
		// value: the coordinator found concurrent versions and refused to pick one.
		if resp.Header.Get(node.ConflictHeader) != "" {
			if err := json.Unmarshal(body, &res.Siblings); err != nil {
				c.logger().Error("read: malformed conflict body", "key", key, "err", err)
				return ReadResult{}, fmt.Errorf("get %s: bad conflict body: %w", key, err)
			}
			res.Conflict, res.Found = true, true
			c.logger().Debug("read hit conflict", "key", key, "coordinator", res.Coordinator,
				"siblings", len(res.Siblings), "took", took)
			return res, nil
		}
		res.Value, res.Found = string(body), true
		if res.Fallback() {
			c.mu.Lock()
			c.logf("read", "read %q from %s — its primary %s could not serve it, so a replica did", key, res.ServedBy, res.Primary())
			c.mu.Unlock()
		}
		c.logger().Debug("read hit", "key", key, "coordinator", res.Coordinator,
			"served_by", res.ServedBy, "fallback", res.Fallback(), "took", took)
		return res, nil
	case http.StatusNotFound:
		// A miss still carries the path: every owner answered, none had the key.
		c.logger().Debug("read miss", "key", key, "took", took)
		return res, nil
	default:
		c.logger().Error("read failed: all owners unreachable",
			"key", key, "coordinator", coord, "status", resp.StatusCode, "took", took)
		return ReadResult{}, fmt.Errorf("get %s: status %d", key, resp.StatusCode)
	}
}

// Seed writes n *new* demo keys. The cluster numbers them itself rather than taking a
// range from the caller: a client tracking "how many have I seeded" would be holding
// state it does not own, so two tabs or a reload would rewrite existing keys.
func (c *Cluster) Seed(n int) error {
	if n <= 0 {
		return nil
	}

	// Claim the numbers under the lock, then write outside it: Set does network I/O,
	// and holding c.mu across that would block the dashboard's State() polls.
	c.mu.Lock()
	first := c.seeded
	c.seeded += n
	c.mu.Unlock()

	start := time.Now()
	for i := first; i < first+n; i++ {
		// ttl 0: seeded keys are permanent, so the ring never empties itself mid-demo. via ""
		// lets any live node coordinate — seeding is not a per-node demo, it just fills the ring.
		if err := c.Set(fmt.Sprintf("key:%d", i), fmt.Sprintf("v%d", i), 0, ""); err != nil {
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

// Delete removes a key and returns the ids of the nodes that were holding a copy. Empty is
// not an error: the caller asked for the key to be gone, and it is.
//
// The coordinator broadcasts it to every peer rather than to the key's owners — see
// node.handleClientDelete, which is where that matters.
func (c *Cluster) Delete(key string) ([]string, error) {
	start := time.Now()
	c.mu.Lock()
	coord := c.anyLiveAddrLocked()
	c.mu.Unlock()
	if coord == "" {
		c.logger().Error("delete dropped: no live node to coordinate it", "key", key)
		return nil, fmt.Errorf("no live node to coordinate the delete")
	}

	req, err := http.NewRequest(http.MethodDelete, "http://"+coord+"/del/"+key, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger().Error("delete failed: coordinator unreachable", "key", key, "coordinator", coord, "err", err)
		return nil, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		c.logger().Error("delete rejected by coordinator", "key", key, "coordinator", coord, "status", resp.StatusCode)
		return nil, fmt.Errorf("delete %s: status %d", key, resp.StatusCode)
	}

	// Non-nil: a nil slice marshals to JSON null, and the API's contract is a list.
	dropped := []string{}
	if h := resp.Header.Get(node.DroppedHeader); h != "" {
		dropped = strings.Split(h, ",")
	}

	c.mu.Lock()
	// Forget the deadline, or noteExpiries reports a deleted key whose TTL had just run
	// out as an expiry: it tells "expired" from "lost" by the deadline it remembers.
	delete(c.deadlines, key)
	if len(dropped) == 0 {
		c.logf("delete", "deleted %q — no node was holding it", key)
	} else {
		c.logf("delete", "deleted %q from %s — every peer was asked, not just the owners", key, strings.Join(dropped, ", "))
	}
	c.mu.Unlock()

	c.logger().Info("key deleted",
		"key", key,
		"coordinator", coord,
		"dropped_by", strings.Join(dropped, ","),
		"took", time.Since(start).Round(time.Millisecond),
	)
	return dropped, nil
}

// Clear removes every key from every node and returns how many distinct keys it dropped.
// The count is keys, not copies: one key on three replicas counts once.
func (c *Cluster) Clear() (int, error) {
	start := time.Now()

	c.mu.Lock()
	coord := c.anyLiveAddrLocked()
	// Count distinct keys here: the nodes can only report copies, and "cleared 36 keys" for
	// a 12-key ring at R=3 is just the replication factor leaking into the UI.
	before := make(map[string]struct{})
	for _, n := range c.nodes {
		for _, k := range n.HeldKeys() {
			before[k] = struct{}{}
		}
	}
	c.mu.Unlock()

	if coord == "" {
		c.logger().Error("clear dropped: no live node to coordinate it")
		return 0, fmt.Errorf("no live node to coordinate the clear")
	}

	req, err := http.NewRequest(http.MethodDelete, "http://"+coord+"/del", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger().Error("clear failed: coordinator unreachable", "coordinator", coord, "err", err)
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		c.logger().Error("clear rejected by coordinator", "coordinator", coord, "status", resp.StatusCode)
		return 0, fmt.Errorf("clear: status %d", resp.StatusCode)
	}
	copies, _ := strconv.Atoi(resp.Header.Get(node.DroppedHeader))

	c.mu.Lock()
	clear(c.deadlines) // same reason as Delete: nothing survives to expire

	// Seed's counter exists so a second Seed appends instead of rewriting key:0..key:n-1.
	// Nothing is left to rewrite, so restart it: otherwise the next seed opens at key:37 and
	// "clear, then seed" never lands on the same ring twice.
	c.seeded = 0

	c.logf("clear", "deleted all %d key%s — every peer was asked, not just the owners", len(before), plural(len(before)))
	c.mu.Unlock()

	c.logger().Info("cluster cleared",
		"keys", len(before),
		"copies_dropped", copies,
		"coordinator", coord,
		"took", time.Since(start).Round(time.Millisecond),
	)
	return len(before), nil
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

// NoSuchNodeError is returned when a caller names a coordinator (via) that is not a
// currently live node — either killed or never part of this cluster. It is a distinct type,
// not a bare fmt.Errorf, so a handler can tell "you asked for a node that isn't there" (a
// 400: the request named something bad) apart from "the cluster is down" (a 502: the request
// was fine). The whole point of via is determinism, so a dead name must fail loudly rather
// than fall back to another node.
type NoSuchNodeError struct{ ID string }

func (e *NoSuchNodeError) Error() string { return "no such live node: " + e.ID }

// coordAddrLocked resolves which live node's address should coordinate a request. Empty via
// keeps the old behavior: anyLiveAddrLocked picks the first live node, and its address — or
// "" when the whole cluster is down — comes straight back with a nil error. A non-empty via
// names a specific coordinator and must resolve to a node that is live RIGHT NOW: c.nodes
// holds only live nodes (a kill deletes the id, a revive re-adds it), so a via absent from it
// is killed or unknown, and we refuse with *NoSuchNodeError rather than silently pick someone
// else. c.addrs is keyed by every known id, live or not, so it is safe to read once liveness
// is confirmed. Caller holds c.mu.
func (c *Cluster) coordAddrLocked(via string) (string, error) {
	if via == "" {
		return c.anyLiveAddrLocked(), nil
	}
	if _, alive := c.nodes[via]; !alive {
		return "", &NoSuchNodeError{ID: via}
	}
	return c.addrs[via], nil
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

// logf appends an event to the dashboard's activity strip, and mirrors it into the
// server log at Debug (each call site already logs the same fact at Info, with more
// context). Caller holds c.mu.
func (c *Cluster) logf(kind, format string, args ...any) {
	c.appendEvent(Event{Kind: kind, Msg: fmt.Sprintf(format, args...)})
}

// appendEvent stamps an event with the next id and files it, trimming to the last
// maxEvents. Every event goes through here and shares ONE counter, which is what makes
// the log's order a faithful record. Caller holds c.mu.
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

// plural2 is plural for the words that do not just take an "s".
func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// angleOf maps a ring hash to degrees [0, 360).
func angleOf(h uint32) float64 { return float64(h) / 4294967296.0 * 360.0 }
