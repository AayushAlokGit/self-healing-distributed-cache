# Phase 7 Demo — the partition / AP↔CP dial, on screen

**What this doc is for.** `CAP.md` owns the *reasoning* (why a partition splits the cluster, why a quorum
works, why versions are needed). **This doc owns the *demo*:** what a stranger clicks, what happens, and
**what the UI shows** for each step. Read `CAP.md` first if the *why* is unclear; read this for the *what
you'll see*.

**Status:** design only — taught, not built. This is the target the Phase 7 build aims at.

**Running example everywhere below:** 5 nodes `n0..n4`, `R=3` (3 copies per key), and one network cut:
**Side A = {n0,n1,n2}** | **Side B = {n3,n4}**.

---

## 1. The headline: same failure, opposite ring

The Phase 6 money moment was *"kill a node, watch keys re-replicate."* Its power was **motion** — dots
moving across the ring. Phase 7's money moment is a **toggle**:

> **Cut the network. Write the same key on both sides. Flip AP → CP. The ring behaves in opposite ways
> under an identical failure — and you can flip it back and forth yourself.**

- **AP:** both halves of the ring glow **green** (both writes accepted), and the key visibly **tears into
  two colors** that survive even after the cable is repaired. *Two truths on one ring.*
- **CP (same cut, same writes):** one half glows **green**, the other flashes **red padlocks** on nodes
  that *visibly still hold the key* — and the key stays **one color**. *No divergence, at the cost of
  refusing service.*

That flip-and-replay is the striking beat. Everything else in this doc supports it.

---

## 2. The artifact — the same dashboard, three new things

We are **not** building a new app. It is the existing dark control-room SVG ring (nodes, virtual-point
ticks, key dots on their true hash angles, ownership links, flying packets, kill/revive shockwaves, the
under-replication pulse, the read-path trace). Phase 7 bolts on:

1. **A network-cut control** — sever the ring into two sides (and un-sever it).
2. **An AP ↔ CP toggle** — one switch that changes five numbers under the hood (`CAP.md` §11).
3. **A live scorecard** — four numbers the cluster measures about itself, AP vs CP (see §6).

Plus a **coordinator picker**: click a node to route your next write/read *through* it (so you can write
"the same key from side A" and "from side B").

---

## 3. The new visual vocabulary

Each new thing you'll see on the ring, what it means, and where the reasoning lives:

| what the UI shows | what it means | `CAP.md` |
|---|---|---|
| **The tear** — a jagged split across the ring; links crossing it go dark; far-side heartbeat halos stop | the partition — *"two clusters wearing one name"* | §1–2 |
| **A packet that flies toward the cut and dies** (fades/bounces) instead of crossing | a partition is the *same silence* as a death | §2 |
| **A key dot drawn in two colors** + a ⚠️ "divergent" badge | split-brain — one key holding two values (`bob` and `carol`) | §3 |
| **A red padlock / "⛔ 503" pulse** on a node that is still **colored and alive** (not grey-dead) | CP refusing *while holding a copy* — *holding data ≠ knowing it's current* | §7 |
| **A checkerboard** — some keys green-on-A/red-on-B, others the reverse | *availability is a property of the KEY, not the SIDE* | §7 |
| **A value that flashes `204 ✓` then ghosts out** during reconciliation | a **lost update** — an acked write silently destroyed | §10 |
| **The scorecard filling in live**, AP column vs CP column | *CAP as a measurement, not a definition* | §12 |

**Two visual distinctions that carry a lot of the teaching:**
- **Grey-dead vs red-padlock.** Phase 6 already greys out a *dead* node. Phase 7 adds a node that is
  **alive, colored, holding data — and refusing** (red padlock). Those are different states, and seeing
  both on screen is the difference between *"the data is gone"* and *"the data is right there and it won't
  serve it."* That second one **is** CP.
- **Clean split vs checkerboard.** A naive viewer expects CP to make the ring go *"good side / dead side."*
  It doesn't — it's a mosaic (some keys serve on A, some on B). The checkerboard is what makes the
  pigeonhole (`CAP.md` §7) *visible* instead of asserted.

---

## 4. Walkthrough — three scenarios (this is also the build order)

Each scenario ships as a complete demo on its own, and each adds one layer of visual.

### Scenario 1 — "Split-brain on a button" (build step 7A, *no knobs move*)

**You do:** click **Cut network**. Wait ~600ms. Then write `user:1 = bob` via **n0**, and `user:1 =
carol` via **n4**.

