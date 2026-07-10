# Quiz Bank

Question text + model answers, so quizzes can be re-asked cold weeks later. Scores live in
`docs/PROGRESS.md`.

**Conventions** — newest session at the top · ✅ correct · ⚠️ partial · ❌ wrong · ⊘ not attempted ·
⚠️/❌ entries name the *specific* gap, never just "revisit."

> Sessions 1–3 predate this file; flagged concepts are in `PROGRESS.md`.

---

## Session 5 — 2026-07-10 · cold re-ask of the nine carried-forward questions

No re-teaching first, as the ritual requires. **2 ✅ · 5 ⚠️ · 1 ❌ · 1 ⊘.**

| Q | Concept | | The gap |
|---|---|---|---|
| 1 | check-then-act (`GetOrRefresh`) | ❌ | Answered *"there is no lock for `c.Set()`"* — but `Set` **does** lock. Went hunting for a missing lock. **Every access is synchronized and the function is still wrong.** Instinct is *"unsynchronized access → bug"*; needed instinct is *"decision made under a lock, acted on after the unlock → bug."* |
| 2 | deadlock vs starvation | ⚠️ | Deadlock ✅. Starvation defined **backwards** ("blocked briefly, not indefinitely" = ordinary waiting). Correct: *postponed indefinitely **while the system as a whole progresses***. Deadlock is global; starvation is local and looks like health. Mechanism unnamed: see below. |
| 3 | happens-before | ⊘ | Not attempted. Taught below. |
| 4 | naive-timer overwrite bug | ✅ | Including *"the first timer does not check the expiry time."* Added: the deletion is **silent** — a crash would be a gift. |
| 5 | value vs resource | ⚠️ | Opened *"heap allocation makes a type a resource"* — **wrong**; `[]byte` is heap-allocated and is a value. Correct: **a resource owns something the GC cannot reclaim** (fd, socket, goroutine). Named the sweeper's reference ✅ but missed the link that makes it unfixable: **a running goroutine's stack is a GC root.** |
| 6 | compare, don't remember | ⚠️ | Recited the slogan word-perfect; produced **no interleaving**. Knowing the slogan ≠ being able to run the argument. |
| 7 | `Close()` / `select` / `Ticker` | ✅ | All three. Sharpened: `close(done)` is the **request**, `wg.Wait()` the **confirmation** — the sweeper may still be inside `samplePass` holding the mutex. |
| 8 | expiry-aware eviction | ⚠️ | Right victim (`config`), then gave **Q9's** principle. No scan here; a second access to `config` wouldn't help — still less recent than 999 corpses. Principle: **recency of use and freshness of value are independent orderings**, anti-correlated on session workloads. |
| 9 | scan resistance | ⚠️ | 3 of 4 families (segmented LRU, LFU+decay, TinyLFU); *described* admission correctly. Missed **hinting** (PG ring buffer, `MADV_SEQUENTIAL`). Never **named** the reframe: **LRU has no admission policy — it admits every key unconditionally; the only decision it makes is who to evict.** |

**The through-line across Q1, Q4, Q6** — all one bug: **a second party holding a stale opinion about a
key, and acting on it.** Q4's is a **timer**, Q6's a **remembered slice**, Q1's a **local `e` plus the
`load()` result**. Fix is always the same: carry nothing across the release; re-read under the lock.

**Q2's missing mechanism — starvation mode.** A naive mutex is a free-for-all: a goroutine already on
a CPU beats a parked one, which must be woken (**barging**). Go's `sync.Mutex` flips into **starvation
mode** once a waiter has blocked **>1ms**: `Unlock` then hands the lock *directly* to the queue head
and arrivals go to the back. `TestSweepStallsReaders`' 8,769 gets (vs 0) exist only because of this.

### Happens-before, taught (was Q3)

Source order guarantees **nothing** across goroutines, for two stacked reasons:
1. **The compiler reorders.** Its contract is not "run statements in order" but *"a single goroutine,
   reading only its own writes, cannot tell."* Goroutine B can tell.
2. **The CPU reorders.** Each core has a **store buffer**; writes drain later, not in issue order.

```go
config = loadConfig()   // a *Config — a whole struct
ready  = true           // one byte
```
B spins on `ready`, then reads `config.Timeout`. It can see `ready == true` and a **non-nil `config`
whose fields have not landed.** No panic, no race report. `Timeout == 0` once a week in production.

**Happens-before is not about time.** It is a *visibility guarantee*: if X happens-before Y, everything
written as of X is visible to whoever performs Y. It is **manufactured** — the compiler may not reorder
across it and the CPU emits a **memory barrier**. Edges come only from synchronization ops:
`Unlock`→a later `Lock` · `close(ch)`→a receive observing it · a send→its receive.

