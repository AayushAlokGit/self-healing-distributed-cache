# CAP, partitions, and the consistency dial — Phase 7 design

**Status: Phase 7 BUILT & verified end-to-end (S18–S21, branch `cap-demo`).** 7.0 (two clusters), 7A (the cut,
vector clocks, conflict-detecting read/heal/cleanup, the coordinator picker), **7B (the dial — `W`/`R_read` with
a held ring, a `DialPanel` + `Scorecard`)**, and **read-repair-on-read** are all built and browser-verified.
Turns CAP from three letters into a *button a stranger can press* and a *table our cluster produces* (§12). The
deliverable is numbers, not a diagram. Where building caught up with (or corrected) the design, it's marked
**⇒ AS BUILT** in place.

> ⚠️ **Two things this doc deliberately does not say.** It does not call the strong end of the dial **"CP"** —
> it is Cassandra's `QUORUM`, *stronger than eventual*, not *consistent* (§13). And the version scheme is a
> **vector clock**, not Lamport (§9, decided S17) — because the two writes either side of a cut are genuinely
> concurrent (§4), and Lamport would invent an order that does not exist.

**Running example:** 5 nodes `n0..n4`, `R=3`, key `user:1 = alice`, cut **A = {n0,n1,n2}** | **B = {n3,n4}**.

**One fact, one home:** phases → `ROADMAP.md`, status → `PROGRESS.md`, quizzes → `QUIZZES.md`,
architecture → `HLD.md`. This doc owns the **partition / quorum / version reasoning** and the **knob map**.

---

## 1. A partition is a fact about a *pair*, not a node

A **network partition**: the network stops delivering messages between two groups while every machine
keeps running perfectly — a cut cable, a firewall rule, a lost datacenter link. Nobody crashed or slowed.

> *"Is n3 partitioned?"* is not well-formed. *"Can n0 reach n3?"* is.

So the cut belongs **under the HTTP clients** (`n.client`, `n.healthClient`), not as a flag in `Node`. No
handler ever learns partitions exist — correct, because a partition is a property of the network, not a node.

> **⇒ AS BUILT (7A):** `node.gate` — one mutex-guarded set of blocked peer addresses, **shared by both
> clients**, that refuses a blocked peer in `RoundTrip` (so a keep-alive connection pooled before the cut is
> refused too). `Cluster.Cut(sideA, sideB)` / `Mend` drive it; a blocked request fails fast, indistinguishable
> from a dead node — exactly §2.

---

## 2. A partition and a death are the same silence

Our detector asks one thing: *"heard from you in 500ms?"* Silence means dead — and **a partition is the
same silence as a death.** From n0's chair, *"n3 crashed"* and *"n3 is alive but unreachable"* are
byte-for-byte identical; no cleverer detector helps, because the information n0 needs is across the cut.

So ~600ms after the cut, **with no new code**, each side convicts the other, calls `ring.Remove`, and
heals. Side A's 3-node ring and side B's 2-node ring are each *complete and healthy by their own lights.*
We now have **two clusters wearing one cluster's name** — neither malfunctioning, both running our
algorithm correctly.

---

## 3. Split-brain: two truths, not one truth missing

> **Killing a node gives ONE TRUTH WITH A COPY MISSING. Partitioning gives TWO TRUTHS.**

Kill n2 and `user:1` is still one thing — copies gone, survivors heal, nothing to **decide**. Under the
cut, a client writes `user:1 = bob` via n0 → **204**, another writes `user:1 = carol` via n4 → **204**.
Both acked, neither node wrong. `user:1` is `bob` on one side, `carol` on the other — **divergence, with
no error to point at.**

It works because today's defaults are maximally permissive: `W=1` (one ack) **and** a ring that shrinks
to the survivors (§8), so each side writes against its own smaller owner set. Tightening *either* is §7–§9.

---

## 4. Concurrent writes: "concurrent" means *no information flowed*

