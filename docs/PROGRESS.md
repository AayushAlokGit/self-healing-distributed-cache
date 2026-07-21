# Progress Tracker

Where we are, what's been taught, and what each thing earned. Update at the end of every session.

**One fact, one home** — so this file stays readable:

| Doc | Owns |
|---|---|
| **PROGRESS.md** (this file) | What we built, the **number** it earned, the **lesson** it taught. The concept checklist is the canonical record; the session log is the dated narrative. |
| `QUIZZES.md` | Quiz questions, model answers, and the **named gap** behind every ⚠️/❌. |
| `GO_NOTES.md` | Go idioms and traps (⚠️ = compiles fine, fails at runtime). |
| `HLD.md` | Architecture, the failure-mode catalog (§6.2), the locked decisions (§10). |
| `ROADMAP.md` | The phase plan. |

---

## Current status

**Phases 0–6 COMPLETE, and SHIPPED.**
[Dashboard](https://self-healing-distributed-cache.vercel.app/) (Vercel) ·
[API](https://self-healing-cache-api.onrender.com/) (Render — the root lists the clusters). Kill→heal→revive verified
against the live deployment, not just locally: killing `n2` cost **0 keys**, the survivors pushed the
copies back to R=3, and the revived node came back empty and was repopulated by the heal alone.

Built, tested under `-race`, browser-verified. Go 1.26.5; HLD **APPROVED**, all six §10 decisions locked
(nodes as goroutines over real HTTP · primary-only ack + a W knob · all-to-all heartbeats · HTTP/JSON ·
dashboard polish is a priority · R=3 configurable). Run it locally: `go run ./cmd/server` (:8080) and
`cd frontend && npm run dev` (:5173).

### Next action — pick one
- **(a) The writeup** ← *recommended; deployment is DONE.* Two honesty caveats: **cluster-in-a-box** (only
  the *topology* is collapsed — the protocol is real) and S9 Q5's **no god's-eye view exists** (the
  dashboard's omniscient state is impossible in a real deployment, because *dead* is a **belief**, not a
  property; an honest dashboard would show N disagreeing views).
- ✅ **(b) Read-repair-on-read — DONE S21.** The read (`handleClientGet`) already gathered every owner and
  reconciled; now `readRepair` `storeOn`s each surviving version back to any reachable owner that doesn't
  `covered()` it, so a **lagging-but-alive** replica (one that missed a write while never declared dead — a blip
  under the 500ms timeout, or a failed best-effort write) converges **on the read** instead of waiting for an
  overwrite or a membership-change heal (the heal is event-driven, so it never sees this replica). Traps avoided,
  each a comment: propagate **all** siblings (make a conflict consistent, don't resolve it); carry **each
  version's own deadline** (else a frequently-read expiring key resurrects — the heal-resurrect bug's twin); only
  **reachable** owners and only on a **returned value** (a refusal/404 repairs nothing); presence≠version via
  `covered`. Synchronous + self-limiting (a converged read stores nothing); a production store would run it off
  the read path. Test `TestReadRepairsAStaleReplica` (repairs a blocked-out replica on read, idempotent second
  read). *Optional follow-up: surface it in the activity log (needs a drain like `DrainHealLog`).* The two
  heavier cousins stay unbuilt on purpose, named not hidden (`HLD §9`, `CAP §13`): **hinted handoff** (queue a
  write for a down owner) and **Merkle-tree anti-entropy** (continuous background sync; our heal is its
  event-driven cousin).
- **(c) `HEAD /kv/{key}`** — a cheap, real win. `fetchFrom` is a `GET`, so every *"do you have this key?"*
  probe **downloads the whole value**. A `HEAD` makes the check free and roughly halves heal traffic.
- **(d) NOT the arc-diff heal** (touch only the ring arcs that changed owner). S9 Q7 sketched it and argued
  **against** building it: it taxes every `Set` forever to speed up a rare death. Wait for a measured need.
- **(e) Pin the `cluster` flake** — **2 of 4** full-suite `-race` runs fail; **0 of 4** in isolation. Fails under
  load, passes alone = a test leaning on a timing budget it does not own (a fixed `time.Sleep` where it should
  **poll for the condition**). Reproduce it with `go test -race -count=1 ./...` (the whole suite is the load), not
  by re-running `./cluster/`. A flake you tolerate is a flake that teaches you to ignore red.
- **(f) Phase 7 — the partition / consistency-dial demo** ← *designed S16, reworked S17.* `docs/CAP.md` owns
  the reasoning, `docs/CAP_DEMO.md` the demo spec + the scorecard. Highest-value *new capability*, and it's
  where **presence ≠ version** finally gets fixed in code. Build arc, now **three steps**:
  - ~~**7.0 — a second cluster on a second tab.**~~ **DONE S17** (`48307dd` Go, `d39a305` frontend,
    `f02805e`, `7b86654`). `cluster/` needed **zero changes**, as predicted: nodes bind `127.0.0.1:0` so the
    OS assigns ports, and there is **no package-level mutable state** in `cluster/`, `node/` or `cache/` —
    isolation is structural, not disciplined. Browser-verified: killed `n1` on Replication (4/5, healed onto
    survivors), CAP stayed 5/5 with 0 pushed. ⚠️ **It broke the live demo** (see the S17 log) — **fixed
    2026-07-17: Vercel↔GitHub reconnected, verified live.**
  - **7A — the cut, the coordinator picker, vector clocks, sibling-aware heal, the conflict card.** Demo:
    both sides accept a write, the heal proves the two writes never saw each other, and **keeps both**.
    **COMPLETE on branch `cap-demo` (S18–S19), verified end-to-end in the browser** — built there, merge to
    main by hand so a breaking API change never auto-deploys onto the live frontend again (the S17 failure).
    The steps:
    - ✅ **`vclock/`** (`129ab1f`) — `Clock` = one counter per node; `Merge`/`Bump`/`Compare`
      (Before/After/Equal/**Concurrent**). Merge-then-bump; the resolution-must-merge trap is a test.
    - ✅ **Versioned cache** (`3cf80dc`) — a key holds `[]Entry`, each with a `vclock.Clock` and its own
      deadline. Additive: `Set`/`Get`/`Snapshot` keep single-value behaviour (Phase-1 suite untouched),
      new `SetVersioned`/`GetEntries`/`SnapshotAll` + `reconcile` (drop dominated, keep concurrent) ride
      alongside. **Nothing produced versions yet at this commit.**
    - ✅ **Write path flows versions** (`3d5251f`) — `/kv` carries versions (JSON array on GET, `X-Version`
      header on PUT → `SetVersioned`); `handleClientSet` does read-before-write (`currentVersion` merges the
      owners' clocks, `Bump(coordinator)` stamps). Two coordinators that can't see each other bump different
      slots ⇒ concurrent ⇒ both kept — the seam the cut exploits. Heal's `storeOn` carries the version too.
    - ✅ **Read + heal version-awareness** — **DONE S19** (`42d1335` read, `814df5d` heal + cleanup).
      **Read:** `handleClientGet` gathers every reachable owner's versions and folds them with `cache.MergeVersions`
      — one survivor ⇒ a plain value, two+ ⇒ concurrent siblings under `X-Conflict: n` + a JSON array (conflict
      *detection*); `servedBy` now names the first owner holding a *surviving* version. **Heal:** `SnapshotAll` +
      a **per-version** healer (I heal `v` only if no owner ahead of me *covers* it), so a stranded `carol` and a
      concurrent `bob` both propagate; a dominated local version stands down. **Cleanup:** drops a non-owned copy
      only once every owner *covers* every version it carries, so a sibling no owner holds is kept and the heal
      re-armed. Unifying: **presence≠version applied to each mechanism's own decision** (read's who-answers, heal's
      who-heals, cleanup's is-this-surplus).
    - ✅ **Coordinator picker · the cut · the conflict card** — **DONE S19, 7A COMPLETE.**
      - **Coordinator picker** (`6f088fb`): `Set`/`Get` take a `via` naming the coordinating node ("" = any
        live, unchanged); a dead/unknown `via` is refused with `*NoSuchNodeError` (400), never rerouted —
        determinism is the point. Frontend: a `CoordinatorSelect` on the Write and Read cards.
      - **The cut** (`4ca295c`, simplified to one `gate` in `90126eb`): a partition lives **under** the HTTP
        clients — each node's `gate` (a mutex-guarded blocked-address set shared by the data *and* health
        clients) refuses blocked peers in `RoundTrip`, so a cut drops data **and** heartbeats together and each
        side convicts the other and shrinks its ring. `Cluster.Cut(sideA, sideB)`/`Mend`; symmetric though each
        node blocks only its own outgoing dials. Gated at RoundTrip (not DialContext) so a keep-alive connection
        pooled before the cut is refused too.
      - **The conflict card + cut UI** (FE `1f10793`): the card renders siblings off `X-Conflict` + the array;
        a `PartitionPanel` (per-node A|B toggles), a "NETWORK PARTITIONED" banner, and ring A/B tags. ~~⚠️ The
        cut is tracked **client-side** — `/state` reports no partition, so a page reload loses the banner.~~
        **CLOSED S20 (below): `/state` reports the partition; a cut renders as two independent per-side rings.**
      - ✅ **`/state` reports the partition — two independent rings (S20).** `Cluster` now records the active cut
        (`cutA/cutB`, set in `Cut`, cleared in `Mend`); `State()` returns a `Partition{sideA,sideB,vnodesA,vnodesB}`
        and per key `OwnersA`/`OwnersB` from **two per-side rings** (side A = alive − B, side B = alive − A; a
        bridge node lands in both). Under-replication is judged **per side**. Frontend: banner + panel + ring driven
        by `state.partition` (client-only `cut` deleted, so a reload is faithful). A cut renders as **two separate
        `RingViz`, side by side** — App slices the snapshot into a per-side `State` (`sideState`) and draws each as
        the independent cluster it is (from A's view, the far side is simply dead). The key table shows both sides'
        owners. Browser-verified: cut → two rings + banner, **reload → all reconstructed from `/state`**, mend →
        single ring; `NODES ALIVE` stayed 5/5 (no god's-eye view). Test `TestStateReportsPartitionWithPerSideOwnership`.
    - ✅ **Verified end-to-end in the browser (S19).** Cut `{n0,n2,n4} | {n1,n3}` → wrote `cart=milk,eggs` via
      **n0** and `cart=milk,bread` via **n3** (both accepted, each side serving alone) → mend → the version-aware
      heal reconciled (activity log: `HEAL n0→n1`, `n1→n0`, `n1→n2`; cleanup dropped surplus, **not** the
      siblings) → a gather-all read returned the **conflict card with both real values**. **NODES ALIVE stayed
      5/5 the whole time** — the manager cannot see a partition (S9's "no god's-eye view", live). ⚠️ First take
      used a 2-min TTL and `cart` expired mid-demo; redone with `never`.
  - ✅ **7B — the dial. DONE S21, browser-verified.** `W` and `R_read` as a cluster-wide consistency dial. The
    non-obvious core wasn't `SetQuorum` — it was that **the ring must stop shrinking**, or a quorum is a rubber
    stamp: `node.holdRing` gates `ring.Remove`/`Add` in `heartbeatRound`, so at the strong setting the far owners
    stay in the ring and a partitioned side that can't reach W refuses instead of re-owning the keyspace among its
    survivors. Reads refuse **503** below `R_read` (a reachable-count threshold on the 7A gather-all, so conflict
    detection is intact; `R_read=1` is byte-for-byte the old read); writes refuse **503** below `W` (was 502).
    `cluster.SetQuorum(w, rRead)` validates `1≤w,rRead≤rf`, sets `holdRing = w+rRead>rf`, inherited by `wireAll`/
    `Revive`; `State` reports `W`/`RRead`; `POST /quorum`. **Frontend:** a `DialPanel` (ONE/QUORUM presets + W and
    R_read selectors + a live `W+R_read vs R` readout), a Stats dial tile, and a **Scorecard** — one probe row per
    dial, so flipping and re-probing stacks the controlled experiment. **Browser-verified live: QUORUM → 5
    accepted / 5 refused / 0 conflicts (the checkerboard); ONE → 10 / 0 / 5 (divergence). The refused writes and
    the conflicts are the same writes.** Tests: node read/write refusal below quorum; `TestSetQuorumValidation`;
    and the money pair — a held ring refuses the losing side of a cut, an unheld ring (W=2,R_read=1) rubber-stamps
    it after the ring shrinks.
  - ⚠️ **Vector clocks, not Lamport (decided S17).** The two writes either side of a cut are *genuinely
    concurrent* (`CAP.md` §4: no "later" one exists), so Lamport would **invent** an order and silently
    destroy an acked write. Vector clocks detect the clash and surface it. **`CAP.md` §9 still decides
    Lamport and needs a rewrite** — along with §10–§14; `CAP_DEMO.md` §7 lists every stale line.

### Carried forward — re-ask cold (full text in `QUIZZES.md`)
1. ~~**Presence ≠ version** (S9)~~ — **CLOSED S18.** Re-asked the read half cold before any 7A code: he got
   ring geometry decides (not the coordinator), it's stable across nodes *because membership views agree*, and
   named the precondition unprompted — which is exactly the seam 7A opens. Both halves now solid.
2. **Reversibility, not just cost** (S9) — the rule splitting the instant reaction from the delayed one. He
   reliably produces "cheap/expensive" and drops "reversible/irreversible."
3. **The stranded-key case** (S9) — *"a revived node is promoted back to primary of an arc while holding
   nothing; under 'only the primary heals,' who repopulates it?"* (Answer: **nobody, ever.**)
4. **The deadline's frame of reference** (S10, never asked) — *"what deadline does a healed copy carry, and
   what breaks if the TTL travels as a duration rather than an instant?"*
5. **Why a delete cannot address the owners** (S11, never asked) — *"name two ways the key survives an
   owners-only delete. What would a real system add?"* (Leftovers from an old ring; a paused holder that
   heals it back. A **tombstone**.)
6. **Cleanup's ordering** (S13, never asked) — *"a node holds a key it does not own. Why may it not just
   delete it? What must it check first, and why is 'unreachable' not the same answer as 'no'?"* (A surplus
   copy and the last copy alive are indistinguishable from there. Confirm all R owners hold it. Silence is
   not consent.)

### Deferred on purpose, with reasons
- **`sync.RWMutex`** — measured: the *uncontended* mutex is **40% of a 67 ns `Get`** (26.9 ns), with only
  ~22 ns of real work to overlap, and an `RLock` costs *more* than a `Lock`. Also `Get` **deletes**, so it
  cannot take an `RLock` as written.
- **`SetIfAbsent`** — the caller-side check-then-act gap. A *correctness* fix, so it needs a real caller,
  never a benchmark.
- **Go maps never shrink** — 16.5 MB of bucket array survives sweeping 200k entries to `Len()==0`; only
  replacing the map frees it. Redis rehashes into a smaller table; Go doesn't. Known limit, not fixed.
- **Injectable clock** — the test suite spends ~4 s sleeping.
- **`sampleSize` / `expiredThreshold`** (20 / 25%) are **Redis's** constants, unmeasured by us.
- **Scan resistance** (segmented LRU, W-TinyLFU) — Phase 1 checklist: ~12 points at a 1:1 scan ratio against
  skewed traffic does not pay for the complexity.
- **Gossip / SWIM** — Phase 4 checklist: all-to-all is O(N²) and at N=5 we never hit the wall.

### Flagged (learning)
- **"Compare, don't remember"** has been missed **twice** (S4 Q2, S4c Q1) — a value read before releasing a
  lock is a **rumor** after it. Re-teach on sight.
- Two spots needed correction during the failure-mode quiz and were re-taught (solid now, worth a light
  re-check): **available ≠ fully-replicated** (reads serve while the cluster is still one copy short during
  the heal window) and **frozen ≠ partitioned** (a GC-frozen node yields only *staleness*; an
  *alive-but-unreachable* one yields genuine **conflicting writes**).
- **Teaching mode:** Aayush is a complete beginner in distsys — plain explanation + concrete example first,
  analogies only when a concept is genuinely hard or on request. See `CLAUDE.md`.

---

## Concept checklist
The canonical record. ☑ = taught **and** the quick-check passed · ◐ = partial · ☐ = not yet.

### Phase 0 — Foundations
- ☑ **What a cache is** — key→value, hit/miss, TTL, eviction, and the **bounded-staleness bargain** (you
  accept stale answers in exchange for speed; TTL is what *bounds* the staleness).
- ☑ **Why distribute one** — the single-node walls: capacity, throughput, availability (a SPOF whose death
  causes a thundering herd on the DB). The fix, in the three steps that became this project: **split**
  (capacity/throughput) → **replicate** (survive a death) → **self-heal** (survive repeated deaths).
- ☑ **CAP / why this system is AP** — P is not optional (networks partition), so the real choice is C vs A.
  **AP always answers but may lie; CP never lies but may refuse.** A cache that refuses to answer has failed
  at its one job.
- ☑ **Quorum, as the CP alternative we are NOT building** — a quorum is a **count**, not a hand-picked set.
  Two flavours: **membership quorum** (a majority of all N; decides which side of a partition may serve) and
  **per-key R/W quorum** (Dynamo-style W-acks over the key's R replicas, where `R+W>N` forces the read and
  write sets to **overlap**, so a read always sees the latest write). Prefer an **odd N** (an even N risks a
  2–2 split-vote where *neither* side serves). ⚠️ `W=N` destroys write fault-tolerance — one replica down and
  every write fails. And **overlap ≠ ordering**: a quorum makes the sets intersect, it does not order the
  writes, so a leaderless design still needs LWW or vector clocks. **A coordinator does not dodge the CAP
  choice** — one is a SPOF, and a *replicated* one needs consensus among coordinators, which is a quorum,
  which is CP.
- ☑ **Cluster-in-a-box** — real: nodes, HTTP message-passing, failure detection, replication, heal.
  Collapsed: N goroutines in one container. Shared fate is the honest caveat — one process dying takes all
  "nodes" with it.
- ☑ **The failure-mode catalog** — lost writes, the detection window, available≠fully-replicated, false
  positives, partition/split-brain, two-primaries conflict, correlated total loss. **→ `HLD.md` §6.2** (table)
  and §6.1 (why node death causes staleness). Every one was later *built and demonstrated* in Phases 3–5.
- ☐ Go basics as needed → recorded in `GO_NOTES.md` as they come up, not here.

### Phase 1 — Single-node cache
- ☑ **Hash-map store** — `cache/cache.go`: a struct-wrapped map, `New`/`Set`/`Get`, comma-ok,
  miss-vs-empty-value distinguished.
- ☑ **Concurrency / races** — demonstrated **live** before fixing: plain `go test` → `fatal error:
  concurrent map writes`; `go test -race` → `DATA RACE`, with two goroutines writing the **same address**
  despite writing *disjoint keys* (proof the shared thing is the map's bucket array and growth flag, not the
  value slots). Fixed with a `sync.Mutex` locked in `Set` **and** `Get`.
  - **A data race is not a race condition.** A data race is a *mechanical* memory property (same address, ≥1
    write, no synchronization) → **undefined behavior**, and machine-detectable. A race condition is defined
    relative to *intent*, so it is **not** machine-detectable: a read-modify-write counter whose every access
    is individually locked has **zero** data races and is still wrong. **The mutex protects the data, not the
    invariant.** → the deliberate `SetIfAbsent` gap.
  - The five failure modes and the mutex-as-publication-barrier → `GO_NOTES.md`.
- ☑ **TTL expiry** — an **absolute deadline** per entry, **lazy** expiry on read.
  - **Expiry is not an event to schedule, it's a comparison to make.** A timer goroutine per key is not just
    wasteful, it is **silently wrong on overwrite**: `Set(k,"a",30s)` then `Set(k,"b",10min)` and the first
    timer still fires at t=30s, deleting a value with 9.5 minutes left. The timer holds a **stale belief**
    about what it is deleting.
  - **The leak, measured** (`leak_test.go`): 200k short-TTL keys never read back = **40.9 MB retained,
    200,000 corpses, 1 live key** — surviving a *forced* `runtime.GC()`. A GC frees **unreachable** memory;
    every corpse is reachable from `c.data`, so it is by definition live. **A logical leak, which no GC in any
    language can fix**, because "useless" is a fact about intent, not reachability. Extrapolated: 1k
    logins/sec ≈ **8.4 GB/day** → OOM within a day.
  - **The sweeper, and why `Close()` is load-bearing** — a background goroutine (`time.Ticker` + `select` on a
    `done` channel). **A running goroutine's stack is a GC root**, so the sweeper keeps the whole `Cache`
    reachable forever: goroutine and cache hold *each other* up, and only the goroutine **returning** frees
    either. `runtime.SetFinalizer` cannot help — a finalizer runs on unreachability, which is exactly what
    can't happen. **And this project is the case where it matters:** every demo Kill destroys a node and its
    cache, so forty demo clicks would be forty leaked sweeper goroutines — *the process demonstrating
    self-healing would slowly die of the thing it demonstrates.* Ownership rule: **whoever constructs it,
    closes it** (`main` → `node.Close()` → `cache.Close()`), and shutdown order is **stop the users, then stop
    the thing they use** (`srv.Shutdown` before `cluster.Close`).
  - **The full scan was worse than the leak.** `sweepAll` is O(*total* keys), not O(*expired* keys) — ~24
    ns/key whether it deletes anything or not. **The scan pays for looking, not for finding**, and at ~24 ns/key
    it is already one map step + a compare, so **a full scan cannot be optimized below this; you have to stop
    scanning.** One sweep of 1M keys holds the lock **27.5 ms** and drops reader throughput **6,584,449 →
    8,769** gets per 500 ms window (**751×**). Those 8,769 reads are 0.67 ms of work in 500 ms — **the reader
    was productive 0.13% of the time.** Not "slower": an *outage* that answers 8,769 requests. **The naive
    sweeper converts a memory problem into a tail-latency problem**, and tuning the interval slides along that
    curve without leaving it.
  - **The sampling sweeper** (Redis's design). `samplePass` locks, walks ≤20 keys of a separate `expiring`
    index, deletes the expired ones, unlocks; `sampleSweep` repeats immediately while >25% of a sample came
    back expired. **Lock hold at 1M keys: 27,489,911 ns → 7,064 ns (3,891×). Reader cost: 751× → 2.0×** — and
    that residual 2.0× **isn't a stall**, it's two goroutines fairly splitting one mutex in a test that runs the
    sampler flat out.
    - **Dropping the lock is safe here and was fatal before** because `samplePass` is **stateless**: it locks,
      re-reads fresh, acts, unlocks, and carries out only two ints. *Compare, don't remember*, satisfied
      **structurally** rather than by discipline.
    - **The rate is emergent, not tuned.** A sample that comes back 100% expired fails the threshold and passes
      again *immediately* — 50k corpses cleared in **2 calls**, no sleep, no tick. So `defaultSweepInterval` —
      the constant flagged as "picked by gut" — **no longer sets the sweep rate**, only how often we check. *We
      deleted the guess rather than tuning it.*
    - **Two bounds, two jobs.** `sampleSize` bounds the **pause** (one request's latency); the **budget** bounds
      how long the sweeper keeps **competing** for the lock, because pass count is O(expired keys) — set by the
      workload, not by us. Unbounded, reclaiming 500k corpses takes 25,000 passes over 513.9 ms of continuous
      lock churn = **51% of wall time in contention**, which is the very damage the rewrite existed to prevent,
      re-entering at 20 µs granularity instead of one 27 ms lump.
    - ⚠️ **My original justification for the budget was a rationalization** ("a 90%-corpse cache would loop
      forever" — false; corpses are finite, the loop always terminates). **Aayush caught it.** The real reason
      only appeared once it was measured. *A plausible-sounding rationale that was never checked is exactly the
      habit this project exists to train out.*
  - ☑ **Session 10 — made it end-to-end.** For five phases every layer *above* the cache passed a hardcoded
    `ttl=0`: the cache could expire keys and nothing could **ask** it to. Now `cache.SetAt` takes an
    **instant**, `Snapshot()` carries it, and `X-Expires-At` moves it between nodes. See Phase 5 for the bug
    this was hiding.
- ☑ **LRU eviction — O(1).** Bélády's optimal (evict what's needed farthest in the future) is provably optimal
  and **unimplementable** — it needs the future. **So every real policy is an approximation of Bélády using
  only the past, and choosing one is choosing a theory about how your users behave:** Random (no theory) ·
  FIFO ("old things stop being useful") · **LRU** (temporal locality) · LFU (popularity is stable, and ⚠️ naive
  LFU never decays, so last Tuesday's viral key is immortal).
  - **A size limit is a second, independent bound.** TTL bounds *staleness*, not *size*: 1k sessions/sec × a
    30-min TTL = **1.8M live entries** in steady state, none of them stale.
  - **Corpse-first eviction**, and Aayush found it by arguing: *"won't LRU evict the expired keys anyway —
    aren't corpses least-recently used?"* **No — recency and expiry are independent orderings.** A `Set` is an
    access, so 999 corpses written in the last second are all **more recently used** than a live `config` key
    touched a minute ago: LRU evicts `config` and keeps the corpses, leaving the cache **worse than empty**.
    Converse: a key `Set` 1 ms ago with a 1 ms TTL is the **MRU entry and already a corpse**. *Recency of use ≠
    freshness of value.*
  - ⚠️ **`lastUsed` had to be a logical clock, not a timestamp.** `time.Now()` **stands still for 541 µs** on
    this box (13,397 consecutive calls returned the identical instant), so ~5,400 back-to-back `Set`s share one
    timestamp, every comparison ties, and the victim becomes whichever key `range` happens to yield first —
    **chosen at random**. The test failed 5 runs in 10. The code was right; the **type** was wrong. **You cannot
    order events by asking a clock** — the single-node case of the Lamport clock problem, arrived at because a
    Windows timer wasn't precise enough.
  - **Naive → measured → rewritten:** a scan for the minimum `lastUsed` costs **25.6 ms per `Set`** into a full
    1M cache (it is literally `BenchmarkSweep`'s scan, moved onto the caller's goroutine). Replaced with a
    hand-rolled **hash map + doubly linked list** (sentinel head/tail — the map has no order, the list has no
    lookup; together both operations are O(1)):
    ```
                  scan for min lastUsed   unlink the tail
        1k             22,843 ns/op          410.1 ns/op       56×
      100k          2,010,846 ns/op          452.4 ns/op    4,445×
        1M         25,608,480 ns/op          579.4 ns/op   44,199×
    ```
    The left column grows with n and the right one doesn't — that is the whole claim, and it is the reason to
    measure at four sizes rather than one. Then `lastUsed` and the logical clock were **deleted**: *position in
    the list **is** recency*, and you don't keep the scaffolding after the building stands.
  - **Corpse-first survived the rewrite** via a **bounded probe** of the `expiring` index, and the reason
    generalizes: **the probe's hit rate equals the corpse density, and the cost of a miss is inversely
    proportional to it.** At 99% corpses it never misses — exactly the catastrophic case; at 0.1% it almost
    always misses and wastes one slot in a thousand. *Accurate where accuracy matters, sloppy where sloppiness
    is free.* Measured against `1-(1-d)^20`: density 0.001 → 2% hit (theory 2%); 0.01 → 16% (18%); 0.1 → 88%
    (88%); 0.99 → 100% (100%).
- ☑ **Hit rate** — the metric for a *policy*, as opposed to latency. (A cache that instantly evicts exactly the
  wrong key has excellent latency.) Cache-aside harness + Zipf / uniform / cyclic workloads (`hitrate_test.go`).
  **The scan-collapse hypothesis we had asserted for four sessions was measured and half-refuted:**
  ```
  zipf s=1.1 over 10k keys, cap 1000      flat working set of 900, cap 1000
    no batch job              78.2%         no batch job             100.0%
    1 scan per 10 user        75.5%         1 scan per 10 user        89.3%
    1 scan per  1 user        65.6%         1 scan per  1 user        47.5%
  ```
  A batch job issuing **as many requests as every user combined** costs Zipf traffic **12.6 points** — real, but
  not a collapse. **A power law's working set is tiny**: the hot keys are re-requested every few operations and
  never drift near the tail, while the scan's keys sink there immediately and **evict each other.** Where LRU
  actually breaks is a **flat** working set, where every stolen slot is a lost hit.
  - **And it doesn't degrade — it falls off a cliff.** A cyclic loop over **900** keys in a 1000 cache scores
    **100%**; over **1100** keys it scores **exactly 0%** — every key is evicted one request before it is wanted
    again. A 22% wider working set turns a perfect cache into a useless one. (Bélády would score ~91% here. So
    would **MRU** — evict the *most* recent.)
- ◐ **Scan resistance — taught, quizzed, measured, DEFERRED** with a number rather than a shrug. Four families,
  three of which weaken the meaning of a single access:

  | Family | The question it adds | Real systems |
  |---|---|---|
  | More evidence | "have you been used **twice**?" | InnoDB young/old sublists, 2Q, Linux active/inactive |
  | Frequency | "how **often**?" | LFU + decay (Redis), ARC, LIRS (RocksDB) |
  | **Admission** | "are you **better than whoever you'd evict**?" | **TinyLFU / W-TinyLFU** (Caffeine) |
  | Hinting | "will the caller just **tell** us?" | PostgreSQL seq-scan ring buffer, `MADV_SEQUENTIAL` |

  - **Admission is the deep reframe: LRU has no admission policy.** Every arriving key is admitted
    unconditionally and the only question ever asked is *who leaves*. TinyLFU asks *should this key come in at
    all* — victim `a` has frequency ~1000, scan key `x1` has frequency 1, so **reject `x1`** and leave the cache
    untouched. The whole scan then costs nothing.
  - Counting every key would cost more than the cache, so TinyLFU uses a **Count-Min Sketch** (a few bits per
    key, error only ever an over-estimate) + a doorkeeper Bloom filter + periodic halving for decay.
    **Approximate answer, bounded error, memory independent of data size** — *the same bargain as the sampling
    sweeper, and the one Phase 4's failure detection makes.* It keeps recurring because it is how you get O(1)
    out of problems that look O(n).
  - **Not built:** ~12 points at a 1:1 scan ratio against skewed traffic does not pay for the complexity.
    Revisit if a flat-working-set workload appears. *That is naive→measure→iterate being allowed to say **no**.*

### Phase 2 — Consistent hashing
- ☑ **Why `hash % N` breaks on resize** — the divisor N is a single global baked into every key's placement, so
  changing N **re-rolls everyone**. Counted over one period of 12: going 4→3 nodes moves **9 of 12 keys ≈ 75%**,
  i.e. ~(N−1)/N, **not** 1/N. Every moved key is a miss → a **cache stampede** on the DB across the *whole*
  keyspace (no hot key needed). And patch-the-mapping "fixes" fail *worse*: placement becomes a function of the
  **ordered history** of changes, so two clients that learned the same failures in a different order disagree
  about where a key lives.
- ☑ **The ring + wraparound** — `ring/ring.go`. Hash nodes and keys into the same 32-bit space; a key belongs to
  the first node **clockwise**, wrapping past the top (sorted points + `sort.Search`). **Measured: removing 1 of
  10 nodes moved 9.2% of keys** (≈1/N) vs `hash%N`'s ~90%.
- ☑ **Virtual nodes / balance** — `defaultReplicas = 150`. Each physical node contributes many scattered points,
  so its load is the **sum of many small arcs** and concentrates on the mean. The naive ring measured lumpy (**65×
  span**, one node holding 2.45× its fair share); the sweep collapses it: 10 replicas → 3.8× span, 50 → 1.5×, 150
  → **1.4×**. Diminishing returns ~1/√replicas, then a plateau. **Second win, measured:** a dead node's keys
  spread across **all 9 survivors** (the busiest absorbs 19%) where the naive ring dumped **100% on one** — i.e.
  virtual nodes remove the **cascade seed**.
- ☑ **Hash choice — FNV-1a was a bad call, caught by measurement.** Its weak avalanche clustered `node0..node9`
  into a 4% sliver of the ring, so **one node owned 96%** of it. Switched to **SHA-256 truncated to 4 bytes**
  (crypto avalanche → uniform, so any truncation is uniform too). ⚠️ `maphash` is unusable for anything
  cross-process: it is **per-process seeded** on purpose.
- ☑ **Key ownership lookup** — `Ring.GetClockwiseN(key, n)` returns up to n **distinct physical** nodes: the
  primary plus the next n−1 distinct clockwise. **Distinctness is the whole point** — consecutive ring points are
  often the *same machine's* virtual nodes, and replicas sharing a machine **die together**.

**Phase 2 COMPLETE.** `hash%N` diagnosed → ring built → hash fixed (caught by measurement) → virtual nodes
(65×→1.4× span; failures spread across survivors) → R-way ownership lookup.

### Phase 3 — Replication
- ☑ **Storage node** — `node/node.go`. A cache behind an HTTP server (`GET/PUT /kv/{key}`, the *internal*
  endpoint one node calls on another). Binds `127.0.0.1:0` so the OS picks the port (read back via `ln.Addr()`);
  `Close` = `srv.Shutdown` then `cache.Close`.
- ☑ **A coordinating role, NOT a central coordinator** — every node holds its **own** ring + peer map and exposes
  client-facing `/get`+`/set` alongside the internal `/kv`. **Any node coordinates any key**: hash it, serve
  locally if it owns it, else forward (2s timeout, so a dead owner fails fast). A central coordinator would need
  consensus to be fault-tolerant, and consensus is CP — we are AP.
  - **The naive failure, demonstrated:** at **R=1**, killing a key's owner returns 502 from **every** survivor.
    Data gone, no copy to fall back to. **This earns replication.**
- ☑ **Replication factor R=3 + read fallback** — a write stores to all R owners and acks after `writeQuorum`
  succeed (**W=1** default — *a knob, not consensus*: W=1 favours availability, larger W trades latency for
  durability, and W>R is impossible). A read tries owners in ring order and returns the first reachable hit.
  - **THE MONEY MOMENT, tested under `-race`:** at R=3, reads survived **2 owner deaths** by falling down the
    replica list; the key was lost only when **all 3** owners were dead. **R copies tolerate R−1 failures.**

**Phase 3 core COMPLETE (naive on purpose).** Still synchronous (writes hit all owners in-band — no async, no
hinted handoff), and membership is **static**, so a dead node stays in every ring: the ring still *routes to
corpses* and every read pays a failed hop before falling back. That earns Phase 4.
- ☑ Consistency vs availability trade-off — built and **demonstrated E2E** on `cap-demo` (S18–S21):
  **versioned values** (`[]Entry` + vector clocks), **conflict-detecting reads** (`X-Conflict` + the sibling set),
  a **version-aware heal + cleanup** (a stranded concurrent sibling survives), the **coordinator picker** (`via`),
  **the cut** — a partition under the HTTP clients that lets both sides accept a write, the heal keep both, and a
  read surface the conflict — (S20) **`/state` reports the partition so the ring splits into two** (per-side rings,
  reload-faithful), and (S21) **the dial** (`W`/`R_read` with a held ring — refusals, checkerboard, live
  scorecard), and (S21) **read-repair-on-read** — a read now writes each surviving version back to any reachable
  owner that lacked it (`readRepair`), so a lagging-but-alive replica converges on the read. Browser-verified end
  to end. The heavier convergence cousins (hinted handoff, Merkle anti-entropy) stay unbuilt on purpose — see (b).

### Phase 4 — Failure detection
- ☑ **Heartbeats & timeouts** — a `/health` endpoint; every node pings every peer each `heartbeatInterval`
  (100ms), records `lastSeen`, and reconciles an `alive` view against `failureTimeout` (500ms). alive→dead flips
  `ring.Remove` (stop routing to the corpse); dead→alive flips `ring.Add`. **The ring now holds only the nodes
  this view believes alive**, so `peers` (all known) and the ring (alive) *diverge* — and **each node's view is
  its own. There is no consensus.** Measured: **death detected in ~600ms = the timeout + one beat**, concluded
  independently by each peer.
- ☑ **False positives (GC pause vs death)** — **the core impossibility: a crash, a slow node, and a dropped
  packet are all just *silence*.** The timeout is the only knob, and it points both ways at once: short = fast
  detection **and** false positives; long = fewer false positives **and** a ring that routes to a corpse for
  longer.
  - **Demonstrated** (`PauseHealth` + `TestSlowNodeIsFalselyDeclaredDead`): a node that stalls *only* `/health`
    while serving all other traffic is convicted by n0 after ~500ms — **yet still counts itself alive.**
    Asymmetric views: the split-brain seed. Un-stalling it makes n0 re-admit it — a needless eviction+recovery
    **flap**, the pure cost of guessing too eagerly. **The same 500ms timeout that catches a real death fast is
    shown here misfiring on a live node, and you cannot have both, because both are silence.**
- ☑ **Gossip / SWIM — taught, not built.** All-to-all is **O(N²)** (N=5 → 20 msgs/interval; N=1000 → 1M), and the
  HLD locks us to it precisely because at N=5 we never hit the wall. **Gossip:** a node learns of a death
  **second-hand** — it pings a few random peers and the fact spreads *transitively* (O(N) messages, converging in
  O(log N) rounds) instead of everyone pinging everyone. **SWIM** adds the two parts that would fix *our* false
  positive: **indirect probing** (ask k peers to probe the suspect before convicting, routing around one bad link)
  and **suspicion + incarnation numbers** (a "suspected" state the accused can **refute** — the voice our
  falsely-convicted node never had).

**Phase 4 COMPLETE.** The ring holds only what a view believes alive; each view is independent. Next: a detected
death should *trigger* re-replication — the other half of the money moment.

### Phase 5 — Self-heal
- ☑ **Re-replication to restore R.** A coalescing `healTrigger` (buffered-1 chan + non-blocking send) fires on any
  membership change; a separate `healLoop` goroutine runs `heal()` — kept **off** the heartbeat loop, because a
  slow copy stalling the pings would cause *more* false deaths. `cache.Snapshot()` enumerates live entries
  **without touching recency**: a bulk heal scan must not look like user access, or it re-creates the Phase-1
  sequential-scan pollution *inside our own cache*.
  - **Who heals, which keys, and why no election:** ownership is a **pure function of (ring, alive nodes)**, so
    promotion is automatic and needs no coordination. Each node scans **only the keys it already holds** — no node
    knows the global keyset, and none needs to.
  - **Measured:** killed the primary of a key at R=3 and the promoted newcomer received its copy in **~550ms**
    (detection ~500ms + heal). Two live copies healed back to three, **with no client involved.**
- ☑ **The re-replication storm, demonstrated** — the naive heal re-pushes *every* key it is primary of, to
  co-owners that **already have it**. A `PauseHealth` false positive (a node that is alive but looks silent)
  therefore makes every observer heal: **exactly `keys×(R−1)` = 200 copies for a node that never died.** Per-node
  breakdown: **the accused node pushes 0** — the storm is driven entirely by the observers, which is Phase 4's
  independent-views lesson resurfacing as a cost.
- ☑ **The grace period — decouple the two reactions to a death by COST and REVERSIBILITY.** Cheap + reversible
  (`ring.Remove` → re-route) fires **instantly on suspicion**. Expensive + irreversible (**copying data**) waits
  `healGracePeriod` and then **rechecks** — a suspect that recovered inside the window leaves nothing dead,
  so the heal is skipped entirely. **Measured: the same false positive that cost 200 copies now costs 0.**
  - **The price, honestly** — the universal detection tradeoff made concrete: at the **1s** grace these numbers
    were taken at, a **genuine** death heals in **~1.55s instead of ~550ms**. Extra under-replication exposure,
    bought with storm-immunity. *Convict cheaply on suspicion; copy only on conviction.*
  - ⚠️ **Two defaults, and the demo uses the slower one.** `node.defaultHealGracePeriod` is **1s**, but the
    server's `-grace` flag defaults to **2s** (`cmd/server/main.go`) and the cluster overrides every node with
    it — so the *deployed* demo waits 2s and a genuine death heals in ~2.55s, not the ~1.55s measured above. The
    node default only applies to a node nobody configured, i.e. the tests.
- ☑ **Check-first heal + recovery repopulation.** The heal now asks each owner whether it already holds a key
  (`fetchFrom` → 200/404) and copies **only what's missing**. That made the heal safe to trigger on **any**
  membership change (death *or* recovery): a flapped node still holds its data → **0 copies**, and a genuinely
  **revived node comes back empty → gets repopulated** with no client writes.
  - Side effect: the false-positive "storm" fell from ~200 copies to just the genuinely-needed newcomer copies
    (~49 for 100 keys). Grace still makes it **0**.
  - Also fixed a latent **data race** (caught by the new revive test): the cluster handed **one shared `peers`
    map** to every node and `SetMembership` **aliased** it, so `SetPeerAddr` on one node raced another's heartbeat
    read. Each node now `maps.Clone`s its own.
- ☑ **THE STRANDED-KEY BUG, and the heal's real invariant.** *"Only the **primary** of a key pushes it"* sounds
  like a clean de-duplication rule. It quietly requires one node to be **both the primary AND a holder** — and
  there is a case where **nobody is**:
  - A revived node comes back **empty**; the ring **promotes it straight back to primary** of its own arcs
    (automatic, which is exactly the property we celebrate elsewhere). So the **primary has nothing to send** — the
    key isn't in its `Snapshot()`, it never even considers it — and the **holders stand down**, because they aren't
    the primary. **Nobody is both, and the key stays under-replicated forever**, since no further membership change
    is coming to retrigger anything.
  - **Found live in the browser** (kill to 2 nodes, revive all three → **7 of 20 keys never recovered**; for some,
    *not one of the three owners held it* while two non-owners did). **This is the exact inverse of milestone-quiz
    Q1:** a primary that *dies* is fine, because the ring promotes a node that **already holds a copy**. A primary
    that ***returns*** is the killer — promoted while holding **nothing**. The model answer and the code shared the
    same blind spot.
  - **The fix — permission follows the DATA, not the ring position:** **the healer for a key is the first owner, in
    ring order, that actually holds it.** This keeps exactly what primary-only was *for* (one sender ⇒ no duplicate
    copies) **and** guarantees a sender **exists** whenever anybody has the data. Ranked below a holder → stand
    down; ranked above one, or holding a key **no owner has at all** (a leftover from an older ring) → step up.
    `TestReviveRestoresFullReplication` was **verified to fail** against the old rule and pass with the fix.
  - **Cost, honestly:** each holder makes up to one extra probe per key to decide whether to stand down ≈ **2× the
    heal's probe traffic**. And `fetchFrom` is a `GET`, so a *"do you have this?"* check **downloads the whole
    value** — see Next action (c).
- ☑ **THE HEAL RESURRECTED EXPIRING KEYS** — a bug living in the **seam** between two individually correct
  features. `Snapshot()` returned `map[string]string` — **the deadline discarded** — and `storeOn` PUT the copy
  with no expiry. A key with a 60s TTL whose primary died at t=50s was healed onto a fresh replica **as a permanent
  key**: at t=60s the originals expired correctly and **the healed copy served forever.** *The more reliably the
  cluster healed, the more thoroughly it preserved what should have died.*
  - **The principle: a deadline is absolute, decided ONCE, and carried.** A **duration** is relative to whoever
    holds it; an **instant** is not. The client sends a duration on the **first hop only**; the coordinator turns it
    into an instant and hands **that same instant** to every owner (so replicas cannot even disagree by clock skew);
    and **a heal copies the deadline the key already has** rather than minting a new one. Re-basing per hop would
    push the deadline out on **every heal** — *a frequently healed key would never die.*
  - `TestHealDoesNotResurrectAnExpiringKey` waits for the heal to place a copy **on a node that did not have one**
    before waiting out the deadline, and was **verified to fail** against the naive version. Confirmed live: after a
    kill the key's remaining life kept counting **down** (15.8s → 11.3s) on its new holder instead of resetting.
- ☑ **Serving reads during heal** — true via the Phase 3 read fallback (**available ≠ fully-replicated**): reads
  hop past the missing copy while the heal runs. Session 10 made it **visible**: a read returns
  `X-Coordinator` (who took it) and `X-Served-By` (who answered), plus `X-Read-Path`, a per-owner **trace** of
  what each owner said. There is no `X-Primary` — the primary is *rank 0 of the trace*, derived by the reader
  (`ReadResult.Primary`), so who-the-owners-were has one source of truth instead of two that can drift.
  `miss` (alive, holds no copy — a
  revived node mid-heal) is kept distinct from `unreachable` (dead): both mean "did not serve the read," only one
  means the node is **gone**.
- ☑ **The causal heal log.** Heals live in the **same event list as the kills**, not a log of their own — the
  question a viewer has is *"which kill caused which copies,"* and that is a question about **order**, so one list +
  one counter, appended as each thing happens, answers it with **no ordering logic anywhere**. Each heal carries the
  cause **its sender observed** (`because n4 saw n2 went silent`), *not* what the manager knows: a node heals because
  **its own heartbeat** stopped hearing a peer, and two nodes can **disagree** — a false positive is precisely one
  node seeing a death nobody else sees. The event cap was **kept** and raised 40 → 300: an append-only list anyone
  can grow forever by clicking Kill is **the Phase-1 logical leak in a new hat.**

### Phase 6 — Dashboard
- ☑ **Cluster-in-a-box manager** (`cluster/`) — the 5 nodes as goroutines in one process;
  Start/Kill/Revive/Pause/Set/Get/Seed, plus a god's-eye `State()` that **diffs intended owners (the alive ring)
  against actual holders (the node caches) — and that gap *is* the heal in flight.** Kill just `Close()`s a node, so
  peers must still detect the death **themselves** via heartbeat; Revive brings it back on a fresh port **without**
  resetting anyone's liveness.
- ☑ **Control API** (`cmd/server/`) — `go run ./cmd/server` → JSON on :8080
  (`/api/state|set|get|seed|kill|revive|pause|delete|clear` — `delete` and `clear` arrived in S11/S13),
  API-only with permissive CORS.
- ☑ **React frontend** (`frontend/` — React + Vite + TypeScript) — talks to the API (Vite proxies `/api` in dev;
  builds to static files, satisfying the HLD's "static FE + one backend container"). A `useClusterState` polling
  hook keeps the previous snapshot for animation diffing.
- ☑ **Ring viz + failure-injection controls** — a dark control-room SVG ring: per-node colours, virtual-point
  ticks at their **true hash angles** (the real load spread — though see the vnode note below: the demo ring
  carries 8 points per node, not the library's 150), node markers with heartbeat halos, key dots on their
  **true hash angles** with
  ownership links, a **red pulse on under-replicated keys**, **packets that fly primary→newcomer on
  re-replication**, kill/revive shockwaves, and a *"re-replicating N keys…"* indicator during the heal window.
  **Verified live in a real browser:** kill → grey-out → heal (0→24 copies, **0 data lost**); reads keep serving; a
  false positive shows the indicator while grace holds copies at **0**.
  - **Node markers are placed by even spacing, not `hash(id)`** — and that is the *honest* choice, not a cheat: a
    node has **many** scattered ring positions, so it **has no single true position**, and faking one (which
    clustered n0/n3/n4 at the bottom) would be faking a *value*. Keys and ticks keep their true hash angles.
    **Fake what has no true value; never fake the mechanism.**
  - ⚠️ **The demo ring is NOT the measured ring: 8 vnodes per node, not 150.** `ring.defaultReplicas = 150` is the
    library default and what Phase 2 measured (65× → 1.4× load span); `cluster.demoRingReplicas = 8` is what the
    server actually runs, because 150 × 5 = 750 ticks is hair, not a diagram — nobody can watch a key land in an
    arc that thin. **A legibility/balance trade, and it only costs the picture:** the mechanism is byte-identical,
    the tests keep the default, so the *claim* stays measured even though the *rendering* shows the coarse case.
    Worth stating out loud, because for four sessions three docs said the dashboard drew ~150 ticks. It never did.
- ☑ **TTL + read-path controls** — TTL presets and a custom-millisecond box with a live preview; the read card shows
  the value, who coordinated, who served it, and the full read-path trace; the key table shows each key's
  **remaining** life. The dashboard is sent a **remaining duration, not a deadline** — an instant would be read
  against the *browser's* clock, and a countdown that disagrees between two laptops gets blamed on the cache.
  - ⚠️ **`Number('')` is `0`, and the backend reads a TTL of 0 as "never expires."** An empty custom box would have
    silently written a **permanent** key for someone who explicitly asked for an expiring one. Now rejected, along
    with non-numeric, negative, and an explicit `0`.
  - ⚠️ `ttlText` ceilinged to whole seconds, so a 1500ms preview read *"dies in 2s"* — correct for a countdown, **a
    lie about a duration**, at exactly the scale someone reaches for milliseconds to control.
- ☑ **Write · SET and Read · GET are separate cards.** One card was answering two different questions and the seam
  showed: a shared error hook parked a failed write's complaint above an unrelated read result. Separate cards,
  separate error lines, and the read card takes no `onAction` — **a read changes nothing and has nothing to
  refresh.** The split left room to state the asymmetry out loud: **a write goes to ALL R owners; a read stops at the
  FIRST owner that answers.**
- ☑ **Runs on a phone.** ⚠️ The bug under the bug: `overflow-x: hidden` was **quietly clipping** a 390px overflow
  rather than fixing it. Cause: **a `1fr` grid track is really `minmax(auto, 1fr)`, and that `auto` floor is the
  item's min-content width** — so one unshrinkable child sizes the whole column. Plus: `touch-action: manipulation`
  (`none` swallowed the scroll swipe on the ring — the tallest thing on a phone — which reads as a *frozen page*),
  16px inputs (below that, iOS Safari zooms in on focus and stays), and 44px touch targets on Kill/Pause — **the
  buttons this entire demo turns on**.
- ☑ **Structured logging** (`logging/`) — console **text** for a human watching the demo, **JSON on disk** for `jq`
  afterwards, fanned out at the `slog.Handler` level. `cluster` and `node` **discard by default** and accept a logger
  via `SetLogger`: a library that logs on its own terms is one you **cannot silence**, and heartbeats at 100ms would
  spray through every `go test`.

**Phase 6 COMPLETE. Both halves of the money moment are visible and interactive:** kill → reroute *and*
re-replicate.

---

## Session log
What happened, in order — the narrative and the surprises. The detail lives in the checklist above.

### Session 22 — 2026-07-21 · Notifications moved from *visits* to *faults*
**Build only, no quiz.** The ntfy push now fires on **kill a node** and **cut the network** only — the demo's
money moments — and nothing else. `visits.go`/`visits_test.go` deleted; `faults.go`/`faults_test.go` replace them.

**The design point, and it is the whole change: this stopped being a middleware.** A middleware sees the method
and the path, which was *enough* for a visit (the visit **is** the request) and is not enough for a fault. Two
things only the handler knows: **which** node — the id is in the JSON body, so a middleware would have to buffer
and replay `r.Body` to read it — and whether the fault **actually happened**. ⚠️ `kill n7` on a five-node cluster
is a `400`, and a push claiming "n7 killed" for a request the cluster refused is *worse* than no push: it is an
assertion the reader cannot check. So the handlers call `fx.killed` / `fx.cut` after their error check, success
path only. Revive passes `nil` — the fix is not the fault. Detail → HLD §8.6.

Two guards died with the visit framing and one survived. Dedup and the idle window were both answers to *"a poll
storm is not a visit"*; a button press is already a discrete event, so there is nothing to collapse. The **hourly
cap survives, and is the only one that was ever about the outside world**: the API is public and a kill is one
unauthenticated `POST`, so a script holding the button down is a DoS on a phone. 20/hr → 30/hr.

**Testability forced a real design change.** The handlers close over the `*faults`, so a test has no way in — the
first draft of the test needed a `faultsOf(t, h)` helper that cannot exist. The fix was to stop `routes()` reading
`$NTFY_TOPIC` itself and take the `Notifier` as a **parameter**, with `notify.FromEnv()` hoisted to `main()`.
⚠️ *A constructor that reads the environment is a constructor whose behaviour you cannot choose.* → GO_NOTES.
The tests now drive real HTTP through real `routes()` with a channel-backed `Notifier`, and the load-bearing one
is `TestAKillThatFailsDoesNotPush` — the case a middleware could not have passed.

### Session 21 — 2026-07-19 · 7B — the consistency dial (W / R_read), with a held ring
**Teach → quiz (3/3) → build, then browser-verified.** A background Plan agent produced the full 7B spec; the
build followed its commit order. Taught the quorum from first principles (stale-read problem → `R_read+W>R`
overlap → the *pair* not either number), quick-check **3/3** — he nailed Q3 cold (overlap ≠ ordering: quorums
make a reader *see* a recent write, they do not serialize concurrent writes, so two concurrent writes still
become siblings). Then part 2 (refusal = the CP end; the checkerboard; and the trap) before building.

- **The non-obvious core: the ring must stop shrinking, or a quorum is a rubber stamp.** A quorum is a fraction
  and needs a fixed denominator. Under a cut, each side normally convicts the far side and `ring.Remove`s it
  (Phase 4) — re-owning the keyspace among its survivors, so `W=2` is met by an *invented* owner set. `node`
  gained **`holdRing`**, which gates `ring.Remove`/`Add` in `heartbeatRound`: at the strong setting the far owners
  stay in the ring, so a coordinator that can't reach `W` of them **refuses** instead of rubber-stamping. Same
  `ring.Remove` line, *correct for AP, fatal for CP*. This is the connection to S20: the two-ring split we drew is
  exactly the shrinking the CP dial must prevent.
- **Enforcement.** `handleClientGet` refuses **503** when fewer than `R_read` owners answer — a reachable-count
  threshold on the existing gather-all (NOT early-stop, which would break 7A conflict detection); `R_read=1` is
  byte-for-byte the old read. `handleClientSet`'s quorum miss is now **503** (was 502). `writeQuorum` read moved
  under the lock, since the dial mutates it at runtime.
- **Wiring.** `cluster.SetQuorum(w, rRead)` validates `1≤w,rRead≤rf`, sets W/R_read on every node and
  `holdRing = w+rRead>rf`, inherited by `wireAll`/`Revive`. `State` reports `W`/`RRead`. `POST /quorum`.
- **Frontend.** `DialPanel` (ONE/QUORUM presets + W and R_read segmented selectors + a live `W+R_read {>,≤} R`
  readout that flips to a held/cyan state), a **Stats dial tile**, and a **Scorecard** — one probe row per dial, so
  flipping and re-probing stacks the controlled experiment (a conflict counted as *both sides accepted*, no mend
  needed). **Verified live: QUORUM → 5 accepted / 5 refused / 0 conflicts (checkerboard); ONE → 10 / 0 / 5
  (divergence).** The refused writes and the conflicts are the same writes — the CAP dial, measured.
- **Tests** (all `-race`): node `TestReadRefusedBelowReadQuorum`, `TestWriteRefusedBelowWriteQuorum`; cluster
  `TestSetQuorumValidation`, `TestHeldRingRefusesLosingSideOfCut`, and the money contrast
  `TestUnheldRingLetsLosingSideServeAfterShrink` (proves holdRing is load-bearing: unheld, the losing side
  re-owns and serves the very write the held ring refused). Full tree green under `-race`. Still on `cap-demo`.
- **Layout, this session too** (his asks): key table shows per-side owners under a cut (roomier two-row cards);
  Write/Read moved to a side-by-side row in the left column; the partition renders as two independent rings.
- **Split the dial onto its own tab** (his ask — the dial was cluttering the partition demo). Three tabs now,
  each its **own cluster** (`demoClusters = replication, cap, consistency`): **Replication & Self-Heal** (death →
  heal), **Partitions & Conflicts** (the cut → concurrent writes → siblings, at the eventual dial), and
  **Consistency Dial** (the same cut, but tune `W`/`R_read` → refusals / checkerboard / scorecard). The dial
  demo needs its own cluster for the same reason inverted from S17's: it sets `W+R_read>R` so the cut *refuses*
  rather than diverges — the opposite of the partitions tab's siblings story — so the two can't share a dial.
  Both cut-capable tabs get `PartitionPanel`; only the dial tab gets `DialPanel`/`Scorecard`/the dial Stats tile.
- **Activity-log narration for the new system** (his ask — the ordered event log was a standout deliverable, so
  the CAP mechanisms deserved it too). New manager-appended events: **`refuse`** (a `503` — the dial's CP "no",
  on a write or read; only the 503, not a 502-all-unreachable) and **`conflict`** (a read surfaced concurrent
  siblings, values inline). New node-drained event: **`repair`** (a read-repair convergence — `node.repairLog` →
  `DrainRepairLog` → `State` appends, grouped by the caught-up owner, mirroring the heal/cleanup drain). Enhanced
  **`set`** to name the coordinator (`via`). Frontend: tag colours for `cut`/`mend`/`refuse`/`conflict`/`repair`
  (refuse=red, conflict=amber, repair/mend=green). Browser-verified end to end: the log narrated the checkerboard
  (writes via n3 accepted, via n0 refused), the dial changes, the divergence, four conflict reads, and the
  read-repair catch-up — all in causal order in the one shared list.

### Session 20 — 2026-07-19 · `/state` reports the partition — the ring genuinely splits
**Build + browser verification, on `cap-demo`.** Closed the S19 follow-up: the cut was tracked client-side, so a
reload lost the banner while the backend cut stayed live. Now the manager reports it and the ring splits.

- **The concept, first (the honesty question).** *Who is allowed to know there's a partition?* No **node** can —
  each only knows "I can't reach peer X," and can't tell a cut from a crash (Phase-4's silence). But the
  **manager** injected the cut, so it may report the sides the same way it reports a Kill: out-of-band control-
  plane knowledge, honest as a readout, dishonest only if we pretended a node computed it. Aayush's own framing
  sharpened it: consistent hashing's **minimal-movement** survives a partition (each side only re-owns the far
  side's arcs), but **global agreement on ownership** was never a ring property — it held only *because
  membership views agreed* (his S18 answer). A cut breaks that precondition by construction, so **there is no
  longer one ring — there are two, and they disagree.** The current single god's-eye ring is the *fiction*; the
  split render is the honest one. And the shrink is **mandatory**, not cosmetic: a side that kept the far nodes
  in its ring would route to unreachable owners and *refuse* — that's CP; AP requires each side to shrink,
  re-own, and keep serving.
- **Backend (`cluster/`).** `Cluster` records the active cut (`cutA/cutB`); `State()` returns
  `Partition{sideA,sideB,vnodesA,vnodesB}` + per-key `OwnersA`/`OwnersB` from two per-side rings (side A =
  alive − B, side B = alive − A; a bridge node lands in both). **Under-replication judged per side** — a key
  complete for its side no longer paints under-replicated for the whole cut. Helpers `ringOver`/`vnodesOf`/
  `setOf`. New test `TestStateReportsPartitionWithPerSideOwnership` (no polling: per-side owners come from the
  manager's ring math over the stored sides, correct the instant `Cut` returns).
- **Frontend.** Deleted the client-only `cut` state; banner + panel + rings now read `state.partition`, so a
  reload is faithful. **A cut renders as two independent `RingViz` side by side** — this replaced a first pass
  that overlaid the two per-side rings as concentric bands in ONE ring (built, then dropped same session on
  Aayush's call: *a partition splits an AP system into two clusters, so draw two clusters* — the overlap read as
  confusing, the split reads as the reality). `App.sideState` slices the god's-eye snapshot into a self-contained
  per-side `State` (that side's nodes, its ring points, `ownersA`/`ownersB`, holders filtered to the side, and
  `aliveCount` = the side's own count — from A's view the far side is simply dead), and a now partition-agnostic
  `RingViz` draws each with a `sideLabel`. The **Key-ownership table** was the same fiction one layer down — it
  rendered the single-ring `owners`; now under a cut it shows both sides (`A …owners  B …owners`) so the tables
  and rings can't drift. (Layout: Write/Read moved to a side-by-side row in the left column under the ring; the
  right column is Failure Injection · Partition · Activity Log.)
- **Browser-verified.** Cut `{n0,n2,n4}|{n1,n3}` → banner + split ring (gold/purple outer = B, cyan/pink/green
  inner = A) + two-tone keys (key:2 half-purple/half-pink = n1 on B, n2 on A). **Full reload → CAP tab → all
  reconstructed from `/state`** (the bug, fixed). Mend → single ring back. `NODES ALIVE` stayed 5/5 throughout.
  Honest artifact under a live cut: **COPIES 60/36** (gold) — each side re-replicates among itself, so total
  copies exceed the single-ring's intended 36; converges after mend+heal+cleanup.
- **Process.** `gofmt`/`vet`/`tsc`/`vite build` clean; all Go packages green under `-race` **except** the known
  pre-existing `TestDeleteFindsCopiesTheRingNoLongerNames` load-flake (item e; 5/5 in isolation, untouched).
  Separately, a background **Plan agent** produced the full **7B "dial"** implementation spec (teach-then-build,
  not code) — held for the next session; key call: the strong dial setting must **hold the ring fixed**
  (`node/`, gate `ring.Remove`/`Add`), and read enforcement is gather-all + a reachable-count threshold (not
  early-stop, which would break 7A conflict detection). Still on `cap-demo`, merged to main by hand.

### Session 19 — 2026-07-18 · 7A COMPLETE — conflict-aware reads, version-aware heal/cleanup, the coordinator picker, the cut; verified E2E
**Build + browser verification, seven commits on `cap-demo`** (no quiz — he asked to skip). This closes the
**entire 7A build arc**, verified end-to-end in the browser. Heavy use of parallel background agents (FE conflict
card, coordinator-picker BE+FE, the cut BE, cut UI, the docs) while the core algorithm work happened in the
main thread. The first three increments share one through-line: **presence≠version, applied to each mechanism's
own decision** — the read's which-owner-answered, the heal's who-is-the-healer, the cleanup's is-this-surplus.
All three carried the same blindness; all three are closed here.

- **Reads detect conflicts** (`42d1335`). `handleClientGet` no longer stops at the first hit — it **gathers every
  reachable owner's versions** and folds them with a new exported `cache.MergeVersions` (wrapping the internal
  `reconcile`, so the invariant has one home). One survivor ⇒ a plain value (the unchanged client contract);
  two+ ⇒ concurrent siblings, returned as a JSON array under a new `X-Conflict: n` header — **none dominates, so
  picking one would destroy an acked write.** `servedBy` now names the first owner holding a **surviving**
  version, not merely the first that answered — presence≠version, at the header level. Wired end to end:
  `cluster.ReadResult` gained `Conflict`/`Siblings`, `cluster.Get` parses `X-Conflict`, `/get` emits both, and
  the frontend conflict card renders off the same two fields.
  - ⚠️ **Behaviour change, not a regression:** a conflict-aware read *cannot stop early*, so a healthy read now
    hits **every** owner instead of hitting the primary and marking replicas "skipped." `TestReadPathNamesEvery`
    `OwnerAndWhatItSaid` was updated to gather-all semantics. "skipped" returns in **7B**, when `R_read < R` asks
    only the first `R_read` owners. New test `TestReadDetectsConcurrentSiblings`.
- **Heal + cleanup go version-aware** (`814df5d`). Same presence≠version blindness, closed in both:
  - **Heal:** was `Snapshot()` (one version per key) + a has-the-key probe, so a `bob` on one owner and a
    concurrent `carol` on another each looked "already present" to the other's healer — **neither sibling ever
    replicated.** Now `SnapshotAll()` sees every local version and the healer is chosen **per version**: I heal
    `v` only if no owner ranked ahead of me *covers* it (holds `v` or a dominator). `bob`'s healer and `carol`'s
    are different owners ⇒ both propagate; a stale local version a dominator covers is stood down and replaced.
  - **Cleanup:** dropped a non-owned copy once every owner answered "has the key" (presence) — which would
    **discard a sibling no owner holds** (a write a down owner missed, or one side of a cut), losing an acked
    write. Now it drops only once every owner **covers every version** the copy carries; an uncovered version is
    a stranded sibling — kept, with the heal re-armed to propagate it.
  - New shared helpers `covered` (holds-it-or-a-dominator) and `coveredAhead` (an owner ranked ahead covers it).
    New tests `TestHealPropagatesStrandedSiblings`, `TestHealReplacesDominatedVersion`, and cleanup's "keeps a
    concurrent sibling no owner holds."
- **The coordinator picker** (`6f088fb`). `Set`/`Get` gained a `via` naming the coordinating node — `""` is the
  old any-live behaviour byte-for-byte; a dead/unknown `via` is refused with a new exported `*NoSuchNodeError`
  (→ 400), **never rerouted**, because determinism is the whole point of the partition demo (drive a write
  through one side and another through the other). Frontend: a shared `CoordinatorSelect` on both cards, fed by
  the existing state poll, that collapses a stale pick back to auto if the chosen node dies before submit.
- **The cut** (`4ca295c`), then **simplified to one `gate`** (`90126eb`, 116 → 45 lines). A partition is a fact
  about a **pair**, not a node, so it lives **under** the HTTP clients: each node's `gate` is a mutex-guarded set
  of blocked peer addresses that *is* the `RoundTripper`, shared by the data **and** health clients, so one cut
  drops both — which is what makes each side convict the other and shrink its ring. Gated at `RoundTrip`, not
  `DialContext`, so a keep-alive connection pooled before the cut is refused too. `Cluster.Cut(sideA, sideB)`
  validates disjoint, all-live sides up front (all-or-nothing) and `Mend` clears every block set. The
  simplification restored the pre-cut behaviour: both clients originally shared `http.DefaultTransport`, so
  "one shared gate" *removed* a per-client-pool layer the first draft had added for no reason. `covered`/
  `coveredAhead` from the heal are the exact primitives the mend's reconciliation leans on.
  - ⚠️ **A partition is invisible to the manager.** It kills no one, so `State().AliveCount` stays 5/5 under a
    cut — only a per-node read trace sees a side's ring shrink. The demo test polls that trace instead of
    sleeping. This is S9's "no god's-eye view" landing as a *code* constraint, not just a slogan.
- **The cut UI + conflict card wiring** (FE, `1f10793` and earlier). A `PartitionPanel` (per-node A|B toggles,
  "split evenly"), a pulsing "NETWORK PARTITIONED" banner, and ring A/B tags; the conflict card renders the
  sibling set off `X-Conflict` + the array. ⚠️ `/state` reports no partition, so the active cut is tracked
  **client-side** and a reload loses the banner (the backend cut stays live). Flagged as the next follow-up —
  partition state in `/state`, which also unlocks per-key two-colour conflict markers.
- **Verified the whole money moment E2E in a real browser.** On the CAP tab: cut `{n0,n2,n4} | {n1,n3}` (all
  nodes jumped to 12 keys as each side re-replicated alone), wrote `cart=milk,eggs` via **n0** and
  `cart=milk,bread` via **n3** — **both accepted** — then mended. The activity log narrated the reconcile
  (`MEND` → `HEAL n0→n1`, `n1→n0`, `n1→n2` → `CLEANUP` dropping surplus, not the siblings), and a gather-all
  read returned the **conflict card with both real values**. Every layer this session built fired in sequence.
  - ⚠️ **First take failed on a self-inflicted TTL:** the writes used a 2-min TTL and `cart` **expired** while I
    fumbled the native `<select>`; the sweeper reclaimed it before the read. The heal logs proved it had
    reconciled correctly regardless. Redone with `never` — clean. Lesson banked: a demo key gets `never`.
- **Process:** all seven commits on `cap-demo`, still merged to main by hand (the S17 auto-deploy lesson).
  `partition.go` was reviewed line-by-line and then simplified at his request — the first-draft `blocker` +
  `blockingTransport` + `partitionedError` + constructor + assert collapsed to one `gate` with no behaviour
  change (full tree green under `-race`, cut test 3× clean).
- **Reframed the whole system (his framing, sharpened together).** It is a **Dynamo-style leaderless AP cache**,
  and the two tabs are **two failure modes**: node death → *staleness* (heal fixes it, one truth to copy) vs
  partition → *divergence* (two truths, vector clocks detect + siblings surfaced à la Dynamo). Two points he
  drove and we specified: **divergence's sole cause is concurrent writes** (a partition *forces* concurrency but
  a healthy-network race can too), and **linearizability would delete conflict resolution** — not by forbidding
  concurrent *issue* but by *serializing* at write time, so the conflict reappears as a *refused write* under
  partition. Hence **AP detects and defers; CP serializes and prevents**, and the `W`/`R_read` dial is *tunable
  consistency spending availability*, never linearizable. Written into **HLD §0 "The system, in one frame"**
  (the canonical statement), the two dashboard tab blurbs (failure-mode framing) + a top-line identity, and two
  now-stale HLD table rows corrected to **⇒ AS BUILT (7A)** (vector clocks, and the cut). ⚠️ `CAP.md`'s deeper
  S17-flagged rewrite (§9 Lamport → vclocks, §10–14) is still open — the framing lives in HLD §0 meanwhile.
- **Framing, sharpened further, then `CAP.md` brought to AS-BUILT.** Two forks specified in HLD §0: (a)
  **siblings vs LWW** — we are the **Dynamo/Riak** branch (never silently lose an acked write), not Cassandra's
  LWW; this axis is *independent* of the dial (we borrow the dial from Cassandra, the resolution from Dynamo).
  (b) **The dial is delegation, not "more consistency"** — it hands the *"how fresh must this read be?"* choice
  to the caller; **vector clocks are architecturally forced, the dial is optional.** ⚠️ His catch: **a request
  carries W *or* R_read, never both** (read XOR write), and no-stale-reads is the *pair* `R_read+W>R` (a strong
  writer AND reader) — a `R_read=2` read over `W=1` writes is 3, not >3, still stale. And **our dial is
  cluster-wide, not per-request** (Dynamo's ideal); noted as the demo's simplification. `CAP.md` marked
  AS-BUILT throughout (§1 gate, §9 presence≠version closed, §11 gather-all + dial-is-7B, §12 7A done). The CAP
  tab blurb went through several passes and finally **dropped the dial entirely** — it belongs in a dedicated
  **scorecard section** (a "same requests, two dial settings" controlled experiment), built with 7B.
- **Two partition-fidelity UI fixes** (`RingViz.tsx`). (1) **No heal packet crosses an active cut** (`ee389a2`)
  — the packet animation picked its sender from the manager's *partition-blind* owner list, so a within-side
  copy (n4→n3) was drawn from an owner on the far side; now the sender must be on the holder's side of the cut
  (`canReach`), else nothing is drawn. Browser-verified across 3 cut shapes incl. an **unassigned bridge node**.
  A clean instance of "no god's-eye view": the animation faithfully rendered a view that cannot see the
  partition. (2) **Packets sequence by side** (`f1465a2`) — one side's whole burst plays before the other's,
  since two at once read as chaos. ⚠️ **Open for tomorrow:** the sequenced pacing is ~8s for a big two-sided
  heal (PACKET_MS 1900 × two sides) — decide whether to keep or snappier-tune (overlap tails / faster packets
  under a cut). Also open: whether to suppress cross-partition **ownership links** in the frontend now vs the
  proper fix — **`/state` reports the partition** so the ring can genuinely split (the flagged backend follow-up,
  which also unlocks two-colour conflict keys).
- Full tree green under `-race` (bar the known intermittent `cluster` delete flake, item (e)). Still on
  `cap-demo`, merged to main by hand. **Remaining in the 7A arc:** *the cut* (fault injector, next headline) and
  the *coordinator picker* (`via=n0`, now in progress via background agents); the conflict card is done + wired.

### Session 18 — 2026-07-17 · 7A begins — versions built and flowing, on a branch
**Cold quiz + teach + build, three commits on `cap-demo`.** Opened by confirming the live demo was fixed
(Vercel↔GitHub reconnected — verified `/api/replication/state` serves 200 live, not on faith). Then 7A.

- **Carried #1 CLOSED.** Re-asked the presence≠version **read half** cold, before writing any 7A code (the
  point: that code closes the gap, so answering after proves nothing). He nailed it — **ring geometry decides,
  not the coordinator**, stable across nodes *because membership views agree*. He volunteered the precondition
  unprompted, which is the exact seam 7A pries open. The S17 gap is gone.
- **Taught vector clocks from scratch** (shape · merge-then-bump · dominance · concurrency). Quick-check landed
  3/3 with one real correction: on a resolution he reached for "just bump my own slot" — the trap. Shown that
  bump-only leaves the loser **concurrent forever** (it doesn't dominate the sibling it didn't merge). He then
  derived the storage consequence himself: for an owner to hold both `bob` and `carol`, a key must become a
  **set**, not one value. Arrived at `CAP.md` §11's "read is now `[]Entry`" from the data side.
- **Built, in three tested + committed increments** (full tree green under `-race` at each):
  - `vclock/` — the primitive. `Bump` (renamed from `Next` at his request — "merge, then bump").
  - versioned `cache` — `[]Entry` per key, `reconcile` by dominance, additive so Phase-1 stayed untouched.
    ⚠️ `reconcile`'s **Equal** case must take the *incoming* value (LWW for an identical clock) — `TestOverwrite`
    caught the first version dropping a nil-version overwrite.
  - `node` write path — `/kv` speaks versions (JSON + `X-Version`), `handleClientSet` does read-before-write
    (`currentVersion` merge → `Bump(self)`). Read/heal still presence-based; that's next.
- **Process decisions (his calls):** build 7A on a **branch** and merge to main by hand — the direct lesson
  from S17's auto-deploy break; **reuse `Entry`/`[]Entry`**, no new "sibling" vocabulary; and **leaner comments**
  than the old docs pushed (recorded as a preference). Scope kept to `cache`+`node`+`vclock` — no `cluster`,
  `cmd`, or frontend touched yet.

### Session 17 — 2026-07-16 · Phase 7 reversed twice, 7.0 shipped, and the demo it took down
**Design + build + cold quiz.** Two reversals, both driven by Aayush pushing back on my recommendation, and
both of which the docs already half-argued for against themselves:

- **Vector clocks, not Lamport** (`CAP.md` §9 rewritten). His argument: the two writes either side of a cut
  *are* concurrent (§4 says so — no "later" exists), so Lamport doesn't resolve the clash, it **invents a
  fact** and destroys an acked write on the strength of it. I argued against it — the ghost was the demo's
  best beat — and I was wrong: I claimed siblings leave eventual with no real cost, when siblings **are** the
  cost, and a heavy one. The lesson that survived: **detection is the ceiling of any clock; only
  serialization is above it. Raft doesn't resolve collisions better, it prevents them.**
- **Fold the naive-AP demo away; the dial is two numbers, not five** (§11). One rule settles both: *a dial
  should only offer settings a real operator would pick.* "No versions at all" and "quorum on a shrinking
  ring" are bugs, not consistency levels. Cassandra's real dial is `ONE` vs `QUORUM` — exactly what's left.
- **"CP" retired as a label.** §13 always said the strong end is Cassandra's `QUORUM`, not CP; six other
  sections ignored it. The caveat was quarantined while the rest of the doc overstated — same pattern as S16.

**Built 7.0** — two demo clusters in one process behind `/api/{cluster}/`, one tab each. `cluster/` needed
**zero changes**, and the reason is the interesting part: nodes already bind `127.0.0.1:0` (the OS assigns
ports, so two clusters cannot collide) and there is **no package-level mutable state** in `cluster/`, `node/`
or `cache/`. **Isolation was structural before we asked for it.** Proven with a throwaway `-race` probe
before writing a line, then browser-verified.

**Two silent regressions caught by reaching for a literal path.** `noisyPaths` and the visit notifier both
compared `r.URL.Path == "/api/state"`. Adding a segment made both match **nothing**: every poll would have
logged at Info (burying each kill under thousands) and visit notifications would have **stopped firing
outright**. Nothing errors, no test fails. Then my *first* fix (`HasPrefix`+`HasSuffix`) also matched the
now-dead `/api/state`, so a curl to a 404 would push a visit — caught by the test I wrote for it. **Match the
shape, never a literal.**

**⚠️ The one that matters: 7.0 took the live demo down, and the docs predicted it wrongly.** I removed
`/api/state` reasoning *"the dashboard is the only client and they ship together."* **They don't.** Render
auto-deployed the new backend; **Vercel has not built since 2026-07-11** (7 commits back — and the giveaway is
that its last build was a *docs-only* commit, so it wasn't path-filtering, it just stopped). So: live frontend
calls `/api/state`, live backend 404s it, dashboard shows *"waking the cluster…"* forever. **Both deploys
green, feature dead** — HLD §8.5's own lesson, collected. A manual redeploy from the Vercel UI produced **zero
new deployments**, consistent with the Git link being broken (`live: false`, no `link` field on the project).
**RESOLVED 2026-07-17: Vercel↔GitHub reconnected, live demo works again** — verified against
`/api/replication/state` (HTTP 200, 5 alive nodes, keys with owners/holders). The design question it raised
still stands: two independently-deployed halves mean a breaking API change needs either lockstep or a
compatibility window, and we have neither.

**Cold quiz before 7A** (4 Q + 2 follow-ups; **0 ✅ · 3 ⚠️ · 1 ⊘**, follow-ups **1 ✅ · 1 ⚠️** — full text in
`QUIZZES.md`). The S9 pattern has *shifted*: he now reaches for the principle unprompted, but **answers the
sub-question he finds most interesting and drops the rest** — Q2 and Q3 each asked three things and got one.
A completeness habit, not a knowledge gap.
- **Presence ≠ version narrowed, not closed.** Heal half ✅. Read half ❌ — he thinks the value depends on the
  **coordinator**; it depends on the **ring**, which is worse, because ring geometry makes it **stable**.
- **He beat a model answer.** On the cost of `W`, §14 said "write fault-tolerance", which makes `W=1` sound
  free. He spotted that the surviving `W=1` write lives on **one node**: *"high chance that the write can be
  lost since other 2 are down."* → **`W` is how much a `204` is worth** — availability vs **durability**, paid
  on the healthy path. Folded into §14, credited.

### Session 16 — 2026-07-14 · CAP made teachable, and the doc that overstated itself
**Teach + doc, no build, no formal quiz.** A deep pass on the Phase 7 material — partition, concurrent writes,
versions, the AP↔CP dial — driven by Aayush's questions, then written down. Three through-lines landed:
- **"Concurrent" means no information flowed** (happened-before), *not* "same wall-clock instant." Two writes an
  hour apart are concurrent if no knowledge passed between them.
- **A version does two jobs** — resolve *staleness* (CP; a real "later" exists, causally ordered) and cope with
  *divergence* (AP; no "later" exists). Kicked off by Aayush's catch: *"if quorums prevent conflicts, why do we
  need versions?"* The fix — a quorum makes the latest write **reachable**; only a version says **which reply it
  is**. Overlap ≠ identification; the quorum handles divergence, not staleness (replicas lag by design at `W<R`).
- **Lost updates survive the quorum** — *overlap ≠ serialize.* Two concurrent writes both reach `W=2` on a
  **healthy** network and one is silently dropped; even `W=3` doesn't fix it (a serialization problem, not a
  quorum-size one — only consensus does). And CP's *refused* writes are AP's *divergent/lost* writes — the same
  conflicts, moved from **silent to loud**.

**The doc overstated itself; a critical pass caught six claims.** Rewrote `CAP.md` lean (466 → ~230 lines, one
example per idea), then reviewed it adversarially. Worst was an internal contradiction: §9 said Lamport *"only
ever arbitrates staleness"* while §10 says CP still loses genuinely concurrent writes. Also fixed *"exactly one
side under any cut"* (only true for a 2-way cut), *"zero lost writes"* (scoped to the partition scenario), and
*"only because W=1"* (the shrinking ring is the co-cause). Then Aayush flagged *"quorums prevent conflicts"* as
**itself** the confusing bit → removed it everywhere; the divergence/staleness split carries the point without it.

**New: `docs/CAP_DEMO.md`** — the visual/UI demo spec. What a stranger clicks and what the ring reflects (the
tear, two-colored keys, red-padlock refusals on nodes that *hold the data*, the checkerboard, the lost-update
ghost, the live scorecard), plus the scorecard with a full 5-key worked example (**AP** 100% accepted / 5
divergent · **CP** 50% accepted / 0 divergent / 5 refused — the same five writes, refused instead of corrupted).
Both docs committed (`19839e9`).

**Debt:** `presence ≠ version` (carried #1) was taught hard here but the teaching was **doc-driven, not quizzed**
— still deserves a cold re-ask. The `CAP.md` §12 quick-checks were reasoned through in conversation but not graded.

### Session 15 — 2026-07-11 · The deploy the notify commit broke
**Fix only.** Render failed the build: `no required module provides package .../notify`. Green locally, red in the
container — the Dockerfile copied packages **one `COPY` line per package**, and the new one had no line. An
enumerated list is a second source of truth about what packages exist, and **production is the only place it gets
checked.** Fixed the class, not the instance: `COPY . .` + a `.dockerignore`.

The same commit had a second, quieter bug — one the Dockerfile's own comment had predicted: *"scratch has no CA
certificates… fine only because the process makes no outbound TLS calls."* `notify` POSTs to `https://ntfy.sh`.
Verifying a certificate means knowing who you trust, and that list is **just a file** — without it every push dies
`x509: certificate signed by unknown authority` while the deploy goes **green** and the health check passes. Now
`COPY --from=build /etc/ssl/certs/ca-certificates.crt`. ⚠️ **A passing health check does not mean a working
feature** — nothing on the request path touched TLS, so nothing on the request path could fail.

Then the comment pass the other packages already had (`notify/`, `visits.go`, the deploy files): narration out,
contracts and ⚠️ traps in. Go files land at 16–21%, the band the rest of the project sits in.

Last, **the push now names the visitor's IP** rather than only a hash of it. Worth being clear-eyed about what that
trades: an ntfy topic has no password, so the topic *name* is now what guards visitor IPs, not merely the fact that
somebody showed up — a guessable topic went from an annoyance to a privacy leak. The `sha256` stays as the **dedup
key** (it is only ever compared, never read); it was never privacy *against a topic holder* anyway, since an IPv4
address is brute-forced from its hash in seconds. **The security of the whole thing is one unguessable string.**

⚠️ **The `cluster` flake, measured rather than shrugged at:** **2 failures in 4** full-suite `-race` runs this
session, and **0 in 4** when `./cluster/` runs alone (also clean alongside `cache`+`node`). Fails under a *full*
load, passes in isolation — the signature of a **test leaning on a timing budget it does not own**, not of a broken
cluster. Still unpinned, and now the top code item.

### Session 14 — 2026-07-11 · Visit notifications, and the interface under them
**Build only, no quiz.** An ntfy push when somebody opens the live demo. Not a distsys feature, but three of its
traps are shapes the cache already taught. **The real problem: a visit is not a request** — the dashboard polls
every 600 ms, so push-per-request is ~1.7 pushes a second per open tab. Guards: dedup on `sha256(IP+UA)`, an **idle** (not
fixed) 30-min window, ≤20 pushes/hour. `notify.Notifier` is one method wide and `Nop` is the unconfigured default,
so no call site carries a nil check. Design detail → HLD §8.6; Go traps (the request dying at handler return, its
context cancelled at first write) → GO_NOTES.

Tests drive a real `httptest` ntfy server; `visits_test.go` takes the clock as a parameter, so the 30-min and 1-hour
windows are exercised in nanoseconds — *a test that waits 30 real minutes is a test nobody runs.*

⚠️ **A pre-existing flake, seen but not caused here:** `TestDeleteFindsCopiesTheRingNoLongerNames` failed once under
full-suite load, then passed 8/8 on re-run. Timing-sensitive; worth pinning down.

### Session 13 — 2026-07-11 · Cleanup: heal was a ratchet
**Build only, no quiz.** Aayush's question — *"when the killed node is restored, is the copy the other node gained
deleted?"* — and the answer was **no. Heal only ever COPIES.** Measured on 6 keys: one kill+revive went **18 copies →
22**, R creeping toward N. Built `cleanup` (Cassandra's `nodetool cleanup`; Dynamo avoids the problem with *hinted
handoff*, where the temporary copy is a hint deleted on handback). Design → HLD §7.

**Two things the tests taught, both by failing:**
1. **A cluster-level safety test PASSED against a deliberately broken (drop-without-asking) cleanup.** It was passing
   for the wrong reason: below R=3 live nodes *every* survivor owns *every* key, so cleanup returns at the ownership
   check and the confirm path never runs. Renamed to what it actually proves (`TestShrinkingClusterKeepsEveryKey`) and
   rewritten properly at node level. **A test that cannot fail is not a test.**
2. **Cleanup left one copy stranded anyway** — a node cleaned up *while the revived node was still being repopulated*,
   so an owner could not confirm and the copy was correctly kept; but cleanup only runs inside a heal, so nothing came
   back for it. A **kept copy is deferred, not settled**: it now re-arms the heal trigger, which is self-limiting.
   22 → 19 without the retry; 22 → **18** with it.

New dashboard metric: **copies stored vs keys × R**, amber when there is a surplus.

### Session 12 — 2026-07-11 · Deployment, and the host that would have broken the demo
**Build only, no quiz.** Split deploy wired up: `$PORT`, `VITE_API_URL`, a `scratch`-based `Dockerfile` (zero deps ⇒
static binary ⇒ empty base image), a `render.yaml`.

**The find: the wrong host silently breaks the whole thesis.** Cloud Run's default *request-based billing* allocates
CPU only during a request, so the heartbeat goroutines would freeze between clicks and every node would **falsely
convict every other node** on the next request — the failure detector firing on the platform's idleness rather than on
any real failure. *A system whose liveness is "did I hear from you recently" cannot run somewhere that stops time when
nobody is looking.* Chose **Render free** and accepted its ~30–60 s cold start; the dashboard now says *"waking the
cluster…"* rather than showing an error (locally "unreachable" means *you forgot to start the backend* — not the same
message). Rejected the GitHub-Actions keep-alive pinger: an explicit Acceptable Use violation. → HLD §8.5.

### Session 11 — 2026-07-11 · Delete, and the ring that cannot tell you where the data is
**Build only, no quiz.** Seeding took a key count; then **delete**. The naive delete ("ask the ring who owns the key,
tell those R nodes to drop it") is **wrong twice**, because *nothing in this system ever removes a surplus copy*, so
where a copy **is** and where the ring **says it should be** drift apart permanently: **leftovers** (reproduced with
Kill + Revive alone: `key:0` owned by `[n2 n1 n0]`, *held* by `[n0 n1 n2 n4]`) and **resurrection** (a health-paused
node never gets the delete; resume it and heal pushes the key back). Fix: **the delete broadcasts to every peer.** Both
failures are guarded by tests confirmed to fail against the naive version. Full reasoning + why a real system needs a
**tombstone** → HLD §7.

Smaller: an explicit delete is **not an expiry** — it must not reach the reclaim log, or the dashboard reports keys the
user deleted as having died of old age. `Cache.Clear` re-points the LRU sentinels (getting that wrong is invisible until
the *next* eviction walks off a stale tail). Deleting a key must also drop its remembered deadline, or `noteExpiries`
invents an `expire` event for it.

### Session 10 — 2026-07-11 · TTL end to end, and the heal that defeated it
**Build only, no quiz.** Wiring TTL through the wire exposed the session's real find: **the heal was resurrecting
expiring keys** (→ Phase 5 checklist). Neither feature was wrong on its own — the bug lived in the **seam**, and it
existed *because* the system healed.

Also: **reads now name their source** and carry a per-owner trace, so the fallback that *is* the self-healing story is
finally visible to a client instead of buried in a server log; millisecond TTLs (and the `Number('')` trap); the Keys
panel split into SET and GET cards; a frontend simplification pass with **no behaviour change** (verified live against a
running cluster, not by inspection); the dashboard now **works on a phone**; and `logging/` finally written up.

**A new cold re-ask went on the board:** *the deadline's frame of reference.*

### Session 9 — 2026-07-11 · The milestone quiz, and the bug it found
**Phase 5 + 6 milestone quiz: 2 ✅ · 3 ⚠️ · 3 ⊘.** The carried-forward Snapshot-recency ⚠️ was re-asked cold → **✅,
debt closed.** The through-line across the three ⚠️: he states *what the code does* and stops one step short of *the
principle it instances*. The one real gap: **presence ≠ version.**

**Then the quiz paid for itself.** Aayush asked for a heal log in the UI. It took an hour to build, and **within
minutes of existing it showed the heal was broken** — the **stranded-key bug**, which five sessions of tests and four
browser demos had never revealed.

**Why the tests missed it for a whole session:** `TestRevivedNodeRepopulates` asserted `keyCount > 0`. A revived node
*does* get back the keys where it is a non-primary **replica**, so the count leaves zero and the assertion passes. It
never checked that the cluster returned to **full R**.
> **A weak assertion is a test that cannot fail in the way that matters.**

**…and then the new test flaked, teaching the same lesson twice.** It **waited** on *"no key is under-replicated"*
(`holders < R`) but **asserted** *"every owner holds its key."* Those differ **precisely because of the bug being
fixed**: after a kill/revive cycle the survivors keep leftover copies of keys they no longer own, and those pad the
holder count to 3 while a genuine owner sits empty — so the wait could exit *before the heal had converged.*
`holders >= R` is **not** the replication invariant; **"every owner holds its key"** is.
> **Three for three: a test is only as good as its weakest predicate.** *A test that cannot fail is not evidence* (S5)
> → *a weak assertion is a test that cannot fail in the way that matters* → *a weak **wait** is an assertion evaluated
> too early.*

**Two design calls worth keeping.** Aayush wanted heals in the same list as the kills — right, and the reason is that
*"which kill caused which copies"* is a question about **order**. He also wanted the 40-entry cap **removed**; kept it
and raised it to 300 instead, because an append-only list anyone can grow forever by clicking Kill is **the Phase-1
logical leak in a new hat**. The cap wasn't the problem; *40* was.

**Also:** *"Seed 8 more keys" was a total no-op* — `Seed(n)` always wrote `key:0..key:n-1` and the server seeds 12 at
startup, so every click **rewrote keys that already existed. Zero new keys, ever.** Fixed by having the *cluster* number
them; deliberately **not** tracked in the frontend, since a client remembering "I've seeded 8 so far" is
**check-then-act in a UI costume** (a reload or a second tab hands out the same numbers twice). New Go idiom:
**lock-order inversion** — a heal→manager *callback* deadlocks, and ⚠️ `-race` cannot see it, because a deadlock is not
a data race.

### Session 8 — 2026-07-11 · Check-first heal, and repopulating a revived node
Made the heal **ask before it copies** (`fetchFrom` → 200/404), which let us trigger it on *any* membership change and
delete the `hasSuspectedDead` gate. Fixed a latent **data race** in the shared `peers` map, caught by the new revive
test.
> ⚠️ **The repopulation claim was only half true, and we didn't know it for a session.** A revived node got back the
> keys where it was a *replica* — never the ones where it was the *primary*. Session 9 found the rest.

### Session 7 — 2026-07-10 · The self-heal arc, and the dashboard
**Cold re-ask: Q4 (self-suspicion & split-brain) → ✅** — sharpened that the data loss happens at **reconciliation**
(LWW silently drops the older *acked* write), not at the conflict itself. **Q6 (false-positive mitigations) was left
blank a third time** and was **taught, not attempted**; its lesson — *every mitigation delays correct convictions as
much as wrong ones, because a slow node and a dead node are the same silence* — fed straight into the storm work.

Built the full Phase 5 arc in one session (**naive heal → storm demo → grace-period fix**), then Phase 6: the `cluster/`
manager, the Go control API, and the React dashboard with an animated SVG hash ring. **Both halves of the money moment
became visible and interactive.**

### Session 6 — 2026-07-10 · Phase 4 milestone quiz
**2 ✅ · 2 ⚠️ · 2 ⊘.** The pattern was **label-not-mechanism**: he named "gossip/SWIM" without the mechanism
(second-hand, transitive learning) and got the timeout's *conclusion* right with the *mechanism* wrong (a crashed node
fails pings **fast** — the delay is the `lastSeen` **declaration** threshold, not a hanging connection). The genuinely
hard questions were clean, so this is **precision, not comprehension.**

### Session 5 — 2026-07-10 · Eviction: naive, measured, rewritten
**Cold re-ask of nine carried-forward questions: 2 ✅ · 5 ⚠️ · 1 ❌ · 1 ⊘.** **check-then-act is now a three-time miss**
— given a `GetOrRefresh` where *every* map access is locked, he answered *"there is no lock for `c.Set()`."* The
instinct is *"unsynchronized access → bug"*; the needed instinct is ***"decision made under a lock, acted on after the
unlock → bug."*** Starvation was defined backwards, and **happens-before** was taught from scratch.

Built capacity + expiry-aware LRU, hit the **`time.Now()` stands still for 541µs** wall (→ logical clock), and then
rewrote the O(n) scan into an O(1) map+list — **44,199× at 1M keys.**

**Two things got faster that nobody asked for**, and I predicted one of them backwards: `BenchmarkGet` went **61.31 →
52.52 ns** *because* a `*node` is addressable, so `Get` stopped rewriting the map slot — one hash and one store
**deleted from the read path**. I had predicted the pointer deref would make it *slower*.

**A test failed twice before it measured anything** — first because a 1ns TTL never ticked (the clock again), then
because the single corpse was also the LRU tail, so the fallback would have evicted it **whether or not the probe found
it**: *the test could not have failed.*
> **A test that cannot fail is not evidence.**

**And I broke an old test honestly:** `entry` grew 40 B → 48 B and a magic-threshold assertion (`afterSweep <=
afterWrite/2`) failed by 0.4 MB while the sweep still reclaimed everything. The real finding: **the never-shrinking
bucket residue scales with `sizeof(entry)`, not with the payload.** A test asserting on a fraction of peak heap is
really asserting on a struct size.

### Session 5 (cont.) — Hit rate, and a hypothesis half-refuted
**Wrote a prediction down first, and it was wrong.** I predicted the post-scan hit rate would fall to 20–40%; measured
**76.5%**, a 1.7-point dip. Two mistakes, both instructive: I aggregated over a 20,000-request window having *just*
warned that aggregating hides a transient (**a window is a smaller aggregate** — at 200-request resolution the crater is
real and ~2,000 requests wide); and I had never done the arithmetic showing **a scan's damage is bounded by capacity** —
you cannot lose more than you were holding.

**And I wrote fabricated numbers into the comments before running the code.** Caught it, deleted them.
> **A number in a comment that was never measured is a rumor with a monospace font.**

### Session 4 — 2026-07-09 · Concurrency, TTL, and the sweeper
**Quiz: 4 ✅ · 2 ⊘.** Demonstrated the data race live (both failure signals), taught the **five failure modes of
unsynchronized memory** and **mutex = mutual exclusion + publication barrier** (→ `GO_NOTES.md`), then built TTL and the
sweeper.

**Measuring turned out to be harder than building**, and three attempts failed before one worked: per-op latency printed
`p50=0s` (a `Get` is 67 ns; `time.Now()` resolves to **829 µs** — 12,000× coarser); a phantom 10 ms "max latency" **with
nothing running** turned out to be `append` growing a slice and triggering a GC (**measuring the measurement**); and a
component benchmarked **slower than the whole containing it**, because `var sink any` **boxed** the value and allocated.
→ `GO_NOTES.md`.

**Aayush caught a bad comment**, and it mattered: I had justified the sweep budget with a story about a 90%-corpse cache
looping forever. **False** — corpses are finite. The *real* reason only appeared when it was measured, and it was a
different reason entirely (51% of wall time in lock contention).

### Session 3 — 2026-07-08 · Phase 0
Go 1.26.5 installed. Phase 0 concepts taught and quizzed (**all passed**): what a cache is, why distribute one, CAP,
cluster-in-a-box.

### Session 2 — 2026-07-08 · Failure modes, quorum, and locking the design
A long informal deep-dive that became **HLD §6.1** (why node death causes staleness) and **§6.2** (the failure-mode
catalog), then **walked the six §10 tradeoffs and LOCKED all of them** — HLD flipped DRAFT → APPROVED.

Aayush reconstructed the **false-positive cascade** unprompted, reasoned out the **coordinator→consensus trap** on his
own, and traced conflict resolution back to its single root cause: **two primaries.** The teaching (CAP, quorum,
split-brain) is consolidated in the Phase 0 checklist above.

**Teaching preference corrected this session:** analogies are now *optional* — default to a plain explanation +
concrete example.

### Session 1 — 2026-07-07 · Scaffolding
Project set up; decisions locked (Go · complete-beginner teaching level · mixed quizzing · cluster-in-a-box);
`docs/HLD.md` drafted with six open ⚑ decisions. Taught informally: why consensus is out of scope, CAP, eventual
consistency, split-brain, control plane vs data plane. **Aayush's key insight, unprompted: replicating a coordinator
would itself need consensus among the coordinators** — which is the argument the whole AP design rests on.

---

## Quiz scoreboard
Score and what to revisit. **Full question text, model answers, and the named gap behind every ⚠️/❌ live in
`docs/QUIZZES.md`.**

| Date | Quiz | Score | What it flagged |
|---|---|---|---|
| 2026-07-11 | **S9 · Phase 5+6 milestone** | 2 ✅ · 3 ⚠️ · 3 ⊘ | States *what the code does*, stops short of *the principle*. Real gap: **presence ≠ version**. Q0 re-ask ✅ (debt closed). |
| 2026-07-10 | **S7 · cold re-ask (Q4, Q6)** | 1 ✅ · 1 ⊘ | Q4 **✅ closed**. Q6 (false-positive mitigations) **blank a third time** → taught. |
| 2026-07-10 | **S7 · Phase 5 quick-checks** | see QUIZZES | Passed before building the heal. |
| 2026-07-10 | **S6 · Phase 4 milestone** | 2 ✅ · 2 ⚠️ · 2 ⊘ | **Label-not-mechanism.** Hard questions clean → precision, not comprehension. |
| 2026-07-10 | **S5b · O(1) eviction** | 3 ✅ · 1 ⚠️ | Passed before coding. |
| 2026-07-10 | **S5 · cold re-ask ×9** | 2 ✅ · 5 ⚠️ · 1 ❌ · 1 ⊘ | **check-then-act ❌ (third miss)** · starvation backwards · happens-before ⊘ → taught. |
| 2026-07-09 | **S4d · eviction** | see QUIZZES | **Aayush's two challenges changed the design** (cache-aside ⇒ eviction only in `Set`; corpse-first eviction). |
| 2026-07-09 | **S4c · the sweeper** | 1 ✅ · 1 ⚠️ · 1 ½ | ⚠️ was **compare-don't-remember** *again* — logged as a pattern. |
| 2026-07-09 | **S4b · TTL** | see QUIZZES | Passed before coding. |
| 2026-07-09 | **S4 · concurrency & races** | 2 ✅ · 2 ⚠️ · 2 ⊘ | *(tally corrected 2026-07-11 from "4 ✅ · 2 ⊘")*: race condition ≠ data race (named the category, produced no code) · called **deadlock** starvation. |
| 2026-07-08 | **S2 · quorum & conflict resolution** | 4 ✅ | Strong. Correctly identified **two primaries** as the sole conflict source. |
| 2026-07-08 | **S2 · failure modes** | 5 ✅ · 3 ⚠️ | Re-taught: **available ≠ fully-replicated**; **frozen ≠ partitioned**; react to *transitions*, not the alive *count*. |
| 2026-07-08 | **S3 · Phase 0** | all ✅ | Cache, distribution, CAP, cluster-in-a-box. |
