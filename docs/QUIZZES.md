# Quiz Bank

Question text + model answers, so quizzes can be re-asked cold weeks later. Scores live in
`docs/PROGRESS.md`.

**Conventions** — newest session at the top · ✅ correct · ⚠️ partial · ❌ wrong · ⊘ not attempted ·
⚠️/❌ entries name the *specific* gap, never just "revisit."

> Sessions 1–3 predate this file; flagged concepts are in `PROGRESS.md`.

---

## Session 9 — 2026-07-11 · Phase 5 + Phase 6 MILESTONE QUIZ

The per-phase review the ritual owed. 8 Q. **2 ✅ · 3 ⚠️ · 3 ⊘.**

**The through-line across the three ⚠️:** Aayush can state *what the code does* and stops one step short
of *the principle it instances*. "Cheap vs expensive" but not "reversible vs irreversible"; "ownership is
deterministic" but not "the ring promotes the next clockwise node with nobody deciding"; "LWW or vector
clocks" but not "the heal skips the conflict and preserves it forever." Machinery solid, framing thin.
The one genuine *gap* (not vocabulary): **presence ≠ version.**

**Q0 (cold re-ask, Session 7's ⚠️) — Why must `Snapshot()` not update recency? — ✅.**
Named the failure mode this time ("same problem as a sequential scan; keys lose the recency they earned
from real access"). **Sharpened:** keys don't *lose* recency, they all *gain* it at once — identical
outcome, because a recency ordering is information held in the **differences** between entries. Promote
everything and the LRU tail becomes whatever the heal's map iteration touched first, so the next eviction
picks its victim from the heal's traversal order rather than user behavior. Worse than a client scan: a
heal touches **every** primary key, on **every** membership change — the cache polluted by its own
maintenance, exactly when it most needs to be healthy. **The rule:** an internal bulk scan must never be
indistinguishable from user access; maintenance paths (heal, snapshot, sweeper) read *around* the policy,
not through it. That is why `Snapshot()` exists instead of a loop over `Get()`.
**Carried-forward debt from Session 7 is now CLOSED.**

**Q1 — Who heals; and who repopulates `k` when `k`'s primary dies, with no election? — ⚠️.**
Got primary-only and its cost correctly: without it, all R owners push to the other R−1, so each key is
copied R×(R−1) instead of R−1 — converges anyway (idempotent overwrite), at 3× the bandwidth.
**Missed the promotion mechanism.** `ownersFor()` is computed against the **alive ring**. Remove `n2` and
`GetClockwiseN(k,3)` returns a *different list*: the old replica #1 is now at index 0 — **it is the
primary**, it already holds a copy (R=3 put one there), and its next `heal()` sees `owners[0].id == n.id`
and pushes to the new owner set. **Nobody assigned the role. Removing a node from a sorted ring promotes
the next clockwise node by construction** — the promotion is a side effect of the data structure, not a
decision. That is what "no coordinator, no election" cashes out to in code.
**Also sharpened:** his "eventually all nodes agree on membership" is doing too much work — there is **no
consensus**; a partitioned node can disagree *forever*. What saves us is that disagreement is **safe**:
two nodes both believing they're primary both push, and the pushes are idempotent. We *tolerate* divergent
views rather than prevent them. AP being AP.

**Q2 — State the rule that decides which reaction fires instantly and which waits. Name the price. — ⚠️.**
Gave one of the two properties ("cheap vs expensive," "more evidence to pay a big cost"). **Missed
reversibility**, which is the load-bearing half. `ring.Remove` is cheap **and undoable** — `ring.Add` puts
it back and *nothing happened*. Re-replication is expensive **and unrecoverable** — you cannot un-send a
copy; the bandwidth is spent whether or not the premise was true.
> **React to a suspicion immediately with anything cheap and reversible. Make anything expensive or
> irreversible wait for confirmation.** The evidence threshold is set by the cost of being *wrong*, not
> by the cost of the action.

**Did not name the price:** a *genuine* death now heals in **~1.55s vs ~550ms**. Storm-immunity bought
with ~1s of extra under-replication exposure — Session 7's Q6 made concrete: *every mitigation that delays
a wrong conviction delays a correct one by exactly as much, because a slow node and a dead node are the
same silence.*

**Q3 — Check-first should make a false positive cost 0 copies. It cost ~49. Why? — ✅.**
The hardest question on the page, answered cleanly from the mechanism, unprompted. Removing the paused
node from the ring **shifts the owner set**: `GetClockwiseN(k,3)` now returns a node that was *not*
previously an owner. That **newcomer genuinely does not have the key**, so check-first gets a genuine 404
and does genuinely-necessary work. The copies are not redundant — they are **correct given a false
premise.** Hence the two mitigations are not substitutes:
- **check-first** eliminates work that is *redundant* (the copy already exists);
- **the grace period** eliminates work that is *correct but predicated on a lie*.

**Q4 — A revived node holds a *different* value for `k`. Trace the heal, the client read, and the fix. — ⚠️.**
Named LWW / vector clocks (correct fix vocabulary) but did not run the mechanism, and the mechanism is
uglier than he assumed. `node.go:395` — `if _, has, err := n.fetchFrom(...); err != nil || has { continue }`
— **`has` is presence, not version.** The revived node answers *"200, I have it"* → the heal **skips it**.
The heal neither creates nor resolves the divergence: it **silently preserves it forever**, and no
anti-entropy pass ever revisits that key.
And the read is not "the primary serves stale" — `handleClientGet` (`node.go:496`) returns **the first
reachable hit in ring order**, so which of the two conflicting values a client sees is decided by **ring
geometry**, an accident of where `sha256(key)` landed. Worse than random because it's *stable*: the
cluster will confidently serve one value forever while a different one sits on another node, unnoticed.
**Fix = three parts, he named one:** (1) version every value (LWW timestamp, or a vector clock if you want
to *detect* concurrency rather than silently pick); (2) heal compares **versions, not presence** — that is
what turns the heal into a real **anti-entropy** pass; (3) **read-repair**, or the cluster only converges
on membership *changes* and a stable cluster never converges at all.
**GAP (the only real one this quiz): presence ≠ version.** `has == true` means "somebody has *a* value,"
not "somebody has *the* value." The entire AP staleness story lives in that gap.

**Q5 — Why is `Cluster.State()`'s god's-eye view impossible in a real deployment? — ⊘ (taught).**
It reaches into every node's cache and ring **from one process at one instant** — possible only because the
nodes are goroutines sharing an address space. Really distributed, **there is no such instant**: five
machines means five messages and five replies describing five *different moments*, each a fact about the
past by the time it lands. No observer stands outside the system.
Recall Phase 4: **each node's `alive` view is its own, and views legitimately disagree.** So a real
dashboard cannot show "the ring" — **there is no such object**. It must poll each node for *its own view*
and render N of them, and during the ~500ms detection window it would show them **contradicting each
other**: `n0` says `n2` is dead and has re-routed; `n2` says it is fine and is still serving; `n3` hasn't
timed out yet and still routes to `n2`. **All three are correct.** There is no fact of the matter about
whether `n2` is dead — ***dead* is not a property of a node, it is a belief held by an observer.**
The honest dashboard shows a **union of beliefs**; the flickering disagreement during a failure is not a
rendering bug, it *is* the distributed system. → **Belongs in the writeup next to the cluster-in-a-box
caveat: same honesty, one layer up.**

**Q6 — Why is even-spaced node placement honest, and what must keep its true hash angle? — ⊘ (taught).**
**A node does not have a position on the ring.** It has ~150 (`defaultReplicas = 150`), scattered by
`hashKey(node+"#"+i)` — the scattering *is* the mechanism that took the load span from **65× to 1.4×**.
"Where is `n2`?" has **no answer**. Drawing it at `hash("n2")` picks one arbitrary point out of 150 and
dresses it up as *the* location: precise-looking and meaningless. (Empirically it also clustered n0/n3/n4
at the bottom, which reads as "those nodes are neighbors" — false, and false in a way that undermines the
lesson the ring exists to teach.) Even spacing claims nothing; it is a legend.
Two things **must** keep their true angle: the **~150 virtual-point ticks** (they are the real load
distribution — faking them fakes the property Phase 2 spent itself measuring) and the **key dots** (a key's
angle *is* its identity, `sha256(key)`; the arc it lands in decides its owner, so the ownership links are
meaningful only because the angles are real).
> **Fake the thing that has no true value; never fake the thing whose value is the mechanism.**

**Q7 — How would you make the heal O(under-replicated keys)? What must you track, and what does it cost? — ⊘ (taught).**
Today: O(primary keys) × (R−1) round-trips on **every** membership change — 10k keys at R=3 is 20,000
`fetchFrom` calls to discover ~50 needed copies. Check-first made it cheap in *bytes*; it did nothing for
**chattiness**.
The ring already knows which keys moved: when `d` dies, the only keys whose owner set changed lie in **the
arcs `d` owned** (the ~150 arcs ending at its virtual points). Everything else maps to the same three nodes
as before, and re-checking it is pure waste. **Sketch:** (1) diff old ring vs new → the changed arcs;
(2) scan the primary keyset **restricted to those arcs**; (3) push straight to the newcomer — **no presence
check needed**, since a node that was not an owner cannot have the key.
**What you'd have to track:** the cache is `map[string]*node` — no hash order, so "keys in arc [a,b)" is
unanswerable without a full scan. You need a **hash-ordered index**, maintained on every `Set` and every
eviction.
**The tradeoff (this is the actual answer):** you make the **rare** path (a death) cheaper by taxing the
**hot** path (every `Set`, forever) with an ordered-index insert — plus a second structure that can drift
out of sync with `data`, a bug class that already cost us `TestExpiringIndexStaysConsistent`. **Deaths are
rare; `Set`s are not.** By this project's own rules: **don't build it until a measured heal is too slow.**
Same verdict as segmented LRU, reached the same way.

---

## Session 7 — 2026-07-10 · Phase 5 quick-checks (self-heal)

Asked after teaching each design piece, before/around the build. Running tally below.

**After Q1 (who heals) — 3 Q's: 3 ✅.** (1) ownership-vs-data: the ring re-points owners for free; the
heal must still *copy the bytes* to the promoted owner (redundancy/fault-tolerance restored). (2) not a
dedicated healer: avoids consensus-among-healers (SPOF→CP), and a global keyset no node has. (3) no
election: ownership is a deterministic function of (membership view, key), so nodes agree without
talking. **Sharpened on (3):** the "if all agree on the view" precondition is load-bearing — Phase 4
gives *independent* views, so determinism only yields agreement *to the degree views have converged*;
divergence (false positive/partition) makes different nodes confidently do *conflicting* heals. That
"if" is exactly why Q3 (the storm) exists.

**After Q2 (which keys) — 1 Q: ✅.** "For each key, compare owners before vs after removal; the changed
ones lost a replica, the newly-appearing node is the target." Correct. Layered on: scan only *local*
keys (every under-replicated key is held by ≥1 survivor since R≥2, so local scans collectively cover
everything with no global index), and only the *primary* pushes (else co-owners double-send).

**After the step-1 build — 2 Q's: 1 ✅ · 1 ⚠️.**
- **Q1 (why heal in its own goroutine, not inline in heartbeatRound) — ✅.** Copying is slow I/O; a
  synchronous heal would keep the heartbeat goroutine from pinging → it declares *more* false deaths →
  more heals. **Self-reinforcing loop**; decoupling breaks it. (Nailed the feedback loop.)
- **Q2 (why Snapshot must not update recency) — ⚠️.** Got the direction ("recency disturbed") but the
  mechanism wrong ("random map-iteration order randomises it"). **Correct:** order is irrelevant; the
  problem is a heal touches *every* key, so promoting all of them marks the whole cache MRU at once,
  flattening the recency signal. **This is the Phase-1 sequential-scan LRU pollution** — a background
  heal would evict the hot working set it is trying to protect. Named the failure mode, not just the
  symptom, is the gap.

---

## Session 7 — 2026-07-10 · cold re-ask of the two carried-forward Q's (Q4, Q6)

Taken cold at session start, before any Phase 5 teaching, per the ritual. **Q4 ✅ · Q6 ⊘ (taught).**

| Q | Concept | | The gap |
|---|---|---|---|
| 4 | self-suspicion & split-brain | ✅ | (a) Clean: node is authority on its own liveness, hard-sets `alive[self]=true`, never self-suspects. (b) Full split-brain chain (partition → both AP sides accept writes → concurrent same-key writes conflict → stale reads → consistency lost). **Sharpened:** the *conflict* isn't the loss; the loss is at **reconciliation** — LWW keeps the newer copy and **silently discards the older acked write**. Carry it one step to where the byte vanishes. |
| 6 | reduce false positives + the universal tradeoff | ⊘ | Third blank (S6 ×1, this ×1 as re-ask, plus S6 note). **Taught, not attempted.** Levers: longer timeout / N-consecutive-misses / indirect probing / suspicion+refutation / phi-accrual. **The tradeoff (the actual point):** every mitigation reduces false positives by gathering more evidence or waiting longer — which delays *correct* convictions exactly as much as wrong ones, because at decision time a slow node and a dead node are the same silence. Detection speed and accuracy are one dial pointed opposite ways. This is Q1's impossibility seen from the other side. |

**Carry into Phase 5 wiring:** Q6's tradeoff isn't academic — a false positive that triggers a full
**re-replication storm** (copy a whole range to restore R) turns "a few failed hops" into "gigabytes
copied for nothing." One of the three self-heal design questions.

---

## Session 6 — 2026-07-10 · Phase 4 milestone quiz (failure detection)

**2 ✅ · 2 ⚠️ · 2 ⊘.** Taken cold at phase end. Q4 was left blank despite being walked through live
minutes earlier.

| Q | Concept | | The gap |
|---|---|---|---|
| 1 | three causes of silence | ✅ | Crash, GC pause, network delay on the reply path. All three. They're indistinguishable because all produce the *identical* observation — no reply in time; there is no "I'm slow" message. |
| 2 | the timeout knob, both ways | ⚠️ | 50ms → false positives ✅. 5s: right conclusion ("death learnt only after 5s") but **muddled mechanism** — said a dead node *"holds the connection open 5s."* A crashed node refuses the connection *instantly*; each ping fails fast. The 5s delay is the **`lastSeen` threshold** withholding the *declaration*, not a hanging ping. Real cost: ring routes to a corpse for 5s (failed hops, under-replicated writes). Per-ping hang only happens on a node that accepts TCP but never replies. |
| 3 | independent views vs forced agreement | ✅ | Nailed the chain: force agreement → coordinator → SPOF → redundant coordinator needs **consensus**. Plus the availability point. Sharpened: the requirement is *consensus*, and consensus is *what makes a system CP* — it needs a majority quorum, so the minority partition must stop serving. |
| 4 | self-suspicion & split-brain | ⊘ | Not attempted (despite the live demo minutes before). (a) A node is the **authority on its own liveness** — suspicion comes from `lastSeen` of *inbound* replies; it never pings itself, hard-sets `alive[self]=true`; a dead node isn't running to mark itself. (b) n0-says-dead / n1-says-alive with no reconciler → if it hardens (partition), both sides serve, writes diverge → **split-brain**, LWW silently drops a loser = data loss. |
| 5 | scaling the detector | ⚠️ | Named "gossip or SWIM" but **not the mechanism**. The change: today n0 learns of n1's death by *directly pinging n1*; under gossip it learns **second-hand** — pings a few random peers, the fact propagates transitively (rumor spread), O(N) not O(N²). The *label* isn't the answer; the *how* is. |
| 6 | extend it — reduce false positives | ⊘ | Not attempted. Any one: indirect probing (SWIM), suspicion+incarnation refutation, N-consecutive-misses / longer timeout, phi-accrual. **Universal tradeoff: every false-positive mitigation slows detection of real deaths** — more evidence/waiting delays the right convictions too. |

**Through-line flagged:** the two ⚠️ and the Q5 miss are the same habit — **naming the label, not the
mechanism.** "Gossip"/"SWIM" are labels; *how a node learns second-hand* is the answer. Q2 named the
right outcome but the wrong *why*. Push for the mechanism on re-ask. The genuinely hard ones (Q1, Q3)
were clean, so this is a *precision* gap, not a comprehension gap.

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

**Open (from the Session 9 milestone quiz, 2026-07-11):**
1. **Presence ≠ version** (S9 Q4) — the only genuine knowledge gap on the board. Re-ask cold: *"A revived
   node answers `200, I have it` for key `k`, but its value differs from the primary's. What does
   `heal()` do, and what does a client read return?"* Looking for: the heal **skips** it (`has` is
   presence, not version) and thus **preserves the conflict forever**; the read returns the **first
   reachable owner in ring order**, so ring geometry decides — stably, silently.
2. **Reversibility, not just cost** (S9 Q2) — re-ask: *"State the rule for which reaction to a suspected
   death fires instantly and which waits. Two properties."* He reliably produces "cheap/expensive" and
   drops "reversible/irreversible."

**Retired 2026-07-10 at Aayush's request — do not re-ask.** The Phase 1 carry-forward list
(check-then-act, compare-don't-remember, starvation mechanism, happens-before, resource semantics,
expiry-aware eviction, admission control) is closed. The concepts remain taught and are recorded in
the session logs above; several were also *demonstrated in code* during Phase 1 (expiry-aware eviction
in `evictLocked`, resource semantics in `Close`, admission control as the reason segmented LRU was
deferred). If any resurfaces as a real gap during Phase 2+, treat it as new material then, not as a
debt to collect. Start a fresh carry-forward list from Phase 2 quizzes.
