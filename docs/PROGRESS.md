# Progress Tracker

Living log of where we are, what's been taught, and quiz results. Update at the end of every
session and after each milestone. Newest entries at the top of the log.

## Current status
- **Phase:** **Phases 0–6 COMPLETE — the demo is built, working, and browser-verified.** Go 1.26.5;
  HLD APPROVED, all 6 §10 decisions LOCKED. **Session 7 (2026-07-10):** re-asked Q4 ✅ / Q6 taught;
  built the full self-heal arc (Phase 5: naive re-replication → storm demo → grace-period fix); then
  **built Phase 6 — the dashboard** (`cluster/` cluster-in-a-box manager, `cmd/democache/` control API
  + embedded flashy SVG ring UI). `go run ./cmd/democache` → http://localhost:8080. Both halves of the
  money moment (kill → reroute *and* re-replicate) are visible and interactive; verified live in a
  real browser. See the Phase 5 and Phase 6 checklists.
- **Locked decisions:** (1) nodes = goroutines in one process, real HTTP over localhost ports;
  (2) primary-only write ack to start, W-ack knob added in Phase 3; (3) all-to-all heartbeats;
  (4) HTTP/JSON transport; (5) dashboard — **polish is a priority** (recruiter-facing money moment);
  framework/viz-library OK if it elevates the demo, must stay static-hostable + free; (6) **R=3**,
  configurable.
- **Next action:** **The core project and demo are COMPLETE (Phases 0–6).** `go run ./cmd/democache`.
  **Candidate next steps (all optional polish):** (a) **milestone quizzes** for Phase 5 and Phase 6
  (the per-phase review the ritual calls for — not yet taken); (b) optimize the naive heal to copy
  only *actually under-replicated* keys to the *newcomer* (not every primary key to every co-owner);
  (c) the genuine-recovery repopulation gap (a revived node comes back empty); (d) deploy the demo to
  a free host + writeup; (e) hinted handoff / read-repair for the AP staleness gaps. Pick per Aayush's
  interest. **Carried-forward to re-ask cold:** the Snapshot-recency ⚠️ (it's the Phase-1
  sequential-scan LRU pollution) from Session 7. **Carried-forward re-ask done (Session 7 cold):** Q4 (self-suspicion & split-brain)
  now **✅** — sharpened that the data loss happens at *reconciliation* (LWW silently drops the older
  acked write), not at the conflict itself. Q6 (false-positive mitigations + the universal tradeoff)
  **taught, not attempted — third blank**; the tradeoff (every mitigation delays correct convictions
  as much as wrong ones — a slow node and a dead node are the same silence) feeds directly into the
  Phase 5 re-replication-storm question.
- **The finding that closed Phase 1:** we asserted for four sessions that *"a sequential scan
  collapses LRU's hit rate."* Measured, that is **false for realistic traffic and dramatically true
  for a flat working set.** A Zipf workload loses only 12.6 points to a batch job issuing as many
  requests as every user combined (78.2% → 65.6%), because a power law's working set is tiny: the hot
  keys never drift near the tail, and the scan's keys sink there and **evict each other**. A flat
  working set of 900 in a 1000-entry cache goes **100% → 47.5%** under the same job. And a *cyclic*
  loop over `capacity+100` keys scores **exactly 0%** where `capacity-100` scores 100% — LRU does not
  degrade, it falls off a cliff.
- **Deferred, on purpose:** (a) `sync.RWMutex` — measured: the *uncontended* mutex is **40% of a 67 ns
  `Get`** (26.9 ns), with only ~22 ns of real work to overlap, and an `RLock` costs more than a
  `Lock`. Not an obvious win; also `Get` *deletes*, so it can't take an `RLock` as written.
  (b) `SetIfAbsent` — the caller-side check-then-act gap; (c) **Go maps never shrink** — 16.5 MB of
  bucket array survives sweeping 200k entries to `Len()==0`; only replacing the map frees it. Known
  limit, not fixed. (d) injectable clock — the test suite spends ~4 s sleeping.
  (e) `sampleSize`/`expiredThreshold` (20 / 25%) are Redis's constants, unmeasured by us.
- **Flagged (learning):** **"compare, don't remember"** has now been missed **twice** (S4 Q2,
  S4c Q1) — a value read before releasing a lock is a rumor after it. Re-teach on sight.
- **Flagged for review:** two spots needed correction during the failure-mode quiz and were
  re-taught (appear solid now, worth a light re-check before Phase 4/5):
  (a) **available ≠ fully-replicated** — reads can be served while the cluster is still one copy
  short during the heal window; (b) **frozen ≠ partitioned** — a fully GC-frozen node yields only
  staleness, whereas an *alive-but-unreachable* node yields genuine conflicting writes.
- **Teaching mode reminder:** Aayush is a COMPLETE BEGINNER in distsys — **default to a simple
  explanation + concrete example, analogies only when a concept is genuinely hard or on request**
  (updated 2026-07-08), define every term, small steps. (See CLAUDE.md.)

## Concept checklist
Mark ☑ when taught AND the quick-check quiz was passed.

### Phase 0 — Foundations
- ☑ What a cache is (key→value, TTL, eviction) and why distribute one
- ☑ CAP / why this system is AP
- ☑ Cluster-in-a-box (what's real vs collapsed)
- ☐ Go basics as needed (packages, structs, interfaces, goroutines, channels, context)

### Phase 1 — Single-node cache
- ☑ Hash-map store (`cache/cache.go`: struct-wrapped `map[string]string`, `New`/`Set`/`Get`, comma-ok, miss-vs-empty) + tests
- ☑ Concurrency / races — demonstrated live (`DATA RACE` + `fatal error: concurrent map writes`), then fixed: `sync.Mutex` on `Cache`, locked in `Set` **and** `Get`. Same `race_test.go` now green under `go test` and `go test -race`. Known gap (deliberate): compound read-modify-write by callers is still a race *condition* — see `SetIfAbsent` note.
- ☑ TTL expiry — absolute deadline per entry, **lazy** expiry on read. Leak measured (200k unread keys = 40.9 MB retained through a forced GC), then fixed with a background **sweeper**. Full-scan sweep measured to stall readers **751×** → replaced with Redis-style **sampling** (`samplePass` + `expiring` index): 27ms lock hold → 7µs at 1M keys, reader cost 751× → 2.0×.
- ☑ LRU eviction — **O(1)**. Bélády's optimal is unimplementable (needs the future); every real policy approximates it from the past. LRU = a bet on *temporal locality*, and a **sequential scan is that bet losing**. Built naive first: `capacity` + a scan for the minimum `lastUsed`, **corpse-first** (recency and expiry are independent orderings). `lastUsed` had to be a **logical clock**, not a timestamp — `time.Now()` stands still for 541µs and cannot order back-to-back `Set`s. Measured **25.6 ms per `Set`** into a full 1M cache, then replaced the scan with a hand-rolled **hash map + doubly linked list** (`map[string]*node`, sentinel head/tail): **579 ns, 44,199×**, and `lastUsed`/`clock` deleted — position *is* recency. Corpses survive the rewrite via a bounded probe of the `expiring` index, whose hit rate provably tracks corpse density.
- ☑ Hit rate — the metric for a *policy*, as opposed to latency. Cache-aside harness + Zipf / uniform / cyclic workloads (`cache/hitrate_test.go`). **The scan-collapse hypothesis was measured and half-refuted.** Zipf traffic: 78.2% → 65.6% under a 1:1 batch job. Flat working set of 900 in a 1000 cache: **100% → 47.5%**. Cyclic loop over 1100 keys, capacity 1000: **exactly 0%** (900 keys: 100%). LRU doesn't degrade; it falls off a cliff.
- ◐ Scan resistance — **taught, quizzed, measured, DEFERRED.** Four families: more evidence (segmented LRU / 2Q / InnoDB), frequency (LFU+decay, ARC, LIRS), **admission** (TinyLFU + Count-Min Sketch — the deep reframe: *LRU has no admission policy*), hinting (PG ring buffer, `MADV_SEQUENTIAL`). Not built: for skewed traffic the gain is ~12 points at a 1:1 scan ratio, which does not pay for the complexity. Revisit if a workload with a flat working set appears.

### Phase 2 — Consistent hashing
- ☑ Why `hash % N` breaks on resize — the divisor N is a single global baked into every key's placement, so changing N re-rolls everyone. Counted over one period of 12: going 4→3 nodes moves **9 of 12 keys ≈ 75%**, i.e. ~(N-1)/N, not 1/N. Every moved key is a miss → **cache stampede** on the DB (whole keyspace, no hot key needed). Patch-the-mapping "fixes" fail worse: placement becomes a function of the *ordered history* of changes, so clients that learned failures in a different order disagree.
- ☑ The ring + wraparound — `ring/ring.go`. Hash nodes and keys into the same 32-bit space; a key belongs to the first node **clockwise**, wrapping past the top. `Add`/`Remove`/`Get`, sorted points + `sort.Search`. **Measured: removing 1 of 10 nodes moved 9.2% of keys** (≈1/N) vs hash%N's ~90%.
- ☑ Virtual nodes / balance — `ring/ring.go`, `defaultReplicas = 150`. Each physical node contributes many scattered points (`hashKey(node + "#" + i)`), so its total load is a sum of many small arcs and concentrates on the mean. Naive ring measured lumpy (**65x span**, one node 2.45x fair share); the sweep collapses it: 10 replicas → 3.8x span, 50 → 1.5x, 150 → **1.4x** (busiest 1.23x). Diminishing returns ~1/√replicas then a plateau (50→150 barely moves; residual is finite-keyspace sampling). Second win, measured: a dead node's keys spread across **all 9 survivors** (busiest absorbs 19%) vs the naive ring dumping **100% on one** → no cascade seed.
- ☑ Hash choice — **FNV-1a was a bad call, caught by measurement.** Its weak avalanche clustered `node0..node9` into a 4% sliver so one node owned **96%** of the ring. Switched to **SHA-256 truncated to 4 bytes** (crypto avalanche → uniform). `maphash` is unusable: per-process seeded. Murmur3 (fast + good avalanche) is a hand-roll candidate if hashing shows up hot.
- ☑ Key ownership lookup — `Ring.GetClockwiseN(key, n)` returns up to n **distinct physical** nodes: the primary (== `Get`) plus the next n-1 distinct clockwise. Distinctness is the point — consecutive points are often the same machine's virtual nodes, and replicas sharing a machine die together. Skips already-seen nodes; caps at the node count (can't keep more copies than machines); bounded to one lap. This is the bridge into Phase 3 replication.

