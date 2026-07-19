# High-Level Design — Self-Healing Distributed Cache

> **Status:** APPROVED 2026-07-08; **BUILT (Phases 0–6) as of 2026-07-11.** All six §10 decisions are
> locked. This is the *design* — components, data, and flows, not code. Where building it **corrected**
> the design, the correction is marked **⇒ AS BUILT** in place; those markers are the honest record of
> what a design cannot know in advance. Measurements and the narrative live in `docs/PROGRESS.md`.

**What shipped vs. what this document designed:**

| Designed | Reality |
|---|---|
| `SET` / `GET` / `DELETE` | `DELETE` **broadcasts to every peer**, not to the R owners the ring names (§7). |
| Entry carries a `version` for LWW | **⇒ AS BUILT (7A, on `cap-demo`).** Not LWW — **vector clocks.** A key holds `[]Entry`; the heal reconciles by dominance and *keeps* concurrent siblings instead of dropping them; a read surfaces them as a conflict. The "presence ≠ version" gap is closed in the read, the heal, and cleanup. See PROGRESS S18–S19. |
| Async replication, primary acks first | **Synchronous fan-out** to all R owners, acking after **W** (default W=1). The lost-write window in §6.1 is real all the same — W=1 *is* primary-only ack. |
| Heal coordinated by the range's **primary** | **Corrected — see §6.** The primary rule stranded keys forever. The healer is the first owner, in ring order, that **actually holds** the key. |
| `/admin/partition` (network split injection) | **⇒ AS BUILT (7A, on `cap-demo`).** The cut — a partition injected **under the HTTP clients** (`node.gate` refuses blocked peers in `RoundTrip`, for data *and* health), driven from the dashboard via `Cluster.Cut`/`Mend`. Kill / revive / pause-health remain. |

---

## 0. The system, in one frame

**This is a Dynamo-style, leaderless AP key-value cache.** Consistent-hash ring, R replicas per key,
any node coordinates any key, **no consensus**. Nearly every choice below is downstream of that one
identity. The two dashboard tabs are the *same* system under **two distributed-systems failure modes**,
and the difference between them is the whole point:

| | **Node failure** (Tab 1) | **Network partition** (Tab 2) |
|---|---|---|
| What breaks | a node **dies** | the network **splits** — nodes alive, mutually unreachable |
| What it costs | **staleness / under-replication** — a *missing* copy | **divergence** — two writes that both happened |
| Why | one truth exists; it just is not everywhere yet | two truths exist; neither is "later" than the other |
| Coping mechanism | heartbeats · **independent per-node membership views** (no consensus on who is alive) · a heal loop that restores R copies | **vector clocks** detect the concurrency · **siblings surfaced to the client** to resolve (à la Dynamo's cart merge) |

**Divergence has exactly one cause: concurrent writes** — two writes with no happens-before relation
(neither saw the other). Causally-ordered writes never diverge (the later dominates; the heal just
copies it over). A partition *forces* concurrency — both sides write blind to each other — but
concurrency, and so divergence, can also arise from a plain write race on a healthy network; the
partition demo is simply the most reliable way to *manufacture* it. "Concurrent" is the **happens-before**
sense (no information flowed), not "same wall-clock instant."

**Why vector clocks exist at all: because we chose AP.** They are the machinery for *detecting*
concurrency we chose to allow. The strong-consistency end does not remove them by resolving conflicts
better — it removes the *need* for them by **preventing** the conflict:

- **AP (this system) — detect and defer.** Accept every write, record the lack of order (concurrent
  clocks), surface siblings, let the client reconcile. Always available; may hand back a conflict.
- **CP (linearizable) — serialize and prevent.** Force every write through a single total order
  (consensus / a leader) *at write time*, so no two writes are ever left unordered: no siblings, no
  resolution needed. The conflict does not vanish — it becomes a **refused write** on the minority side
  of a partition. Consistent always; unavailable to the minority.

So the `W` / `R_read` dial (Tab 2) is **Cassandra-style tunable consistency, not a CP switch.** Turning
it toward `QUORUM` (`R_read + W > RF`) buys read-recency — but the currency is **availability** (the
minority partition must refuse), and even maxed it is *not* linearizable: a quorum makes the latest
write *reachable*, it does not *serialize* concurrent ones, so siblings still appear and the vector
clocks are still needed at every setting. **Full/linearizable consistency is a different architecture** —
consensus and a serialization point, i.e. a CP rebuild, not a dial position.

*One honest artifact of being AP:* the manager's god's-eye dashboard **cannot see a partition** — it
kills no one, so it still counts every node alive. *Dead* is a per-node **belief**, not a global fact.

---

## 1. Goal & scope

**Goal:** an N-node in-memory key→value cache that shards keys across nodes, replicates them for
redundancy, detects node death, and **auto-heals** (re-replicates/rebalances) while continuing to
serve reads.

**The signature demo:** kill a node → its keys re-appear on other nodes and the cache keeps
answering GETs, with no downtime.

**In scope:** consistent hashing, replication, heartbeat failure detection, self-heal, a live dashboard
with failure injection. **Out of scope (deliberate):** consensus (Raft/Paxos), strong consistency,
cross-datacenter, disk persistence, authentication. See §9.

---

## 2. Requirements

**Functional**
- `SET key value [ttl]`, `GET key`, `DELETE key`.
- Keys are distributed across nodes; each key is stored on **R** nodes (default R=3).
- A client may contact **any** node; that node routes the request to the right owner(s).
- On node death, under-replicated keys are re-replicated back to R copies automatically.
- Operator can inject failure (kill a node).

**Non-functional**
- **Availability first (AP):** stay serving during node failure; tolerate brief staleness.
- **Free / cluster-in-a-box:** N nodes as N goroutines in ONE container.
- **Observable:** live view of ring, per-node key counts, replication health, heal time.
- **No central coordinator / no single point of failure:** all nodes are equal peers.

---

## 3. Key design decisions (the "why")

| Decision | Choice | Rationale |
|---|---|---|
| Consistency model | **AP / eventual** | A cache tolerates staleness; the DB behind it is the safety net. |
| Coordination | **Peers, no consensus** | Removing the coordinator removes the thing that would need consensus. |
| Sharding | **Consistent hashing + virtual nodes** | Minimal key movement when nodes join/leave (~1/N, not all). |
| Replication | **Primary + next R−1 nodes on the ring** | Simple, deterministic replica placement from the ring. |
| Eviction | **LRU + TTL** | Bounded memory; TTL doubles as a staleness self-heal. |
| Topology | **Cluster-in-a-box** | Free to host; protocol stays real (real sockets, real detection). |
| Conflict resolution | **Last-write-wins (stretch)** | Only needed under partition; simplest reconcile rule. |

---

## 4. Architecture

All nodes are **identical peers**. Each runs the same components and exposes two interfaces: a **client
API** (for cache users) and an **internal API** (node-to-node). A separate **dashboard** reads cluster
state and drives failure injection. Every node talks to peers directly; no coordinator sits above them.

```
 ┌─────────────────────────── Node ───────────────────────────┐
 │  Client API      GET / SET / DELETE  (HTTP)                 │
 │      │                                                      │
 │      ▼                                                      │
 │  Router ──────── consults ──────► Hash Ring (owner+replicas)│
 │      │                                                      │
 │      ├──► Store Engine  (in-mem map + LRU + TTL sweeper)    │
 │      │                                                      │
 │      └──► Replicator ────── forward ──────► peer nodes      │
 │                                                             │
 │  Membership + Failure Detector  (heartbeats)                │
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
| **Membership + Failure Detector** | heartbeats; maintain each node's own "who's alive" view | 4 |
| **Healer** | on membership change, restore R copies; rebalance ownership | 5 |
| **Internal API** | node↔node RPC: replicate, heartbeat, transfer keys | 3–5 |
| **Dashboard** | visualize ring + metrics; inject failure | 6 |

### One process, more than one cluster

> **⇒ PLANNED (Phase 7, step 7.0 — see `CAP_DEMO.md` §2).** The container will host **two independent
> clusters**: the Phase 6 replication demo, and the CAP demo (which leaves the network cut for minutes at a
> time and therefore cannot share a ring). One tab each.

**The cluster layer needs no changes for this, and that's worth understanding rather than just noting.** Two
existing decisions did the work:

- **Nodes bind `127.0.0.1:0`**, so the OS assigns every port. Two clusters cannot collide because nobody
  picked a number to collide over.
- **`Cluster` holds all its state in its own fields** (`nodes`, `addrs`, `events`, `deadlines`, `seeded`),
  and there is **no package-level mutable state** in `cluster/`, `node/`, or `cache/` — the only `var` in any
  of them is a compile-time interface assertion. `wireAll` only ever wires peers listed in that cluster's own
  `addrs`, so two clusters cannot discover each other even in principle.

Verified (S17) with a throwaway probe under `-race`: killing `n2` in cluster A left B reporting 5 alive, a
key written to A was invisible in B, and B's event log stayed clean. **Isolation is structural, not
disciplined** — there is no shared thing for a second cluster to corrupt.

The work is therefore entirely in the serving layer (§7) and the frontend. ⚠️ **The one cost is idle CPU:**
two clusters means 10 nodes heart-beating forever instead of 5, on a free tier chosen partly for tolerating
a process that must never stop beating (§8.5).

---

## 5. Data model & placement

- **Entry:** `key → { value, expiresAt, version }`. `version` supports last-write-wins.
  > **⇒ AS BUILT: there is no `version`.** The entry is `{value, expires}`. This is the design's most
  > consequential unbuilt piece: with no version, the heal can only ask *"do you have key k?"* and a
  > `200` means **"somebody has *a* value," not "*the* value."** So a divergent key is **skipped, and the
  > conflict preserved forever**, and a client read returns the first reachable owner in ring order —
  > **ring geometry decides, stably and silently. Presence ≠ version.**
- **The ring:** a hash space `0 … 2³²−1` wrapped into a circle. Each physical node is placed at many
  points via **virtual nodes** (vnodes) for even spread.
  > **⇒ AS BUILT: two vnode counts, and the demo does not use the good one.** `ring.defaultReplicas = 150`
  > is the library default and what every Phase-2 measurement was taken at (load span 65× → 1.4×). The
  > **cluster overrides it to 8** (`cluster.demoRingReplicas`) because 150 × 5 nodes is 750 ticks of hair on
  > the dashboard and no viewer can see a key land in an arc. So the shipped demo trades balance for
  > legibility — the mechanism is identical, the spread is just coarser. The tests keep the default, which
  > is why the *claim* stays honest even though the *picture* is not the balanced case.
- **Ownership:** `hash(key)` → walk clockwise → first node = **primary**; next R−1 distinct physical
  nodes = **replicas**.
- **Membership view:** each node holds its *own* table `nodeID → {addr, lastHeard, state}`. Views may
  briefly differ, and there is no consensus to converge them (AP).

---

## 6. Core flows

**SET (write path)** — client hits any node ("coordinator for this request"):
```
client → Node A: SET k v [ttl]
  Node A: owner(k)? → ring says {primary=B, replicas=[C,D]}
  Node A: ttl → an ABSOLUTE DEADLINE, decided once, here
  Node A → B, C, D: store k (value + that same deadline)
  Node A: ack the client once W owners have stored it   (W=1 by default)
```
> **⇒ AS BUILT:** the coordinator fans out to **all R owners itself** (in-band, not a background forward
> from the primary) and acks after **W**. With the default **W=1** this is still primary-only ack, so
> §6.1's lost-write window is exactly as described. And the deadline is converted **once, by the
> coordinator** — a TTL re-based at each hop would be pushed further out by every heal, and a
> frequently-healed key would **never die**. → PROGRESS, Phase 5.

**GET (read path):** ring → `{B primary, C, D replicas}`; try B, and if it is down or times out fall back
to C, then D. First reachable copy wins — that fallback *is* availability under failure.

**Node join:** the new node announces itself → peers add it to their membership and to the ring at its
vnodes → keys that now belong to it are transferred (bounded movement, ~1/N).

**Node death → self-heal (the payoff):**
```
Node D stops sending heartbeats
  → peers' failure detectors time out → each marks D dead in its own view
  → ring recomputed without D
  → Healer: find keys now under-replicated (had a copy on D)
       → copy them from a surviving holder to the new owner to restore R
  → throughout: reads keep being served from surviving replicas   ✅
```

**Who runs the heal.** There is no dedicated healer node and **no election**. Each node **scans only the
keys it already holds** (no node knows the global keyset), recomputes each key's owners against the new
ring, and pushes the key to any owner missing it. Exactly one node must send each key, or every co-owner
pushes the same key and the heal costs R×(R−1) copies instead of R−1. Every key that can still be healed
is held by *some* survivor that will find it by scanning locally; if all R copies die at once, nobody
holds it and it is unrecoverable (accepted, §9).

> **⇒ AS BUILT — the original rule here was wrong, and it stranded keys forever.**
> This document said the sender is **the range's current primary**. That silently requires one node to be
> **both the primary AND a holder**, and there is a case where **nobody is**: a revived node comes back
> **empty**, and the ring promotes it straight back to primary of its own arcs. The **primary then has
> nothing to send** (the key isn't in its snapshot), and the **holders stand down** (they aren't the
> primary). The key stays under-replicated **forever** — no further membership change is coming to
> retrigger anything. Found live: kill to 2 nodes, revive all three, **7 of 20 keys never recovered**.
>
> **The rule that actually works: permission follows the DATA, not the ring position.** The healer for a
> key is the **first owner, in ring order, that actually holds it.** That preserves what the primary rule
> existed for (exactly one sender ⇒ no duplicate copies) *and* guarantees a sender **exists** whenever
> anybody has the data. A node ranked below a holder stands down; one ranked above a holder — or holding a
> key **no owner has at all** (a leftover from an older ring) — steps up. → PROGRESS, Phase 5.

### 6.1 Why node death causes staleness (and why we accept it)

**Staleness** = a read returns a value that is no longer the most recent one written. Self-heal restores
*redundancy* (R copies), **not** *freshness* — different problems.

The root cause is decision §10.2: **we ack before every copy has landed** (W=1). So there is a
**replication window** in which one node holds a fresher copy than the others, and node death turns that
window into staleness:

```
client → B (primary): SET k = v2      (replicas C, D still hold v1)
  B stores v2, acks client
  B begins replicating → C, D  … but B dies before it lands
  → ring recomputes without B; a former replica (C) becomes primary
  → GET k now returns v1  ← STALE  (and v2, which lived only on B, is a LOST WRITE)
  → Healer copies C's v1 around to restore R=3
       cluster now reports HEALTHY while serving a STALE value
```

Two sources, in order of severity: **(1) the freshest copy dies** — the newest write existed only on the
primary, so it dies with the node (a *lost write*); **(2) detection lag** — between B dying and every peer
marking it dead, views diverge and a read may fall back to a replica that missed the write.

**Why this is acceptable (the cost of choosing AP, §3):** this is a *cache*, not the source of truth. Two
mechanisms bound the damage — **TTL** (the stale entry expires; the next read misses and refetches from the
authoritative DB) and, later, **read-repair / anti-entropy**. Tuning §10.2 toward "wait for W replica acks"
shrinks the window at the cost of write latency.

### 6.2 Failure modes (catalog)

The design is **AP**: it favors staying available and accepts bounded staleness/loss.

| Failure mode | What happens | Design response / mitigation |
|---|---|---|
| **Lost write** — primary acks client, then dies before replicating | Acked write existed only on the primary → gone. Survivors serve the old value (stale) or 404 (lost insert). | Primary-only ack is the fast default; a larger `W` turns single-node silent loss into needing `≥W` simultaneous failures. TTL + read-repair bound residual staleness. |
| **Detection window** — node dead but still in peers' ring | Ring routes to the corpse: reads time out then **fall back** to a live replica (survive, +latency); writes to its ranges briefly unavailable/risky. | Bounded by an **app-level request timeout** (not OS/TCP). Read fallback (§6). Window = detection + heal (§8). |
| **Available ≠ fully-replicated** — the heal window | After promotion (instant) but before heal (later), a key has `<R` copies while still serving reads. | Healer restores `R` in the background; §8 tracks time-to-heal. Accept the window; a second loss inside it is the risk. |
| **False positive** — live node marked dead (GC pause, slow I/O, blip) | Needless promotion + migration → CPU/network cost → can slow other nodes → secondary false positives → **flapping / cascade storm**. | Tune the timeout (the fundamental fast-vs-wrong tradeoff); grace period; incarnation numbers; phi-accrual / SWIM indirect probes. §8 tracks false-positive rate. |
| **Network partition / split-brain** — each side thinks it's the survivor | Both sides serve (AP) → **two primaries per key** → divergent writes. | Views cannot converge until the link heals. On heal, reconcile with **last-write-wins** (§3, stretch) — the losing write is silently dropped, the accepted price of AP. |
| **Two primaries → write conflict** — general form of the above | Two nodes both accept writes for the same key → conflicting values. | The per-key primary normally serializes writes; conflict arises **only** when that invariant breaks → resolved by LWW-by-version. |
| **Correlated total loss** — all `R` replicas die at once (the whole container) | Key is unrecoverable — no survivor holds it to heal from. | Out of scope for cluster-in-a-box (shared fate); a real deployment spreads replicas across failure domains. The backing DB is the source of truth. |

**Cross-cutting tension:** the detection timeout trades *fast failover* (short → small window, more false
positives) against *stability* (long → few false positives, slow failover). No single value is both — **a
dead node and a slow node emit the same signal: silence.** Choosing **AP** means we take staleness and
lost writes (bounded by TTL) instead of the **CP** alternative (quorum/consensus → the minority side goes
unavailable), which is out of scope (§9).

---

## 7. Interfaces — **as built**

**Client API** (on every node; any node coordinates any key)
- `GET /get/{key}` → value, plus three trace headers, so the read fallback is legible to the client
  instead of buried in a server log: `X-Coordinator` (the node that took the request — any live node can,
  since coordinating needs no local copy), `X-Served-By` (the owner the value actually came from), and
  `X-Read-Path` (`n0:unreachable,n4:miss,n2:hit` — every owner in ring order and what each one said).
  ⚠️ There is **no `X-Primary` header**: the primary is rank 0 of `X-Read-Path`, derived by the reader
  (`cluster.ReadResult.Primary`). One source of truth for who the owners were, rather than two that can
  disagree.
- `PUT /set/{key}` body=value, `?ttl=250ms|2m0s` → fans out to all R owners, acks after W.
- `DELETE /del/{key}` → ⚠️ fans out to **every peer, not the R owners** (see below); `X-Dropped` names the
  nodes that held it. `DELETE /del` clears the cluster.

**Internal API** (node↔node — the same HTTP, one hop down)
- `GET /kv/{key}` → the raw stored value. Also the heal's *"do you have this?"* probe — ⚠️ which means the
  probe **downloads the whole value**; a `HEAD` would make it free (PROGRESS, Next action (c)).
- `PUT /kv/{key}` body=value, header `X-Expires-At` — the **absolute deadline**, carried, never re-based.
- `DELETE /kv/{key}` → 204 if this node held it, 404 if not · `DELETE /kv` → wipe, count in `X-Dropped`.
- `GET /health` → liveness. Silence past the timeout is what convicts a peer.

**Dashboard API** (`cmd/server`, over the cluster manager)
- `GET /api/state` — the god's-eye view: ring, nodes, keys, intended owners vs actual holders.
- `POST /api/kill|revive|pause` — failure injection · `POST /api/set|seed|delete|clear`, `GET /api/get`.
- ⚠️ All `POST`, including the deletes: CORS here allows GET/POST only, so a real `DELETE` verb would fail
  the browser's preflight. The **node↔node** protocol does use real `DELETE`.

> **⇒ PLANNED (Phase 7, step 7.0).** These routes gain a cluster segment — `GET /api/{cluster}/state`,
> `POST /api/{cluster}/kill`, and so on — resolved against a `map[string]*cluster.Cluster` and 404-ing on an
> unknown name. `routes()` currently closes over a single `*cluster.Cluster` and every handler uses it; the
> change is to pass `c` in as a parameter instead. ~40 lines, mechanical. The `ServeMux` wildcard and
> `r.PathValue` are already in use for the node↔node routes (`GO_NOTES.md` § net/http).
>
> ⚠️ **Not two ports.** A second `http.Server` on `:8081` is a smaller diff and unshippable: the host injects
> one `$PORT` and exposes exactly that (§8.5). It would work on the laptop and die in the container — the
> same failure shape as the two production surprises below.
>
> The frontend takes the rest of the work: five files import bound API functions that hardcode the path
> (`hooks.ts`, `NodePanel`, `WritePanel`, `ReadPanel`, `KeyTable`); the rest import types only. The plan is a
> `createApi(clusterId)` factory supplied through React context **at the tab boundary**, so a component
> cannot name the other cluster's API rather than merely being trusted not to. Cross-talk becomes impossible
> on the client for the same reason it already is on the server: there is nothing shared to reach.

### Cleanup — why heal alone is a ratchet

**Heal only ever COPIES.** Kill a node and its keys are re-replicated onto whoever owns them *now*; revive
it and the ring snaps back — but those copies stay on nodes that no longer own them. Nothing removed them,
so **every kill/revive cycle permanently raised the copy count**, and R crept toward N: the sharding the
ring exists to provide, given away one outage at a time. Measured on a 6-key cluster: one kill+revive took
it from 18 copies to **22**.

`cleanup` is the counterweight, and runs at the end of each heal pass: *drop the copies I hold but no
longer own.*

⚠️ **Confirm, then drop.** A copy goes only if **every** one of the key's R owners answers that it holds
the key. An owner that says no, or that cannot be reached, ends the matter and the copy stays — because
**a surplus copy and the last copy alive look identical from here**, and asking is the only thing that
tells them apart. Reverse the order and this is a data-loss bug, not a memory optimisation.

Two apparent races that are not: two non-owners dropping the same key concurrently is safe (neither is an
owner, and each drops only after all R owners confirm, so the count cannot fall below R however they
interleave); and an owner never reaches the drop, so the owners cannot clean each other up.

⚠️ **A kept copy is deferred, not settled.** Cleanup only runs inside a heal, and heals only run on a
membership change — so a copy whose owner was still being repopulated when we asked would stay stranded
until the next kill. It re-arms the heal trigger when anything was kept, which is self-limiting: the retry
that confirms it leaves nothing kept, and the loop stops.

⚠️ **What it does not fix.** It assumes the dropped copy is no fresher than the owners'. Writes go to
owners, so that holds — *except* for a write a **down** owner missed, which heal's presence check will not
repair. So cleanup can discard a fresher copy and keep a staler one. No client can observe the difference
(reads only ask owners), but it is **presence ≠ version** again, and only versioned values close it.

The metric is on the dashboard: **copies stored vs copies the ring asks for**. Equal is healthy; the gap is
cleanup's debt. The second number is `keys × min(R, alive)`, not `keys × R` (`Stats.tsx`) — below R live
nodes a key *cannot* have R copies, and charging the cluster for copies it has nowhere to put would show a
permanent deficit in exactly the state the demo spends most of its time in.

### Why delete broadcasts instead of addressing the owners

The ring says where a copy *should* be; a delete must erase it wherever one *is*, and nothing in this
system ever removes a surplus copy. So the two drift apart, and an owners-only delete leaks two ways:

- **Leftovers.** A heal re-replicates a dead node's keys onto whoever owns them *now*; revive it and the
  ring snaps back, but those copies stay on nodes that no longer own them. Kill + Revive alone produce this
  (`TestDeleteFindsCopiesTheRingNoLongerNames`).
- **Resurrection.** A health-paused node is alive and serving `/kv`, but its peers convicted it and dropped
  it from their ring, so it never gets the delete. Resume it: heal finds a key no owner holds, appoints it
  the healer, and pushes the key *back*. The delete reverts, wearing a heal's clothes.

Real systems need a **tombstone** here — a "deleted at T" marker that replicates like a value, so heal sees
DELETED rather than MISSING. We skip it only because a dead node is destroyed and revives empty:
unreachable means nothing left to resurrect. **Give the nodes durable storage and that argument collapses.**

> Not built: gossip digests, a `transferKeys` bulk move (the heal copies key-by-key), `/admin/partition`.
> Pause-health is the injected failure that matters — a **live** node that merely *looks* silent, which is
> the only way to manufacture a false positive on demand. It is no longer wired to a dashboard button, but
> the API keeps it, and the delete broadcast above is why it still earns its keep.

---

## 8. Metrics (drive the naive→iterate story & the demo)

- **Keys remapped** on join/leave (target ~1/N — proves consistent hashing works).
- **Detection latency** (kill → marked dead) and **false-positive rate**.
- **Time-to-heal** (kill → back to full R copies).
- **Read availability during heal** (should stay ~100%).
- **Hit rate**, ops/sec (baseline from Phase 1).

## 8.5 Deployment — **as shipped**

Dashboard (static, on a CDN) + cluster (one long-running container). `render.yaml` and `Dockerfile` are in
the repo; the frontend takes the API origin from `VITE_API_URL`, ⚠️ **inlined at build time**, so changing
the backend URL means a *rebuild*.

⚠️ **The backend cannot be serverless, and this is a property of the design, not a preference.** Liveness
here is defined as *"did I hear from you in the last 500 ms"* — so the heartbeat goroutines must actually
run. A platform that freezes the process between requests stops those beats, and on the next request
**every node convicts every other node**: the failure detector fires on the *platform's* idleness rather
than on any real failure. Concretely, Google Cloud Run's **default request-based billing** allocates CPU
only during a request and is therefore disqualified; only *instance-based* billing (`CPU always allocated`)
+ `min-instances=1` works, which overruns its free tier. *A system whose liveness is "did I hear from you
recently" cannot live somewhere that stops time when nobody is looking.*

Chosen: **Render free** — no card, sleeps after ~15 min idle, ~30–60 s cold start, and sleeping
**terminates the process** (so the ring re-seeds on boot). Accepted, and surfaced honestly in the UI as
*"waking the cluster…"* rather than an error. Free tiers that never sleep exist (Northflank); a GitHub
Actions keep-alive cron does **not** — that is an explicit Acceptable Use violation.

Splitting the origins also makes two previously-cosmetic things **load-bearing**: the permissive CORS
header, and the fact that it allows `GET`/`POST` only — which is why every mutating control-API route is a
`POST`, deletes included, while the node↔node protocol keeps real `PUT`/`DELETE` verbs.

**Two ways the container disagreed with the laptop** — both found only in production, and both worth the
general lesson:

1. **Don't enumerate your own source tree.** The Dockerfile had one `COPY` line per package. Adding a
   seventh package (`notify/`) without its line broke the build — *in the container only*, because locally
   the directory is simply there. A hand-maintained list of what exists is a **second source of truth that
   production is the first thing to check it**. Now `COPY . .` + `.dockerignore`.
2. **`scratch` has no CA certificates.** Trusting a TLS certificate means checking who signed it against a
   list of trusted signers — and that list is *just a file on disk*. The design was fine while the process
   made no outbound TLS calls (node↔node is plain HTTP on 127.0.0.1); the ntfy push added one. ⚠️ Note the
   failure *shape*: the deploy goes **green** and the health check passes, because nothing on the request
   path touches TLS — only the feature is dead. **A passing health check is not a working feature.**

### 8.6 Visit notifications — a *visit* is not a *request*

A push when somebody opens the live demo. `notify.Notifier` is the interface (*what happened*);
`notify.Ntfy` is today's transport; `notify.Nop` is what an unconfigured server holds, so no call site
carries a nil check. `cmd/server/visits.go` decides *what a visit is* and never learns how it is sent.

⚠️ **The dashboard polls `/api/state` every 600 ms.** Notify per request and it is ~1.7 pushes a second, per
open tab. Three guards turn the poll storm back into visits:

| Guard | Why |
|---|---|
| dedup on `sha256(IP + UA)` | one visitor is one push — hashed because the key is only ever *compared*, never read |
| an **idle** window, refreshed on *every* poll | a tab left open all afternoon is **one** visit; a *fixed* window would push every 30 min at somebody who never left |
| ≤ 20 pushes/hour, hard | the API is public — ⚠️ a bot sweeping it must not become a **DoS on your own phone** |

⚠️ **The ntfy topic is the only secret ntfy has** — no key, no account: whoever knows the name can *read*
your notifications *and* send you some. So it is an env var (`$NTFY_TOPIC`, `sync: false` in
`render.yaml`), never in git, never logged, and **never a `VITE_*`** — those are inlined into the bundle
every visitor downloads.

⚠️ **The message carries the visitor's IP** (the same thing any web server logs), so the topic name is what
guards *visitor IPs*, not merely the fact that somebody showed up. A guessable topic is now a privacy leak
rather than an annoyance. The `sha256` is a dedup key, not a privacy measure — it never was one against
anybody holding the topic, since an IP is trivially brute-forced from its hash.

Two Go traps the design turns on: `*http.Request` is **dead once the handler returns**, so the message is
built before the goroutine spawns; and `r.Context()` is **cancelled when the response is written**, so the
send uses `context.Background()` with its own timeout.

---

## 9. Explicitly out of scope (with honest caveats)

- **Consensus / strong consistency** — we're AP; a production version would likely put consensus on the
  *control plane* (ring/membership) so two nodes never disagree on ownership.
- **Split-brain resolution** — partitions handled only as a stretch goal (last-write-wins).
- **Disk persistence / crash recovery of a single node's data** — pure in-memory; the backing DB is the
  source of truth.
- **Security/auth, multi-region.**

---

## 10. Locked decisions ✅  (2026-07-08)

All chosen for **learnability + a clean demo** over raw performance. All easily reversible.

1. **Node model → goroutines in one process.** Each node binds its own localhost port and talks to peers
   over **real HTTP sockets** (never shared memory) — so message passing and failure detection stay real;
   only the deployment is collapsed. *Reserve:* each node owns its port/config, so splitting into separate
   OS processes later is a small change.
2. **Write acknowledgement → primary-only ack (to start).** Fast; we accept the known lost-write window.
   *Reserve:* a configurable `W`-replica-ack knob, added in Phase 3, to close the durability/latency
   tradeoff live.
3. **Failure detection → all-to-all heartbeats.** O(N²) is fine at N=3–7 and lets us watch every heartbeat.
   *Reserve:* SWIM/gossip is the scale-out path (indirect probes cut false positives); revisit only if N grows.
4. **Transport → HTTP/JSON everywhere.** Every message is human-readable (curl/browser/DevTools), which is
   worth more than speed here. *Reserve:* gRPC/protobuf if performance ever becomes a goal.
5. **Dashboard → polish is a priority** (it is the recruiter-facing money moment). The only hard constraint
   is that it stays **static-hostable and free**. *Chosen in Phase 6:* **React + Vite + TypeScript**,
   hand-rolled SVG ring (no viz library).
6. **Replication factor → R=3, configurable.** Kill one node → 2 copies survive → heal back to 3 (a clean
   "keeps serving + heals" demo; pairs with W=2/R=2 quorum → `R+W>N`). *Reserve:* configurable, so we can
   also demo the scarier R=2 behavior.
