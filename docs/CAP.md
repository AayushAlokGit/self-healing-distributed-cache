# CAP, partitions, and the AP→CP dial — Phase 7 design

**Status: taught, not built.** Turns CAP from three letters into a *button a stranger can press* and a
*table our cluster produces* (§12). The deliverable is numbers, not a diagram.

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
**AP needs it after a heal** (reconcile divergence — today the heal asks *"do you have it?"*, hears `200`,
skips — `presence ≠ version`, flagged since S9).

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

### Decision: **Lamport `(counter, nodeID)`**
`c++` per write coordinated; `c = max(c, incoming) + 1` per write received; compare by counter, tie-break
by node id. ~20 lines — the **same move as Phase 1** (`time.Now()` stood still for 541µs → replace the
clock with a counter): *you cannot order events by asking a clock*, one machine wider.

- ✅ **Delivers what CP promises** — no stale reads, no split-brain (with the fixed ring). Resolving a
  read quorum's replies is a *staleness* question, and staleness is causally ordered, so a causal total
  order picks correctly.
- ❌ **Fixes nothing about genuine concurrency** — it always names a winner, so it still silently drops
  one of two concurrent acked writes, **including CP's own healthy-network lost updates (§10).** No clock
  fixes that; **only consensus does (§13).** A vector clock would at least *detect* them (siblings, not
  silent loss) — at the cost of per-writer metadata and an app merge.
- ⚠️ **Cluster-in-a-box decides it.** Our five "nodes" share **one `time.Now()`** — no skew at all — so
  wall-clock LWW would look flawless *because our topology deleted its failure mode.* A counter has
  nothing to flatter. (A real LWW demo would need a "set n4 +5s" skew button.)
- ⚠️ A **per-key** version forces a read-before-write (a round trip per write); a **per-node** counter is
  stamped immediately — *that's why Cassandra uses wall clocks.* We dodge it.

---

## 10. What quorums still DON'T fix: lost updates

The per-key quorum (fixed ring + versions) fixes **stale reads** and **split-brain** — **not lost
updates.** The reason corrects a natural misconception: *lost updates aren't a residual of split-brain.*
Split-brain is gone; they survive, from an independent cause needing **no partition.**

Healthy network, `user:1`, owners `{n0,n1,n2}`, `W=2`:

```
Client-A writes bob   via n0  →  acks from {n0,n1}  →  W=2 met  →  204
Client-B writes carol via n2  →  acks from {n1,n2}  →  W=2 met  →  204   (concurrent)
```

Both quorums are legitimate and **overlap** at n1 — exactly what `W+R_read>R` gives. But:

> **Quorums OVERLAP; they don't SERIALIZE.** Overlap means a reader *touches* a recent write; it says
> nothing about the **order** writes were applied in.

The owners apply `bob` and `carol` in packet-arrival order and disagree; reconciliation (highest Lamport
stamp) collapses them to **one**. The other acked write is **gone — `204` told, silently lost, no
partition anywhere.**

⚠️ **Bigger W doesn't help.** Even `W=3`: both writes reach all three owners, both get 3 acks, both `204`,
reconciliation still drops one. **Lost updates are a serialization problem, not a quorum-size one.**

The only fix: force all writes to a key through **one order** — a leader per key, a **compare-and-swap on
the version** (the loser's CAS *fails* and it's *told* to retry, not silently dropped), or a replicated
log. **That is consensus. That is Raft. Out of scope** (§13).

---

## 11. The knob map: CAP is a configuration, not an architecture

Not a second system: **flip one toggle and the same code behaves differently under an identical
failure.** The difference is five numbers:

| knob | **AP** (today) | **CP** (target) | where it lives |
|---|---|---|---|
| **R** — copies per key | 3 | 3 *(unchanged)* | `defaultReplicationFactor` |
| **W** — acks before "done" | **1** | **2** | `defaultWriteQuorum` |
| **R_read** — replies before a read answers | **1** *(first reachable hit)* | **2** | `handleClientGet`'s `break` |
| **ring drops silent nodes** | **yes** — re-route | **no** — fixed denominator | `ring.Remove` in `heartbeatRound` |
| **version on each value** | **none** — bare `string` | **Lamport `(counter, nodeID)`** | `cache.Entry` |
| **conflict rule** | **presence** → 200 → skip | **compare versions** → newest wins + read-repair | `heal()` → `fetchFrom` |

It collapses to one inequality:

> **AP:** `W + R_read = 2`, **not** `> R=3` → sets need not overlap → stale reads possible *by design*,
> and both sides of a cut may write.
> **CP:** `W + R_read = 4`, which **is** `> R=3` → sets **must** share a node → no stale reads, and only
> one side of a cut can write.

