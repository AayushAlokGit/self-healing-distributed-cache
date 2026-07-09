# High-Level Design — Self-Healing Distributed Cache

> **Status:** APPROVED (2026-07-08). All six §10 decisions are **locked** — ready to start Phase 0.
> This is a *high-level* design — components, data, and flows, not code. Low-level details (structs,
> exact wire formats) come per-phase in `docs/ROADMAP.md`.

---

## 1. Goal & scope

**Goal:** an N-node in-memory key→value cache that shards keys across nodes, replicates them for
redundancy, detects node death, and **auto-heals** (re-replicates/rebalances) while continuing to
serve reads.

**The signature demo:** kill a node → its keys re-appear on other nodes and the cache keeps
answering GETs, with no downtime.

**In scope:** consistent hashing, replication, heartbeat/gossip failure detection, self-heal, a
live dashboard with failure injection.

**Out of scope (deliberate):** consensus (Raft/Paxos), strong/linearizable consistency,
cross-datacenter, persistence to disk, authentication. See §9.

---

## 2. Requirements

**Functional**
- `SET key value [ttl]`, `GET key`, `DELETE key`.
- Keys are distributed across nodes (no single node holds everything).
- Each key is stored on **R** nodes (replication factor, default R=3).
- A client may contact **any** node; that node routes the request to the right owner(s).
- On node death, under-replicated keys are re-replicated back to R copies automatically.
- Operator can inject failure (kill a node; stretch: partition the network).

**Non-functional**
- **Availability first (AP):** stay serving during node failure; tolerate brief staleness.
- **Free / cluster-in-a-box:** N nodes as N processes/goroutines in ONE container.
- **Observable:** live view of ring, per-node key counts, replication health, heal time.
- **No central coordinator / no single point of failure:** all nodes are equal peers.

---

## 3. Key design decisions (the "why")

| Decision | Choice | Rationale |
|---|---|---|
| Consistency model | **AP / eventual** | A cache tolerates staleness; DB behind it is the safety net. |
| Coordination | **Peer gossip, no consensus** | Removing the coordinator removes the thing that would need consensus. |
| Sharding | **Consistent hashing + virtual nodes** | Minimal key movement when nodes join/leave (~1/N, not all). |
| Replication | **Primary + next R−1 nodes on the ring** | Simple, deterministic replica placement from the ring. |
| Eviction | **LRU + TTL** | Bounded memory; TTL doubles as a staleness self-heal. |
| Topology | **Cluster-in-a-box** | Free to host; protocol stays real (real sockets, real detection). |
| Conflict resolution | **Last-write-wins (stretch)** | Only needed under partition; simplest reconcile rule. |

---

## 4. Architecture

All nodes are **identical peers**. Each node runs the same set of internal components and exposes
two interfaces: a **client API** (for cache users) and an **internal API** (for node-to-node
traffic). A separate **dashboard** reads cluster state and drives failure injection.

```
                         ┌───────────────────────────┐
        cache clients →  │        Dashboard (UI)      │  ← operator (kill node, view ring)
                         └─────────────┬─────────────┘
                                       │ reads state / sends admin cmds (HTTP)
        ┌──────────────────────────────┼──────────────────────────────┐
        │                              │                              │
 ┌──────▼───────┐              ┌───────▼──────┐              ┌────────▼─────┐
 │    Node 1    │◄────────────►│    Node 2    │◄────────────►│    Node 3    │  ... Node N
 │  (peer)      │  internal    │  (peer)      │  internal    │  (peer)      │
 └──────────────┘   RPC        └──────────────┘   RPC        └──────────────┘
   every node talks to peers directly; no coordinator sits above them.
```

**Per-node components** (each node contains all of these):

