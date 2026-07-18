# Quiz Bank

Question text + model answers, so quizzes can be re-asked cold weeks later. Scores live in
`docs/PROGRESS.md`; the build narrative and the measurements live there too — this file points rather
than re-tells.

**Conventions** — newest session at the top · ✅ correct · ⚠️ partial · ❌ wrong · ⊘ not attempted ·
⚠️/❌ entries name the *specific* gap, never just "revisit."

> Sessions 1–3 predate this file; flagged concepts are in `PROGRESS.md`.

---

## Session 18 — 2026-07-17 · presence≠version read half (cold) + vector-clock quick-checks

**Q1 — Presence ≠ version, the READ half (S9/S17 carried #1, cold re-ask) — ✅. Gap CLOSED.**
*"A key `k` holds two different values on two nodes (`n0`=apple, `n2`=banana). A client `GET k`: which value,
what decides it, is it stable across repeats, and does it matter which node the client hit?"*
Model: with no version, the read returns the **first reachable owner in ring order** — ring geometry decides;
it's **deterministic and node-independent** because every node computes the same ring; and the answer can be a
**stably-wrong (older) value forever**.
> Aayush: first reached for versions ("return the larger version") — the *fix*, not today's behaviour. On the
> re-ask he landed it: *"it does not matter which node — the ring geometry is the same across all nodes, since
> the membership view of each node is highly consistent."*

- ✅ **The S17 error is gone.** S17 he said coordinator-dependent; now he says node-**independent** and names
  **ring geometry** as the decider — and volunteered the precondition (*membership views agree*), which is
  precisely the seam 7A opens. Both halves of presence≠version now solid.

**Q2–Q4 — Vector-clock quick-checks (during teaching, not cold) — 3/3 with one correction.**
- **Q2 (read a vector):** `[2,1,0,0,0]` = 3 writes in this value's history, 2 at n0, 1 at n1. ✅
- **Q3 (write rule):** `n2` overwrites `[2,1,0,0,0]` → `[2,1,1,0,0]`. Number ✅. **Correction:** he generalised
  to "the coordinator only bumps its own slot, ignores the other slots." True *here* (single predecessor, so
  `max` is a no-op) but **wrong in general** — resolution merges *multiple* siblings, and skipping the `max`
  yields a resolved clock that fails to dominate a sibling it didn't merge, so the loser resurfaces forever.
  **Merge, then bump — always.**
- **Q4 (dominance):** `x=[3,1,0,0,0]` vs `y=[3,0,0,0,2]` → concurrent (x has slot1, y has slot4). ✅ (labels
  off-by-one — called them n5/n2 — slot logic right; fixed to 0-indexed n0..n4.)

---

## Session 17 — 2026-07-16 · cold re-ask before 7A (4 Q) + 2 follow-ups

4 Q cold. **0 ✅ · 3 ⚠️ · 1 ⊘ (not understood → re-taught).** Follow-ups: **1 ✅ · 1 ⚠️.**

**Through-line:** the S9 pattern has *shifted*, not repeated. He no longer stops at "what the code does" —
he reaches for the principle unprompted. What he now does is **answer the sub-question he finds most
interesting and drop the others**: Q2 and Q3 each asked three things and got one. Not a knowledge gap;
a completeness habit. Worth naming to him rather than re-teaching.

**Q1 — Presence ≠ version (S9 Q4, cold re-ask) — ⚠️ partial. The gap NARROWS but does not close.**
*"A revived node answers `200, I have it` for key `k`, but its value differs from the primary's. What does
`heal()` do, and what does a client read return?"*
Model: the heal **skips** it (`has` is presence, not version) and **preserves the conflict forever**; the
read returns the **first reachable owner in ring order** — ring geometry decides, stably and silently.
> Aayush: *"Heal will ignore the value differen and client read value depends on the corridnating cluster"*

- ✅ **Heal half: right.** He has it. Minor: "ignore" undersells it — the heal doesn't defer, it can never
  fix this, and no anti-entropy pass revisits the key.
- ❌ **Read half: wrong.** Verified against the code before grading: `handleClientGet` does
  `owners := n.ownersFor(key)` then walks them **in ring order**, returning the first hit. The coordinator
  reads locally only when it *is* the owner at that rank — that changes who does the I/O, not the order.
  Every node computes the same ring, so **every coordinator returns the same value.**
- **GAP (specific):** he thinks the answer is **coordinator-dependent**. It is **ring-dependent**, and that
  is worse: coordinator-dependent would look random and get *reported*; ring geometry makes it **stable** —
  the same wrong value, every read, forever. *A conflict that flickers gets filed as a bug; a stable one
  looks like the truth.* The coordinator matters in exactly one way (`CAP.md` §7: "only through how many
  owners it can reach") and that is **under a partition**, not here.
- **Re-ask at S18, the read half only.** The heal half is done.

**Q2 — The cost of W (`CAP.md` §14 Q1, cold) — ⚠️ partial.**
*"Raising `W` 1→2: what does it give, what does it cost, and is any cost paid **even on a perfectly healthy
network**?"*
> Aayush: *"It reduces availability of system fro writes in event of parition, with W=2 and R=2 and 2 sides
> only one side can accept a write. This is for the case with pariiton, for a healthy netowrk it could still
> cause issue due to concurrent writes to different nodes."*

- ✅ Partition half right — the pigeonhole (§7).
- ⚠️ **Framed the benefit as a cost.** "Only one side can accept" *is* what you are buying; refusing the
  second side is the point, not the price.
- ❌ **Named no benefit at all** — `W+R_read = 4 > R=3` ⇒ read and write sets must overlap ⇒ **no stale reads**.
- ❌ **Healthy-network answer wrong.** He imported §10's concurrent writes, which are **not a cost of `W`**:
  they happen at `W=1` too and `W=3` doesn't fix them. The real everyday cost is **write fault-tolerance** —
  `W=1` survives 2 owners down, `W=2` survives 1.
- **Closed by follow-up A** (below), and he improved on the model answer doing it.

**Q3 — What a vector clock does NOT fix (`CAP.md` §14 Q2 — rewritten S17, first outing) — ⚠️ partial.**
*"Side A writes `bob`, side B writes `carol` — concurrent. The vector clock finds neither dominates and keeps
both. What did that fix, what did it **not**, and **who decides now, on what basis?**"*
> Aayush: *"A vector clock does not fix concurrent writes, it surfaces concurrent writes for the app to
> decide how to handle them."*

- ✅ **The ceiling is right** — detection ≠ resolution; the app decides. That is the load-bearing half.
- ❌ Skipped **"what did it fix"**: the *silence*. Nothing is destroyed, nobody is told `204` for a write
  that vanishes, and the clash is **detected** rather than invented — none of which Lamport can do, since it
  cannot tell `carol ∥ bob` from `carol → bob` (both are just "8 beats 6").
- ❌ Skipped **"on what basis"**: **none.** A real app merges by *meaning* (union carts, add counters); ours
  holds opaque text. "The app decides" is true; the honest version is *the app guesses, and there is no right
  answer to find.*
- **GAP:** states what a mechanism *doesn't* do and stops. Same shape as Q2 — one sub-question of three.

**Q4 — Colliding writes without a partition (`CAP.md` §14 Q3 — rewritten S17) — ⊘ not understood → re-taught.**
*"Two clients write `user:1` through n0 and n2 on a healthy network, both reach `W=2`. Why do you still end up
with two siblings, and why doesn't a bigger `W` fix it?"*
Re-taught with the trace: both writes get a real quorum, both overlap at n1 — and neither writer had *heard
of* the other, so no vector dominates. `W=3` changes nothing: both reach all three owners, both still never
met. **Quorums OVERLAP; they don't SERIALIZE.** Landed — see follow-up B.

### Follow-ups (after the re-teach)

**A — `W=1` vs `W=2` with two owners dead — ✅, and he beat the model answer.**
*"`W=1`, three owners, two dead. Does the write succeed? Now `W=2`, same two dead. Does it? What did you pay
for, and was anything broken?"*
> Aayush: *"Yes write succeeds since only one owner needs to ack though there is high chance that the write
> can be lost since other 2 are donw. With W=2 write will not succeed because one other node should ack but
> the other 2 owners are either dead or not reachable so write fails."*

- ✅ Mechanics exact, both directions.
- ✅✅ **His clause improves on §14's model answer, and is now folded into it, credited.** The doc framed the
  cost as *"write fault-tolerance — `W=1` survives 2 owners down, `W=2` survives 1"*, which makes `W=1` read
  as a free win. He spotted that the surviving `W=1` write lives on **one node** and is fragile. That reframes
  the whole trade:
  > **`W` is how much a `204` is worth.** An ack at `W=1` means *one machine has this*; at `W=2`, *two
  > machines have this*. `W=1` says yes more often and each yes means less. It is availability vs
  > **durability**, and it is paid on the healthy path.

**B — what would have to change for `carol` to know about `bob` — ⚠️ half.**
> Aayush: *"The writes must be serialised through one node. COncurrent writes through 2 different nodes
> introduce the problem"*

- ✅ **"Serialised through one node"** — right; that is a leader (`CAP.md` §13's third rung). One node **per
  key** — one node for everything would be a single point of failure and would throw the ring away.
- ✅ **"2 different nodes introduce the problem"** — right *for this system*: two writes through the same
  coordinator are ordered by its own lock, so the second merges the first's vector and dominates it. It takes
  two coordinators for neither writer to see the other.
- ❌ **Did not answer "why no `W` can be that thing"** — the half the question existed for. **`W` counts acks:
  a quantity. Concurrency is a relationship.** Raising `W` makes each write talk to more *nodes*; it never
  makes the two writers talk to *each other*. **You cannot fix an ordering problem by counting higher.**

---

## Session 9 — 2026-07-11 · Phase 5 + Phase 6 MILESTONE QUIZ

8 Q. **2 ✅ · 3 ⚠️ · 3 ⊘.**

**Through-line across the three ⚠️:** he states *what the code does* and stops one step short of *the
principle it instances*. "Cheap vs expensive" but not "reversible vs irreversible"; "ownership is
deterministic" but not "the ring promotes the next clockwise node with nobody deciding"; "LWW or vector
clocks" but not "the heal skips the conflict and preserves it forever." Machinery solid, framing thin.
The one genuine *gap* (not vocabulary): **presence ≠ version.**

**Q0 (cold re-ask of Session 7's ⚠️) — Why must `Snapshot()` not update recency? — ✅.**
Named the failure mode this time ("same problem as a sequential scan; keys lose the recency they earned
from real access"). **Sharpened:** keys don't *lose* recency, they all *gain* it at once — identical
outcome, because a recency ordering is information held in the **differences** between entries. Promote
everything and the LRU tail becomes whatever the heal's map iteration touched first, so the next eviction
picks its victim from the heal's traversal order rather than user behavior. Worse than a client scan: a
heal touches **every** primary key, on **every** membership change — the cache polluted by its own
maintenance, exactly when it most needs to be healthy.
> **An internal bulk scan must never be indistinguishable from user access.** Maintenance paths (heal,
> snapshot, sweeper) read *around* the policy, not through it. That is why `Snapshot()` exists instead
> of a loop over `Get()`.

**Session 7's carried-forward debt is CLOSED.**

**Q1 — Who heals; and who repopulates `k` when `k`'s primary dies, with no election? — ⚠️.**
Got primary-only and its cost: without it, all R owners push to the other R−1, so each key is copied
R×(R−1) instead of R−1 — converges anyway (idempotent overwrite), at 3× the bandwidth.
**GAP: missed the promotion mechanism.** `ownersFor()` is computed against the **alive ring**. Remove
`n2` and `GetClockwiseN(k,3)` returns a *different list*: the old replica #1 is now at index 0 — **it is
the primary**, it already holds a copy (R=3 put one there), and its next `heal()` sees
`owners[0].id == n.id` and pushes to the new owner set. **Nobody assigned the role. Removing a node from
a sorted ring promotes the next clockwise node by construction** — the promotion is a side effect of the
data structure, not a decision. That is what "no coordinator, no election" cashes out to in code.
**Also sharpened:** his "eventually all nodes agree on membership" does too much work — there is **no
consensus**; a partitioned node can disagree *forever*. What saves us is that disagreement is **safe**:
two nodes both believing they're primary both push, and the pushes are idempotent. We *tolerate*
divergent views rather than prevent them. AP being AP.

> **POSTSCRIPT (same session): Q1's model answer had a hole, and the code had it too.** → the
> stranded-key bug, PROGRESS Phase 5 + Session 9 log.
> The answer above is right about a primary that **dies**: the ring promotes the next clockwise node,
> and that node **already holds a copy**, so it can heal. **The mirror case is fatal.** When a primary
> **returns** (a revived node comes back empty), the ring promotes it straight back to primary of its
> own arcs — while it holds **nothing**. And "only the primary pushes" then means:
> - the **primary can't** push it (the key isn't in its `Snapshot()`; it has nothing to send), and
> - the **holders won't** push it (they have it, but they aren't the primary, so they stand down).
>
> **Nobody is both a holder and permitted to push. The key stays under-replicated forever** — no further
> membership change is coming to retrigger anything.
>
> **The fix, and the generalizable lesson: permission must follow the DATA, not the ring position.**
> The healer for a key is the **first owner, in ring order, that actually holds it**. That preserves what
> the primary rule existed for (exactly one sender ⇒ no duplicate copies) *and* guarantees a sender exists
> whenever anybody has the data. A node ranked below a holder stands down; a node ranked above one — or
> holding a key **no owner has at all** (a leftover from an older ring) — steps up.
>
> **Cold re-ask → carried forward #3.**

**Q2 — State the rule that decides which reaction to a suspected death fires instantly and which waits.
Name the price. — ⚠️.**
Gave one of the two properties ("cheap vs expensive," "more evidence to pay a big cost"). **GAP: missed
reversibility**, which is the load-bearing half. `ring.Remove` is cheap **and undoable** — `ring.Add` puts
it back and *nothing happened*. Re-replication is expensive **and unrecoverable** — you cannot un-send a
copy; the bandwidth is spent whether or not the premise was true.
> **React to a suspicion immediately with anything cheap and reversible. Make anything expensive or
> irreversible wait for confirmation.** The evidence threshold is set by the cost of being *wrong*, not
> by the cost of the action.

**GAP: did not name the price** — a *genuine* death now heals in **~1.55s vs ~550ms**. Storm-immunity
bought with ~1s of extra under-replication exposure. This is S7 Q6's universal tradeoff made concrete.

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
rendering bug, it *is* the distributed system. (→ PROGRESS, Next action (a): a writeup caveat.)

**Q6 — Why is even-spaced node placement on the dashboard honest, and what must keep its true hash
angle? — ⊘ (taught).**
**A node does not have a position on the ring.** It has **many**, scattered by `hashKey(node+"#"+i)` — the
scattering *is* the mechanism that took the load span from **65× to 1.4×**. (How many depends on which ring:
`ring.defaultReplicas = 150` in the library and the tests, but `cluster.demoRingReplicas = 8` in the demo the
dashboard draws — 750 ticks would be hair, not a diagram. The argument below does not care about the number,
only that it is **more than one**.)
"Where is `n2`?" has **no answer**. Drawing it at `hash("n2")` picks one arbitrary point out of its many and
dresses it up as *the* location: precise-looking and meaningless — and empirically it clustered n0/n3/n4 at
the bottom, which *reads as* "those nodes are neighbors": false, and false in a way that undermines the very
lesson the ring exists to teach. Even spacing claims nothing; it is a legend.
Two things **must** keep their true angle: the **virtual-point ticks** (they are the real load
distribution — faking them fakes the property Phase 2 spent itself measuring) and the **key dots** (a key's
angle *is* its identity, `sha256(key)`; the arc it lands in decides its owner, so the ownership links are
meaningful only because the angles are real).
> **Fake the thing that has no true value; never fake the thing whose value is the mechanism.**

**Q7 — How would you make the heal O(under-replicated keys)? What must you track, and what does it
cost? — ⊘ (taught).**
Today: O(primary keys) × (R−1) round-trips on **every** membership change — 10k keys at R=3 is 20,000
`fetchFrom` calls to discover ~50 needed copies. Check-first made it cheap in *bytes*; it did nothing for
**chattiness**.
The ring already knows which keys moved: when `d` dies, the only keys whose owner set changed lie in **the
arcs `d` owned** (one arc ending at each of its virtual points). Everything else maps to the same three nodes
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

Asked after teaching each design piece, before/around the build.

**After Q1 (who heals) — 3 Q: 3 ✅.** (1) **ownership vs data** — the ring re-points owners for free; the
heal must still *copy the bytes* to the promoted owner. (2) **not a dedicated healer** — it would need
consensus among healers (SPOF → CP), and a global keyset no node has. (3) **no election** — ownership is a
deterministic function of (membership view, key), so nodes agree without talking.
**Sharpened on (3):** the "if all agree on the view" precondition is load-bearing — Phase 4 gives
*independent* views, so determinism only yields agreement *to the degree views have converged*; divergence
(false positive/partition) makes different nodes confidently do *conflicting* heals. That "if" is exactly
why the storm question exists.

**After Q2 (which keys) — 1 Q: ✅.** "For each key, compare owners before vs after removal; the changed
ones lost a replica, the newly-appearing node is the target." Correct. Layered on: scan only *local* keys
(every under-replicated key is held by ≥1 survivor since R≥2, so local scans collectively cover everything
with no global index), and only the *primary* pushes (else co-owners double-send).

**After the step-1 build — 2 Q: 1 ✅ · 1 ⚠️.**
- **Q1 (why heal in its own goroutine, not inline in `heartbeatRound`) — ✅.** Copying is slow I/O; a
  synchronous heal would keep the heartbeat goroutine from pinging → it declares *more* false deaths →
  more heals. **Self-reinforcing loop**; decoupling breaks it.
- **Q2 (why `Snapshot` must not update recency) — ⚠️.** Got the direction ("recency disturbed") but the
  mechanism wrong ("random map-iteration order randomises it"). **Correct:** order is irrelevant; a heal
  touches *every* key, so promoting all of them marks the whole cache MRU at once, flattening the recency
  signal — **the Phase-1 sequential-scan LRU pollution**, and a background heal would evict the hot working
  set it is trying to protect. **GAP: named the symptom, not the failure mode.** → re-asked cold in S9 Q0:
  ✅, closed.

---

## Session 7 — 2026-07-10 · cold re-ask of the two carried-forward Q's (Q4, Q6)

Taken cold at session start, before any Phase 5 teaching, per the ritual. **Q4 ✅ · Q6 ⊘ (taught).**

| Q | Concept | | Model answer / the gap |
|---|---|---|---|
| 4 | self-suspicion & split-brain | ✅ | Same question as S6 Q4 — **model answer there.** Gave both halves clean. **Sharpened:** the *conflict* isn't the loss; the loss is at **reconciliation** — LWW keeps the newer copy and **silently discards the older acked write**. Carry it one step to where the byte vanishes. |
| 6 | reduce false positives + the universal tradeoff | ⊘ | Same question as S6 Q6. Third blank. **Taught, not attempted.** Levers: longer timeout / N-consecutive-misses / indirect probing / suspicion+refutation / phi-accrual. **The tradeoff (the actual point):** every mitigation reduces false positives by gathering more evidence or waiting longer — which delays *correct* convictions exactly as much as wrong ones, because **at decision time a slow node and a dead node are the same silence.** Detection speed and accuracy are one dial pointed opposite ways. |

Consequence carried into Phase 5: a false positive that triggers a full **re-replication storm** turns "a
few failed hops" into "gigabytes copied for nothing." → PROGRESS, Phase 5.

---

## Session 6 — 2026-07-10 · Phase 4 milestone quiz (failure detection)

**2 ✅ · 2 ⚠️ · 2 ⊘.** Taken cold at phase end. Q4 was left blank despite being walked through live minutes
earlier.

| Q | Concept | | Model answer / the gap |
|---|---|---|---|
| 1 | three causes of silence | ✅ | Crash, GC pause, network delay on the reply path. All three. They're indistinguishable because all produce the *identical* observation — no reply in time; there is no "I'm slow" message. |
| 2 | the timeout knob, both ways | ⚠️ | 50ms → false positives ✅. 5s: right conclusion ("death learnt only after 5s") but **GAP: muddled mechanism** — said a dead node *"holds the connection open 5s."* A crashed node refuses the connection *instantly*; each ping fails fast. The 5s delay is the **`lastSeen` threshold** withholding the *declaration*, not a hanging ping. Real cost: the ring routes to a corpse for 5s (failed hops, under-replicated writes). A per-ping hang only happens on a node that accepts TCP but never replies. |
| 3 | independent views vs forced agreement | ✅ | Nailed the chain: force agreement → coordinator → SPOF → a redundant coordinator needs **consensus**. Plus the availability point. Sharpened: consensus is *what makes a system CP* — it needs a majority quorum, so the minority partition must stop serving. |
| 4 | self-suspicion & split-brain | ⊘ | Not attempted (despite the live demo minutes before). (a) A node is the **authority on its own liveness** — suspicion comes from `lastSeen` of *inbound* replies; it never pings itself, hard-sets `alive[self]=true`; a dead node isn't running to mark itself. (b) n0-says-dead / n1-says-alive with no reconciler → if it hardens (partition), both sides serve, writes diverge → **split-brain**, and LWW silently drops a loser = data loss. |
| 5 | scaling the detector | ⚠️ | Named "gossip or SWIM" but **GAP: not the mechanism.** The change: today n0 learns of n1's death by *directly pinging n1*; under gossip it learns **second-hand** — pings a few random peers, and the fact propagates transitively (rumor spread), O(N) not O(N²). The *label* isn't the answer; the *how* is. |
| 6 | extend it — reduce false positives | ⊘ | Not attempted. Any one: indirect probing (SWIM), suspicion + incarnation refutation, N-consecutive-misses / longer timeout, phi-accrual. **Universal tradeoff: every false-positive mitigation slows detection of real deaths.** (Re-asked S7, blank again → full model answer in the S7 row above.) |

**Through-line flagged:** the two ⚠️ and the Q5 miss are one habit — **naming the label, not the
mechanism.** The genuinely hard ones (Q1, Q3) were clean, so this is a *precision* gap, not a comprehension
gap. Push for the mechanism on re-ask.

---

## Session 5 — 2026-07-10 · cold re-ask of the nine carried-forward questions

No re-teaching first, as the ritual requires. **2 ✅ · 5 ⚠️ · 1 ❌ · 1 ⊘.**

| Q | Concept | | Model answer / the gap |
|---|---|---|---|
| 1 | check-then-act (`GetOrRefresh`) | ❌ | Answered *"there is no lock for `c.Set()`"* — but `Set` **does** lock. Went hunting for a missing lock. **Every access is synchronized and the function is still wrong.** **GAP:** instinct is *"unsynchronized access → bug"*; the needed instinct is *"decision made under a lock, acted on after the unlock → bug."* |
| 2 | deadlock vs starvation | ⚠️ | Deadlock ✅. **GAP: starvation defined backwards** ("blocked briefly, not indefinitely" = ordinary waiting). Correct: *postponed indefinitely **while the system as a whole progresses***. Deadlock is global; starvation is local and looks like health. Mechanism unnamed → see below. |
| 3 | happens-before | ⊘ | Not attempted. Model answer taught below. |
| 4 | naive-timer overwrite bug | ✅ | Including *"the first timer does not check the expiry time."* Added: the deletion is **silent** — a crash would be a gift. |
| 5 | value vs resource | ⚠️ | Opened *"heap allocation makes a type a resource"* — **wrong**; `[]byte` is heap-allocated and is a value. Correct: **a resource owns something the GC cannot reclaim** (fd, socket, goroutine). Named the sweeper's reference ✅ but **GAP: missed the link that makes it unfixable — a running goroutine's stack is a GC root.** |
| 6 | compare, don't remember | ⚠️ | Recited the slogan word-perfect; **GAP: produced no interleaving.** Knowing the slogan ≠ being able to run the argument. |
| 7 | `Close()` / `select` / `Ticker` | ✅ | All three. Sharpened: `close(done)` is the **request**, `wg.Wait()` the **confirmation** — the sweeper may still be inside `samplePass` holding the mutex. |
| 8 | expiry-aware eviction | ⚠️ | Right victim (`config`), then gave **Q9's** principle. **GAP:** no scan here; a second access to `config` wouldn't help — it would still be less recent than 999 corpses. Principle: **recency of use and freshness of value are independent orderings**, anti-correlated on session workloads. |
| 9 | scan resistance | ⚠️ | 3 of 4 families (segmented LRU, LFU+decay, TinyLFU); *described* admission correctly. **GAP: missed hinting** (PG ring buffer, `MADV_SEQUENTIAL`), and never **named** the reframe: **LRU has no admission policy — it admits every key unconditionally; the only decision it makes is who to evict.** |

**Through-line across Q1, Q4, Q6** — all one bug: **a second party holding a stale opinion about a key, and
acting on it.** Q4's is a **timer**, Q6's a **remembered slice**, Q1's a **local `e` plus the `load()`
result**. Fix is always the same: carry nothing across the release; re-read under the lock.

**Q2's missing mechanism — starvation mode.** A naive mutex is a free-for-all: a goroutine already on a CPU
beats a parked one, which must be woken (**barging**). Go's `sync.Mutex` flips into **starvation mode** once
a waiter has blocked **>1ms**: `Unlock` then hands the lock *directly* to the queue head and arrivals go to
the back. `TestSweepStallsReaders`' 8,769 gets (vs 0) exist only because of this.

### Q3's model answer — happens-before, taught

Source order guarantees **nothing** across goroutines, for two stacked reasons:
1. **The compiler reorders.** Its contract is not "run statements in order" but *"a single goroutine,
   reading only its own writes, cannot tell."* Goroutine B can tell.
2. **The CPU reorders.** Each core has a **store buffer**; writes drain later, not in issue order.

```go
config = loadConfig()   // a *Config — a whole struct
ready  = true           // one byte
```
B spins on `ready`, then reads `config.Timeout`. It can see `ready == true` and a **non-nil `config` whose
fields have not landed.** No panic, no race report. `Timeout == 0` once a week in production.

**Happens-before is not about time.** It is a *visibility guarantee*: if X happens-before Y, everything
written as of X is visible to whoever performs Y. It is **manufactured** — the compiler may not reorder
across it and the CPU emits a **memory barrier**. Edges come only from synchronization ops:
`Unlock`→a later `Lock` · `close(ch)`→a receive observing it · a send→its receive.

⚠️ **Go's `sync/atomic` is sequentially consistent**, so an atomic `ready` *does* publish `config`. **Do not
port this.** C++'s `memory_order_relaxed` is atomic with **no** happens-before edge. **Atomicity and
visibility are separate properties**; Go bundles them, C++ makes you ask separately.

So a mutex gives **two** things: **mutual exclusion**, and **a publication barrier** — everything A wrote
before `Unlock` is visible to B after `Lock`, *including memory unrelated to the protected data*. That is
why `c.mu` protects `c.data` though the buckets live elsewhere on the heap. **The mutex does not wrap the
map; it creates an ordering edge over all memory.**

---

## Session 5b — 2026-07-10 · O(1) eviction (before the code)

Phase 1, step 5. **3 ✅ · 1 ⚠️.**

**Q1 — Make the list singly linked to save 8 bytes. Which operation becomes O(n)? — ✅.**
`remove(n)` **given a pointer to `n`** — not removal in general. Unlinking needs `n.prev` rewritten, and a
singly linked node doesn't know who points at it, so you walk from the head. `prev` exists for exactly one
reason: to make `Get`'s move-to-front O(1). (Evicting the *tail* alone would be fine.)

**Q2 — Why `map[string]*node` and not `map[string]node`? — ✅.**
Map values are **not addressable** (`c.data[k].next = x` doesn't compile), and worse, **a rehash moves values
to new addresses**. A linked list is a web of pointers *between* nodes; if nodes lived in map buckets, growing
the map would dangle every one. **Values that other values point at need stable identity, and map values have
none.** Bonus: a `*node` is addressable, so `Get` stopped writing the entry back — `BenchmarkGet` **61.31 →
52.52 ns**.

**Q3 — Construct a workload where option (c) (evict the tail, let the sweeper cope) serves 0% hit rate
forever, despite the sweeper running every second. — ⚠️.**
Touch the hot key, then bury it under enough *new* `Set`s of short-TTL keys that it reaches the tail and dies
before it's read again. Two corrections: they must be **inserts**, since only an insert into a full cache
evicts and only a `Set`/`Get`-hit refreshes recency (reading a corpse *deletes* it). And the real answer is a
**timing** argument: **eviction happens at `Set` rate, corpse reclamation at the sweeper's tick rate, and
those are decoupled by six orders of magnitude.** A thousand `Set`s land in ~100µs; the sweeper wakes once a
second. Within one tick the cache can evict live keys a thousand times over while reclaimable corpses sit
there. Making the sweeper faster doesn't fix it — you'd have to run it *between every pair of `Set`s*, at
which point it isn't a sweeper, it's `evictLocked`.
**GAP: right shape, missed that it is fundamentally about decoupled rates.**

**Q4 — Why does peeking a min-heap on `expires` prove no corpse exists? — ✅.**
The root is the global minimum, so `root.expires > now` proves `expires > now` for **all n entries** — an O(1)
statement about the entire population. A sample can never do that: **absence of evidence is not evidence of
absence.** What the alternative trades away is not space but **time and complexity** — O(1) with no per-node
heap index, vs O(log n) plus a `heapIndex` maintained through every sift. (The trade was misnamed as "space.")

---

## Session 4d — 2026-07-09 · Eviction / LRU (before the code)

Phase 1, step 4. **3 ✅ · 1 ⊘.**

**Q1 — What does LRU substitute for Bélády's future knowledge? — ✅.**
**Temporal locality**: a key used recently is likely to be used again soon — the recent past as a proxy for
the near future. Good proxy: sessions, hot rows, popular products. Bad proxy: a **sequential scan**, where
each key is touched once and never again. (He produced the scan counterexample *unprompted, before it was
taught*.)

**Q2 — Capacity 3, `{a,b,c}` (`a` least recent). `Get(b)`, `Set(d)`, `Get(a)`? — ✅.**
`Get(b)` → order `a,c,b`. `Set(d)` evicts `a`. So `Get(a)` **misses**. That last line is the point:
**eviction is not cleanup; it is a decision about which future request you are willing to lose.**

**Q3 — `lastUsed time.Time` + a scan for the minimum: what's the cost? — ✅.**
**O(n) per eviction**, under the lock. Worse than `sweepAll`: that ran once a second on a *background*
goroutine (2.5% of wall time). An eviction scan runs **on the write path, on every `Set`, once full** — and
full is a cache's *normal steady state*. ~25ms per `Set` at 1M entries. Fix: hash map + doubly linked list →
O(1) lookup / move-to-front / evict-tail.

**Q4 — Capacity 1000: 999 corpses + 1 live key. A `Set` arrives. What's evicted? — ⊘.**
**The live key.** Naive LRU sorts by `lastUsed`; a `Set` *is* an access, so every corpse outranks the live
key. The cache is now **worse than empty** — 1000 slots serving nothing. Fix: **reclaim a corpse before
evicting anything live.** Free the capacity that costs nothing to free first.
Not attempted, but he asked exactly the right question ("does naive LRU evict only from unexpired keys?").

### Aayush's two challenges (both changed the design)

**(a) "Won't LRU evict the corpses anyway? Aren't they least-recently-used most of the time?"**
No — **recency and expiry are independent orderings** (worked out in full → PROGRESS, Phase 1, corpse-first
eviction). And "usually right" is not a bound — not when the check costs one timestamp comparison. → expiry-
aware from commit one.

**(b) "Why is eviction happening in `Get`? Eviction is a `Set`."**
Correct; my trace was wrong. **Our `Get` never inserts.** This is a **cache-aside (look-aside)** store like
Redis/memcached — the *application* does `Get` → miss → `db.Query` → `Set`. (A **read-through** cache —
Caffeine, Guava `LoadingCache` — takes a loader and populates itself; there `Get` really evicts.) The
consequence is sharper, not weaker: the cache sees ten ordinary `Set` calls with no flag saying "batch job."
**Pollution originates in the caller's fill pattern**, so scan resistance must live in the eviction policy —
the only place that can tell a hot key from a scan artifact.

Division of labour: `Get` **hit** updates recency (→ `Get` is a writer, *third* reason) · `Get` **miss** does
nothing · `Set` updates recency **and** evicts.

---

## Session 4c — 2026-07-09 · The sweeper (before the code)

Phase 1, step 3b. **1 ✅ · 1 ⚠️ · 1 half.**

**Q1 — Why can't the sweeper unlock every 1,000 keys to shorten the pause? — ⚠️.**
*Minor:* **Go randomizes map iteration order on every `range`.** No cursor, no "resume from key 1,000" —
restart and you revisit some keys, miss others.
*Fatal:* keep the `range` alive and unlock inside the body, and during the gap someone calls
`Set(k,"fresh",time.Hour)`. The sweeper relocks, looks at `e` — **the copy read before the gap**, still
expired — and deletes a value with an hour left. The naive-timer bug in different clothes.
**Anything read before releasing a lock is a rumor by the time you reacquire it. Compare, don't remember.**
Redis's sampling sidesteps this: each pass is stateless and holds the lock start to finish — no gap.
**GAP: found the iteration-order problem (real, correctly reasoned); missed the fatal stale-read deletion.
Second occurrence of this exact miss** (S4 Q2) → pattern, not incident.

**Q2 — Why sample only keys *with* a TTL? — ✅.**
Sampling estimates a population; the population of concern is *expirable* keys. 10M permanent config keys +
1,000 TTL'd sessions: 20 uniform draws yield an expected **0.002** TTL'd keys. Every sample reads "nothing to
reclaim," and the sessions rot forever. The estimator isn't noisier — it's **biased into uselessness**. Hence
Redis's separate `db->expires` dict: **the data structure exists to serve the sampling requirement.**
(Nailed the statistical core unprompted; didn't name the failing workload or the index consequence.)

**Q3 — Why can't the Cache be GC'd while the sweeper runs, and what must the API add? — ✅ / ⊘.**
**Every running goroutine's stack is a GC root.** The sweeper's stack holds `c`, so `c` is reachable *by
definition*. And it's mutual: the goroutine never exits, so its stack never stops being a root. **Two leaks
holding each other up.** `runtime.SetFinalizer` can't help — a finalizer runs on unreachability, which is
exactly what cannot happen.
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
Cost, stated plainly: **`Cache` is now a resource, not a value**, and no compiler, vet check, or race detector
will catch a caller who forgets `Close()`. (First half exact; second half taught.)

---

## Session 4b — 2026-07-09 · TTL (before the code)

Phase 1, step 3. **2 ✅ · 1 ❌ · 1 ⊘.**

**Q1 — Why store the absolute deadline, not the duration? — ✅.**
The duration version needs a second field `setAt`, and every read then costs `setAt + ttl` before the compare.
Storing `expires` does that addition **once, at write time**. Caches are read-heavy — that's the right place
to pay.

**Q2 — The naive one-timer-per-key bug on overwrite. — ❌.**
`Set("k","a",30s)`, then at t=2s `Set("k","b",10min)`.
```
t=0s    Set("k","a",30s)     → goroutine A sleeps 30s
t=2s    Set("k","b",10min)   → goroutine B sleeps 10min
t=30s   A wakes              → Delete("k")   ← deletes "b", 9.5 min early
t=602s  B wakes              → Delete("k")   ← no-op
```
Not a race between A and B: **A holds a stale belief about what it is deleting.** It deletes by *key*, not by
*deadline*. **Silent** — no panic, no error, no `-race` warning. Had A re-checked `entry.expired(now)` under
the lock instead of trusting its timer, it would have left `"b"` alone. **Compare, don't remember** — the whole
insight behind lazy expiry.
Go fact: `delete(m,k)` on an absent key is a **no-op**, unlike C++'s `std::map::at()`.
**GAP:** spotted the two goroutines, but predicted the *second* would "find the key missing and **fail**."
Wrong twice. **He expects concurrency bugs to crash. They don't.**

**Q3 — Where does "an unread expired key is indistinguishable from a deleted one" break? — ⊘.**
It holds **only through the `Get`/`Set` API**. Widen the observer and it collapses: (1) **memory** — the entry
is plainly on the heap; (2) **introspection** — `Len()`, `Keys()`, a stats endpoint, the Phase 6 dashboard;
(3) **capacity** — corpses **occupy slots**, so the cache fills with dead entries and evicts a **live** key.
Lazy expiry silently sabotages the eviction policy.
The honest statement: **lazy expiry is correct w.r.t. *value* semantics and wrong w.r.t. *resource*
semantics.** That seam is exactly where the sweeper goes.

**Q4 — A workload that grows unboundedly under lazy expiry alone. — ✅.**
A **session cache**: 1,000 logins/sec, `session:<uuid>` with a 30-min TTL, read a few times and never again.
~50,000 live at any instant; the map grows 1,000/sec **forever** — 86M/day, 99.9% corpses no `Get` will ever
touch. Logically 50k keys; physically 86M. (Later measured: **40.9 MB for 200k keys, surviving a forced GC.**)

---

## Session 4 — 2026-07-09 · Concurrency, races, and the mutex

Phase 1, step 2. **2 ✅ · 2 ⚠️ · 2 ⊘** (Q5–Q6 taught rather than attempted).
*(Tally corrected 2026-07-11: long recorded as "4 ✅ · 2 ⊘", but Q2 and Q4 each name a real gap below.)*

**Q1 — Why do disjoint keys still race? — ✅.**
100 goroutines write `k0-*`, `k1-*`, … — no key is shared. The *value slots* aren't shared; the map's
**internal bookkeeping** is: bucket array, entry count, growth flag, overflow chains. A write can trigger a
**rehash** — allocate a bigger array, migrate every entry, swap the pointer. Proof from the live run:
goroutines 9 and 11 collided on the same address `0x00c000030780` despite disjoint keys. That address is
bookkeeping, not a value.
**A map's invariants span all its entries, so writing disjoint keys still mutates common state.**

**Q2 — Data race vs race condition. — ⚠️.**
A **data race** needs all three: (1) two goroutines touch **the same memory location**, (2) ≥1 is a **write**,
(3) **no synchronization** orders them. Consequence: **undefined behavior**. Mechanically defined ⇒
mechanically detectable (`-race`).
A **race condition** is defined relative to **intent**, so no tool can detect it in general:
```go
val, _ := c.Get("hits")             // both read "5"
n, _ := strconv.Atoi(val)           // both compute 6
c.Set("hits", strconv.Itoa(n+1))    // both write "6"
```
Two increments, counter advances by one. **Zero data races.** This is **check-then-act**: *the lock is
released between the `Get` and the `Set`, and that gap is where the other goroutine slips in.*
Data race ⇒ race condition. Race condition ⇏ data race — this is the counterexample.
**GAP:** three conditions right (nit: *same memory location*, not merely "shared memory"). Named
check-then-act as a **category**; **produced no code — recognition vs recall.**

**Q3 — Why does `Get` lock? — ✅.**
The definition needs **at least one** writer, not two. A locked `Set` racing an unlocked `Get` satisfies all
three conditions — it is a data race, full stop. It is not *caution*, it is *required*. And the consequence is
worse than staleness: an unlocked read during a **rehash** can follow a bucket pointer into the array being
torn down. Garbage or a crash. (Named the mid-expansion rehash unprompted.)

**Q4 — What breaks without `defer`? — ⚠️.**
```go
c.mu.Lock()
value, ok := c.data[key]
c.mu.Unlock()          // never reached on early return / panic
```
The lock is never released; a `panic` unwinds straight past it. This is a **deadlock**, not starvation:
starvation means a goroutine *could* acquire the lock but keeps losing; deadlock means it can **never**,
because the holder will never release. Every future `Get`/`Set` blocks forever; the goroutine that caused it
has moved on and looks innocent. `defer` makes the bug **unrepresentable** — same move as `wg.Go` making the
`Add`-after-`go` bug unrepresentable.
**GAP:** mechanism exact; **called it starvation — vocabulary, not understanding.**

**Q5 — Would an atomic map remove the need for a mutex? — ⊘ (taught).**
**No.** (1) **Lost updates survive** — atomicity was granted to *the operations the map exposes*, not *the
operation the caller cares about*. (2) **Safe publication survives**:
```go
u := &User{}; u.Name = "aayush"   // plain write to different memory
c.Set("u1", u)                    // atomic, by assumption
// elsewhere: u,_ := c.Get("u1"); fmt.Println(u.Name)   // may print ""
```
A good pointer to a struct whose fields haven't arrived.
> **Atomicity is a property of one operation on one memory location. A mutex gives two other things: a
> critical section spanning many operations, and a happens-before edge spanning all memory.**

Why it matters: it's why `sync.Map` doesn't free you from thinking, and why Java's `volatile` is not a
substitute for `synchronized`. Reappears in Phase 3 as "when is a replicated write visible?"

**Q6 — `SetIfAbsent` vs `RWMutex`: what justifies each? (One is a correctness fix; one isn't.) — ⊘ (taught).**
**`SetIfAbsent` — correctness.** Closes the check-then-act gap in `if _, ok := c.Get(k); !ok { c.Set(k,v) }`.
*Justified by* a real caller ("first writer wins," cache-stampede prevention) — **never a benchmark**.
Correctness is **reasoned, not measured**, and `-race` will never flag the caller-side version.
*The principle, reused in Phase 3:* **expose the operation the caller needs atomically; don't hand them
primitives and expect them to compose them safely.** Only the type holding the lock can.
**`RWMutex` — a performance change that must be earned.** It is a **more expensive lock**: `RLock` maintains
an atomic reader count, paid on *every* read. You recoup that only if readers spend enough time **inside** the
critical section for overlapping to be worth something. Ours is **one map lookup**. *Justified by* two
benchmarks: one showing `Mutex` is the bottleneck, one showing `RWMutex` beats it. (Later measured:
uncontended `Mutex` = 26.87ns of a 67ns `Get`, with ~22ns of work to overlap. And `Get` deletes, so it
couldn't take an `RLock` anyway.)
> **Guessing correctly and measuring are different skills, and only one scales.**

---

## Carried forward — re-ask cold

**Open (from the Session 9 milestone quiz, 2026-07-11):**
1. ~~**Presence ≠ version** (S9 Q4)~~ — **CLOSED S18** (see the S18 quiz above). Read half re-asked cold
   before any 7A code: he got ring-geometry-decides (not the coordinator), node-independent, stable, and
   volunteered the *membership-views-agree* precondition — the seam 7A opens. Both halves solid; retired.
2. **Reversibility, not just cost** (S9 Q2) — re-ask: *"State the rule for which reaction to a suspected
   death fires instantly and which waits. Two properties."* He reliably produces "cheap/expensive" and
   drops "reversible/irreversible."
3. **The stranded-key case** (S9 Q1 postscript) — re-ask cold: *"A revived node is promoted back to primary
   of an arc while holding nothing. Under 'only the primary heals,' who repopulates it?"* Answer: **nobody,
   ever** — the primary has nothing to send and the holders stand down. Looking for the fix too: **permission
   follows the DATA, not the ring position** (the healer is the first owner, in ring order, that actually
   holds the key).
4. **The deadline's frame of reference** (from the Session 10 bug — **not yet asked at all**). Re-ask cold:
   *"A key has 10s of its TTL left. Its primary dies at t=9s, and the heal copies the key onto a new replica
   at t=11s. What deadline should that copy carry — and what goes wrong if the TTL travels between nodes as a
   **duration** instead of an **instant**?"* Looking for: the copy must carry **the same absolute instant** the
   key already had. As a duration, each hop **re-bases** it against the receiver's now — so every heal pushes
   the deadline further out and a **frequently healed key never dies**. Follow-up if that lands: *"why send the
   **browser** a remaining duration rather than that same instant?"* (Because the browser reads an instant
   against **its own** clock, and a countdown that disagrees between two laptops gets blamed on the cache. The
   frame of reference is the whole point in both directions.)
5. **Why a delete cannot address the owners** (Session 11 — never asked). *"A delete goes to the R nodes the
   ring names for the key. Name two ways the key survives it. What would a real system add?"* Answer:
   leftovers from an old ring; a paused holder that heals it back. A **tombstone**.
6. **Cleanup's ordering** (Session 13 — never asked). *"A node holds a key it does not own. Why may it not just
   delete it? What must it check first, and why is 'unreachable' not the same answer as 'no'?"* Answer: a
   surplus copy and the last copy alive are indistinguishable from there. Confirm all R owners hold it. Silence
   is not consent.

**Retired 2026-07-10 at Aayush's request — do not re-ask.** The Phase 1 carry-forward list (check-then-act,
compare-don't-remember, starvation mechanism, happens-before, resource semantics, expiry-aware eviction,
admission control) is closed. The concepts remain taught and are recorded above; several were also
*demonstrated in code* during Phase 1 (expiry-aware eviction in `evictLocked`, resource semantics in `Close`,
admission control as the reason segmented LRU was deferred). If any resurfaces as a real gap during Phase 2+,
treat it as new material then, not as a debt to collect.
