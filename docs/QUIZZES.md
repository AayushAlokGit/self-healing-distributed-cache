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

## Carried forward — re-ask cold

| From | Concept | The specific gap |
|---|---|---|
| S4 Q2 | check-then-act | Can name the category; must *produce* the concrete interleaving |
| S4 Q4 | deadlock vs starvation | Mechanism solid, vocabulary wrong |
| S4 Q5 | publication / happens-before | Untested; prerequisite for Phase 3 write visibility |