```
 ┌─────────────────────────── Node ───────────────────────────┐
 │  Client API      GET / SET / DELETE  (HTTP)                 │
 │      │                                                      │
 │      ▼                                                      │
 │  Router ──────── consults ──────► Hash Ring (owner+replicas)│
 │      │                                                      │
 │      ├──► Store Engine  (in-mem map + LRU + TTL sweeper)    │
 │      │                                                      │
 │      └──► Replicator ── async forward ──► peer nodes        │
 │                                                             │
 │  Membership + Failure Detector  (heartbeat / gossip)        │
 │      │  detects join/leave/death, updates ring              │
 │      ▼                                                      │
 │  Healer  (on membership change → re-replicate / rebalance)  │
 │                                                             │
 │  Internal API   replicate · heartbeat · transfer-keys       │
 └─────────────────────────────────────────────────────────────┘
```

| Component | Responsibility | Phase |
|---|---|---|
| **Client API** | GET/SET/DELETE over HTTP; any node accepts any key | 1 |
| **Store Engine** | in-memory map, LRU eviction, TTL expiry | 1 |
| **Hash Ring / Router** | map key → owner + replica nodes; recompute on membership change | 2 |
| **Replicator** | forward writes to R−1 replicas; read fallback to replicas | 3 |
| **Membership + Failure Detector** | heartbeats/gossip; maintain each node's own "who's alive" view | 4 |
| **Healer** | on membership change, restore R copies; rebalance ownership | 5 |
| **Internal API** | node↔node RPC: replicate, heartbeat, transfer keys | 3–5 |
| **Dashboard** | visualize ring + metrics; inject failure | 6 |

---

## 5. Data model & placement

- **Entry:** `key → { value, expiresAt, version }`. `version` (timestamp/counter) supports
  last-write-wins if we add conflict resolution.
- **The ring:** a hash space `0 … 2³²−1` wrapped into a circle. Each physical node is placed at
  many points via **virtual nodes** (vnodes) for even spread.
- **Ownership:** `hash(key)` → walk clockwise → first node = **primary**; next R−1 distinct
  physical nodes = **replicas**.
- **Membership view:** each node holds its *own* table `nodeID → {addr, lastHeard, state}`.
  Views may briefly differ; gossip converges them (AP).

---

## 6. Core flows

**SET (write path)** — client hits any node ("coordinator for this request"):
```
client → Node A: SET k v
  Node A: owner(k)? → ring says {primary=B, replicas=[C,D]}
  Node A → B (primary): store k
  B → C, D (replicas): replicate k   [async — see ⚑ ack decision]
  B → Node A: ok  → client
```

**GET (read path):**
```
client → Node A: GET k
  Node A: ring → {B primary, C, D replicas}
  Node A → B: read k
     if B down / times out → try C, then D   (availability via fallback)
  value → client
```

**Node join:**
```
new node N boots → announces itself (gossip)
  → peers add N to membership, N added to ring at its vnodes
  → keys that now belong to N are transferred to it (bounded movement, ~1/N)
```

**Node death → self-heal (the payoff):**
```
Node D stops sending heartbeats
  → peers' failure detectors time out → mark D dead (each in its own view)
  → gossip converges: cluster agrees(-ish) D is gone
  → ring recomputed without D
  → Healer: find keys now under-replicated (had a copy on D)
       → copy them from a surviving replica to a new node to restore R
  → throughout: reads keep being served from surviving replicas   ✅
```

**Who runs the heal (decided).** There is no dedicated healer node. Heal is coordinated **per key
range by that range's current primary** — deterministic from the ring, so no election is needed and
no two nodes fight over the same range. Each node **scans only the keys it already holds** (no node
knows the global keyset); for each such key it recomputes the replica set against the new ring and,
as the range's primary, ensures the R copies are whole — pushing a key to any replica node that is
missing it (and, on a *join*, pulling keys that now belong to it). This works because every key that
can still be healed is held by at least one survivor that will find it by scanning locally; if all R
copies die at once, no survivor holds it and it is unrecoverable (accepted, see §9).

### 6.1 Why node death causes staleness (and why we accept it)