"Concurrent" does **not** mean "same wall-clock instant." Two writes are **concurrent** when neither
writer could have known about the other.

Formally, **happened-before** `a → b`: same node with `a` first, **or** `a` sent a message `b` received,
**or** a chain of those. If **neither** `a → b` nor `b → a`, they are **concurrent** (`a ∥ b`).

> It's about *information flow*, not real time: two writes an hour apart are concurrent if no knowledge
> passed between them; two a microsecond apart are **not** if one saw the other.

`bob` and `carol` (§3) are concurrent — the cut physically blocks either's knowledge from reaching the
other coordinator. **There is no "later" one:** not "we don't know which," but no such fact *exists*.

⚠️ **No partition required.** Two clients writing `user:1` through n0 and n1 on a healthy network — acks
from `{A,B}` and `{B,C}`, neither seeing the other — are concurrent too.

---

## 5. What CAP actually says

- **P is not a choice.** Cables get cut; you only decide what to do *during* a partition.
- During one, a node has exactly **two** options: **answer** (risk being wrong) or **refuse** (be
  unavailable). No third — the info to be both correct *and* responsive is across the cut.
- **AP = always answer, sometimes lie. CP = never lie, sometimes refuse.**
- **CAP binds only DURING a partition.** On a healthy network you get both. "CP" doesn't mean slow — it
  means *"when the network breaks, this is the corner I abandon."*

---

## 6. A stale read is not locally detectable

One write, `user:1 = bob` on side A; side B takes **no writes** — no conflict anywhere. A client reads
`user:1` from n4 → **`alice`**. Stale. The sharp part:

> **n4 believes the read went perfectly** — healthy ring, legitimate owner, held a value, returned it.
> Same code path, same latency, no error. There is no check n4 can run afterwards, on data it can reach,
> that reveals it lied.
>
> **So the only moment to prevent a stale read is BEFORE you answer, and the only thing to act on is WHO
> YOU CAN STILL SEE.**

That sentence is the entire design of CP.

---

## 7. The per-key quorum: two sets that big can't miss

Don't ack a **write** until **W** owners have it; don't answer a **read** until **R_read** reply. With 3
owners, **W=2, R_read=2**, and `2 + 2 > 3`:

> **`W + R_read > R` isn't a formula to memorise — it's "two sets that big can't miss each other."** Any
> 2-owner read set and any 2-owner write set share a node, so every read touches an owner that saw the
> latest write. **No stale reads.**

Same pigeonhole kills split-brain: 3 owners, 2 sides, so **some side holds ≥2 and both can't** (needs 4).

> **For any single 2-way cut, EXACTLY ONE side reaches a quorum** (≥2 of 3 land on one side); a finer
> partition can leave *no* side able to — never *both*. Split-brain isn't "unlikely," it's
> *arithmetically impossible*.

**Availability becomes a property of the KEY, not the SIDE.** Owners `{n3,n4,n0}`: side B reaches 2 →
serves, side A reaches 1 → refused. Owners `{n0,n1,n3}`: reversed. Some keys serve on A, some on B,
**none on both.** (Aayush's catch: the minority does *not* go dark under a per-key quorum — that's only
true of a whole-cluster membership quorum. Strictly more available.)

⚠️ **The coordinator refuses even holding a copy.** Hitting n0 for `{n3,n4,n0}` gets a **503** — n0
reaches only itself (1 of 3). It holds the value and refuses anyway, because *holding data ≠ knowing it's
current.* The coordinator's position matters only through **how many owners it can reach.**

This is **Cassandra's `QUORUM`** / Dynamo's R/W — derived from a pigeonhole.

---

## 8. The ring must stop shrinking

A quorum is a fraction, and **a fraction needs a fixed denominator** — the §7 guarantee holds only if
both sides count against **the same owner set.**