**Phase 2 COMPLETE.** `hash%N` diagnosed → ring built → hash fixed (FNV→SHA-256, caught by measurement) → virtual nodes (65x→1.4x span, failures spread across survivors) → R-way ownership lookup. Next: **Phase 3, replication** — write to all R owners, read with fallback.

### Phase 3 — Replication
- ◐ Storage node (Store Engine layer) — `node/node.go`. A cache behind an HTTP server (`GET/PUT /kv/{key}`), the internal endpoint one node calls on another. Binds `127.0.0.1:0` (OS-chosen port, read back via `ln.Addr()`), serves in a goroutine, `Close` = `srv.Shutdown` then `cache.Close`. Real HTTP, tested under `-race`.
- ☑ **Coordinating role (R=1), NOT a central coordinator** — `node/node.go`. Every node holds its own ring + peer map (injected via `SetMembership`; gossip in Phase 4) and exposes client-facing `/get`+`/set` alongside the internal `/kv`. Any node coordinates any key: `ring.Get(key)` → local cache if it owns it, else forward over HTTP (2s timeout so a dead owner fails fast). Tested under `-race`: any node routes any key to its owner. **Naive failure demonstrated: at R=1, killing a key's owner returns 502 from every survivor — data gone, no copy to fall back to. This earns replication.**
- ☑ Replication factor R=3 + read fallback — `node/node.go`. A write stores to all R owners (`GetClockwiseN`) and acks after `writeQuorum` succeed (W=1 default; **a knob, not consensus** — W=1 favors availability, larger W favors durability). A read tries owners in ring order and returns the first reachable hit, skipping unreachable ones. **THE MONEY MOMENT, tested under `-race`: at R=3, reads survived 2 owner deaths (fell back down the replica list); the key was lost only when all 3 owners were dead. R copies tolerate R-1 failures.**

**Phase 3 core COMPLETE (naive).** Storage node → coordinating role (R=1, data lost on kill) → R=3 replication + fallback (data survives). Still naive/synchronous: writes hit all owners in-band (no async, no hinted handoff), membership is static (no gossip → a dead node stays in every ring, so the ring still *routes* to corpses and reads pay a failed hop before falling back). Known gaps for later: **write to a dead owner just doesn't ack that replica** (no retry/handoff), and **stale reads / conflicting writes** (AP cost) are unaddressed. Next: Phase 4 failure detection (heartbeats) so the ring stops routing to the dead.
- ☐ Consistency vs availability trade-off

### Phase 4 — Failure detection
- ☑ Heartbeats & timeouts — `node/node.go`. `/health` endpoint; each node's heartbeat goroutine pings every peer every `heartbeatInterval` (100ms), records `lastSeen`, and reconciles an `alive` view against `failureTimeout` (500ms). Alive→dead flips `ring.Remove` (stop routing to the corpse); dead→alive flips `ring.Add`. **The ring now holds only nodes this view believes alive**, so `peers` (all known) and the ring (alive) diverge. Each node's view is its own — no consensus. Measured under `-race`: **death detected in 600ms = timeout + 1 beat**, both peers conclude it independently, and the key reroutes off the dead node.
- ☑ False positives (GC pause vs death) — **the core impossibility: a crash, a slow node, and a dropped packet are all just silence.** The timeout is the knob: short = fast detection + false positives (a GC pause looks dead → wrong ring recompute → asymmetric views → split-brain seed); long = fewer false positives + route to corpse longer. **Demonstrated** (`node/node.go` `PauseHealth` + `TestSlowNodeIsFalselyDeclaredDead`): a node that stalls only `/health` (a GC-pause stand-in) while serving all other traffic is declared dead by n0 after ~500ms, yet still counts *itself* alive — asymmetric views, the split-brain seed. Un-stalling it makes n0 re-admit it: a needless eviction+recovery **flap**, the pure cost of guessing too eagerly. The same 500ms timeout that `TestHeartbeatDetectsDeath` shows catching a real death fast is shown here misfiring on a slow node — you cannot have both, because both are silence.
- ☑ Gossip / SWIM intuition — **taught, not built** (all-to-all is O(N²): N=5 → 20 msgs, N=1000 → 1M msgs/interval; HLD-locks us to it because we never hit the wall). Gossip: a node learns of a death **second-hand** — pings a few random peers, the fact spreads transitively (rumor, O(N), converges O(log N)) instead of directly pinging everyone. SWIM adds the two parts that fix *our* false positive: **indirect probing** (ask k peers to probe the suspect before convicting → routes around a single bad link) and **suspicion + incarnation numbers** (a "suspected" state the accused can *refute* → the voice n1 never had in our demo). Deferred on the same naive→measure→iterate logic as segmented LRU: name what it buys, don't build it until a measured scale need appears.

**Phase 4 COMPLETE.** Heartbeats + timeout detection (death caught in ~490–600ms = timeout + up to one beat) → false-positive demonstrated (`PauseHealth`: a healthy-but-stalled node convicted, then flaps back — the timeout's cost made concrete) → gossip/SWIM intuition. The ring now holds only nodes a view believes alive; each view is independent (no consensus). Next: **Phase 5, self-heal** — a detected death should *trigger* re-replication to restore R, the other half of the money moment.

### Phase 5 — Self-heal
- ◐ Re-replication to restore R — **step 1 (naive) built.** `node/node.go`: a coalescing
  `healTrigger` (buffered-1 chan, non-blocking send) fires on any membership change; a separate
  `healLoop` goroutine runs `heal()` (kept off the heartbeat loop so slow copies don't stall pinging
  → more false deaths). The heal invariant: **for every key this node is *primary* of, push a copy to
  that key's other current owners** (primary-only, so co-owners don't all push the same key;
  idempotent overwrite). `cache.Snapshot()` enumerates live entries **without touching recency** — a
  bulk heal scan must not look like user access or it re-creates the Phase-1 sequential-scan LRU
  pollution. Measured under `-race` (`TestHealRestoresReplicationAfterDeath`): killed the primary of
  a key at R=3, the promoted newcomer received its copy in **~550ms** (= detection ~500ms + heal),
  two live copies healed back to three, **no client involved**. Design Q's 1–3 taught first (who
  heals = primary, deterministically from ring, no election; which keys = local scan only, no global
  keyset; storm = decouple cheap reversible re-routing from expensive re-replication).
  - **Naive on purpose:** re-pushes *every* key it's primary of (not just the dead node's), and to
    co-owners that already have the copy — both wasted sends = the re-replication **storm** step 2
    measured on a false positive, before step 3's grace period.
