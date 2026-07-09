# Roadmap — Self-Healing Distributed Cache

The **naive → measure → iterate** spine. Each phase teaches concepts, then earns a distributed
feature against a metric. Each phase ends with a demo checkpoint and a milestone quiz.

Legend: ☐ not started · ◐ in progress · ☑ done. Keep status in `docs/PROGRESS.md`, not here.

---

## Phase 0 — Foundations & setup
**Goal:** working Go toolchain, project skeleton, and a shared mental model of the whole system.
- Install Go, `go mod init`, project layout (`cmd/`, `internal/`).
- Concepts: what a cache *is* (key→value, TTL, eviction), why distribute one at all, the AP corner
  of CAP, "cluster-in-a-box" honesty.
- Go crash-bits as needed: packages, structs, interfaces, goroutines, channels, `context`.
- **Deliverable:** repo builds and runs a "hello node" that logs its ID.
- **Demo checkpoint:** none yet.

## Phase 1 — Single-node cache (deliberately naive)
**Goal:** a correct in-memory cache on ONE node. No distribution yet — establish the baseline.
- Concepts: hash map store, TTL expiry, **LRU eviction**, thread-safety (mutex vs channels),
  concurrent reads/writes (races!).
- Build: `GET/SET/DELETE` over HTTP; an eviction policy; TTL sweeper.
- **Metric:** hit rate, ops/sec under concurrent load. This is our baseline number.
- **Demo checkpoint:** curl the cache; show eviction and TTL working.

## Phase 2 — Consistent hashing (the hash ring)
**Goal:** shard keys across N nodes so each key has a well-defined home — the heart of the project.
- Concepts: naive `hash(key) % N` and *why it's catastrophic* when N changes (mass remap). Then
  **consistent hashing**: the ring, wraparound, **virtual nodes** for balance, key ownership.
- Build: the ring data structure (hand-rolled), key→node lookup, add/remove node → measure how
  many keys move.
- **Metric:** fraction of keys remapped when a node joins/leaves (should be ~1/N, not ~all).
- **Demo checkpoint:** dashboard v1 — the ring with keys spread across nodes.

## Phase 3 — Replication
**Goal:** every key lives on R nodes so one death doesn't lose data.
- Concepts: replication factor R, **primary + replicas** (next R−1 nodes on the ring), write
  propagation, read strategies, consistency vs availability trade-off (why AP here).
- Build: write to primary + replicas; read with fallback to replicas.
- **Metric:** data survives with a node down (availability under failure).
- **Demo checkpoint:** show the same key present on R nodes.

## Phase 4 — Failure detection & membership
**Goal:** the cluster *notices* when a node dies.
- Concepts: **heartbeats**, timeouts, false positives (GC pause vs real death), membership list,
  a taste of **gossip/SWIM** (why gossip scales better than all-to-all pings).
- Build: heartbeat loop, a failure detector, membership updates.
- **Metric:** detection latency (time from kill → cluster marks it dead); false-positive rate.
- **Demo checkpoint:** kill a node, watch the dashboard mark it dead.

## Phase 5 — Self-heal (the payoff)
**Goal:** on node death, re-replicate under-replicated keys and rebalance — while still serving.
- Concepts: **data migration on membership change**, re-replication to restore R, rebalancing
  ownership, doing it *without* dropping reads (the hard part).
- Build: on membership change → recompute ownership → move/copy keys → restore replication factor.
- **Metric:** time-to-heal (kill → back to full R); read availability during heal (should stay up).
- **Demo checkpoint:** ⭐ THE MONEY DEMO — kill a node, watch keys re-replicate and cache keep
  serving. This is the artifact.

## Phase 6 — Dashboard & failure-injection UI
**Goal:** the demo a stranger can drive.
- Build: live hash-ring viz, per-node key counts, **kill-node / (stretch) partition-network**
  buttons, live metrics (hit rate, replication health, time-to-heal).
- **Demo checkpoint:** full interactive demo, hostable free.

## Phase 7 — Polish, writeup, deploy (stretch)
- Honest writeup (cluster-in-a-box caveat), deploy static frontend + one free container, README
  with the metrics story (naive baseline → each feature earned).

---

## Concept coverage map
By the end we'll have touched: consistent hashing · replication · failure detection & membership ·
data migration on membership change · cache eviction (LRU/TTL) · CAP/AP trade-offs · concurrency
control (races, mutexes). Consensus is intentionally **out of scope** (keeps it tractable; noted as
a future deep-dive → Raft).
