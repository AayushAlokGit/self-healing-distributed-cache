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
	"maps"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/node"
)

const (
	nodeCapacity = 10000
	maxEvents    = 40

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
	events       []Event
	nextTick     uint64 // monotonic event id, so the UI can dedupe without a clock
}

// Event is one entry in the dashboard's activity log.
type Event struct {
	ID   uint64 `json:"id"`
	Kind string `json:"kind"` // kill | revive | pause | resume | set | seed | info
	Msg  string `json:"msg"`
}

// New builds (but does not start) a cluster of the given node ids.
func New(rf, wq int, grace time.Duration, ids ...string) *Cluster {
	return &Cluster{
		ids:          ids,
		rf:           rf,
		wq:           wq,
		grace:        grace,
		ringReplicas: demoRingReplicas,
		nodes:        make(map[string]*node.Node, len(ids)),
		addrs:        make(map[string]string, len(ids)),
		client:       &http.Client{Timeout: 2 * time.Second},
	}
}

// Start brings every node up and wires membership so all peers know each other.
func (c *Cluster) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, id := range c.ids {
		n := node.New(id, "127.0.0.1:0", nodeCapacity)
		if err := n.Start(); err != nil {
			return fmt.Errorf("start %s: %w", id, err)
		}
		c.nodes[id] = n
		c.addrs[id] = n.Addr()
	}
	c.wireAll()
	c.logf("info", "cluster up: %d nodes, R=%d, W=%d, grace=%v", len(c.ids), c.rf, c.wq, c.grace)
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
		return fmt.Errorf("node %s is not running", id)
	}
	n.Close()
	delete(c.nodes, id)
	c.logf("kill", "killed %s — peers will detect the silence and re-replicate its keys", id)
	return nil
}

// Revive starts a fresh node for a killed id on a new port and tells the live
// peers its new address, so the next heartbeat re-admits it. It comes back empty
// (repopulation of a returned node is a known gap); reads still serve from the
// copies the heal already placed elsewhere.
func (c *Cluster) Revive(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.nodes[id]; ok {
		return fmt.Errorf("node %s is already running", id)
	}
	if !slices.Contains(c.ids, id) {
		return fmt.Errorf("unknown node id %s", id)
	}

	n := node.New(id, "127.0.0.1:0", nodeCapacity)
	if err := n.Start(); err != nil {
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
		return fmt.Errorf("node %s is not running", id)
	}
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
	c.mu.Lock()
	coord := c.anyLiveAddrLocked()
	c.mu.Unlock()
	if coord == "" {
		return fmt.Errorf("no live node to coordinate the write")
	}

	req, err := http.NewRequest(http.MethodPut, "http://"+coord+"/set/"+key, bytes.NewReader([]byte(value)))
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("set %s: status %d", key, resp.StatusCode)
	}

	c.mu.Lock()
	c.logf("set", "wrote %q via a coordinator", key)
	c.mu.Unlock()
	return nil
}

// Get reads a key through a live node. found is false on a clean miss; err is set
// only when no node could serve it.
func (c *Cluster) Get(key string) (value string, found bool, err error) {
	c.mu.Lock()
	coord := c.anyLiveAddrLocked()
	c.mu.Unlock()
	if coord == "" {
		return "", false, fmt.Errorf("no live node to coordinate the read")
	}

	resp, err := c.client.Get("http://" + coord + "/get/" + key)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK:
		return string(body), true, nil
	case resp.StatusCode == http.StatusNotFound:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("get %s: status %d", key, resp.StatusCode)
	}
}

// Seed writes n demo keys, kept small so the ring stays legible.
func (c *Cluster) Seed(n int) error {
	for i := range n {
		key := fmt.Sprintf("key:%d", i)
		if err := c.Set(key, fmt.Sprintf("v%d", i)); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.logf("seed", "seeded %d keys", n)
	c.mu.Unlock()
	return nil
}

// Close stops every live node.
func (c *Cluster) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.nodes {
		n.Close()
	}
	c.nodes = map[string]*node.Node{}
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

// logf appends an event, trimming to the last maxEvents. Caller holds c.mu.
func (c *Cluster) logf(kind, format string, args ...any) {
	c.nextTick++
	c.events = append(c.events, Event{ID: c.nextTick, Kind: kind, Msg: fmt.Sprintf(format, args...)})
	if len(c.events) > maxEvents {
		c.events = c.events[len(c.events)-maxEvents:]
	}
}

// angleOf maps a ring hash to degrees [0, 360).
func angleOf(h uint32) float64 { return float64(h) / 4294967296.0 * 360.0 }
