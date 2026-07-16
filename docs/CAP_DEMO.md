# Phase 7 Demo — the consistency dial, on screen

**What this doc is for.** `CAP.md` explains *why* (why a partition splits a cluster, why a quorum works, why
versions are needed). **This doc is about the demo:** what a stranger clicks, what happens, and what the
screen shows.

**Status:** design only — taught, not built.

**Running example:** 5 nodes `n0..n4`, `R=3` (3 copies per key), one cut: **A = {n0,n1,n2}** | **B = {n3,n4}**.

> ⚠️ **The strong end of the dial is not "CP."** It is Cassandra's `QUORUM` — *stronger than eventual*, not
> *consistent*. "CP" is not a label on this dashboard. §6 shows what the dial can't reach.
>
> **Version scheme: vector clocks** (`CAP.md` §9, decided S17) — because the two writes either side of a cut
> are genuinely concurrent (`CAP.md` §4), so Lamport would invent an order that doesn't exist.

---

## 1. The money moment

Phase 6's was *"kill a node, watch keys re-replicate"* — its power was **motion**. Phase 7's is a **dial**
with two settings, answering one plain question:

> ### What happens when the same key is written on both sides of a cut?

| the dial | what it does | what it costs | what it's called |
|---|---|---|---|
| **keeps both** | works out that the two writes never saw each other, keeps both, hands you the conflict | **you** have to sort it out — with nothing to go on | eventual consistency (Dynamo/Riak *siblings*) |
| **refuses one** | only one side is allowed to write at all | a `503` the client can **see and retry** | quorum (Cassandra's `QUORUM`) |

**Sibling** = two values living side by side under one key, because the system can't tell which should win.

> **Cut the network. Write the same key on both sides. Turn the dial. The ring behaves in opposite ways
> under the same failure — and you can turn it back and forth yourself.**

A stranger understands the control before you say a word. The proper names go underneath in small type, for
people who already know them.

**Why the two writes are a real conflict and not just a race.** `CAP.md` §4: *"concurrent"* doesn't mean
"at the same moment," it means **neither writer could possibly have known about the other** — and the cut
guarantees that. So **there is no "later" one.** Not *"we can't tell which came later,"* but **there is no
later.** That's why the eventual setting keeps both: it isn't giving up, it's refusing to make something up.

---

## 2. The artifact — a second tab, on its own cluster

**The CAP demo gets its own cluster, on its own tab.** The Phase 6 replication demo keeps the cluster it
has. Nothing you click here can touch that one, and nothing clicked there can touch this one. Same
dashboard components — the dark control-room SVG ring (nodes, virtual-point ticks, key dots on their true
hash angles, ownership links, flying packets, kill/revive shockwaves, the under-replication pulse, the
read-path trace) — just pointed at a different cluster.

**Why a whole second cluster and not a mode on the existing one.** This demo cuts the network and *leaves
it cut* while you write to both sides. Someone arriving from the replication tab would find a cluster that
looks broken, and any node they'd killed over there would quietly wreck the scorecard's run over here. The
two demos want the ring in states that can't both be true — so they get a ring each.

**It's nearly free, which is why it's worth doing** (`HLD.md` §4). Each node already binds `127.0.0.1:0`, so
the OS hands out the ports and two clusters can't collide — nobody picked a number to clash over. And
`Cluster` keeps all its state in its own fields, with no package-level mutable state anywhere in `cluster/`,
`node/`, or `cache/`. Two clusters in one process are independent **by construction** — verified with a
throwaway probe under `-race`: killing a node in A left B at 5 alive, and a key written to A was invisible
in B.

On that cluster, Phase 7 adds:

1. **A network-cut control** — cut the ring in two, and put it back.
2. **The consistency dial** — two settings, changing **two** numbers: `W` 1→2 and `R_read` 1→2.
3. **The conflict card** — when a key holds siblings, it opens and shows them (§3).
4. **A live scorecard** — three numbers, measured at each setting under the same cut (§5).

Plus a **coordinator picker**: click a node to send your next write/read *through* it — so you can write
"the same key from side A" and "from side B."

**Two things come along for the ride, and neither is a control.** *Vector clocks* — both settings need them:
the strong one to pick the current value out of overlapping replies, the eventual one to tell a real
conflict from a replica that's merely behind (`CAP.md` §9). So they're built once, in 7A. *Whether the ring
drops silent nodes* — this comes with the setting, you don't choose it separately: eventual lets the ring
drop them (that's what keeps it as available as possible), quorum keeps them, because **a quorum counted
against a ring that shrinks to fit the survivors is a rubber stamp** (`CAP.md` §8).

Neither is offered as a knob, for the same reason: **a dial should only offer settings a real operator would
actually pick.** "Quorum with a shrinking ring" isn't a consistency level, it's a bug.

**One readout does a lot of the teaching:** show `W + R_read` against `R=3`, live. The point isn't the
formula, it's that **one knob on its own buys nothing** — `W=2` alone still allows a stale read, `R_read=2`
alone still allows one, and only `2+2 = 4 > 3` crosses the line. That's `CAP.md` §7's *"two sets that big
can't miss each other"* as something a stranger **works out by clicking**.

---

## 3. What you'll see, and what it means

| what the screen shows | what it means | `CAP.md` |
|---|---|---|
| **The tear** — a jagged split; links across it go dark; heartbeat halos stop on the far side | the partition — *"two clusters wearing one name"* | §1–2 |
| **A packet that flies at the cut and dies** instead of crossing | to a node, a partition and a death are the same silence | §2 |
| **A key dot in two colors** + a ⚠️ badge that **is still there after the repair** | siblings — one key holding two values that never saw each other | §3–4 |
| **The conflict card** — two values, two vectors, *"neither one covers the other"* | the system **worked out** they clash, and shows its working | §9 |
| **A red padlock / `⛔ 503`** on a node that is still **colored and alive** | refusing *while holding a copy* — *having the data isn't the same as knowing it's current* | §7 |
| **A checkerboard** — some keys green-on-A/red-on-B, others the other way round | *whether you can be served depends on the KEY, not the SIDE* | §7 |

### The conflict card is the centerpiece

Open a key that holds siblings and it shows you the working, not a verdict:

```
⚠️  user:1 — 2 values that never saw each other

              n0 n1 n2 n3 n4
    alice     [1, 0, 0, 0, 0]     the value both sides started from
      ├─ bob  [2, 0, 0, 0, 0]     written via n0  (side A)
      └─ carol[1, 0, 0, 0, 1]     written via n4  (side B)

    bob has n0=2 > 1  ·  carol has n4=1 > 0  →  neither covers the other.
    These clash. There is no later one.

    [ keep bob ]   [ keep carol ]   [ leave both — they stay siblings ]
```

**Read a vector as "what each node had seen when this was written."** Both writes start from `alice`, which
n0 wrote — so both carry `n0=1` underneath. Then **n0** writes `bob` and bumps its own slot to `2`, having
heard nothing from n4. **n4** writes `carol` and bumps its own slot to `1`, still believing n0 has only made
the one write it knows about. Now compare: `bob` is ahead on n0's slot, `carol` is ahead on n4's. **Neither
is ahead everywhere — so neither write could have known about the other.** That's the proof, and it's the
one place a viewer *sees* why the system won't choose rather than being told. (`CAP.md` §9 works the same
example.)

⚠️ **There's no "merge" button, and that's not an omission.** Concatenating to `"bob|carol"` would be a
made-up answer dressed as a real one — §4 explains why. **"Leave both"** is the honest third option, and it's
what a real cluster does by default: nobody resolves, and the siblings pile up (§6).

**Three differences carry most of the teaching:**

- **Grey-dead vs red-padlock.** Phase 6 already greys out a *dead* node. Phase 7 adds one that is **alive,
  in colour, holding the data — and saying no.** That's the difference between *"the data is gone"* and
  *"the data is right there and it won't hand it over."*
- **Clean split vs checkerboard.** A viewer expects the strong setting to make the ring go *"good side /
  dead side."* It doesn't — it's a patchwork. Each key has 3 owners split across 2 sides, so one side always
  has at least 2 and the other can't (`CAP.md` §7 calls this the pigeonhole). The checkerboard is what makes
  that *visible* instead of just asserted.
- **Doesn't know vs won't guess** — the subtle one, worth heading off. A basic heal that only asks *"do you
  have this key?"* would ALSO leave two colours after the repair (`presence ≠ version`, the S9 gap).
  **Same picture, opposite meaning:** that one has no idea, ours knows exactly and refuses to guess. The ⚠️
  badge and the conflict card are the only things telling them apart — without them the demo just looks
  broken.

---

## 4. Walkthrough — two scenarios and a closer (this is also the build order)

### Scenario 1 — "Two truths, and the system says so" (build step **7A**)

**Ships:** the cut, the coordinator picker, vector clocks on every value, a sibling-aware heal, the conflict
card. *No dial yet* — the system sits at `W=1`, `R_read=1`.

**You do:** **Cut network**. Wait ~600ms. Write `user:1 = bob` via **n0** and `user:1 = carol` via **n4**.
Then **Repair network**.

**Why it happens:** neither side can hear the other, so each one decides the other is dead and heals into
its own smaller ring (`CAP.md` §2). Both sides accept their write, because the settings are as loose as they
go — `W=1` plus a ring that shrinks (`CAP.md` §3).

**What the screen shows:**
- The ring **tears**; links across it go dark; n3/n4's halos vanish from side A's view and the other way round.
- Both writes flash **green `204`** — one per side.
- `user:1` goes **two colours** with a ⚠️ badge. **Two truths on one ring.**
- On **Repair**, the heal compares the two vectors, finds **neither covers the other**, and **keeps both.**
  The two colours *stay there on purpose.* The **conflict card** opens.
- The scorecard's **conflicts handed to you** ticks up by 1.
- **You sort it out** — pick `bob`, or `carol`, or keep both. The loser is thrown away **by you, knowingly.**

**The lesson on screen:** the system spotted the clash exactly, showed its working, and **still couldn't
settle it** — because *there is no later one* (`CAP.md` §4). Spotting it is as far as any clock can go.

Then the real point: **you had nothing to go on either.** You clicked one at random. That isn't a gap in the
UI — a real app would merge the two by **meaning** (add up two shopping carts, add two counters together),
but this cache just holds text and the system has no idea what any of it means. **There's nothing to merge
and no fact to appeal to.** That's why Riak made every app author write a merge function by hand, and why
most of the industry ran to last-write-wins instead and quietly ate the losing write.

> **Why vector clocks here and not Lamport.** Lamport squashes this into one number and picks `carol` —
> inventing an order that `CAP.md` §4 says doesn't exist, and destroying `bob` with no error and no warning
> to anyone. It can't even *tell* this case apart from a replica that's simply behind (`CAP.md` §9: from
> `carol=6, bob=8` you can't tell "carol came first" from "they never saw each other"). A vector clock can,
> and the card is where you watch it happen.

### Scenario 2 — "The dial" (build step **7B**, the whole point)

**Ships:** the dial, the refusals, the checkerboard, the live scorecard.

**You do:** turn the dial to **refuses one** (`W=2`, `R_read=2`). Same cut, same two writes.

- One side flashes **green `204`**; the other flashes a **red padlock `503`** on a node that is *still in
  colour and still holding a copy of the key.* It says no anyway — *having the data isn't the same as
  knowing it's current.*
- `user:1` stays **one colour** the whole way through. **No conflict card ever opens** — not because
  anything got settled, but because **the second write never happened.**
- **The stale read dies too.** With side B taking no writes, read `user:1` from **n4**: at `R_read=1` it
  hands back the **old value**, and n4 *thinks the read went perfectly* (`CAP.md` §6). At `R_read=2`, n4 can
  only reach itself — 1 of 3 owners — so it returns **`503` instead of a wrong answer.** The read-path trace
  shows it reaching for a second owner and hitting the cut.
- Seed ~12 keys and cut again: the ring becomes a **checkerboard** — some keys writable only on A, some only
  on B, **none on both.**
- Turn back to **keeps both**, same cut, same writes → the conflict cards come back. **Turn, replay, turn,
  replay.**

**The lesson on screen:** *the same code, two numbers apart, does the opposite thing under the same failure*
— and the clash wasn't **settled**, it was **stopped from happening.** You paid at the door instead of at
the read.

### The closer — "Where the dial runs out" (nothing new to build)

**Repair the network.** Everything green, nothing broken, dial turned all the way up. Write `user:1` through
**n0** and **n2** at the same time.

Both writes reach `W=2`. Both quorums are legitimate. They **overlap at n1** — exactly what `W + R_read > R`
promises. Both flash **green `204`**… and **a conflict card opens anyway**, on a perfectly healthy cluster,
at the strongest setting the dial has.

**Why:** *quorums overlap, but they don't put things in order* (`CAP.md` §10). Overlapping means a reader
*touches* a recent write; it says nothing about **which order** the writes were applied in. And no partition
is needed — `CAP.md` §4's ⚠️: two clients writing through different coordinators, neither seeing the other,
never saw each other on a healthy network either. ⚠️ **Turning `W` up doesn't help** — even `W=3` gives you
the same two siblings.

**The lesson on screen: the dial is maxed out and it still happens.** No clock fixes this — a vector clock
**spots** it, and spotting it is the ceiling. The only fix is to stop the two writes from being unaware of
each other in the first place: push every write to a key through **one queue** — a leader, a
compare-and-swap, a replicated log. **That's consensus. That's Raft. That's a different project**
(`CAP.md` §13).

That's the honest limit of Phase 7, and it's better as something you run than something you read.

---

## 5. The scorecard

| metric | what it means |
|---|---|
| **writes accepted** | of the writes sent during the cut, how many got `204` (rather than `503`) |
| **requests refused** | how many got `503` — the node was right there and wouldn't answer |
| **conflicts handed to you** | keys left holding siblings — a human or a merge function has to decide |

### The run

Five keys, owners taken from the ring. The **quorum side** is whichever side holds at least 2 of a key's 3
owners:

| key | owners | on A | on B | **quorum side** |
|---|---|---|---|---|
| k1 | {n0,n1,n2} | 3 | 0 | **A** |
| k2 | {n1,n2,n3} | 2 | 1 | **A** |
| k3 | {n2,n3,n4} | 1 | 2 | **B** |
| k4 | {n3,n4,n0} | 1 | 2 | **B** |
| k5 | {n4,n0,n1} | 2 | 1 | **A** |

During the cut, each key is written from **both** sides — `X` via **n0**, `Y` via **n4**. That's **10 write
attempts.** Then repair, heal, and read every key.

- **Keeps both** (`W=1`): each side re-owns the whole keyspace among its own nodes, so every write finds an
  owner and is accepted — **10/10, 0 refused.** On repair, neither of each key's two vectors covers the
  other → **5 conflicts**, all handed to you.
- **Refuses one** (`W=2`): a write is accepted only if its side can reach **2 of the key's 3 real owners** —

  | key | A-write via n0 reaches | B-write via n4 reaches |
  |---|---|---|
  | k1 | {n0,n1,n2}=3 → **✓** | 0 → **✗ 503** |
  | k2 | {n1,n2}=2 → **✓** | {n3}=1 → **✗ 503** |
  | k3 | {n2}=1 → **✗ 503** | {n3,n4}=2 → **✓** |
  | k4 | {n0}=1 → **✗ 503** | {n3,n4}=2 → **✓** |
  | k5 | {n0,n1}=2 → **✓** | {n4}=1 → **✗ 503** |

  Exactly **one side gets in per key** (the pigeonhole, `CAP.md` §7) → **5/10, 5 refused** — and with only
  one write accepted per key, there's no second value to clash with → **0 conflicts.**

| metric | **keeps both** | **refuses one** |
|---|---|---|
| writes accepted | **100%** (10/10) | **50%** (5/10) |
| requests refused | **0** | **5** |
| conflicts handed to you | **5** | **0** |

**Put the strong setting's "5 refused" next to the eventual setting's "5 conflicts." They are the same five
writes.**

> **The clash doesn't go away — it just moves.** Neither setting gets rid of it. Quorum makes you deal with
> it at **write** time, as a `503` you can see and retry. Eventual makes you deal with it at **read** time,
> as two values with nothing to choose between them. Same five collisions; all you pick is **when the bill
> arrives.**

### Where the numbers come from

All three are things **the cluster knows about itself.** `writes accepted` and `requests refused` are
counters on whichever node answered. `conflicts handed to you` is just *"how many keys hold more than one
value"* — siblings sit right there in the data, so a node can count its own.

> That's down to the **mechanism**, not luck. Under last-write-wins the third row would be *"writes we said
> yes to and then quietly threw away"*, and **no node could work that out** — the loser is destroyed without
> a trace, so you'd need an outside observer comparing the whole cluster against a log of what clients were
> promised. (That's what Jepsen does, and it's why Jepsen has to exist.) **Choosing to spot conflicts rather
> than invent an answer is what lets this scorecard be honest instead of a cheat.**

---

## 6. Scope and honesty notes (for the writeup)

- **The strong setting is not CP.** It's Cassandra's `QUORUM`: no stale reads, no split-brain. It does not
  stop two writes from clashing — that needs consensus/Raft, which is out of scope (`CAP.md` §13). **The
  closer shows the gap rather than claiming it doesn't exist.** Being able to explain this difference is
  worth more than being able to build Raft.
- **The scorecard only measures the *partition* run.** The strong setting scores `0` conflicts because the
  losing side **refuses**, **not** because quorum stops clashes in general — the closer produces a conflict
  card at the strongest setting with no partition at all.
- **The resolve button only works because this is a demo.** We have 5 keys and a human watching. A real
  cluster has millions of keys and nobody watching, so **unsorted siblings pile up forever** — that was
  Riak's real-world nightmare, and a big part of why the industry took last-write-wins and its silent losses
  instead. Don't let the tidy little card imply this is a solved problem.
- **Vector clocks carry one counter per writer, and that grows** (`CAP.md` §9). Five nodes means five
  counters — free. Real clusters have to trim them, and per-key versioning is exactly the cost Cassandra
  dodged by using wall clocks. **Cluster-in-a-box makes this look cheaper than it really is.**
- **Cluster-in-a-box shrinks one failure right out of sight.** A stale read on a *healthy* network is real
  (a `W=1` write is accepted before the other owners have it), but between goroutines that gap is
  microseconds — you'd never catch it. **The cut is what holds the gap open long enough to demo**, which is
  why §4's stale read runs under the partition. Showing the healthy-network version honestly would need a
  "slow link" injector, in the same family as Kill and Cut. *(Same shape as `CAP.md` §9's wall-clock warning:
  our setup deletes the failure, so we mustn't take credit for it being gone.)*
- **Cluster-in-a-box:** the five "nodes" are goroutines in one process. The *protocol* (message passing,
  failure detection, the cut sitting under the HTTP clients) is real; only the deployment shape is collapsed.

---

## 7. Build order

| step | what it adds | what it unlocks | ships on its own? |
|---|---|---|---|
| **7.0** | a second cluster + a second tab; the API gains a `/api/{cluster}/…` prefix | nothing new to *see* — but every step below needs somewhere to live | ✅ |
| **7A** | the cut, coordinator picker, vector clocks, sibling-aware heal, the conflict card | Scenario 1 — two truths, and the system says so | ✅ |
| **7B** | the dial (`W`, `R_read`), refusals, checkerboard, scorecard | Scenario 2 — the dial. Plus the closer. | ✅ |

**7.0 is the only step with no demo of its own.** It ships an empty second tab showing a second, healthy
ring. Worth doing first anyway: it's small (~40 lines of Go, the rest frontend), it's the one change that
touches the *existing* demo, and doing it later would mean building the cut against a cluster it has to be
moved off afterwards.

**7A closes the S9 `presence ≠ version` gap** — and closes it harder than Lamport would have: the heal stops
asking *"do you have it?"* and starts asking *"did these two ever see each other?"* **7B needs 7A's vectors**
to sort out a read quorum's replies — so the order is a dependency, not a preference.

**One thing 7B needs that doesn't exist yet:** `W` and `R_read` are fixed at `cluster.New(rf, wq, …)` and
there is no way to change them on a running cluster. The dial needs something like
`Cluster.SetQuorum(w, rRead)`. That's a real `cluster/` change — the only one Phase 7 asks for, since 7.0
needs none at all.

> ✅ **All four docs are current as of S17** — `CAP.md` (§9 vector clocks, §11 the two-number dial, §12 the
> three-step arc, §13 the ladder), `CAP_DEMO.md`, `PROGRESS.md` item (f), and `HLD.md` §4 / §7 (the
> two-cluster split). Nothing here is waiting on a doc pass; the next move is code.