Watch `ring.Remove` break it: side B has dropped n0/n1/n2, so n4 asks *its* ring → owners `{n3,n4}` → 2
acks → *"majority of 2? satisfied!"* → **204.** Side A does likewise. **Both hold a "valid quorum."**

> **A quorum against a membership that shrinks to fit the survivors is a rubber stamp.** n4 didn't win a
> majority — it *redefined the electorate and declared itself winner.*

The two modes want **opposite things** from the ring:

| | what the ring *means* | on a silent node |
|---|---|---|
| **AP** | *"who can answer"* — liveness is **inside** the ring | `ring.Remove` — re-route. The ring adapts to reality. |
| **CP** | *"who is responsible"* — liveness **outside** the ring, checked per request | **keep it.** The ring is the fixed denominator. |

**The same line — `ring.Remove(id)` — is correct in AP and fatal in CP. The ring IS the denominator.**

---

## 9. Versions, and the clocks that stamp them

### A version does two jobs — a quorum does neither for you
Tempting to think a quorum makes versions unnecessary. It doesn't: a quorum and a version solve
**different** problems.

**A quorum prevents DIVERGENCE, not STALENESS.**
- **Divergence** = two *different* accepted values for one key (split-brain). A quorum *refuses* the write
  that would create it — the second value never exists (cost: availability).
- **Staleness** = replicas lagging on the *same* history. A quorum does **nothing** here, and it's
  unavoidable: a write acks at `W=2` of `R=3`, so the client hears "done" while the **third owner hasn't
  got it yet.** That owner isn't in conflict — just *behind*.