- ☑ Storm demo (step 2) — `healCopies` atomic counter + `HealCopies()`; `TestFalsePositiveTriggers-
  HealStorm`. A `PauseHealth` false positive (node alive, looks silent) makes every observer heal:
  **exactly `keys×(R-1)` = 200 copies for a node that never died** (proof the naive heal re-pushes
  everything). Per-node breakdown: the *accused* node pushes **0** — the storm is driven entirely by
  the observers (independent-views lesson from Phase 4 resurfacing).
- ☑ Grace period fix (step 3) — **decouple the two reactions to a death by cost.** Cheap+reversible
  (`ring.Remove` → re-route) fires instantly on *suspicion*; expensive+irreversible (re-replication)
  waits `healGracePeriod` (default 1s), then **rechecks `hasSuspectedDead()`** — a suspect that
  recovered inside the window leaves nothing dead, so the heal is skipped. Also: **only a death
  triggers a heal now, not a recovery** (a flapped-back node lost no data → nothing to reconcile;
  this removed step 2's *second* storm). Measured (`TestGracePeriodPreventsHealStorm`): the same
  false positive that cost 200 copies now costs **0**. Price = the Q6 tradeoff made concrete: a
  *genuine* death heals in **~1.55s vs ~550ms** (extra under-replication exposure bought
  storm-immunity). New Go idiom recorded: coalescing signal (buffered-1 chan + non-blocking send).
  - **Known gap (out of scope):** a genuine recovery *after* a real heal does not repopulate the
    returned node. Fine for the demo (the false-positive flap never loses data).
- ◐ Serving reads during heal — **already true via the Phase 3 read fallback** (available ≠
  fully-replicated): reads hop past the missing copy while the heal runs. Not yet made explicit in a
  Phase-5 test; light follow-up.

### Phase 6 — Dashboard
- ☑ Cluster-in-a-box manager (`cluster/`) — runs the 5 nodes as goroutines in one process;
  Start/Kill/Revive/Pause/Set/Get/Seed + a god's-eye `State()` that diffs intended owners (alive
  ring) vs actual holders (node caches) — the gap *is* the heal in flight. Kill just `Close()`s a
  node so peers still detect via heartbeat; Revive brings it back on a fresh port via
  `node.SetPeerAddr` (no liveness reset). Proven under `-race` (`cluster_test.go`): seed → kill
  primary → reads keep serving → heal restores R=3; grace absorbs a false positive.
- ☑ Control API + static dashboard (`cmd/democache/`) — `go run ./cmd/democache` → one binary, HTTP
  API (`/api/state|set|get|seed|kill|revive|pause`) + embedded single-page UI (`go:embed web/`).
- ☑ Ring viz + failure-injection controls — dark control-room SVG ring: per-node neon colors, ~150
  virtual-point ticks (the real load spread), evenly-spaced node markers with heartbeat halos, key
  dots on true hash angles with ownership links, **red pulse on under-replicated keys**, **packets
  that fly primary→newcomer on re-replication**, **kill/revive shockwaves**, and a
  **"re-replicating N keys…" indicator** during the heal window. Per-node kill/revive/pause, write/
  read, seed, live activity log. **Verified live in a real browser** (Claude-in-Chrome): kill → grey
  out + heal (0→24 copies) + 0 data lost; read still serves; false-positive shows the indicator while
  grace holds copies at 0, then heals after grace; revive returns the node. No console errors.

**Phase 6 COMPLETE. Both halves of the money moment are now visible and interactive.** Node markers
placed by even spacing (not `hash(id)`, which clustered n0/n3/n4 at the bottom) — honest, since a
node has ~150 scattered points and no single true position; keys/ticks keep their true hash angles.

---

## Session log
### Session 5 — 2026-07-10 · Phase 1 step 4: eviction, built

**Cold re-ask of the nine carried-forward questions: 2 ✅ · 5 ⚠️ · 1 ❌ · 1 ⊘.** Full text in
`docs/QUIZZES.md`. Three things worth carrying:
- **`check-then-act` is now a three-time miss.** Given a `GetOrRefresh` where *every* map access is
  locked, Aayush answered "there is no lock for `c.Set()`." The instinct is *"unsynchronized access →
  bug"*; the needed instinct is *"decision made under a lock, acted on after the unlock → bug."*
  Re-asked a second time in-session, deferred, **still unanswered.**
- **Starvation was defined backwards** ("blocked briefly, not indefinitely" = ordinary waiting).
  Taught the real definition (postponed indefinitely *while the system as a whole progresses*) and
  **starvation mode** — Go's mutex hands the lock directly to a waiter blocked >1ms. `TestSweepStalls-
  Readers`' 8,769 gets exist only because of that mechanism.
- **Happens-before taught from scratch** (was ⊘): compiler reordering, store buffers, memory barriers,
  and the trap that Go's `sync/atomic` is sequentially consistent while C++'s `relaxed` is not.
  **Atomicity and visibility are separate properties.** Prerequisite for Phase 3.

**Built: capacity + expiry-aware LRU** (`cache/cache.go`, `cache/eviction_test.go`).
- `Cache.capacity`; `noLimit = 0` means unbounded. Bounds **entries, not bytes** — a lie when values
  vary in size (Redis bounds bytes via `maxmemory`). Noted, not fixed.
- Eviction lives in **`Set` only**, and only when inserting a **new** key into a full cache.
  Overwriting doesn't grow the map. (`TestOverwriteDoesNotEvict` guards the bug where a capacity-2
  cache evicts `a` while `Set`ting `a`.)
- `Get` **hit** refreshes recency → `Get` writes three ways now. Third nail in `RWMutex`'s coffin.
- `evictLocked` scans for the **first corpse**, else the minimum `lastUsed`. Corpse-first is a
  *correctness* fix: `TestEvictsCorpseBeforeLiveKey` is quiz S4d Q4 as a test.

**The naive design failed, and the measurement said why.**
`lastUsed time.Time` made `TestEvictsLeastRecentlyUsed` fail **5 runs in 10** — flaky, not broken.
Probed it: **`time.Now()` returned the identical instant for 13,397 consecutive calls; the clock did
not tick for 541µs.** A `Set` is ~100ns, so **~5,400 back-to-back `Set`s share one timestamp**. With
ties everywhere, `e.lastUsed.Before(oldest)` never fires and the victim is whichever key `range`
happens to yield first — **chosen at random.** The code was right; the *type* was wrong.
> **You cannot order events by asking a clock.** LRU needs the order of accesses, not their times.

Fixed with a **logical clock**: `Cache.clock uint64`, `tickLocked()` increments it under the lock,
`entry.lastUsed` stores the value. Two events tie only if they *are* the same event. Ten consecutive
runs green. This is the single-node case of the **Lamport clock** we'll need in Phase 3, where wall
clocks on different machines disagree by milliseconds and can run backwards under NTP. Arrived at it
because a Windows timer wasn't precise enough — which is not a coincidence, it's the same problem.

**Measured (13th Gen i7-13700H, Go 1.26):**
```
BenchmarkSetAtCapacity/1000-20         22,843 ns/op   -> 22.8 ns/key
BenchmarkSetAtCapacity/10000-20       223,358 ns/op   -> 22.3 ns/key
BenchmarkSetAtCapacity/100000-20    2,010,846 ns/op   -> 20.1 ns/key
BenchmarkSetAtCapacity/1000000-20  25,608,480 ns/op   -> 25.6 ns/key
```
**25.6 ms per `Set`** into a full 1M cache — Aayush predicted "~25ms" from theory last session. It is
`BenchmarkSweep`'s 27.5 ns/key: literally the same scan. The difference is *where* it runs. `sweepAll`
paid it once a second on a background goroutine; this pays it on the caller's goroutine on **every
`Set`**, and a cache that isn't full has the wrong capacity. Earns step 5.

**Unpredicted:** `BenchmarkGet` went **66.99ns → 61.31ns, 0 allocs** despite `Get` now performing an
extra map *write*. Rewriting a slot the lookup just pulled into L1 costs nothing measurable. I'd have
guessed a few ns of cost; the measurement won.

**Broke an old test, honestly.** `entry` grew 40 B → 48 B, and `TestSweepReclaimsUnreadKeys` asserted
`afterSweep <= afterWrite/2` — a magic threshold, crossed by 0.4 MB (25.2 vs 24.8). The sweep still
reclaims everything. The real finding: **the never-shrinking bucket residue scales with `sizeof(entry)`,
not with the payload** — 16.5 MB → 25.2 MB from one added `uint64`, across the bucket arrays of *both*
`data` and `expiring`. A test asserting on a fraction of peak heap is asserting on `sizeof(entry)`.
Rewrote it to assert on the ~24 MB of payload the sweep actually owes us.

**Also:** compressed `docs/QUIZZES.md` 543 → 215 lines (every question, model answer, and named gap
kept; the restatements cut).

---

### Session 5 (cont.) — Phase 1 step 5: O(1) eviction

**Quick-check before coding: 3 ✅ · 1 ⚠️** (full text in `docs/QUIZZES.md`, Session 5b).

**Built: hash map + doubly linked list.** `map[string]entry` → `map[string]*node`; sentinel `head`
and `tail`; `unlink`/`pushFront`/`removeLocked`. The map has no order, the list has no lookup;
together both operations are O(1). `lastUsed` and `Cache.clock` **deleted** — position in the list
*is* recency, and you don't keep scaffolding after the building stands.

```
                scan for min lastUsed   unlink the tail
    1k               22,843 ns/op          410.1 ns/op       56x
   10k              223,358 ns/op          489.3 ns/op      456x
  100k            2,010,846 ns/op          452.4 ns/op    4,445x
    1M           25,608,480 ns/op          579.4 ns/op   44,199x
