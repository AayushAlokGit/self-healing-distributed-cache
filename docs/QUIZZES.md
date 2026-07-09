# Quiz Bank

Full text of every quiz, with model answers. `docs/PROGRESS.md` records the *scores* and what to
revisit; this file records the *questions* so they can be re-asked cold weeks later.

**Conventions**
- Newest session at the top.
- Each question carries the **model answer** (what a complete answer contains) and **Aayush's
  result**: ✅ correct · ⚠️ partial · ❌ wrong · ⊘ not attempted.
- ⚠️ and ❌ entries name the *specific* gap, not just "revisit." A gap you can't state precisely
  isn't a gap you can close.
- Re-ask ⚠️/❌/⊘ questions cold at the start of a later session. Mark the re-ask inline.

> Sessions 1–3 predate this file. Their outcomes are in `PROGRESS.md`'s quiz log, but the question
> text was not preserved. Not worth reconstructing — the flagged concepts are recorded there.

---

## Session 4 — 2026-07-09 · Concurrency, races, and the mutex

Phase 1, step 2. Covers `cache/race_test.go`, the `sync.Mutex` fix, and the surrounding theory.
**Result: 4 ✅ · 2 ⊘ · 0 ❌** (Q5–Q6 taught rather than attempted).

---

### Q1 — Why do disjoint keys still race?
> `race_test.go` has 100 goroutines writing `k0-*`, `k1-*`, … — no two goroutines ever write the
> same key. Explain why it still races. What is the shared thing?

**Model answer.** The *value slots* are not shared: `k7-3` and `k12-9` live in different places. What
is shared is the map's **internal bookkeeping** — bucket array, entry count, growth flag, overflow
chains. A write can trigger a **rehash**: allocate a larger array, migrate every entry, swap the
pointer. Two goroutines writing different keys still mutate that common state, so the map's
invariants (count agrees with buckets, no half-migrated entries) can be violated.

Proof from the live run: goroutines 9 and 11 collided on the *same address* (`0x00c000030780`)
despite disjoint keys. That address is bookkeeping, not a value.

The sentence to be able to say: **a map's invariants span all its entries, so writing disjoint keys
still mutates common state.**

**Aayush: ✅** — named the shared map correctly; sharpened to say *which part* of it (bookkeeping,
not value slots).

---

### Q2 — Data race vs race condition
> Define a data race precisely, then give a concrete example of a **race condition that is not a
> data race**, using our `Cache`.

**Model answer, part 1.** A data race requires all three:
1. Two or more goroutines access **the same memory location**
2. At least one access is a **write**
3. There is **no synchronization** ordering them (no happens-before edge)

Consequence: **undefined behavior**. Mechanically defined ⇒ mechanically detectable (`-race`).

**Model answer, part 2.** With the mutex committed, every `Get`/`Set` is individually synchronized.
Yet:

```go
val, _ := c.Get("hits")             // both goroutines read "5"
n, _ := strconv.Atoi(val)           // both compute 6
c.Set("hits", strconv.Itoa(n+1))    // both write "6"
```

Two increments; the counter advances by one. **Zero data races** — `-race` is silent, every access
was locked. Still wrong. This shape is **check-then-act**: *the lock is released between the `Get`
and the `Set`, and that gap is where the other goroutine slips in.*

A race condition is defined relative to **intent**, so no tool can detect it in general.
Data race ⇒ race condition (UB voids all reasoning). Race condition ⇏ data race — this is the
counterexample.

**Aayush: ⚠️** — three conditions correct (nit: *same memory location*, not merely "shared memory").
Described check-then-act as a **category** but did not produce concrete code. **Gap: recognition vs
recall.** Code review needs recall. → re-ask cold.

---

### Q3 — Why does `Get` lock?
> "Reads don't modify anything, so a lock-free `Get` can't corrupt the map — locking it only costs
> performance." Where is this wrong, and what could a lock-free `Get` observe?

**Model answer.** The data-race definition needs **at least one** writer, not two. A locked `Set`
racing an unlocked `Get` satisfies all three conditions — it is a data race, full stop. Locking only
writes buys nothing.

It is not *caution*, it is *required*.

And the consequence is worse than staleness: an unlocked read concurrent with a **rehash** can follow
a bucket pointer into the array being torn down. Garbage or a crash, not merely an old value.

**Aayush: ✅** — got "`Get` can race with `Set`" and named the mid-expansion rehash unprompted.
Sharpened: replace "to be cautious" with "required by the definition"; consequence is corruption,
not staleness.

---

### Q4 — What breaks without `defer`?
> `Get` unlocks explicitly, then someone later adds a `panic` or early `return` mid-function.
> Describe the failure and who it affects.

```go
func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	value, ok := c.data[key]
	c.mu.Unlock()          // never reached on early return / panic
	return value, ok
}
```