**The fixed ring (§8) is what makes the inequality mean anything** — a shrinking ring lets `W=2` be
satisfied by an invented set, and the quorum is a rubber stamp.

**AP isn't a special case in the code — it's the parameters at their most permissive:**

```
READ:   owners := ring.Owners(key)          // fixed ring in CP, shrunk in AP
        ask owners in ring order until R_read successes, or run out
        if successes < R_read:  503          // CP refuses here; AP (R_read=1) never gets here
        return the reply with the highest version
WRITE:  owners := ring.Owners(key)
        write to all; if acks < W: 503; else ack   // CP refuses; AP (W=1) never gets here
```

Two **additions, not knobs**: the **partition mechanism** (a fault injector like Kill, under the two
`http.Client`s) and the **version field** (a prerequisite for both modes, built once in the middle).

---

## 12. The build arc, and the table the cluster produces

- **7A — Partition. No knobs move.** Build the link cut; let a client pick its coordinator (`via=n0` /
  `via=n4`). The system changes *not at all*; we gain the ability to **see** split-brain — both sides
  accept a write to the same key, both `204`, and the heal preserves the conflict forever. A complete
  demo on its own; drags `presence ≠ version` into daylight.
- **7B — Versions. Still AP.** Every value gains a Lamport `(counter, nodeID)`. The heal stops asking
  *"do you have it?"* and asks ***"whose is newer?"*** — it now **detects and resolves** the conflict, and
  shows AP's price plainly: **an acked write disappears, no error.** Closes the S9 gap; read-repair for free.
- **7C — CP mode. The toggle.** `W=2`, `R_read=2`, fixed ring, version-picking reads. Same cut, opposite
  behaviour: availability becomes a property of the key, **no key served on both sides**, zero divergence,
  and — *under the partition* — zero silently-lost writes (the losing side **refuses** with `503` rather
  than accepting then dropping)… while the node holding their data sits right there refusing.
  ⚠️ *Not* "no lost updates ever" — healthy-network concurrency still loses one (§10).

**The deliverable — CAP as a measurement, not a definition:**

| under an identical partition | **AP** | **CP** |
|---|---|---|
| writes accepted | 100% | ~60% |
| keys divergent after the heal | *n* | **0** |
| acked writes silently lost | *n* | **0** |
| requests refused | 0 | *m* |

*This table measures the **partition** scenario. CP shows 0 lost writes because the losing side refuses
rather than accepting-then-dropping — **not** because CP fixes lost updates: the §10 healthy-network case
is separate, and CP doesn't fix it.* Reproducible by a stranger with a button.

---

## 13. The honest limits: the consistency ladder

Quorums buy **no split-brain** and **no stale reads**, but **not no lost updates** (§10). The honest
label for 7C is **Cassandra's `QUORUM`, not "strongly consistent"** — and being able to state that
distinction is worth more in the writeup than being able to build Raft.

```
W=1, R_read=1, shrinking ring          →  AP                  (today)     — stale reads, split-brain, lost updates
W=2, R_read=2, fixed ring, versions    →  QUORUM consistency  (7C target) — fixes stale reads + split-brain
leader + CAS / replicated log          →  LINEARIZABLE        (Raft)      — fixes lost updates too, out of scope
```

Also missing, named not hidden:
- **Tombstones** (`HLD.md` §7) — a delete is not a value, so heal can't tell *deleted* from *missing*.
- **Hinted handoff** — a write to a down owner is dropped, not queued.
- **Vector clocks** — with Lamport we *detect nothing*; it always picks a winner (§9).

---

## 14. Open quick-checks (carried forward — ask cold)

1. **The cost of W.** Raising `W` 1→2: what does it *give*, what does it *cost*, and is any cost paid
   **even on a perfectly healthy network?**
   *(gives read/write overlap ⇒ no stale reads + only one side writes; costs write fault-tolerance — `W=1`
   survives 2 owners down, `W=2` survives 1; **yes, paid every ordinary day.**)*
2. **What a Lamport clock does NOT fix.** Side A writes `bob`, side B writes `carol`, concurrent. Lamport
   picks a winner. What did it fix, what did it not, and **what was the losing client told at the time?**
   *(fixed skew, fixed nothing about concurrency; fabricates an order and silently destroys an acked write;
   the loser was told **`204 success`.**)*
3. **Lost updates without a partition.** Two clients write `user:1` through n0 and n2 on a healthy network,
   both reach `W=2`. Why is one write still lost, and why doesn't a bigger W fix it?
   *(quorums overlap but don't serialize; no agreed order exists; only consensus/CAS fixes it.)*
