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
- **Next action:** **Phase 1, step 4 — build LRU eviction** (concepts taught & quizzed 2026-07-09;
  no code written yet). Agreed build order:
  1. Naive LRU: `lastUsed time.Time` per entry, evict by scanning for the minimum. **Expiry-aware
     from the start** — reclaim a corpse before evicting anything live (a *correctness* fix, not a
     perf guess; Aayush argued it out himself).
  2. Measure the O(n) victim scan. It's `sweepAll`'s bug on the **write path**: `sweepAll` froze 2.5%
     of wall time on a background goroutine; an O(n) eviction scan runs on **every `Set` once the
     cache is full**, which is a cache's normal steady state.
  3. Replace with a hand-rolled **hash map + doubly linked list** → O(1) lookup / move-to-front /
     evict-tail.
  4. Build a **hit-rate benchmark**: hot-key workload, then the same interrupted by a scan. Watch the
     hit rate collapse. *Only then* consider segmented LRU. The scan story is a hypothesis until it
     prints a number.
  Open the next session by re-asking the **seven** carried-forward questions cold — table at the
  bottom of `docs/QUIZZES.md`.
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
- ◐ LRU eviction — **taught & quizzed, not yet built.** Bélády's optimal is unimplementable (needs the future); every real policy approximates it from the past. LRU = a bet on *temporal locality*, and a **sequential scan is that bet losing**. Eviction must be **expiry-aware** (corpses occupy capacity). Next: naive O(n) victim scan → measure → hash map + doubly linked list.

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