**Staleness** = a read returns a value that is no longer the most recent one written. Self-heal (above)
restores *redundancy* (R copies), **not** *freshness* — these are different problems, and it's worth
being explicit about the second one.

The root cause is in our own write path + open decision #2: **writes are asynchronous.** The primary
stores the value and acks the client *immediately*, then forwards to the R−1 replicas **in the
background**. So there is always a **replication window** in which the primary holds a fresher copy
than its replicas. Node death turns that window into staleness:

```
client → B (primary): SET k = v2      (replicas C, D still hold v1)
  B stores v2, acks client
  B begins async replicate → C, D  … but B dies before it lands
  → ring recomputes without B; a former replica (C) becomes primary
  → GET k now returns v1  ← STALE  (and v2, which lived only on B, is a LOST WRITE)
  → Healer copies C's v1 around to restore R=3
       cluster now reports HEALTHY while serving a STALE value
```

Two sources, in order of severity:
1. **The freshest copy dies.** If the primary dies inside the replication window, the newest write
   existed only there — it dies with the node (a *lost write*), and survivors serve an older value.
2. **Detection lag.** Between B actually dying and every peer marking it dead, membership views
   diverge; a read may fall back to a replica that missed the latest write → stale.

**Why this is acceptable (it's the cost of choosing AP, see §3):** this is a *cache*, not the source
of truth. Two mechanisms bound the damage — **TTL** (the stale entry expires; the next read misses and
refetches from the authoritative backing DB) and, later, **read-repair / anti-entropy** to shorten the
window. Tuning open decision #2 toward "wait for W replica acks" shrinks the window at the cost of
write latency. This is exactly the "tolerate brief staleness / eventual consistency" we committed to.

### 6.2 Failure modes (catalog)

The design is **AP**, so it favors staying available and accepts bounded staleness/loss. The table
summarizes what can go wrong and how the design responds. (Detailed reasoning lives in the session
notes; this is the concise reference.)

| Failure mode | What happens | Design response / mitigation |
|---|---|---|
| **Lost write** — primary acks client, then dies before replicating | Acked write existed only on the primary → gone. Survivors serve the old value (stale) or 404 (lost insert). | Async + primary-only ack is the fast default; open decision #2 (`W` replica acks) turns single-node silent loss into needing `≥W` simultaneous failures. TTL + read-repair bound residual staleness. |
| **Detection window** — node dead but still in peers' ring | Ring still routes to the corpse: reads time out then **fall back** to a live replica (survive, +latency); writes to its ranges briefly unavailable/risky. | Bounded by an **app-level request timeout** (not OS/TCP). Read fallback (§6). Window length = detection + heal (tracked in §8). |
| **Available ≠ fully-replicated** — the heal window | After promotion (instant) but before heal (later), a key has `<R` copies while still serving reads. | Healer restores `R` in the background; §8 tracks time-to-heal. Accept the window; a second loss inside it is the risk. |
| **False positive** — live node marked dead (GC pause, slow I/O, blip) | Needless promotion + migration → CPU/network cost → can slow other nodes → secondary false positives → **flapping / cascade storm**. | Tune the timeout (fundamental fast-vs-wrong tradeoff); grace/debounce; incarnation numbers; phi-accrual / SWIM indirect probes (open decision #3). §8 tracks false-positive rate. |
| **Network partition / split-brain** — cluster splits; each side thinks it's the survivor | Both sides serve (AP) → **two primaries per key** → divergent writes. | Views can't converge until link heals. On heal, reconcile with **last-write-wins** (§3, stretch) — the losing write is silently dropped (the accepted price of AP). Partitions are *injected* via `/admin/partition`. |
| **Two primaries → write conflict** — general form of the above (also from asymmetric false positive) | Two nodes both accept writes for the same key → conflicting values. | The per-key primary normally serializes writes (no conflict); conflict arises **only** when that invariant breaks → resolved by LWW-by-version. |
| **Correlated total loss** — all `R` replicas die at once (e.g. the whole container) | Key is unrecoverable — no survivor holds it to heal from. | Out of scope for cluster-in-a-box (shared fate); a real deployment spreads replicas across failure domains. Backing DB is the source of truth. |

**Cross-cutting tension:** the detection timeout trades *fast failover* (short → small window, but more
false positives) against *stability* (long → few false positives, but slow failover). No single value
is both — a dead node and a slow node emit the same signal (silence). Choosing **AP** means we take
staleness/lost-writes (bounded by TTL) instead of the **CP** alternative (quorum/consensus → minority
side unavailable), which is out of scope (§9).

---

## 7. Interfaces (high level)

**Client API (HTTP)**
- `GET /kv/{key}` → value | 404
- `PUT /kv/{key}` body=value, `?ttl=` → ok
- `DELETE /kv/{key}` → ok

**Internal API (node↔node)**
- `heartbeat(from, membershipDigest)` — liveness + gossip piggyback
- `replicate(key, value, version, ttl)` — primary → replica
- `transferKeys(range)` — bulk move during join/heal
- `readReplica(key)` — fallback read

**Admin/Dashboard API**
- `GET /cluster` → nodes, states, ring layout, key counts, metrics
- `POST /admin/kill/{nodeID}` — failure injection
- `POST /admin/partition` — stretch: simulate network split

---

## 8. Metrics (drive the naive→iterate story & the demo)

- **Keys remapped** on join/leave (target ~1/N — proves consistent hashing works).
- **Detection latency** (kill → marked dead) and **false-positive rate**.
- **Time-to-heal** (kill → back to full R copies).
- **Read availability during heal** (should stay ~100%).
- **Hit rate**, ops/sec (baseline from Phase 1).

---

## 9. Explicitly out of scope (with honest caveats)

- **Consensus / strong consistency** — we're AP; a production version would likely put consensus on
  the *control plane* (ring/membership) so two nodes never disagree on ownership.
- **Split-brain resolution** — partitions handled only as a stretch goal (last-write-wins).
- **Disk persistence / crash recovery of a single node's data** — pure in-memory; the backing DB is
  the source of truth.
- **Security/auth, multi-region.**

---

## 10. Locked decisions ✅  (2026-07-08)

All chosen for **learnability + a clean demo** over raw performance. All easily reversible.

1. **Node model → goroutines in one process.** Each node binds its own localhost port and talks to
   peers over **real HTTP sockets** (never shared memory) — so message passing and failure detection
   stay real; only the deployment is collapsed. *Reserve:* write each node to own its port/config so
   splitting into separate OS processes later is a small change.
2. **Write acknowledgement → primary-only ack (to start).** Fast; we accept the known lost-write
   window. *Reserve:* add a configurable `W`-replica-ack knob in Phase 3 to measure and then close
   the durability/latency tradeoff live.
3. **Failure detection → all-to-all heartbeats.** O(N²) is fine at N=3–7 and lets us watch every
   heartbeat. *Reserve:* SWIM/gossip noted as the scale-out path (indirect probes cut false
   positives); revisit only if N grows.
4. **Transport → HTTP/JSON everywhere.** Every message is human-readable (curl/browser/DevTools),
   which is worth more than speed here. *Reserve:* gRPC/protobuf if performance ever becomes a goal.
5. **Dashboard → polish is a priority** (it's the recruiter-facing "money moment": kill a node, watch
   keys re-replicate live). Invest in the visuals — smooth ring animation, live per-node state, a
   satisfying failure-injection interaction. A framework or a viz library (e.g. D3) is fine **if it
   elevates the demo**; the only hard constraint is that it stays **static-hostable and free** (build
   to static assets — no paid backend for the UI itself). Final stack chosen in Phase 6.
6. **Replication factor → R=3, configurable.** Kill one node → 2 copies survive → heal back to 3
   (clean "keeps serving + heals" demo; pairs with W=2/R=2 quorum → `R+W>N`). *Reserve:* configurable
   so we can also demo the scarier R=2 behavior.
