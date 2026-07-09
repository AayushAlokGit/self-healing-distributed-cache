# Progress Tracker

Living log of where we are, what's been taught, and quiz results. Update at the end of every
session and after each milestone. Newest entries at the top of the log.

## Current status
- **Phase:** **Phase 0 COMPLETE (concepts) — Go 1.26.5 installed. Starting Phase 1 (naive
  single-node cache).** HLD APPROVED, all 6 §10 decisions LOCKED (2026-07-08).
- **Locked decisions:** (1) nodes = goroutines in one process, real HTTP over localhost ports;
  (2) primary-only write ack to start, W-ack knob added in Phase 3; (3) all-to-all heartbeats;
  (4) HTTP/JSON transport; (5) dashboard — **polish is a priority** (recruiter-facing money moment);
  framework/viz-library OK if it elevates the demo, must stay static-hostable + free; (6) **R=3**,
  configurable.
- **Next action:** **Phase 1** — first real Go: naive single-node cache (hash-map store, thread
  safety, TTL, then LRU eviction). Teach Go idioms as they appear.
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
- ◐ Concurrency / races — TAUGHT & demonstrated live (`DATA RACE` + `fatal error: concurrent map writes`); mutex fix written & proven green, then temporarily reverted to the naive version so the "before" is committed first. Mutex + `race_test.go` land in the next commit.
- ☐ TTL expiry
- ☐ LRU eviction

### Phase 2 — Consistent hashing
- ☐ Why `hash % N` breaks on resize
- ☐ The ring + wraparound
- ☐ Virtual nodes / balance
- ☐ Key ownership lookup

### Phase 3 — Replication
- ☐ Replication factor R, primary + replicas
- ☐ Write propagation, read fallback
- ☐ Consistency vs availability trade-off

### Phase 4 — Failure detection
- ☐ Heartbeats & timeouts
- ☐ False positives (GC pause vs death)
- ☐ Gossip / SWIM intuition

### Phase 5 — Self-heal
- ☐ Data migration on membership change
- ☐ Re-replication to restore R
- ☐ Serving reads during heal

### Phase 6 — Dashboard
- ☐ Ring viz + failure-injection controls

---

## Session log
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
_(Record each quiz: date, phase, score, and any concept to revisit.)_

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