```

**Two things got faster that nobody asked for.**
- `BenchmarkGet` **61.31 → 52.52 ns**. I predicted it would get *slower* (a pointer deref is a cache
  miss the value-map didn't have). Instead the pointer **paid for itself**: a `*node` is addressable,
  so `Get` stopped doing `c.data[key] = e` — one hash and one store deleted from the read path.
- `BenchmarkSamplePass` at 1M: **7,064 → 2,105 ns (3.4×)**. `expiring` became `map[string]*node`, so
  the sweeper stopped looking each sampled key up in `data` a second time.

**The design tension, and how it resolved.** The list orders by **recency**; `evictLocked` prefers
**corpses**; those are independent orderings, so the tail says nothing about whether a corpse exists.
Making eviction O(1) *reintroduces* the S4d Q4 bug. Three options: (a) bounded probe of `expiring`,
(b) an exact min-heap on `expires`, (c) do nothing and let the sweeper cope.

Chose **(a)**, and the reason generalizes: **the probe's hit rate equals the corpse density, and the
cost of a miss is inversely proportional to it.** At 99% corpses it never misses — which is exactly
the catastrophic case. At 0.1% corpses it almost always misses, and wastes one slot in a thousand.
*Accurate where accuracy matters, sloppy where sloppiness is free* — the same self-tuning property
that makes Redis's sampler work. (b) would buy exactness in the regime where exactness is worthless.
Put on the record in `TestEvictionProbeTracksCorpseDensity`, measured against `1-(1-d)^20`:

```
density 0.001   probe hit   2%   (theory   2%)
density 0.010   probe hit  16%   (theory  18%)
density 0.100   probe hit  88%   (theory  88%)
density 0.990   probe hit 100%   (theory 100%)
```

**That test failed twice before it measured anything.** First: the corpses were given a 1ns TTL to
avoid sleeping, but the clock doesn't tick for 541µs, so ~30% of trials had **no corpses at all**.
Second, and worse: the corpses were inserted *first*, making the single corpse the LRU tail — the
fallback evicted it whether or not the probe found it, so **the test could not have failed.** Fixed
by backdating `expires` directly (no clock) and inserting the live keys first. Rule earned: *a test
that cannot fail is not evidence.*

**Also:** `CLAUDE.md` now documents the test/benchmark commands — `-count=1` (defeats the test cache),
`-v` (surfaces `t.Logf`), `-run xxx -bench` (benchmarks only), `-benchmem`, never `-race` a benchmark.

---

### Session 5 (cont.) — Phase 1 step 6: hit rate, and a hypothesis half-refuted

**Hit rate, not latency, is the metric for an eviction policy.** A cache that instantly evicts exactly
the wrong key has excellent latency. Everything measured before this was latency; `Set` at 1M is
579ns and flat, and that number says *nothing* about whether we evict the right things.

Built a cache-aside harness (`cache/hitrate_test.go`): `Get` → on miss, `Set`. That miss-path `Set`
**is** the experiment — the cache never chooses to admit the scan's keys, the application hands them
over as ordinary writes. Three workloads: **Zipf** (power law, real traffic), **uniform** (flat
working set), **cyclic loop** (a report, a table scan).

**Prediction, written down first, then wrong.** I predicted the post-scan hit rate would fall to
20–40% and recover slowly. Measured: **76.5%**, a 1.7-point dip. Two mistakes, both instructive:
1. I used a 20,000-request window, having *just* warned that aggregating hides a transient. **A
   window is a smaller aggregate.** At 200-request resolution the crater is real (78.2% → 45.5%) and
   ~2,000 requests wide.
2. **A scan's damage is bounded by capacity** — you cannot lose more than you were holding. Recovery
   costs at most one cache-worth of misses, and the scan cost the app 10,000 DB queries anyway.
   The marginal harm to everyone else is small. I had never done that arithmetic.

**And I wrote fabricated numbers into the comments before running the code.** Caught it, deleted them.
A number in a comment that was never measured is a rumor with a monospace font.

**The real finding: the collapse depends entirely on the shape of the working set.**

```
zipf s=1.1 over 10k keys, cap 1000     flat working set 900, cap 1000
  no batch job              78.2%        no batch job             100.0%
  1 scan per 10 user        75.5%        1 scan per 10 user        89.3%
  1 scan per  4 user        72.8%        1 scan per  4 user        76.0%
  1 scan per  1 user        65.8%        1 scan per  1 user        47.5%