**Model answer.** The lock is never released. A `panic` in particular unwinds straight past the
`Unlock()` line as though it weren't there.

This is a **deadlock**, not starvation — the distinction matters:
- **Starvation** — a goroutine *could* acquire the lock but keeps losing to others. Unlucky, not
  doomed; it may still run. (Go's mutex has a starvation-prevention mode for this.)
- **Deadlock** — the lock can *never* be acquired, because the holder will never release it. Waiting
  does not help.

The holder returned; nobody will ever call `Unlock`. Every future `Set` and `Get` blocks forever —
the whole cache is dead and the process hangs. The goroutine that caused it has moved on and looks
innocent, which makes it hard to trace.

`defer` makes the bug **unrepresentable** — same move as `wg.Go` making the `Add`-after-`go` bug
unrepresentable.

**Aayush: ⚠️** — mechanism exactly right (early return/panic skips unlock, lock held forever). Called
it **starvation**; it is **deadlock**. **Gap: vocabulary, not understanding.** Matters in Phase 4
when nodes wait on each other. → re-ask cold.

---

### Q5 — Would an atomic map remove the need for a mutex?
> Suppose each `Set`/`Get` were individually atomic, no corruption possible. Still need the mutex?
> Name a surviving failure mode and explain why atomicity doesn't fix it.

**Model answer. Yes, still needed.** At least two of the five modes walk straight through
per-operation atomicity.

**1. Lost updates survive.** Each `Get` atomic, each `Set` atomic — but `Get → +1 → Set` is not,
because atomicity was granted to *the operations the map exposes*, not *the operation the caller
cares about*. Q2's counter again.

**2. Safe publication survives.** Subtler. Suppose the cache stored pointers:

```go
u := &User{}
u.Name = "aayush"        // (1) plain write, unsynchronized
c.Set("u1", u)           // (2) atomic, by assumption
```
```go
u, _ := c.Get("u1")      // (3) atomic — definitely a valid pointer
fmt.Println(u.Name)      // (4) may print ""
```

`u.Name` is an ordinary write to **different memory**. Nothing forces it visible before, or along
with, the pointer. The reader holds a good pointer to a struct whose fields haven't arrived.
Failure modes 4 (visibility) and 5 (reordering), both alive.

**The sentence to keep:**
> **Atomicity is a property of one operation on one memory location. A mutex gives two other things:
> a critical section spanning many operations, and a happens-before edge spanning all memory.**

That second half is the **publication barrier** — on `Unlock`, *everything* written beforehand
(the map entry, `u.Name`, a variable elsewhere entirely) is visible to whoever `Lock`s next. Not
just the last location touched.

Why this matters beyond the thought experiment: it's why `sync.Map` doesn't free you from thinking,
and why Java's `volatile` is not a substitute for `synchronized`. Go's real map isn't atomic anyway
— the point is that **even granting the strongest per-operation guarantee, you'd still want the
lock.**

**Aayush: ⊘** — not attempted; taught instead. New material. Reappears in Phase 3 as "when is a
replicated write visible?" → **re-ask before Phase 3.**

---

### Q6 — `SetIfAbsent` vs `RWMutex`: what justifies each?
> Both are deferred. For each: what problem does it solve, and what must be true before adding it?
> (Different answers — one is a correctness fix, one isn't.)

**Model answer.**

**`SetIfAbsent` — a correctness fix.** Closes the check-then-act gap. Today a caller must write
`if _, ok := c.Get(k); !ok { c.Set(k, v) }` and the lock drops in the middle:

```go
func (c *Cache) SetIfAbsent(key, value string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[key]; ok {
		return false
	}
	c.data[key] = value
	return true
}
```

*Justified by:* an actual caller with a real requirement — "first writer wins," or cache-stampede
prevention (one goroutine computes the expensive value, the rest don't recompute it). **Not** a
benchmark. No measurement could justify or refute it: correctness is **reasoned**, not measured, and
`-race` will never flag the caller-side version. Absent a caller it's speculative API surface.

*The principle, reused in Phase 3:* **expose the operation the caller needs atomically; don't hand
them primitives and expect them to compose them safely.** A caller composing `Get` + `Set` cannot
make it atomic however careful they are — only the type holding the lock can.

**`RWMutex` — a performance change that must be earned.** `sync.Mutex` serializes two concurrent
`Get`s even though neither mutates. `sync.RWMutex` splits it: `RLock`/`RUnlock` for readers (many
hold it at once), `Lock`/`Unlock` for writers (exclusive). Caches are read-heavy, so it *looks* like
a free win.

It is not. `RWMutex` is a **more expensive lock** — `RLock` maintains an atomic reader count, and you
pay that on *every read*. You recoup it only if readers spend enough time **inside** the critical
section for overlapping them to be worth something. Our critical section is **one map lookup —
nanoseconds**. There is a real chance `RWMutex` makes this cache measurably *slower*. Rule of thumb:
`RWMutex` wins when critical sections are long and reads dominate; ours are as short as they get.

*Justified by:* a `testing.B` + `b.RunParallel` benchmark on a read-heavy workload showing `Mutex` is
the bottleneck, **and** a second showing `RWMutex` actually beats it. Two numbers, before and after,
or it doesn't ship.

Textbook case of an optimization "everybody knows" is faster and frequently isn't — exactly the
project's *naive → measure → iterate* rule. **Guessing correctly and measuring are different skills,
and only one scales to the parts of this system where intuition has no training data.**

**Aayush: ⊘** — not attempted; taught instead.

---

## Session 4b — 2026-07-09 · TTL (before writing the code)

Phase 1, step 3. **Result: 2 ✅ · 1 ❌ · 1 ⊘.**

---

### Q1 — Why store the absolute deadline, not the duration?
**Model answer.** The duration version needs a second field, `setAt`, and then every read costs an
addition (`setAt + ttl`) before the comparison. Storing `expires` does that addition **once, at write
time**. Caches are read-heavy, so that's the right place to pay.

**Aayush: ✅** — got both the extra state and the sufficiency of `now > expires`.

---

### Q2 — The naive one-timer-per-key bug on overwrite
> `Set("k","a",30s)`, then 2s later `Set("k","b",10*time.Minute)`. Walk through t=30s.

**Model answer.**
```
t=0s    Set("k","a",30s)     → goroutine A sleeps 30s
t=2s    Set("k","b",10min)   → goroutine B sleeps 10min
t=30s   A wakes              → Delete("k")   ← deletes "b"!
t=602s  B wakes              → Delete("k")   ← no-op
```
At t=30s the key no longer holds `"a"`; it holds `"b"`, with 9.5 minutes left. **Goroutine A destroys
live data.** The bug is not a race between A and B — it's that **A holds a stale belief about what
it's deleting**. It deletes by *key*, not by *value* or *deadline*. The failure is **silent**: no
panic, no error, no `-race` warning. Just data gone early.

Note the fix: if A re-checked `entry.expired(now)` under the lock instead of trusting its own timer,
it would find `"b"`'s deadline in the future and leave it alone. **Compare, don't remember** — which
is the whole insight behind lazy expiry.

Go fact: `delete(m, k)` on an absent key is a **no-op** — no panic, no error. Unlike C++'s
`std::map::at()`. So B at t=602s does nothing at all, harmlessly.

**Aayush: ❌** — spotted the two goroutines, but predicted "the second finds the key missing and may
**fail**." Wrong twice: `delete` of an absent key can't fail, and the real bug is the *first*
goroutine deleting live data. **Gap: expects concurrency bugs to crash.** They don't — lost updates,
torn values, premature deletion are all silent. → re-ask cold.

---

### Q3 — Where does "an unread expired key is indistinguishable from a deleted one" break?
**Model answer.** The claim holds **only through the `Get`/`Set` API**. Widen the observer and it
collapses, three ways:
1. **Memory.** Watch the heap and the entry is plainly there. (This is Q4's leak, arriving from the
   other direction.)
2. **Introspection.** The moment the cache exposes `Len()`, `Keys()`, a stats endpoint, or the
   Phase 6 dashboard, corpses show up. `Len()` would report 86 million.
3. **Capacity interaction (bites us in step 4).** LRU eviction has a size limit. Expired-but-present
   entries **occupy capacity**, so the cache fills with corpses, hits its limit, and evicts a **live**
   key while thousands of dead ones sit untouched. Lazy expiry silently sabotages the eviction policy.

The honest statement: **lazy expiry is correct with respect to *value* semantics and wrong with
respect to *resource* semantics.** Values behave perfectly; memory, size, and capacity do not. That
seam is exactly where the sweeper goes.

**Aayush: ⊘** — flagged as unsure, correctly; the hardest of the four. Taught instead.

---

### Q4 — A workload that grows unboundedly under lazy expiry only
**Model answer.** A **session cache**: 1,000 logins/sec, each storing `session:<uuid>` with a 30-min
TTL, read a few times during the visit and never again. At any instant ~50,000 sessions are live. The
map grows 1,000 entries/sec **forever** — 86M/day — of which 99.9% are corpses no `Get` will ever
touch, so no `Get` will ever reclaim them. Logically 50k keys; physically 86M.

**Aayush: ✅** — right mechanism ("expired keys no longer accessed accumulate"). Sharpened with the
concrete workload and the numbers. Later measured for real: **40.9 MB for 200k keys, surviving a
forced GC.**

---

## Session 4c — 2026-07-09 · The sweeper (before writing the code)

Phase 1, step 3b. **Result: 1 ✅ · 1 ⚠️ · 1 half.**

---

### Q1 — Why can't the sweeper unlock every 1,000 keys to shorten the pause?
**Model answer.** Two reasons, one fatal.

*Minor:* **Go randomizes map iteration order on every `range`**, deliberately. There is no cursor, no
index, no "resume from key 1,000." Abandon the loop and start a new `range` and you begin at a fresh
random point — some keys visited twice, others never.

*Fatal:* keep the same `range` alive and merely unlock inside the body, and during the gap another
goroutine calls `Set(k, "fresh", time.Hour)`. The sweeper relocks, looks at `e` — **the copy it read
before the gap**, still expired — and deletes `k`, destroying a value with an hour left.

That is exactly the naive-timer bug wearing different clothes. **Anything you read before releasing a
lock is a rumor by the time you reacquire it. Compare, don't remember.**

Note Redis's sampling design sidesteps this entirely: each pass is stateless, holds the lock start to
finish, and carries nothing across a gap because there is no gap.

**Aayush: ⚠️** — found the iteration-order problem (real, and correctly reasoned). Missed the fatal
stale-read deletion. **This is the second time this exact miss has appeared** (S4 Q2 check-then-act).
→ **pattern, not incident.** Re-ask cold.

---

### Q2 — Why sample only keys *with* a TTL?
**Model answer.** Sampling estimates a population; the population of concern is *expirable* keys, so
the sample must be drawn from those.

Concretely: 10M permanent config keys + 1,000 TTL'd session keys. Sample 20 uniformly from all
10,000,001 and you expect **0.002** to have a TTL. Every sample comes back with zero expired keys; the
adaptive rule reads "nothing to reclaim," sleeps, and the sessions rot forever. The estimator isn't
merely noisier — it's **biased into uselessness**, diluted by a population that can never expire.

Design consequence: Redis keeps a **separate dict of keys with an expiry** (`db->expires`) so it can
sample the right population cheaply. The data structure exists to serve the sampling requirement.

**Aayush: ✅** — nailed the statistical core unprompted ("the population in concern for the sweeper is
the keys with TTL"). Didn't name the concrete failing workload or the separate-index consequence.

---

### Q3 — Why can't the Cache be GC'd while the sweeper runs, and what must the API add?
**Model answer.** **Every running goroutine's stack is a GC root.** The collector marks from the roots
— globals and goroutine stacks — and the sweeper's stack holds `c`. So `c` is reachable *by
definition*, no matter that every other part of the program forgot it. And it's mutual: the goroutine
never exits (`for {}`), so its stack never goes away, so it stays a root. **Two leaks holding each
other up.** `runtime.SetFinalizer` can't help — a finalizer runs when an object becomes unreachable,
which is precisely what can't happen.

**What the API needs:** a way to make the goroutine *return*. Nothing else works.
```go
done chan struct{}                       // make() it — nil channels block forever
func (c *Cache) Close() {
    c.closeOnce.Do(func() { close(c.done) })   // double close panics → sync.Once
    c.wg.Wait()                                // asking to stop ≠ knowing it stopped
}
```
and `select` on `<-ticker.C` vs `<-c.done`. **`Ticker`, not `Sleep`** — `Sleep` can't be interrupted,
so `Close()` would block up to a full interval. **`close()`, not send** — closing broadcasts to every
receiver forever; a send wakes exactly one.

Cost, stated plainly: **`Cache` is now a resource, not a value.** A caller who forgets `Close()` leaks
a goroutine and everything the cache holds, and no compiler, vet check, or race detector will say so.

**Aayush: ✅ / ⊘** — first half exactly right (goroutine references the cache, GC won't collect it).
Second half not attempted; taught. → re-ask the `Close()`/`select`/`Ticker` mechanics cold.

---

## Carried forward — re-ask cold

| From | Concept | The specific gap |
|---|---|---|
| S4 Q2 | check-then-act | Can name the category; must *produce* the concrete interleaving |
| S4 Q4 | deadlock vs starvation | Mechanism solid, vocabulary wrong |
| S4 Q5 | publication / happens-before | Untested; prerequisite for Phase 3 write visibility |
| S4b Q2 | naive-timer overwrite bug | Expected a crash; the bug is **silent** deletion of live data |
| S4b Q3 | value vs resource semantics | Not attempted; sets up step 4 (corpses occupy LRU capacity) |
| S4c Q1 | **compare, don't remember** | **PATTERN, not incident** — same miss as S4 Q2. A value read before an unlock is a rumor after it. |
| S4c Q3 | `Close()` / `select` / `Ticker` | Knew *why* it leaks, not *what fixes it* |