⚠️ **Go's `sync/atomic` is sequentially consistent**, so an atomic `ready` *does* publish `config`.
**Do not port this.** C++'s `memory_order_relaxed` is atomic with **no** happens-before edge.
**Atomicity and visibility are separate properties**; Go bundles them, C++ makes you ask separately.

So a mutex gives **two** things: **mutual exclusion**, and **a publication barrier** — everything A
wrote before `Unlock` is visible to B after `Lock`, *including memory unrelated to the protected data*.
That is why `c.mu` protects `c.data` though the buckets live elsewhere on the heap. **The mutex does
not wrap the map; it creates an ordering edge over all memory.**

---

## Session 5b — 2026-07-10 · O(1) eviction (before the code)

Phase 1, step 5. **3 ✅ · 1 ⚠️.**

**Q1 — Make the list singly linked to save 8 bytes. Which operation becomes O(n)?**
`remove(n)` **given a pointer to `n`** — not removal in general. Unlinking needs `n.prev` rewritten,
and a singly linked node doesn't know who points at it, so you walk from the head. `prev` exists for
exactly one reason: to make `Get`'s move-to-front O(1). (Evicting the *tail* alone would be fine.)
**✅** — precise, named the predecessor as the cost.

**Q2 — Why `map[string]*node` and not `map[string]node`?**
Map values are **not addressable** (`c.data[k].next = x` doesn't compile), and worse, **a rehash moves
values to new addresses**. A linked list is a web of pointers *between* nodes; if nodes lived in map
buckets, growing the map would dangle every one. **Values that other values point at need stable
identity, and map values have none.** Bonus: a `*node` is addressable, so `Get` stopped writing the
entry back — `BenchmarkGet` **61.31 → 52.52 ns**.
**✅** — got copy-semantics and stable addresses; sharpened with the rehash and the addressability link.

**Q3 — Construct a workload where option (c) (evict tail, let the sweeper cope) serves 0% hit rate
forever, despite the sweeper running every second.**
Touch the hot key, then bury it under enough *new* `Set`s of short-TTL keys that it reaches the tail
and dies before it's read again. Two corrections: they must be **inserts**, since only an insert into
a full cache evicts and only a `Set`/`Get`-hit refreshes recency (reading a corpse *deletes* it).
And the real answer is a **timing** argument: **eviction happens at `Set` rate, corpse reclamation at
the sweeper's tick rate, and those are decoupled by six orders of magnitude.** A thousand `Set`s land
in ~100µs; the sweeper wakes once a second. Within one tick the cache can evict live keys a thousand
times over while reclaimable corpses sit there. Making the sweeper faster doesn't fix it — you'd have
to run it *between every pair of `Set`s*, at which point it isn't a sweeper, it's `evictLocked`.
**⚠️** — right shape, missed that it is fundamentally about decoupled rates.

**Q4 — Why does peeking a min-heap on `expires` prove no corpse exists?**
The root is the global minimum, so `root.expires > now` proves `expires > now` for **all n entries** —
an O(1) statement about the entire population. A sample can never do that: **absence of evidence is
not evidence of absence.** What (a) trades away is not space but **time and complexity** — O(1) with
no per-node heap index, vs O(log n) plus a `heapIndex` maintained through every sift.
**✅** — heap invariant correct; the trade was misnamed as "space."

---

## Session 4d — 2026-07-09 · Eviction / LRU (before the code)

Phase 1, step 4. **3 ✅ · 1 ⊘.**

**Q1 — What does LRU substitute for Bélády's future knowledge?**
**Temporal locality**: a key used recently is likely to be used again soon — the recent past as a proxy
for the near future. Good proxy: sessions, hot rows, popular products. Bad proxy: a **sequential scan**,
where each key is touched once and never again.
**✅** — named it, gave both workloads, produced the scan counterexample *unprompted, before it was taught*.

**Q2 — Capacity 3, `{a,b,c}` (`a` least recent). `Get(b)`, `Set(d)`, `Get(a)`?**
`Get(b)` → order `a,c,b`. `Set(d)` evicts `a`. So `Get(a)` **misses**. That last line is the point:
**eviction is not cleanup; it is a decision about which future request you are willing to lose.**
**✅** — correct victim and reasoning; didn't state the miss.

**Q3 — `lastUsed time.Time` + scan for the minimum: what's the cost?**
**O(n) per eviction**, under the lock. Worse than `sweepAll`: that ran once a second on a *background*
goroutine (2.5% of wall time). An eviction scan runs **on the write path, on every `Set`, once full** —
and full is a cache's *normal steady state*. ~25ms per `Set` at 1M entries. Fix: hash map + doubly
linked list → O(1) lookup / move-to-front / evict-tail.
**✅** — got O(n) and the write-path tail latency unprompted.

**Q4 — Capacity 1000: 999 corpses + 1 live key. A `Set` arrives. What's evicted?**
**The live key.** Naive LRU sorts by `lastUsed`; a `Set` *is* an access, so every corpse outranks the
live key. The cache is now **worse than empty** — 1000 slots serving nothing. Fix: **reclaim a corpse
before evicting anything live.** Free the capacity that costs nothing to free first.
**⊘** — not attempted, but asked exactly the right question ("does naive LRU evict only from unexpired
keys?"). It doesn't — and the instinct that expiry belongs in the eviction path was right.

### Aayush's two challenges (both changed the design)

**(a) "Won't LRU evict the corpses anyway? Aren't they least-recently-used most of the time?"**
Sometimes — but **recency and expiry are independent orderings.** LRU sorts by last touch, expiry by
deadline. They anti-correlate on our own leak workload: 999 sessions `Set` with a 50ms TTL in the last
second, never read, plus one permanent `config` touched a minute ago. Every corpse is *more recently
used*. LRU evicts `config`. Converse: a key `Set` 1ms ago with a 1ms TTL is the **most recently used
entry in the cache and already a corpse.** **Recency of *use* ≠ freshness of *value*.** And "usually
right" is not a bound — when the check costs one timestamp comparison. → expiry-aware from commit one.

**(b) "Why is eviction happening in `Get`? Eviction is a `Set`."**
Correct; my trace was wrong. **Our `Get` never inserts.** This is a **cache-aside (look-aside)** store
like Redis/memcached — the *application* does `Get` → miss → `db.Query` → `Set`. (A **read-through**
cache — Caffeine, Guava `LoadingCache` — takes a loader and populates itself; there `Get` really evicts.)
Consequence is sharper, not weaker: the cache sees ten ordinary `Set` calls with no flag saying "batch
job." **Pollution originates in the caller's fill pattern**, so scan resistance must live in the
eviction policy — the only place that can tell a hot key from a scan artifact.

Division of labour: `Get` **hit** updates recency (→ `Get` is a writer, *third* reason) · `Get` **miss**
does nothing · `Set` updates recency **and** evicts.

---

## Session 4c — 2026-07-09 · The sweeper (before the code)

Phase 1, step 3b. **1 ✅ · 1 ⚠️ · 1 half.**

**Q1 — Why can't the sweeper unlock every 1,000 keys to shorten the pause?**
*Minor:* **Go randomizes map iteration order on every `range`.** No cursor, no "resume from key 1,000" —
restart and you revisit some keys, miss others.
*Fatal:* keep the `range` alive and unlock inside the body, and during the gap someone calls
`Set(k,"fresh",time.Hour)`. The sweeper relocks, looks at `e` — **the copy read before the gap**, still
expired — and deletes a value with an hour left. The naive-timer bug in different clothes.
**Anything read before releasing a lock is a rumor by the time you reacquire it. Compare, don't remember.**
Redis's sampling sidesteps this: each pass is stateless and holds the lock start to finish — no gap.
**⚠️** — found the iteration-order problem (real, correctly reasoned); missed the fatal stale-read
deletion. **Second occurrence of this exact miss** (S4 Q2). → pattern, not incident.

**Q2 — Why sample only keys *with* a TTL?**
Sampling estimates a population; the population of concern is *expirable* keys. 10M permanent config
keys + 1,000 TTL'd sessions: 20 uniform draws yield an expected **0.002** TTL'd keys. Every sample reads
"nothing to reclaim," and the sessions rot forever. The estimator isn't noisier — it's **biased into
uselessness**. Hence Redis's separate `db->expires` dict: **the data structure exists to serve the
sampling requirement.**
**✅** — nailed the statistical core unprompted; didn't name the failing workload or the index consequence.

**Q3 — Why can't the Cache be GC'd while the sweeper runs, and what must the API add?**
**Every running goroutine's stack is a GC root.** The sweeper's stack holds `c`, so `c` is reachable *by
definition*. And it's mutual: the goroutine never exits, so its stack never stops being a root. **Two
leaks holding each other up.** `runtime.SetFinalizer` can't help — a finalizer runs on unreachability,
which is exactly what cannot happen.
The only fix is to make the goroutine **return**:
```go
done chan struct{}                              // make() it — a nil channel blocks forever
func (c *Cache) Close() {
    c.closeOnce.Do(func() { close(c.done) })    // a double close panics → sync.Once
    c.wg.Wait()                                 // asking to stop ≠ knowing it stopped
}
```
plus `select` on `<-ticker.C` vs `<-c.done`. **`Ticker`, not `Sleep`** — `Sleep` can't be interrupted.
**`close()`, not send** — closing broadcasts to every receiver forever; a send wakes exactly one.
Cost, stated plainly: **`Cache` is now a resource, not a value**, and no compiler, vet check, or race
detector will catch a caller who forgets `Close()`.
**✅ / ⊘** — first half exact; second half taught.

---

## Session 4b — 2026-07-09 · TTL (before the code)

Phase 1, step 3. **2 ✅ · 1 ❌ · 1 ⊘.**

**Q1 — Why store the absolute deadline, not the duration?**
The duration version needs a second field `setAt`, and every read then costs `setAt + ttl` before the
compare. Storing `expires` does that addition **once, at write time**. Caches are read-heavy — that's
the right place to pay.
**✅** — got both the extra state and the sufficiency of `now > expires`.

**Q2 — The naive one-timer-per-key bug on overwrite.**
`Set("k","a",30s)`, then at t=2s `Set("k","b",10min)`.
```
t=0s    Set("k","a",30s)     → goroutine A sleeps 30s
t=2s    Set("k","b",10min)   → goroutine B sleeps 10min
t=30s   A wakes              → Delete("k")   ← deletes "b", 9.5 min early
t=602s  B wakes              → Delete("k")   ← no-op
```
Not a race between A and B: **A holds a stale belief about what it is deleting.** It deletes by *key*,
not by *deadline*. **Silent** — no panic, no error, no `-race` warning. Had A re-checked
`entry.expired(now)` under the lock instead of trusting its timer, it would have left `"b"` alone.
**Compare, don't remember** — the whole insight behind lazy expiry.
Go fact: `delete(m,k)` on an absent key is a **no-op**, unlike C++'s `std::map::at()`.
**❌** — spotted the two goroutines, but predicted the *second* would "find the key missing and **fail**."
Wrong twice. **Gap: expects concurrency bugs to crash.** They don't.

**Q3 — Where does "an unread expired key is indistinguishable from a deleted one" break?**
It holds **only through the `Get`/`Set` API**. Widen the observer and it collapses: (1) **memory** — the
entry is plainly on the heap; (2) **introspection** — `Len()`, `Keys()`, a stats endpoint, the Phase 6
dashboard; (3) **capacity** — corpses **occupy slots**, so the cache fills with dead entries and evicts a
**live** key. Lazy expiry silently sabotages the eviction policy.
The honest statement: **lazy expiry is correct w.r.t. *value* semantics and wrong w.r.t. *resource*
semantics.** That seam is exactly where the sweeper goes.
**⊘** — flagged as unsure, correctly; the hardest of the four.

**Q4 — A workload that grows unboundedly under lazy expiry alone.**
A **session cache**: 1,000 logins/sec, `session:<uuid>` with a 30-min TTL, read a few times and never
again. ~50,000 live at any instant; the map grows 1,000/sec **forever** — 86M/day, 99.9% corpses no
`Get` will ever touch. Logically 50k keys; physically 86M.
**✅** — right mechanism. Later measured: **40.9 MB for 200k keys, surviving a forced GC.**

---

## Session 4 — 2026-07-09 · Concurrency, races, and the mutex

Phase 1, step 2. **4 ✅ · 2 ⊘** (Q5–Q6 taught rather than attempted).

**Q1 — Why do disjoint keys still race?**
100 goroutines write `k0-*`, `k1-*`, … — no key is shared. The *value slots* aren't shared; the map's
**internal bookkeeping** is: bucket array, entry count, growth flag, overflow chains. A write can trigger
a **rehash** — allocate a bigger array, migrate every entry, swap the pointer. Proof from the live run:
goroutines 9 and 11 collided on the same address `0x00c000030780` despite disjoint keys. That address is
bookkeeping, not a value.
**A map's invariants span all its entries, so writing disjoint keys still mutates common state.**
**✅** — sharpened to say *which part* of the map.

**Q2 — Data race vs race condition.**
A **data race** needs all three: (1) two goroutines touch **the same memory location**, (2) ≥1 is a
**write**, (3) **no synchronization** orders them. Consequence: **undefined behavior**. Mechanically
defined ⇒ mechanically detectable (`-race`).
A **race condition** is defined relative to **intent**, so no tool can detect it in general:
```go
val, _ := c.Get("hits")             // both read "5"
n, _ := strconv.Atoi(val)           // both compute 6
c.Set("hits", strconv.Itoa(n+1))    // both write "6"
```
Two increments, counter advances by one. **Zero data races.** This is **check-then-act**: *the lock is
released between the `Get` and the `Set`, and that gap is where the other goroutine slips in.*
Data race ⇒ race condition. Race condition ⇏ data race — this is the counterexample.
**⚠️** — three conditions right (nit: *same memory location*, not merely "shared memory"). Named
check-then-act as a **category**; produced no code. **Gap: recognition vs recall.**

**Q3 — Why does `Get` lock?**
The definition needs **at least one** writer, not two. A locked `Set` racing an unlocked `Get` satisfies
all three conditions — it is a data race, full stop. It is not *caution*, it is *required*. And the
consequence is worse than staleness: an unlocked read during a **rehash** can follow a bucket pointer
into the array being torn down. Garbage or a crash.
**✅** — named the mid-expansion rehash unprompted.

**Q4 — What breaks without `defer`?**
```go
c.mu.Lock()
value, ok := c.data[key]
c.mu.Unlock()          // never reached on early return / panic
```
The lock is never released; a `panic` unwinds straight past it. This is a **deadlock**, not starvation:
starvation means a goroutine *could* acquire the lock but keeps losing; deadlock means it can **never**,
because the holder will never release. Every future `Get`/`Set` blocks forever; the goroutine that
caused it has moved on and looks innocent. `defer` makes the bug **unrepresentable** — same move as
`wg.Go` making the `Add`-after-`go` bug unrepresentable.
**⚠️** — mechanism exact; called it **starvation**. **Gap: vocabulary, not understanding.**

**Q5 — Would an atomic map remove the need for a mutex?**
**No.** (1) **Lost updates survive** — atomicity was granted to *the operations the map exposes*, not
*the operation the caller cares about*. (2) **Safe publication survives**:
```go
u := &User{}; u.Name = "aayush"   // plain write to different memory
c.Set("u1", u)                    // atomic, by assumption
// elsewhere: u,_ := c.Get("u1"); fmt.Println(u.Name)   // may print ""
```
A good pointer to a struct whose fields haven't arrived.
> **Atomicity is a property of one operation on one memory location. A mutex gives two other things: a
> critical section spanning many operations, and a happens-before edge spanning all memory.**

Why it matters: it's why `sync.Map` doesn't free you from thinking, and why Java's `volatile` is not a
substitute for `synchronized`.
**⊘** — taught. Reappears in Phase 3 as "when is a replicated write visible?"

**Q6 — `SetIfAbsent` vs `RWMutex`: what justifies each?** (One is a correctness fix; one isn't.)
**`SetIfAbsent` — correctness.** Closes the check-then-act gap in `if _, ok := c.Get(k); !ok { c.Set(k,v) }`.
*Justified by* a real caller ("first writer wins," cache-stampede prevention) — **never a benchmark**.
Correctness is **reasoned, not measured**, and `-race` will never flag the caller-side version.
*The principle, reused in Phase 3:* **expose the operation the caller needs atomically; don't hand them
primitives and expect them to compose them safely.** Only the type holding the lock can.
**`RWMutex` — a performance change that must be earned.** It is a **more expensive lock**: `RLock`
maintains an atomic reader count, paid on *every* read. You recoup that only if readers spend enough time
**inside** the critical section for overlapping to be worth something. Ours is **one map lookup**.
*Justified by* two benchmarks: one showing `Mutex` is the bottleneck, one showing `RWMutex` beats it.
(Later measured: uncontended `Mutex` = 26.87ns of a 67ns `Get`, with ~22ns of work to overlap. And `Get`
deletes, so it couldn't take an `RLock` anyway.)
**⊘** — taught. **Guessing correctly and measuring are different skills, and only one scales.**

---

## Carried forward — re-ask cold

**Retired 2026-07-10 at Aayush's request — do not re-ask.** The Phase 1 carry-forward list
(check-then-act, compare-don't-remember, starvation mechanism, happens-before, resource semantics,
expiry-aware eviction, admission control) is closed. The concepts remain taught and are recorded in
the session logs above; several were also *demonstrated in code* during Phase 1 (expiry-aware eviction
in `evictLocked`, resource semantics in `Close`, admission control as the reason segmented LRU was
deferred). If any resurfaces as a real gap during Phase 2+, treat it as new material then, not as a
debt to collect. Start a fresh carry-forward list from Phase 2 quizzes.