```

A batch job issuing **as many requests as every user combined** costs Zipf traffic 12.6 points. That
is real, and it is not a collapse. **A power law's working set is tiny**: the top hundred keys carry
most of the load, are re-requested every few operations, and never drift near the tail. The scan's
keys, touched once, sink to the tail immediately and **evict each other.** The scan chews a slice off
the cache and leaves the hot core alone. LRU is far more scan-resistant than this project asserted
for four sessions — *on skewed traffic*.

Where it breaks is a **flat** working set: every entry worth as much as every other, so every stolen
slot is a lost hit. 100% → 47.5%.

**And the cliff:**
```
cyclic loop over  900 keys (capacity 1000)   100.0%
cyclic loop over 1100 keys (capacity 1000)     0.0%
```
A 22% wider working set turns a perfect cache into a useless one. Every key is evicted exactly one
request before it is wanted again. **LRU does not degrade; it falls off a cliff.** Bélády would score
~91% here, and so would *MRU* — evict the most recent. Nothing about the cache size is the problem.

**Decision: segmented LRU is DEFERRED**, with a number rather than a shrug. ~12 points on a 1:1 scan
against skewed traffic does not pay for the complexity. Revisit if a flat-working-set workload shows
up. That is the naive→measure→iterate loop actually being allowed to say *no*.

---

### Session 4 — 2026-07-09
- **`cache/race_test.go` written** — 100 goroutines × 100 writes of *disjoint* keys into the naive
  map. No assertions by design: the pass/fail signal is the Go **runtime**, not `t.Errorf`. Verified
  both failure paths live — plain `go test` → `fatal error: concurrent map writes` at `cache.go:22`;
  `go test -race` → `WARNING: DATA RACE`, two goroutines writing the *same address* despite disjoint
  keys (proof that the shared thing is the map's **buckets/growth flag**, not the value slots).
- **Test simplified** to Go 1.25's `wg.Go(func(){...})` (bundles `Add(1)` + `go` + `defer Done()`,
  making the "Add must precede `go`" bug unrepresentable) + `for i := range n`. `go vet` clean; both
  failure signals still fire. Kept disjoint keys deliberately — they carry the lesson.
- **Go idioms taught:** goroutines (vs `std::thread` / Python threads, no GIL); `sync.WaitGroup`
  (zero value usable, no constructor); `defer` (= C++ RAII / `try...finally`); loop-var capture and
  why Go 1.22 retired the `func(id int){...}(g)` workaround; `strconv.Itoa` (= `std::to_string` /
  `str()`), why Go forbids `"k" + intVal`, and the `string(65) == "A"` trap.
- **Data race vs race condition** — data race = mechanical memory property (same address, ≥1 write,
  no synchronization) → **undefined behavior**, machine-detectable. Race condition = correctness
  depends on interleaving; defined relative to intent, so **not** machine-detectable. Not the same
  set: the read-modify-write counter (`Get` → `+1` → `Set`, each individually locked) has **zero**
  data races and is still wrong — **check-then-act**. Slogan: *the mutex protects the data, not the
  invariant*; the atomic unit must be the operation the **caller** cares about (→ motivates a future
  `SetIfAbsent`). Flagged as the single-machine miniature of the whole distributed problem.
- **The 5 failure modes of unsynchronized shared memory** (escalating severity): (1) **lost update**
  — `counter++` is load/add/store; demoed live, 100 goroutines × 1000 incs gave 97938 / 95007 / 96209
  across three runs, never 100000, never the same twice; (2) **corrupted data structure** — map
  rehash breaks cross-field invariants (our test); (3) **torn values** — slice `(ptr,len,cap)` and
  interface `(type,data)` are multi-word, half-written → *memory unsafety in a language with no
  pointer arithmetic*; (4) **stale reads / visibility** — compiler hoists the load out of the loop →
  infinite loop; atomicity isn't the issue, **publication** is; (5) **reordering** — `data=42;
  ready=true` may become visible out of order. Punchline: which one you get is unpredictable — that
  *is* what UB means, and it's why "ran it 1000× and it was fine" is worthless.
- **Mutex = mutual exclusion + publication barrier** (the half people forget): everything written
  before `Unlock` is visible to whoever `Lock`s next. Kills all 5 modes at once.
- **Cross-language check:** the problem and the primitives are universal (`std::mutex`,
  `threading.Lock`, `synchronized`, `Mutex<T>`, `Interlocked`). What varies: **consequences** (C++/Go
  = UB; **Java defines racy reads** — no fabricated values, no corruption, but non-`volatile`
  `long`/`double` may tear), **prevention** (Rust makes data races a *compile error* via ownership /
  `Send`+`Sync`, and `Mutex<T>` holds the data *inside* the lock — but Rust still permits race
  conditions and deadlocks), and **tooling**. Python's GIL half-protects: `dict` internals stay sane,
  but `counter += 1` is several bytecodes → lost updates anyway; free-threaded Python (PEP 703)
  removes that accidental safety net. JS dodges it with one event loop until `SharedArrayBuffer`.
  Go's actual contributions: `-race` in the toolchain, the runtime's own map guard, and channels as
  an alternative discipline — **not** the mutex itself.
- Reinforced: Go's `sync.Mutex` and the map it guards are *separate fields* — nothing forces the
  association, forgetting to lock compiles fine. (Contrast Rust's `Mutex<T>`.)
- **Session 4 quiz taken** after all (6 Q, 4 ✅ / 2 ⊘). Full text + model answers in `docs/QUIZZES.md`.

#### Session 4 (cont.) — Phase 1 step 3: TTL, and step 3b: the sweeper
- **TTL taught.** TTL = the *bound* on the bounded-staleness bargain; without it staleness is
  unbounded. Naive design (one timer goroutine per key) rejected for two reasons: a goroutine per
  key, and — the real one — it is **silently wrong on overwrite**. `Set(k,"a",30s)` then
  `Set(k,"b",10min)`: the first timer still fires at t=30s and deletes `"b"`, which had 9.5 min left.
  The timer holds a **stale belief** about what it's deleting. Insight: **expiry is not an event to
  schedule, it's a comparison to make.** Nobody can observe a key expired unless they look at it.
- **Built lazy expiry** — `entry{value, expires time.Time}`; deadline stored **absolute** (arithmetic
  once at write time, not per read); `ttl <= 0` → zero `time.Time` sentinel, tested via `IsZero()`
  not a magic constant; `Get` compares and deletes on the spot. Note **`Get` is now a writer** →
  cannot take an `RLock` if we ever move to `RWMutex`. (Tension with the deferred `RWMutex` idea.)
- **Measured the leak** (`cache/leak_test.go`). Session-cache workload: 200k keys, short TTL, never
  read back. Result: **40.9 MB retained, 200 000 corpses, 1 live key** — and `heapMB()` forces
  `runtime.GC()` first, so this survives a full collection. **A GC frees *unreachable* memory; every
  corpse is reachable from `c.data`, so it is by definition live.** A *logical* leak, not a collector
  bug — no GC in any language can fix it, because "useless" is a fact about intent, not reachability.
  (Same intent-vs-mechanism split as race condition vs data race.) Extrapolated: 1k logins/sec ≈
  8.4 GB/day → OOM within a day while never holding more than a few thousand live keys.
- **Built the sweeper** — background goroutine, `time.Ticker` + `select` on a `done chan struct{}`,
  `Close()` idempotent via `sync.Once` and blocking on a `sync.WaitGroup`.
  - **Why `Close()` is mandatory, not politeness:** a running goroutine's stack is a **GC root**, so
    the sweeper keeps the whole `Cache` reachable forever. Goroutine and cache hold each other up.
    Only the goroutine *returning* can free either. `runtime.SetFinalizer` can't help — a finalizer
    runs when an object becomes unreachable, which is exactly what can't happen.
  - **Ticker not `time.Sleep`:** `Sleep` cannot be interrupted, so `Close()` would block up to a full
    interval. `select` can only race *channels*; a `Ticker` is `Sleep` reshaped into a channel.
  - **`close()` not send:** closing broadcasts (every receiver unblocks, now and forever); a send
    wakes exactly one. Double-close **panics** → `sync.Once`.
  - **`wg.Wait()` in `Close()`** separates *asking* to stop from *knowing* it stopped — makes
    `TestCloseStopsSweeper` deterministic instead of sleep-and-hope. If `sweepLoop` ignored `done`,
    `Close` blocks forever and the hang **is** the assertion.
  - **Channels are the exception to "zero value is usable."** `sync.Mutex`/`Once`/`WaitGroup` work
    unconstructed; a channel's zero value is `nil` and **receiving from nil blocks forever** —
    a silent hang, no panic. Must `make()`.
- **Go facts nailed down:** deleting from a map *during* `range` is legal (an entry removed before
  it's reached is never produced) — unlike C++ iterator invalidation; map iteration order is
  **randomized per `range`**, so there is no cursor and "resume where I left off" is not
  implementable; lowercase identifiers are package-private (test seam without public API).
- **Surprise, measured:** after sweeping 200k entries to `Len()==0`, the heap sat at **16.5 MB**.
  Confirmed by throwaway experiment: replacing `c.data` with a fresh map dropped it to 0.5 MB.
  **Go maps never shrink** — `delete()` frees keys and values, the bucket array stays sized for the
  all-time peak. A cache that spikes to 1M keys once at 3am holds that array until the process dies.
  Redis rehashes into a smaller table; Go doesn't. **Filed as a known limit, not fixed.**
- **A test failure that taught something:** the first `leak_test.go` had the background sweeper
  reclaiming keys *while the 200k-key write loop was still running* (writes take ~1s under `-race`,
  TTL was 50ms) → `Len()=10529`. Rewritten to disable the background sweeper
  (`newWithSweepInterval(time.Hour)`) and call `c.sweep()` directly. Timing moved out of the
  assertion into a place where it can't lie. Also fixed a fake payload: 200k entries all storing the
  *same* `strings.Repeat("x",100)` share **one** backing array (a Go string is a 16-byte
  `(ptr,len)` pair), so the values must be made distinct to allocate for real.
- **Sweeper quick-check quiz** (3 Q) — see `docs/QUIZZES.md`. 1 ✅ · 1 ⚠️ · 1 half.
  **Pattern flagged:** the ⚠️ was *again* "acted on a value read before releasing the lock" —
  the same **compare, don't remember** miss as Session 4 Q2. Twice now; logged as a pattern.
#### Session 4 (cont.) — Phase 1 step 3c: measure the pause, then the sampling sweeper
- **Benchmarked the defect** (`cache/bench_test.go`). `Get` = **67 ns/op, 0 allocs**, decomposed:
  mutex Lock+Unlock **26.9 ns**, map lookup **22.2 ns**, `time.Now()` **8.0 ns**. So an *uncontended*
  mutex is **40% of a read** — that's the `RWMutex` question answered with evidence: an `RLock` costs
  *more* than a `Lock` and would have only ~22 ns of work to overlap. Not an obvious win.
- **`sweepAll` is O(total keys), not O(expired keys):** 24.4 / 24.2 / 23.5 / 27.5 ns per key at
  1k / 10k / 100k / 1M. Ten times the keys, ten times the time. And `fill()` writes **permanent**
  keys, so every one of those sweeps **deleted nothing** and still cost 27 ms at 1M. **The scan pays
  for looking, not for finding.** At ~24 ns/key it's already one map-iteration step + a compare —
  **a full scan cannot be optimized below this; you have to stop scanning.**
- **The damage:** one sweep of 1M keys holds the lock **47.9 ms cold** / 27.5 ms warm. Reader
  throughput **6,584,449 → 8,769** gets per 500 ms window (**751×**). Those 8,769 reads × 76 ns is
  0.67 ms of work in 500 ms — **the reader was productive 0.13% of the time.** Not "slower": an
  outage that answers 8,769 requests. It gets 8,769 rather than 0 only because **Go's mutex enters
  starvation mode** after a waiter blocks 1 ms and hands the lock over directly. On a 1 s interval the
  cache is **frozen 2.5% of wall time**, blowing any p99 budget by construction, on an idle machine.
  **The naive sweeper converts a memory problem into a tail-latency problem** — for a cache, a bad
  trade. Tuning the interval slides along that curve; it doesn't leave it.
- **Measuring is hard — three failed attempts, all recorded in `GO_NOTES.md`:**
  (a) per-op latency printed `p50=0s` — a `Get` is **67 ns**, this machine's `time.Now()` resolves to
  **829 µs**, 12,000× coarser; (b) a phantom 10 ms "max latency" **with nothing running** — that was
  `append` growing a 4M-element slice and triggering a GC, i.e. *measuring the measurement*;
  (c) the `Get` decomposition first reported `RawMapLookup` at **78 ns — slower than the 67 ns `Get`
  containing it**, because `var sink any` **boxes** the value and allocates. Typed sinks + `-benchmem`
  + `0 allocs/op` fixed it. Rules: *fix the count and time the batch, or fix the time and count the
  operations*; *never allocate inside the measured loop*; *a component can't cost more than the whole*.
- **Built the sampling sweeper.** `samplePass` locks, walks ≤20 keys of the `expiring` index, deletes
  the expired ones, unlocks. `sampleSweep` repeats immediately while >25% of a sample came back
  expired. `sweepAll` kept **only** as the benchmark baseline.
  - **`expiring map[string]struct{}`** — the separate TTL index Aayush's quiz answer implied.
    Verified by `TestSampleSweepIgnoresPermanentKeys` (100k permanent + 100 expiring).
    Three sites mutate `data`, so three must mutate `expiring`: `Set` (index if `ttl>0`, **un-index
    otherwise** — overwrite-to-permanent), `Get` (lazy expiry), `samplePass`. New bug class: two maps
    that must agree → `TestExpiringIndexStaysConsistent`.
  - **Why dropping the lock is safe here and was fatal before:** `samplePass` is **stateless** — locks,
    re-reads fresh, acts, unlocks, carries out only two ints. Nothing to be stale about.
    *Compare, don't remember*, satisfied structurally rather than by discipline.
  - **Go's randomized map iteration** — the property that made "resume where I left off" impossible
    in the sweeper quiz — is exactly what makes `for k := range m { …; break }` a cheap random sample.
    Same language decision punishing one design and rewarding the next. (Samples *buckets*, not keys,
    so not perfectly uniform. Fine for estimating a fraction.)
- **Results:** lock hold at 1M keys **27,489,911 ns → 7,064 ns (3,891×)**. Reader cost **751× → 2.0×**
  — and the residual 2.0× **isn't a stall**: that test runs the sampler flat out, so two goroutines
  split one mutex and half each is *correct*. The real `sweepLoop` costs ~7 µs/s = **0.0007%** of wall
  time vs the full scan's 2.5%.
- **My "constant pause" claim was WRONG, and the measurement said so:** `samplePass` = 953 ns at 1k
  keys → **7,064 ns at 1M**, a 7.4× growth over 1000× the data. It touches exactly 20 keys at every
  size; what changes is what a touch *costs* — at 1k the map is in L1, at 1M every random bucket probe
  is a cache and TLB miss. **48 ns/key → 353 ns/key. Not algorithmic — physics.** Still flat-*ish* vs
  the full scan's true O(n) (1,128× over the same range), so the design holds. Lesson recorded:
  **the algorithm said constant, the hardware said nearly. Memory locality is invisible from the
  algorithm.**
- **Adaptive rate, not a tuned interval:** 50k corpses cleared in **2 `sampleSweep` calls** — a sample
  that comes back 100% expired fails the threshold and passes again *immediately*, no sleep, no tick.
  "Sample 20 keys" ≠ "reclaim 20 keys per interval"; it means *reclaim as fast as there is garbage, in
  bites that never block a reader for long*. The rate is **emergent**. `defaultSweepInterval` — the
  constant flagged as "picked by gut" — **no longer sets the sweep rate**, only how often we check.
  We deleted the guess rather than tuning it. A healthy cache's first sample comes back clean and
  `sampleSweep` returns after ~1 µs.
- **Aayush caught a bad comment.** I'd justified the sweep budget as "without it a cache that is 90%
  corpses would pass forever and pin a core." **False** — corpses are finite, the loop always
  terminates. Probed it: reclaiming 500k corpses unbounded takes **25,000 passes over 513.9 ms** of
  continuous lock churn; on a 1 s tick that's **51% of wall time in contention**, which is the
  throughput damage the rewrite existed to prevent, re-entering at 20 µs granularity instead of one
  27 ms lump. So the two bounds do **different jobs**: `sampleSize` bounds the **pause** (individual
  request latency); the **budget** bounds how long the sweeper keeps **competing** for the lock,
  because pass count is O(expired keys) — set by the workload, not by us. Budgeted: reclaims 347,900
  of 500,000 in 251 ms and **carries 152,100 to the next tick**. Same bargain as the sampler itself,
  applied to time instead of space: **bounded waste in exchange for bounded interference.** (Redis
  caps itself at 25% CPU the same way.) *The original comment was a plausible-sounding rationalization
  that was never checked — exactly the habit this project is meant to train out.*

#### Session 4 (cont.) — who calls `Close()`? (design, no code)
- **Ownership rule: whoever constructs it, closes it.** Ownership is created by `New()` and does not
  float. A function that *receives* a `*Cache` must never close it. So the chain is
  `main` → `node.Close()` → `cache.Close()`; each layer closes exactly what it made, and the
  obligation propagates upward — a node that owns a resource is *forced* to grow a `Close()`.
- **Honest caveat: for a one-cache, one-process server, `Close()` is nearly pointless** — process exit
  reclaims everything. Leaking a goroutine only costs you if caches are created and destroyed
  *repeatedly* within one process lifetime.
- **…which is exactly this project.** The money moment is `/admin/kill`. Every kill destroys a node
  and its cache; every recovery makes new ones. Forty demo clicks = forty leaked sweeper goroutines,
  and each goroutine stack is a **GC root**, so none of that memory is collectable. The process
  demonstrating self-healing would slowly die of the thing it demonstrates. **`Close()` is
  load-bearing *because of* the failure-injection design.**
- **Shutdown ordering:** `signal.NotifyContext` → `<-ctx.Done()` → `srv.Shutdown(ctx)` (stop
  accepting, drain in-flight handlers) → **then** `cluster.Close()`. **Stop the users, then stop the
  thing they use.** Reversing it tears down a cache a handler is mid-`Get` on. (Ours survives that —
  `Close` only stops sweeping — but relying on it is fragile.)
- Decided: `Close()` returns **nothing**, not `error`. It can't fail, and inventing an always-nil
  error to satisfy `io.Closer` is a lie about the API. And **not** `New(ctx)`: `context` is for
  request scope and in-flight cancellation, `Close()` is for resource lifetime. Tying cache lifetime
  to a shared context would make it impossible to kill *one* node — precisely what the demo needs.
- Nothing calls `Close()` outside tests yet (no `main`). Not a bug. `node.Close()` / `Cluster.Close()`
  are the first things Phase 2 must grow.

#### Session 4 (cont.) — Phase 1 step 4: eviction (taught & quizzed; NO CODE YET)
- **Why a size limit is a second, independent bound.** TTL bounds **staleness**, not **size**:
  1k sessions/sec × 30-min TTL = **1.8M live entries** in steady state, none of them stale. And
  `ttl <= 0` keys never expire — a cache of permanent keys with no limit is a **memory leak by
  design**.
- **Bélády's optimal algorithm** — evict the entry needed *farthest in the future*. Provably optimal,
  **unimplementable** (needs the future). Reframe: **every real policy is an approximation of Bélády
  using only the past.** You are not choosing a data structure, you are choosing **a theory about how
  your users behave.** Random (no theory) · FIFO ("old things stop being useful") · **LRU** (temporal
  locality) · LFU (popularity is stable).
- **LRU's failure mode — the sequential scan.** Capacity 3, hot `{a,b,c}` at ~100% hit rate. A batch
  job reads `x1..x10` once each: each miss makes the app `Set` the key, each `Set` evicts the LRU.
  End state `{x8,x9,x10}` — keys **nobody will ever read again**; `a,b,c` gone; hit rate **0%**. The
  scan got *zero* benefit (every read missed regardless) and destroyed the hot set: **strictly
  negative work.** With a scan longer than the cache, it even flushes itself. Root cause: **LRU treats
  a single access as sufficient evidence a key deserves to be cached**, and for a scan that evidence
  is a lie.
- **Aayush caught a real error in my trace.** I wrote `Get(x1) → insert, evict a`, but **our `Get`
  never inserts** — it's a **cache-aside (look-aside)** store, like Redis/memcached: the *application*
  does `Get` → miss → `db.Query` → `Set`. **Eviction can only happen in `Set`.** (A **read-through**
  cache — Caffeine, Guava `LoadingCache` — takes a loader and populates itself; there `Get` can evict.)
  The consequence is sharper, not weaker: the cache sees ten ordinary `Set`s with no flag saying
  "batch job." **Pollution originates in the caller's fill pattern**, so scan resistance must live in
  the eviction policy — the only place with enough information to tell a hot key from a scan artifact.
  Division of labour: `Get` **hit** updates recency (→ `Get` is a writer, **third** time, which kills
  `RWMutex` properly rather than provisionally) · `Get` **miss** does nothing · `Set` updates recency
  **and** evicts.
- **Aayush's challenge: "won't LRU evict the expired keys anyway — aren't corpses least-recently
  used?"** Sometimes, but **recency and expiry are independent orderings**: LRU sorts by *last touch*,
  expiry sorts by *deadline*. Killer case (our own leak workload): 999 sessions `Set` with a 50 ms TTL
  in the last second and never read, plus one permanent `config` key touched a minute ago. A `Set` is
  an access, so **every corpse is more recently used than the live key** → LRU evicts `config` and
  keeps 999 corpses. The cache is then *worse than empty*: 1000 slots serving nothing. Converse: a key
  `Set` 1 ms ago with a 1 ms TTL is the **MRU entry and already a corpse**. Recency of *use* ≠ freshness
  of *value*. And "usually right" is not a bound when the check costs **one timestamp compare**.
  → **Reclaim a corpse before evicting anything live.** The `expiring` index + `samplePass` already
  give us the machinery. This is the step-3↔step-4 seam, found by Aayush arguing with me.
- **Scan resistance — four families**, three of which weaken the meaning of a single access:
  | Family | Question it adds | Real systems |
  |---|---|---|
  | More evidence | "have you been used *twice*?" | InnoDB young/old sublists, 2Q, Linux active/inactive |
  | Frequency | "how *often*?" | LFU **+ decay** (Redis log counter, halving), ARC (adaptive recency/frequency boundary), LIRS (reuse distance, RocksDB) |
  | **Admission** | "are you better than whoever you'd evict?" | **TinyLFU / W-TinyLFU** (Caffeine) |
  | Hinting | "will the caller just tell us?" | PostgreSQL seq-scan ring buffer, `MADV_SEQUENTIAL` |
  - **Admission is the deep reframe: LRU has no admission policy.** Every arriving key is admitted
    unconditionally; the only question ever asked is *who leaves*. TinyLFU asks *should this key come
    in* — victim `a` has freq ~1000, `x1` has freq 1, so **reject `x1`** and leave the cache untouched.
    The whole scan costs nothing.
  - Tracking a count per key would cost more than the cache, so TinyLFU uses a **Count-Min Sketch**
    (few bits/key, error only ever an over-estimate) + a **doorkeeper** Bloom filter for one-hit
    wonders + periodic counter halving for decay. **Approximate answer, bounded error, memory
    independent of data size** — the *same bargain* as the sampling sweeper, and the one Phase 4's
    failure detection will make. It keeps recurring because it's how you get O(1) out of problems that
    look O(n).
  - Naive LFU is broken on its own: counts never decay, so last Tuesday's viral key is immortal and a
    genuinely rising key can never accumulate enough count to displace it. **LFU can't adapt.**
- **The metric changes here.** "Does it work" is the wrong question — LRU always evicts *something*.
  The metric is **hit rate** = hits/(hits+misses). Eviction policy is the main lever on it, and **you
  cannot compare two policies without measuring it.** Hence step 4's benchmark before any scan fix.
- **Deliberately NOT fixing scan resistance yet.** Segmented LRU is ~30 lines, but we have no hit-rate
  benchmark and no scan workload. Adding W-TinyLFU now = adding a solution to a problem we haven't
  measured, on the strength of a story. **The scan story is a hypothesis until it prints a number.**

### Session 3 — 2026-07-08
- Confirmed HLD §6.2 failure-mode catalog present (line 206) — user had missed it (it's a
  subsection of §6, not a top-level section).
- **Go 1.26.5 installed** via `winget install GoLang.Go` (at `C:\Program Files\Go\bin`).
- **Phase 0 concepts taught & quizzed (all passed):**
  - **What a cache is** — key→value, cache hit/miss, TTL, eviction, *bounded staleness* bargain.
    Quick-check: stale read (sharpened "inconsistent"→"stale"); LRU + temporal locality. ✅
  - **Why distribute** — single-node walls: capacity, throughput, availability (SPOF → thundering
    herd + retry storms). Fix = split (capacity/throughput) → replicate (survive a death) →
    self-heal (survive repeated deaths). Quick-check ✅ (sharpened Q3 onto the 3 named steps).
  - **CAP consolidation** — C/A/P definitions, P-not-optional (real choice = C vs A), AP-vs-CP
    partition behavior ("AP always answers but may lie; CP never lies but may refuse"). ✅
  - **Cluster-in-a-box** — real protocol (nodes, real HTTP msg-passing, real detection/replication/
    heal) vs collapsed topology (N goroutines/1 container); shared-fate honesty caveat (legit vs
    dishonest claims for the writeup). ✅
- Phase 0 checklist: cache ☑, CAP ☑, cluster-in-a-box ☑. (Go idioms taught on-demand in Phase 1.)

### Session 2 — 2026-07-08
- **Teaching preference corrected:** analogies are now *optional* — default to a simple explanation +
  concrete example, use an analogy only when a concept is genuinely hard or Aayush asks. Updated in
  both `CLAUDE.md` (teaching-style bullets) and the `learning-project-preferences` memory.
- **HLD enriched** (`docs/HLD.md`):
  - Added **§6.1 "Why node death causes staleness"** — async-write replication window; the freshest
    copy can die with the primary (lost write vs stale read); self-heal restores *redundancy* not
    *freshness*; TTL / read-repair / W-acks bound the damage.
  - Added **§6 "Who runs the heal (decided)"** — no dedicated healer node; each key range's current
    primary coordinates its own heal, deterministically from the ring (no election); nodes scan only
    the keys they already hold (no node knows the global keyset).
- **Failure-mode deep dive (informal, pre-phase — like Session 1):**
  - **Lost writes:** acked-but-unreplicated write dies with the primary; primary-only ack vs W-acks
    is a dial that turns single-node silent loss into correlated-multi-node loss (never zero).
  - **Heal trigger mechanics:** *promotion is lazy/triggerless* (just what `owner(k)` returns once
    the view changes) vs *the heal is event-triggered* off the `alive→dead` transition; edge- not
    level-triggered; react to node-identity **transitions**, not the alive **count** (one-out/one-in
    keeps the count constant yet ownership shifts).
  - **available ≠ fully-replicated:** the window between promotion (instant) and heal (later) serves
    reads while under-replicated.
  - **Detection window:** ring still includes the dead node → reads survive via fallback but pay a
    timeout of latency (bounded by the app-level request timeout, not OS/TCP); writes to the dead
    node's ranges briefly unavailable or risky.
  - **False positives:** GC pause/slow node looks identical to death (silence is ambiguous); needless
    migration → CPU/network cost → secondary false positives → flapping/cascade storm; the
    short-vs-long timeout tradeoff is fundamental. Named phi-accrual, SWIM indirect probes,
    grace/incarnation numbers as mitigations.
  - **Partition / split-brain:** both sides think they're the survivor; both serve (AP) → divergent
    writes → LWW on reconcile silently drops the loser (real data loss = the price of AP). CAP made
    concrete; **quorum/majority** as the CP alternative (minority side goes unavailable). Key
    correction: a **coordinator does not dodge the CAP choice** — a single one is a SPOF, a
    replicated one partitions too and needs consensus (= quorum = CP). Callback to Session 1's
    "replicating a coordinator itself needs consensus" insight. Cluster-in-a-box partitions are
    *injected* (`/admin/partition`), not natural.
  - **Quorum:** quorum = a *count/threshold*, NOT hand-picked nodes. Two flavors — (1) **membership
    quorum** (majority of all N; decides which side serves under partition; a side has quorum iff
    reachable-nodes > N/2), (2) **per-key R/W quorum** (Dynamo W-acks; candidate pool = the key's N
    replicas from the ring; `R+W>N` guarantees read/write set overlap → no stale read). Core property:
    any two majorities of a set must overlap → at most one writable side / read always sees latest
    write. Prefer **odd N** (even N wastes a machine + risks a 2–2 split-vote → both sides
    unavailable). `W=N` destroys write fault-tolerance (any 1 replica down → all writes fail).
  - **Conflict resolution under quorum:** *overlap ≠ ordering*. Consensus/single-leader quorum (CP)
    → no conflicts (writes serialized by leader). Leaderless R/W quorum (Dynamo) → concurrent writes
    to different W-subsets diverge → needs LWW / vector clocks. **For our design:** the per-key
    primary serializes writes → no conflict in normal operation; conflict (hence LWW) arises *only*
    when the single-primary invariant breaks → **two primaries**, via partition or asymmetric false
    positive. LWW is our split-brain *repair* tool, not a data-path concern.
- **Quiz outcomes:** see quiz log below. Overall strong; Aayush independently reconstructed the
  false-positive cascade, reasoned the coordinator→consensus trap, and traced conflict resolution
  back to the two-primaries root cause.
- **Added HLD §6.2 "Failure modes (catalog)"** — concise 7-row table (lost write, detection window,
  available≠fully-replicated, false positive, partition/split-brain, two-primaries conflict,
  correlated total loss) + the timeout & AP-vs-CP cross-cutting tension.
- **Walked the 6 §10 tradeoffs and LOCKED all of them** (see Current status). HLD status banner
  flipped DRAFT → APPROVED; §10 rewritten from open ⚑ questions to locked ✅ decisions with reserves.

### Session 1 — 2026-07-07
- Set up project scaffolding: CLAUDE.md, ROADMAP.md, PROGRESS.md, memory files.
- Decisions locked: Go · **complete-beginner** teaching level (updated from "some exposure"
  mid-session) · mixed quizzing · cluster-in-a-box.
- Removed all references to the (now-deleted) DISTRIBUTED_SYSTEMS_PROJECT_IDEAS.md.
- **Taught (informal Q&A, not yet a formal phase):** why consensus is out of scope; CAP & AP vs CP;
  consistency / staleness / eventual consistency; async replication vs read-repair vs anti-entropy
  vs TTL; split-brain & conflict resolution; control plane vs data plane; peer-gossip failure
  detection (no coordinator → no consensus needed); full failure-mode catalog for our design; and
  how the whole discussion maps onto the project phases.
- Aayush's quick-checks: answered strongly (correctly reasoned that replicating a coordinator would
  itself need consensus among coordinators — the key insight). Comfortable with the material.
- **Drafted `docs/HLD.md`** (architecture, components, flows, interfaces, 6 open ⚑ decisions).
- Go toolchain still NOT installed — first practical task in Phase 0.
- Stopped for the day; will continue tomorrow.

---

## Quiz results log
_(Score + what to revisit. **Full question text and model answers live in `docs/QUIZZES.md`.**)_

### 2026-07-10 — Session 6: Phase 4 milestone quiz (failure detection) → **2 ✅ · 2 ⚠️ · 2 ⊘**
- ✅ three causes of silence (crash/GC/network) · ✅ independent views → coordinator→SPOF→**consensus**→CP chain
- ⚠️ **the timeout knob** — 50ms false-positives ✅; 5s conclusion right but *mechanism* wrong (a crashed node fails pings *fast*; the delay is the `lastSeen` **declaration** threshold, not a hanging connection).
- ⚠️ **scaling** — named "gossip/SWIM" but not the mechanism (a node learns of a death **second-hand** / transitively, not by direct ping).
- ⊘ **self-suspicion & split-brain** — left blank *despite the live demo minutes earlier*. Re-ask cold.
- ⊘ **extend it / reduce false positives** — left blank. Model answers (indirect probing, suspicion+incarnation, N-misses, phi-accrual) + the universal tradeoff (every mitigation slows *real* detection) in QUIZZES.md.
- **Revisit:** the pattern is **label-not-mechanism** (Q2 why, Q5 how, Q4/Q6 blank) — the hard concepts (Q1, Q3) were clean, so it's precision, not comprehension. Push for the mechanism on re-ask. Carry Q4 and Q6 forward cold into Session 7.

### 2026-07-10 — Session 5: cold re-ask of the 9 carried-forward questions → **2 ✅ · 5 ⚠️ · 1 ❌ · 1 ⊘**
- ✅ naive-timer overwrite bug · ✅ `Close()`/`select`/`Ticker`
- ❌ **check-then-act** — third miss. Looked for a missing lock in a fully-locked function.
- ⚠️ **starvation** defined backwards · ⚠️ **resource semantics** ("heap allocation makes it a
  resource" — no; a `[]byte` is heap-allocated and is a value) · ⚠️ **compare-don't-remember** —
  slogan recited, interleaving not produced · ⚠️ **expiry-aware eviction** — right victim, imported
  the wrong principle · ⚠️ **scan resistance** — 3 of 4 families, never *named* admission control
- ⊘ **happens-before** — not attempted; taught in full.
- **Revisit first:** check-then-act (still unanswered), then admission control, then happens-before.

### 2026-07-09 — Session 4: concurrency, races, mutex (6 Q) → **4 ✅ · 2 ⊘ · 0 ❌**
- **Disjoint keys still race** — ✅ named the shared map; sharpened to *which part* (bucket array /
  growth flag / count, not the value slots).
- **Data race definition** — ✅ all three conditions. **Race condition ≠ data race** — ⚠️ named
  check-then-act as a category but did not produce concrete code. → re-ask cold.
- **Why `Get` locks** — ✅ strong; produced "map mid-rehash" unprompted. Sharpened: it's *required*
  by the definition (≥1 writer suffices), not caution; consequence is corruption, not staleness.
- **`defer` / lock never released** — ⚠️ mechanism exactly right, called it **starvation** when it is
  **deadlock**. Vocabulary gap, not understanding. Matters in Phase 4. → re-ask cold.
- **Atomic map ⇒ no mutex?** — ⊘ taught instead: lost updates + **safe publication** survive
  per-operation atomicity; mutex = critical section **+ happens-before edge over all memory**.
  → **re-ask before Phase 3** (becomes "when is a replicated write visible?").
- **`SetIfAbsent` vs `RWMutex`** — ⊘ taught instead: correctness (reasoned, needs a *caller*) vs
  performance (needs a `b.RunParallel` benchmark; `RWMutex` may well be *slower* on ns-long
  critical sections).

### 2026-07-08 — Failure-mode quick-checks (pre-phase, informal)
- **Deterministic promotion / no election** — ✅ strong. Correctly explained ownership as a pure
  function of (ring, alive nodes), added the "given agreement on membership" caveat when prompted.
- **W-acks don't eliminate loss** — ✅ correct (shrinks to correlated-multi-node loss).
- **Read succeeds during heal / replication status** — ⚠️ partial. Got promotion + routing right but
  missed that the cluster is *under-replicated* at that instant. Re-taught "available ≠
  fully-replicated." → revisit.
- **React to transitions not count** — ⚠️ partial. Right direction but didn't produce the concrete
  one-out/one-in counterexample. Re-taught.
- **False-positive cascade (short timeout)** — ✅✅ excellent, reconstructed the storm unprompted.
- **Detection-window latency source** — ✅ correct (timeout before fallback; refined to app-level
  request timeout).
- **Two conflicting values under false positive** — ⚠️ partial → re-taught **frozen (→ staleness)
  vs alive-but-unreachable (→ true conflicting writes)**. → revisit.
- **Coordinator resolves split-brain?** — ✅ Aayush self-corrected via Session 1's insight
  (replicated coordinator needs consensus = quorum = CP). Solid.

### 2026-07-08 (cont.) — Quorum + conflict-resolution quick-checks
- **Partition CP behavior** — ✅ (refined: minority always refuses writes; refuses reads only if a
  strong-read guarantee is wanted, else it *may* serve stale reads).
- **W=3, R=1 on N=3** — ✅ `R+W>N`, read-your-writes; downside sharpened from "latency" to "W=N
  destroys write fault-tolerance — any 1 replica down → all writes fail."
- **Why 4 nodes worse than 3** — ✅ 2–2 split-vote (neither has majority) + no extra fault tolerance
  for the extra machine.
- **Conflict resolution possible in our design?** — ✅ correctly identified **two primaries**
  (split-brain via partition or asymmetric false positive) as the sole root cause.