**What happens (and why):** neither side can hear the other, so each convicts the other and heals into
its own smaller ring (`CAP.md` §2). Both sides accept their write, because today's system is maximally
permissive (`W=1` + a shrinking ring, `CAP.md` §3).

**What the UI reflects:**
- The ring **tears** down the middle; the three cross-cut ownership links go dark; n3/n4's heartbeat
  halos vanish from side A's view (and vice-versa).
- Your write to n0 flashes **green `204`** on side A; your write to n4 flashes **green `204`** on side B.
- `user:1`'s dot now shows **two colors** with a ⚠️ badge.
- Click **Repair network**: the ring snaps back to one — **but the two-color badge stays.** A `GET
  user:1` returns `bob` or `carol` depending on who answers first (the read-path trace shows the
  disagreement). *The heal cannot fix it, because it only asks "do you have it?" — `presence ≠ version`.*

**The lesson on screen:** killing a node loses a copy; **partitioning creates a second truth**, and the
self-heal you built is powerless against it.

### Scenario 2 — "AP's price, in color" (build step 7B, *still AP*)

**Adds:** every value now carries a Lamport version `(counter, nodeID)` (`CAP.md` §9). The heal stops
asking *"do you have it?"* and asks ***"whose is newer?"***

**You do:** repeat Scenario 1's cut + two writes, then **Repair network**.

**What the UI reflects:**
- Same tear, same two green `204`s, same two-color divergence during the cut.
- On repair, reconciliation runs: the key **converges to one color** (say `carol`, higher Lamport
  stamp)… and the losing value **ghosts out** with a *"write lost"* fade on the node that held `bob`.
- The scorecard's **"acked writes silently lost"** ticks up by 1.

**The lesson on screen:** AP doesn't refuse anything — it takes both writes, says `204` to both, then
**silently destroys one.** The client who wrote `bob` was told *success*. Nobody is ever notified. That
ghost is AP's price, shown without euphemism.

### Scenario 3 — "The toggle" (build step 7C, *the whole point*)

**Adds:** the **AP ↔ CP switch** (`W=2`, `R_read=2`, fixed ring, version-picking reads), the red-padlock
refusals, and the checkerboard.

**You do:** flip to **CP**. Cut the network. Write `user:1` from both sides again.

**What the UI reflects (contrast with Scenario 1–2):**
- One side flashes **green `204`**; the other flashes a **red padlock `503`** on a node that is *still
  colored and still holding a copy of the key.* It refuses anyway.
- `user:1` stays **one color** — no ⚠️ badge, no ghost, no divergence.
- Seed ~12 keys and cut again: the ring becomes a **checkerboard** — some keys writable only on A, some
  only on B, **none on both.** Reading a key from its non-quorum side returns `503`, from its quorum side
  returns the value.
- Flip back to **AP**, same cut, same writes → divergence and ghosts return. **Flip, replay, flip,
  replay** — the ring's behavior inverts each time under an identical failure.

**The lesson on screen:** *CAP is a configuration, not an architecture.* The exact same failure produces
opposite behavior because of five numbers — and you can watch the corner of the triangle the system
abandons move as you toggle.

---

## 5. What is genuinely striking vs what is just a number

Being honest so the build aims effort at the right places:

- **Striking (real motion / color):** the tear, packets dying at the cut, the two-color key, the
  red-padlock refusal, the lost-update ghost, the checkerboard, the flip-and-replay.
- **Just a number (fine — it lives in the scorecard):** the healthy-network lost update (`CAP.md` §10).
  It's a subtle race with **no partition to point at**, so there's no dramatic frame for it. It stays a
  caveat + a scorecard row, not a hero animation.

---

## 6. The scorecard, with a full worked example

The scorecard is **four numbers the cluster measures about itself under one identical partition**, shown
AP vs CP. It turns CAP from a definition you're told into a measurement you *read off the screen*.

| metric | plain meaning |
|---|---|
| **writes accepted** | of the writes issued during the cut, how many got `204` (vs `503`) |
| **keys divergent after the heal** | after repair, how many keys hold *two values* that never got resolved |
| **acked writes silently lost** | writes that got `204` but whose value no longer exists afterward |
| **requests refused** | how many got `503` — the node was there but wouldn't answer |

### The run

Cut **A={n0,n1,n2} | B={n3,n4}**. Five keys, with these owners (from the ring). The **quorum side** is
whichever side holds ≥2 of a key's 3 owners:

| key | owners | on A | on B | **quorum side** |
|---|---|---|---|---|
| k1 | {n0,n1,n2} | 3 | 0 | **A** |
| k2 | {n1,n2,n3} | 2 | 1 | **A** |
| k3 | {n2,n3,n4} | 1 | 2 | **B** |
| k4 | {n3,n4,n0} | 1 | 2 | **B** |
| k5 | {n4,n0,n1} | 2 | 1 | **A** |

**Write sequence:** during the cut, each key is written from **both** sides — value `X` via **n0** (A)
and value `Y` via **n4** (B). That's **10 write attempts.** Then repair, heal, read every key.

### Working the numbers

**1 — Writes accepted.**
- **AP** (`W=1`, shrinking ring): each side re-owns the whole keyspace among its own nodes, so every write
  finds an owner and acks. **10/10 = 100%.**
- **CP** (`W=2`, fixed ring): a write acks only if its side reaches **2 of the key's 3 real owners**:

  | key | A-write via n0 reaches | B-write via n4 reaches |
  |---|---|---|
  | k1 | {n0,n1,n2}=3 → **✓** | 0 → **✗ 503** |
  | k2 | {n1,n2}=2 → **✓** | {n3}=1 → **✗ 503** |
  | k3 | {n2}=1 → **✗ 503** | {n3,n4}=2 → **✓** |
  | k4 | {n0}=1 → **✗ 503** | {n3,n4}=2 → **✓** |
  | k5 | {n0,n1}=2 → **✓** | {n4}=1 → **✗ 503** |

  Exactly **one side accepts per key** (the pigeonhole, `CAP.md` §7). **5/10 = 50% accepted.**

**2 — Requests refused.** AP: **0.** CP: **5** (the five `✗`).

**3 — Keys divergent after heal.** AP: each key got a value on both sides, heal skips (presence) → **5
divergent.** CP: each key accepted on only one side → **0 divergent.**

**4 — Acked writes silently lost.** AP (naive, no versions): both values persist → **0 lost** (cost shows
up as divergence instead). CP: one acked write per key, nothing to reconcile → **0 lost.**

### The scorecard

| metric | **AP** | **CP** |
|---|---|---|
| writes accepted | **100%** (10/10) | **50%** (5/10) |
| requests refused | **0** | **5** |
| keys divergent after heal | **5** | **0** |
| acked writes silently lost | **0** | **0** |

### The insight the numbers deliver

Line up CP's **"5 refused"** against AP's **"5 divergent."** **They are the same five writes.**

> **CP doesn't do less work than AP — it moves the cost from *silent* to *loud*.** AP takes the write,
> says `204`, and lets it quietly corrupt the key. CP refuses with a `503` the client can *see and retry*.
> Same conflicts; one hides them in the data, the other surfaces them at the door.

### One nuance the scorecard shows over time

**Divergence and lost-writes are the same conflict wearing two hats**, depending on whether AP reconciles:

| AP variant | divergent | lost |
|---|---|---|
| **naive (no versions, Scenario 1)** | 5 | 0 |
| **with Lamport LWW (Scenario 2)** | 0 | **5** (reconciliation keeps one value per key, destroys the other) |

So a single AP configuration shows divergence **or** lost-writes, not both — the demo makes that visible
as you turn versions on (Scenario 1 → 2). CP makes **both** zero, and pays in the **refused** column.

---

## 7. Scope / honesty caveats (for the writeup)

- **This scorecard measures the *partition* scenario.** CP shows `0` lost writes here because the losing
  side **refuses** rather than accepting-then-dropping — **not** because CP fixes lost updates in general.
  The healthy-network lost update (`CAP.md` §10) is a separate scenario this table never exercises, and CP
  doesn't fix it either.
- **CP here = Cassandra's `QUORUM`, not "strongly consistent."** It kills stale reads and split-brain, not
  lost updates; that needs consensus/Raft, which is out of scope (`CAP.md` §13).
- **Cluster-in-a-box:** the five "nodes" are goroutines in one process. The *protocol* (message passing,
  failure detection, the cut under the HTTP clients) is real; only the deployment topology is collapsed.

---

## 8. Build order (what ships when)

| step | what it adds | the demo it unlocks | ships alone? |
|---|---|---|---|
| **7A** | the cut, coordinator picker | Scenario 1 — split-brain on a button | ✅ |
| **7B** | Lamport versions, version-aware heal | Scenario 2 — the lost-update ghost | ✅ |
| **7C** | AP↔CP toggle, refusals, checkerboard, scorecard | Scenario 3 — same failure, opposite ring | ✅ |

Each step is a complete, showable increment — the ring always does *something* new you can point a
stranger at.