**A quorum makes the latest write *reachable*; only a version says *which reply is it*.** An `R_read=2`
read overlaps the write set, so the latest value is *among your replies* — but they can differ (one
lagged), and with a bare `string` **nothing tells fresh from stale.** Overlap is *reachability* ("the
truth is in your hand"); the version is *identification* ("it's this one"). Neither alone is consistency.

So a version has **two jobs**:

| version's job | mode | what it does | a real conflict? |
|---|---|---|---|
| **resolve staleness** | **CP** & AP | pick the newest reply — a genuine "later" exists, causally ordered; the version *discovers* it | **no** — replica lag |
| **cope with divergence** | **AP** | two concurrent writes both accepted, no "later" exists; the version *fabricates* a winner or *detects* concurrency | **yes** |

### Consistency is impossible without versions — not harder, impossible.
Both branches of CAP dead-end at the same missing field: **CP needs it on every read** (pick the current
value from overlapping replies — the staleness job, needed *even on a perfectly healthy cluster*);
**AP needs it after a heal** (reconcile divergence).

> **⇒ AS BUILT (7A):** the old heal asked *"do you have it?"*, heard `200`, and skipped — `presence ≠ version`,
> flagged since S9. Now the **read**, the **heal**, and **cleanup** all reconcile by version: the read gathers
> every owner and returns siblings; the heal picks a **per-version healer** (a stranded concurrent sibling
> propagates); cleanup keeps a sibling no owner covers. The S9 gap is closed in all three.

### The clocks — one question hiding as two
Stamp each write so reconciliation can order them. Three families, **not** equivalent:

| scheme | how it orders | what it fabricates / costs |
|---|---|---|
| **Wall-clock LWW** | biggest timestamp wins | ⚠️ a node 5s fast wins **every** conflict it enters; still fabricates an order on concurrent writes |
| **Lamport `(counter, nodeID)`** | one integer per node + node-id tiebreak; a causal total order | skew-proof, but **still forces an order on concurrent writes** ⇒ still drops one, just *fairly* |
| **Vector clock** | a **vector** — one counter per node — on the value | **detects** concurrency (neither vector dominates); can't pick a winner, returns **both** ("siblings"); metadata grows per writer |

> **"Which write wins?"** needs a *total order* → LWW/Lamport supply one **by fiat.**
> **"Did these two actually conflict?"** needs *causality* → **only a vector clock answers it.**

**Detection ≠ resolution.** Lamport collapses history to one number: from `carol=6, bob=8` you can't tell
`carol → bob` (later) from `carol ∥ bob` (concurrent). A **vector clock** can — `bob=[2,0,0,0,0] ∥
carol=[1,0,0,0,1]`, neither ≤ the other → detected. But detection is the ceiling: "concurrent" *means* no
winner exists, so it hands both siblings to the **application** (union the carts, add the counters, ask a human).

### Decision: **vector clock** — one counter per node, carried on the value

*(Decided S17. This section previously chose **Lamport**, for being ~20 lines. The reversal is argued below;
`CAP_DEMO.md` is written against this choice.)*

Merge, then stamp your own slot: on a write, `v[i] = max(v[i], local[i])` for every `i`, then `v[self]++`.
Compare by **dominance** — `a` is later than `b` when `a[i] ≥ b[i]` for **every** `i` and `a[i] > b[i]` for at
least one. If **neither** dominates, the two writes are **concurrent (§4)** and both are kept, as
**siblings**. Still the **same move as Phase 1** (`time.Now()` stood still for 541µs → replace the clock with
a counter): *you cannot order events by asking a clock* — one counter per machine instead of one.

**Why not Lamport, which is smaller.** Because of §4. The two writes either side of a cut are *genuinely
concurrent*: neither writer could have known about the other, so **there is no "later" one to find.** Lamport
names one regardless — it collapses history into a single integer and takes the bigger. That is not
*resolving* the conflict, it is **inventing a fact §4 says does not exist** and destroying an acked write on
the strength of it, with no error to anybody. Worse, it cannot even *tell the two cases apart*: from
`carol=6, bob=8` you cannot distinguish `carol → bob` (really later) from `carol ∥ bob` (never met). **A
mechanism that cannot separate "these clash" from "this one is merely behind" cannot be the centrepiece of a
demo about what happens when writes clash.**

- ✅ **Does everything Lamport did for the quorum setting.** Resolving a read quorum's replies is a
  *staleness* question (above), and staleness **is** causally ordered — so one vector genuinely dominates and
  the current value is picked correctly. No stale reads, no split-brain (with the fixed ring).
- ✅ **Detects real concurrency — the reason it's here.** `bob=[2,0,0,0,0] ∥ carol=[1,0,0,0,1]`: neither is
  ≤ the other. The demo can put both vectors on screen and let a viewer *see* why no winner exists, instead
  of taking our word for it (`CAP_DEMO.md` §3).
- ❌ **Detection is still the ceiling.** *"Concurrent" means no winner exists*, so it hands both siblings to
  the **application** — union the carts, add the counters, ask a human. Our cache holds opaque strings, so
  there is **nothing to merge and no basis for the app to choose either.** That is not a hole in our
  implementation; it is what the word means. **Only consensus prevents the collision (§10, §13).**
- ⚠️ **Siblings accumulate.** A value is now a *set*, and nothing shrinks it but a resolution. Riak shipped
  exactly this, and sibling explosion was its most-cursed operational failure — which is a large part of why
  the industry took LWW's silent loss instead. Our demo has 5 keys and a human watching; a real cluster has
  millions and nobody.
- ⚠️ **The vector grows per writer** — one counter per node that ever wrote the key. Five nodes is five ints,
  free. Real clusters must prune it. And a **per-key** version forces a read-before-write (a round trip per
  write) that a **per-node wall clock** dodges — *that is why Cassandra uses wall clocks and eats the silent
  loss.* We dodge the round trip only because the coordinator is already an owner and merges its local copy.
  **Cluster-in-a-box makes this look cheaper than it is.**
- ⚠️ **Cluster-in-a-box would have flattered wall-clock LWW.** Our five "nodes" share **one `time.Now()`** —
  no skew at all — so LWW would have looked flawless here *because our topology deleted its failure mode.*
  (A real LWW demo would need a "set n4 +5s" skew button.) Not a reason to pick vectors, but a reason never
  to read a clean LWW run here as evidence.

---

## 10. What quorums still DON'T fix: writes that collide

The per-key quorum (fixed ring + versions) fixes **stale reads** and **split-brain** — but it does **not
stop two writes colliding.** The reason corrects a natural misconception: *colliding writes aren't a
leftover of split-brain.* Split-brain is gone; they survive, from an independent cause needing **no
partition.**

Healthy network, `user:1`, owners `{n0,n1,n2}`, `W=2`:

```
Client-A writes bob   via n0  →  acks from {n0,n1}  →  W=2 met  →  204
Client-B writes carol via n2  →  acks from {n1,n2}  →  W=2 met  →  204   (concurrent)
```

Both quorums are legitimate and **overlap** at n1 — exactly what `W+R_read>R` gives. But:

> **Quorums OVERLAP; they don't SERIALIZE.** Overlap means a reader *touches* a recent write; it says
> nothing about the **order** writes were applied in.

The owners apply `bob` and `carol` in packet-arrival order and disagree — and **neither vector dominates**
(§9), because neither writer had heard of the other. So the pair survives as **siblings**, on a healthy
network, at the strongest setting the dial has. **Both clients were told `204`, and both were told the
truth.**

> ⚠️ **This is what the version choice buys, and where it stops.** A **Lamport** stamp would have collapsed
> the pair to one here and **silently destroyed an acked write** — no partition, no error, nobody told. A
> **vector clock** cannot do that: it reports the collision instead of inventing a winner. But it cannot
> *resolve* it either — the update is no longer **lost**, it is **unresolved**, and for an opaque string the
> app's only move is to pick one arbitrarily and lose it knowingly. **Detection is the ceiling (§9).**

⚠️ **Bigger W doesn't help.** Even `W=3`: both writes reach all three owners, both get 3 acks, both `204`,
and you still get the same two siblings. **Colliding writes are a serialization problem, not a quorum-size
one.**

The only fix: force all writes to a key through **one order** — a leader per key, a **compare-and-swap on
the version** (the loser's CAS *fails* and it's *told* to retry, rather than being handed a sibling to sort
out later), or a replicated log. That doesn't *resolve* the collision — **it stops the two writes from ever
being concurrent.** That is consensus. That is Raft. **Out of scope** (§13).

---

## 11. The knob map: the dial is two numbers

Not a second system: **turn one dial and the same code behaves differently under an identical failure.**
The difference is **two numbers**:

| the dial | **keeps both** (eventual, `ONE`) | **refuses one** (`QUORUM`) | where it lives |
|---|---|---|---|
| **W** — acks before "done" | **1** | **2** | `defaultWriteQuorum` (`SetReplication`) |
| **R_read** — owners a read asks before answering | **1** | **2** | `SetQuorum(w, rRead)` — **⇒ AS BUILT (7B)** |

⚠️ **Not "AP → CP."** The strong end is **Cassandra's `QUORUM`**, not a CP system (§13). It is *stronger than
eventual*; it is not *consistent*.

> **⇒ AS BUILT (7B):** `cluster.SetQuorum(w, rRead)` sets both numbers on every node (inherited by `wireAll`/
> `Revive`), `POST /quorum` and a dashboard `DialPanel` drive it, and `State` reports `W`/`RRead`. The read
> stayed **gather-all** and R_read is enforced as a **reachable-count threshold**: gather every reachable owner,
> reconcile, then **refuse `503` if fewer than R_read answered** — *not* an early-stop after R_read hits.
> Early-stop would miss a concurrent sibling on a later owner, and an unseen conflict is a silently-picked
> winner, so it would break 7A's detection. The threshold keeps detection intact **and** still forbids a stale
> read: with `R_read+W>R`, the R_read owners that answered must include a W-writer. `R_read=1` is byte-for-byte
> 7A (there `answered==0` is the only sub-threshold case, already the 502).

**What rides along** — implied by the setting, never chosen beside it:

| | **keeps both** | **refuses one** | why |
|---|---|---|---|
| **ring drops silent nodes** | **yes** — re-route | **no** — the fixed denominator | §8. **⇒ AS BUILT (7B):** `node.holdRing` gates `ring.Remove`/`Add` in `heartbeatRound`; `SetQuorum` sets it `= w+rRead>R`. Quorum needs the ring held or `W=2` is a rubber stamp; eventual needs the shrink to stay available. |

**What is a prerequisite** — built once in 7A, and the dial never touches it:

| | both settings | why |
|---|---|---|
| **R** — copies per key | 3 | never moves. `defaultReplicationFactor` |
| **vector clock on each value** | **required** | quorum needs it to pick the current reply; eventual needs it to tell a clash from a lagging replica (§9). `cache.Entry` |
| **conflict rule** | dominant vector wins; **if neither dominates, keep both** | same field, same rule, both settings. `heal()` → `fetchFrom` |

> ⚠️ **A dial should only offer settings a real operator would actually pick.** "Quorum on a shrinking ring"
> is a rubber stamp (§8). "No versions at all" is a heal that preserves divergence forever
> (`presence ≠ version`). Neither is a consistency level — **both are bugs** — so neither gets a position on
> the dial. Cassandra's real dial is `ONE` vs `QUORUM`, which is exactly the two settings above.

It collapses to one inequality:

> **keeps both:** `W + R_read = 2`, **not** `> R=3` → sets need not overlap → stale reads possible *by
> design*, and both sides of a cut may write.
> **refuses one:** `W + R_read = 4`, which **is** `> R=3` → sets **must** share a node → no stale reads, and
> only one side of a cut can write.

⚠️ **Neither number does anything alone.** `W=2, R_read=1` sums to 3, and so does `W=1, R_read=2` — both
still allow a stale read. Only the **pair** crosses `> R`. That's the readout the dashboard puts next to the
dial (`CAP_DEMO.md` §2), so a stranger finds the threshold by clicking rather than being shown a formula.

**The fixed ring (§8) is what makes the inequality mean anything** — a shrinking ring lets `W=2` be
satisfied by an invented set, and the quorum is a rubber stamp.

**The eventual setting isn't a special case in the code — it's the parameters at their loosest:**

```
READ:   owners := ring.Owners(key)          // held fixed at quorum, shrunk at eventual
        ask EVERY reachable owner, reconcile their versions   // gather-all, NOT early-stop (keeps detection)
        if answered == 0:        502
        if answered <  R_read:   503          // quorum refuses; eventual (R_read=1) never gets here
        reply := the version that dominates — or EVERY sibling, if none does
        read-repair: store reply back onto any reachable owner missing it   // §9, converges a lagging replica
WRITE:  owners := ring.Owners(key)
        write to all; if acks < W: 503; else ack   // quorum refuses; eventual (W=1) never gets here
```

⚠️ **Note what the read returns.** Not "the newest" — *the dominant one, or all of them.* A read is now a
`[]Entry`, not an `Entry`, and that ripples out to the client API and the dashboard. It is the largest
structural cost of choosing vectors over Lamport (§9), and it is not optional: **if a read could always
return one value, we wouldn't have needed a vector clock.**

Two **additions, not knobs**: the **partition mechanism** (a fault injector like Kill, under the two
`http.Client`s) and the **vector clock field** (a prerequisite for both settings, built once in 7A).

---

## 12. The build arc, and the table the cluster produces

*(Reworked S17: three steps, not the old 7A/7B/7C. The old **7A** shipped the cut with no versions, so its
heal preserved divergence **forever** — that isn't a consistency level, it's the `presence ≠ version` bug
with a demo built on it. Versions are a prerequisite (§11), so they moved into 7A and the arc lost a step.
`CAP_DEMO.md` is the demo spec.)*

- **7.0 — a second cluster, a second tab. Nothing new to see.** ✅ **DONE S17.** This demo leaves the network cut for minutes
  while you write to both sides; it cannot share a ring with the Phase 6 replication demo. **`cluster/` needs
  no changes** — nodes already bind `127.0.0.1:0` (the OS assigns ports, so two clusters can't collide) and
  there is **no package-level mutable state** to share (`HLD.md` §4, verified under `-race`). The work is
  `cmd/server` (a cluster map + an `/api/{cluster}/…` prefix) and the frontend. Ships as an empty second ring.
- **7A — the cut, the coordinator picker, vector clocks, sibling-aware heal, the conflict card.** ✅ **DONE
  S18–S19, verified E2E** (browser: cut `{n0,n2,n4}|{n1,n3}`, wrote `milk,eggs` via n0 and `milk,bread` via n3,
  both accepted, mend, the heal kept both, a read showed the conflict card). A client picks its coordinator
  (`via=n0` / `via=n4`); every value carries a vector clock (§9). The heal stopped asking *"do you have it?"*
  and started asking ***"did these two ever see each other?"*** — it **detects the clash and keeps both**, then
  puts the two values on screen and asks *you*. Closes the S9 gap. **⇒ Read-repair AS BUILT (S21):** the read now
  also writes each surviving version back to any reachable owner missing it (`readRepair`), so a
  lagging-but-alive replica converges **on the read**, not only on a membership-change heal — detection *and*
  repair now.
- **7B — the dial. ✅ DONE S21, verified E2E.** `W=2`, `R_read=2`, ring held fixed (`holdRing`). Same cut, opposite
  behaviour: availability becomes a property of the key, **no key served on both sides**, no clash to resolve —
  the losing side **refuses** with a `503` rather than accepting a write it can't reconcile, while the node
  holding the data sits right there saying no. A `DialPanel` + `Scorecard` run the controlled experiment (below)
  live. ⚠️ *Not* "no collisions ever" — healthy-network concurrency still collides (§10), the demo's closing move.

**The deliverable — CAP as a measurement, not a definition** (worked run in `CAP_DEMO.md` §5: 5 keys, each
written from both sides during one cut, 10 attempts):

| under an identical partition | **keeps both** | **refuses one** |
|---|---|---|
| writes accepted | **100%** (10/10) | **50%** (5/10) |
| requests refused | **0** | **5** |
| conflicts handed to the app | **5** | **0** |

**The 5 refused and the 5 conflicts are the same five writes.** Neither setting destroys the collision — it
**moves in time.** Quorum bills you at *write* time, as a `503` you can see and retry; eventual bills you at
*read* time, as two values with nothing to choose between them.

*This table measures the **partition** scenario. The strong setting shows 0 conflicts because the losing side
refuses — **not** because quorum stops writes colliding: the §10 healthy-network case is separate, and quorum
doesn't fix it.* **⇒ AS BUILT (7B):** the dashboard **Scorecard** produces this table live — one probe row per
dial setting under the current cut — and it was browser-verified S21 (QUORUM `5/5/0`, ONE `10/0/5`, the same
five writes). A stranger reproduces it with a button.

---

## 13. The honest limits: the consistency ladder

Quorums buy **no split-brain** and **no stale reads**, but they do **not stop writes colliding** (§10). The
honest label for 7B is **Cassandra's `QUORUM`, not "strongly consistent"** — and being able to state that
distinction is worth more in the writeup than being able to build Raft.

```
W=1, R_read=1, shrinking ring, vectors →  eventual consistency (7A)   — stale reads; a cut yields siblings the app must merge
W=2, R_read=2, fixed ring,     vectors →  QUORUM consistency   (7B)   — fixes stale reads + split-brain
leader + CAS / replicated log          →  LINEARIZABLE         (Raft) — stops writes colliding at all, out of scope
```

⚠️ **Read the third rung carefully.** Raft doesn't *resolve* collisions better than a vector clock does —
**it prevents them**, by making every write to a key pass through one order so no two are ever concurrent.
That's the whole difference: rungs 1–2 argue about what to do *after* a clash; rung 3 removes the clash.
**Detection is the ceiling of any clock (§9); only serialization is above it.**

**Convergence, what's built vs not.** **⇒ AS BUILT (S21): read-repair-on-read** — a read writes each surviving
version back to any reachable owner missing it, so a *lagging-but-alive* replica converges on read traffic. It is
**read-triggered, not periodic**: a key nobody reads never repairs. The two heavier cousins stay unbuilt on
purpose — **hinted handoff** (queue a write for a down owner; below) and **Merkle-tree anti-entropy** (a
continuous background scan that reconciles even cold keys; our event-driven heal is its poorer cousin).

Also missing, named not hidden:
- **Tombstones** (`HLD.md` §7) — a delete is not a value, so heal can't tell *deleted* from *missing*.
- **Hinted handoff** — a write to a down owner is dropped, not queued.
- **A semantic merge.** We *surface* siblings and hand them to a human with a button. A real system needs a
  merge function per data type — union the carts, add the counters — or a CRDT that carries its own. Our
  values are opaque strings, so **there is nothing to merge**, and the demo's resolve button is an
  affordance, not an answer (§9).
- **Vector pruning.** The vector grows one counter per writer, forever. Five nodes makes that invisible;
  real clusters must cap it (Dynamo did), and cluster-in-a-box is what hides the cost (§9).

---

## 14. Open quick-checks (carried forward — ask cold)

1. **The cost of W.** Raising `W` 1→2: what does it *give*, what does it *cost*, and is any cost paid
   **even on a perfectly healthy network?**
   *(gives read/write overlap ⇒ no stale reads + only one side writes; costs write fault-tolerance — `W=1`
   survives 2 owners down, `W=2` survives 1; **yes, paid every ordinary day.**)*
   > **Aayush's reframing (S17), which is better than the above.** "Fault-tolerance" makes `W=1` sound like a
   > free win. It isn't: the write that survives two deaths lives on **one node**, and dies with it. So —
   > **`W` is how much a `204` is worth.** An ack at `W=1` means *one machine has this*; at `W=2`, *two
   > machines have this*. `W=1` says yes more often and each yes means less; `W=2` says yes less often and
   > each yes means more. It is not availability vs *consistency* here, it is availability vs **durability**,
   > and you pay it on the healthy path.
2. **What a vector clock does NOT fix.** Side A writes `bob`, side B writes `carol` — concurrent. The vector
   clock finds that neither dominates and keeps both. What did it fix, what did it **not**, and **who decides
   now, on what basis?**
   *(Fixed the silence: nothing is destroyed, nobody is lied to, and the clash is **detected** rather than
   invented away — which is exactly what Lamport could not do, since it can't tell `carol ∥ bob` from
   `carol → bob`. Fixed **nothing about the clash itself**: "concurrent" means no winner exists, so detection
   is the ceiling. The **app** decides — on **no basis at all** for an opaque string. A real app merges by
   meaning (union carts, add counters); ours can't. Only consensus prevents the collision, §10/§13.)*
3. **Colliding writes without a partition.** Two clients write `user:1` through n0 and n2 on a healthy
   network, both reach `W=2`. Why do you still end up with two siblings, and why doesn't a bigger `W` fix it?
   *(Quorums overlap but don't serialize — overlap means a reader touches a recent write, it says nothing
   about the order writes were applied in. Neither writer saw the other, so no vector dominates and no agreed
   order exists to find. `W=3` changes nothing: both writes reach all three owners and both are legitimate.
   It's a serialization problem, not a quorum-size one — only consensus/CAS fixes it.)*
