# Go Notes

Go idioms and gotchas as we hit them, with C++/Python parallels. Append as new ones come up;
don't repeat what the standard library docs already say — record what *surprised* us.

**Conventions:** ⚠️ marks a trap that compiles fine and fails at runtime. `→` gives the file where
we first used it.

---

## Zero values: usable, except when they're not

Go initializes every variable. Several stdlib types are designed so the zero value is immediately
usable — **no constructor**:

```go
var mu sync.Mutex       // ready
var wg sync.WaitGroup   // ready
var once sync.Once      // ready
var t time.Time         // valid instant (Jan 1, year 1)
```

⚠️ **Channels and maps are the exception.** Their zero value is `nil`:

| Zero value | Write | Read |
|---|---|---|
| `nil` map | **panics** | returns zero value, no panic |
| `nil` channel | blocks forever | **blocks forever** |

A nil channel receive is the nastier one: no panic, no error, just a goroutine that never wakes.
Both must be `make()`d. → `cache.go` `newWithSweepInterval`

```go
c := &Cache{
    data: make(map[string]entry),   // nil map write would panic
    done: make(chan struct{}),      // nil channel receive blocks forever
}
// mu, closeOnce, wg deliberately left as zero values
```

**`time.Time`'s zero is a valid instant**, so it works as a "never" sentinel only if you test it
explicitly with `IsZero()` — never by comparing against `now`, or "never" reads as "expired 2000
years ago." → `entry.expired`

---

## Concurrency

### goroutines
`go f()` runs `f` concurrently. Like `std::thread` / `threading.Thread`, but cheap enough to have
100,000 of them, and **genuinely parallel** — no GIL. → `race_test.go`

### `sync.WaitGroup`
A counter: `Add(1)` / `Done()` / `Wait()`. Collapses "join every thread" into one counter.

`wg.Go(func(){...})` (Go 1.25) bundles `Add(1)` + `go` + `defer Done()`. Prefer it: it makes the
classic "`Add` must precede `go`" bug **unrepresentable**. (Put `Add(1)` inside the goroutine and
`Wait()` can see zero and return before anything started.) → `race_test.go`, `cache.go`

### `defer`
Runs on function return, however it returns — including `panic`. Go's RAII / `try...finally`.

⚠️ **Not using `defer mu.Unlock()` is a deadlock waiting to happen.** Any early `return` or `panic`
added later skips a manual `Unlock()`, the lock is never released, and **every future caller blocks
forever**. That's a *deadlock*, not starvation:
- **starvation** — could acquire, keeps losing the race; may still run
- **deadlock** — can never acquire; the holder is gone

### `sync.Mutex`
`Lock()`/`Unlock()`. A mutex gives you **two** things, and people forget the second:
1. **mutual exclusion** — one goroutine in the critical section
2. **a happens-before edge over ALL memory** — everything written before `Unlock` is visible to
   whoever `Lock`s next

That second half is the *publication barrier*. Atomicity alone doesn't give it: an atomic pointer
store can publish a pointer to a struct whose fields aren't visible yet.

⚠️ **Never copy a mutex** (or a struct containing one). The copies stop excluding each other. Always
`*Cache`, never `Cache`. `go vet`'s `copylocks` catches it — but **`go test` does not run
`copylocks`**, so `go vet` must be a separate step. → see `CLAUDE.md` Commands

### `sync.Once`
`once.Do(f)` runs `f` exactly once, ever, across all goroutines. Needed because
**closing a closed channel panics** and `Close()` gets called twice in real code. → `Cache.Close`

### Channels
A pipe between goroutines. `ch <- v` sends, `<-ch` receives; **the arrow points where data moves**.
A receive **blocks** until a value arrives — that's the point, it's how you wait without spinning.

`chan struct{}` carries no data (`struct{}` is zero bytes). The *arrival* is the message.

**`close(ch)` is a broadcast.** Every receive on a closed channel returns immediately, forever, for
every receiver. A *send* wakes exactly one. So `close` is the idiom for "stop everybody."

⚠️ Sending on a closed channel panics. ⚠️ Closing a closed channel panics.

### `select`
A `switch` for channel operations. Blocks until one case can proceed; picks **randomly** among ready
cases (prevents starvation).

```go
select {
case <-ticker.C: c.sweep()   // whichever happens first
case <-c.done:   return
}
```

⚠️ **Adding `default` flips the semantics: `select` never blocks.** If no case is ready, run
`default` immediately. → `bench_test.go` (a flat-out sweeper loop)

### `time.Ticker` vs `time.Sleep`
Use a `Ticker` when a goroutine must remain interruptible. **`Sleep` cannot be interrupted**, so a
`Close()` would block for up to a full interval. `select` can only race *channels*, and
`ticker.C` is `Sleep` reshaped into a channel. → `sweepLoop`

### Request ≠ confirmation
Two separate signals, always:
```go
close(stop)   // ask it to stop
<-swept       // wait until it HAS stopped
```
`Cache.Close()` does this with `close(done)` + `wg.Wait()`. Skip the second and you return while a
goroutine is still touching your data.

### Goroutines and the GC
**Every running goroutine's stack is a GC root.** A goroutine that never returns keeps everything it
references reachable *forever* — and since it never returns, its stack never stops being a root. Two
leaks holding each other up.

`runtime.SetFinalizer` can't save you: a finalizer runs when an object becomes unreachable, which is
exactly what can't happen.

**Consequence:** a type owning a background goroutine is a **resource, not a value**. It needs
`Close()`, and callers who forget it leak. No compiler, vet, or `-race` check will tell them.

---

## Data races

Three conditions, all required:
1. two goroutines touch the **same memory location**
2. at least one is a **write**
3. no synchronization orders them

Result: **undefined behavior**, not merely a wrong value. Mechanically defined ⇒ mechanically
detectable (`go test -race`).

**A data race is not a race condition.** A race condition is "correctness depends on interleaving" —
defined relative to *intent*, so undetectable by any tool. Neither implies the other:

```go
val, _ := c.Get("hits")             // both goroutines read "5"
c.Set("hits", inc(val))             // both write "6"
```
Every access locked. **Zero data races.** Still wrong. This is **check-then-act**, and the fix is to
widen the critical section (`SetIfAbsent`), not to add more locks.

> **The mutex protects the data, not the invariant.** The atomic unit must be the operation the
> *caller* cares about.

### ⚠️ compare, don't remember
**A value read before releasing a lock is a rumor by the time you reacquire it.** Re-check under the
lock before acting. Three bugs, one root:
- a per-key expiry timer deletes a key that was overwritten with a fresh TTL
- a sweeper that unlocks mid-scan deletes an entry another goroutine just refreshed
- `Get`-then-`Set` loses an increment

### The five failure modes of unsynchronized memory
Escalating severity — a mutex kills all five:
1. **lost update** — `x++` is load/add/store *(wrong number, no crash)*
2. **corrupted structure** — a map rehash breaks invariants spanning all entries
3. **torn values** — slice `(ptr,len,cap)` and interface `(type,data)` are multi-word; half-written
   ⇒ *memory unsafety in a language with no pointer arithmetic*
4. **stale reads** — the compiler hoists a load out of a loop ⇒ infinite loop. Not atomicity —
   **publication**
5. **reordering** — `data=42; ready=true` may become visible out of order

Which one you get is unpredictable. That *is* undefined behavior. "Ran it 1000× and it was fine" is
worth nothing.

---

## Maps

```go
v, ok := m[k]   // comma-ok: distinguishes "missing" from "zero value stored"
delete(m, k)    // no-op on an absent key — never panics (unlike C++ std::map::at)
m["missing"]    // returns the zero value, no panic
```

⚠️ **Iteration order is randomized on every `range`.** Deliberate, so nobody depends on it.
- **Cost:** no cursor. "Resume where I left off" is not implementable.
- **Benefit:** `for k := range m { ... break }` is a *random* sample — which is what makes
  Redis-style sampling cheap.

**Deleting during `range` is legal.** An entry removed before it's reached is simply never produced.
Unlike C++ iterator invalidation. → `Cache.sweep`

⚠️ **Maps never shrink.** `delete()` frees keys and values; the bucket array stays sized for the
all-time peak. Measured: sweeping 200k entries to `Len()==0` left **16.5 MB** allocated; replacing
`c.data` with a fresh map dropped it to 0.5 MB. Only reallocation releases it. Redis rehashes into a
smaller table; Go doesn't.

The residue scales with the *entry struct*, not the payload: adding one `uint64` to `entry`
(40 B → 48 B) moved it from **16.5 MB → 25.2 MB**. A test asserting on a *fraction* of the peak heap
is therefore asserting on `sizeof(entry)`. Assert on the payload you expect back instead.
→ `leak_test.go`

⚠️ **Map values are not addressable.** `m[k].field = v` is a compile error, and `e := m[k];
e.field = v` mutates a **copy**. Read, mutate, write back — or store pointers:
```go
e := c.data[key]         // map[string]entry: e is a copy
e.lastUsed = ...
c.data[key] = e          // the write-back is not optional

c.data[key].value = v    // map[string]*node: fine, the pointer is the value
```
Unlike C++ `m[k].field = v` or Python `d[k].field = v`, which mutate in place. Why: **a rehash moves
values to new addresses**, so a pointer into a bucket would dangle.

That rule decides the LRU design. A doubly linked list is a web of pointers *between* nodes; if the
nodes lived in map buckets, growing the map would invalidate every one. So nodes must be
independently heap-allocated: `map[string]*node`, not `map[string]node`. **Values that other values
point at need stable identity, and map values have none.**

**And the pointer was free.** `BenchmarkGet`: 66.99 → 61.31 → **52.52 ns** across the two rewrites.
The expected cost was a pointer chase; instead it *paid* for one, because a `*node` is addressable
and `Get` stopped hashing the key a second time to store the entry back.

---

## Strings, numbers, time

**`strconv.Itoa(n)`** — int → string. `std::to_string` / `str()`. Go has no implicit numeric→string
coercion, so `"k" + i` is a compile error.

⚠️ **`string(65)` is `"A"`, not `"65"`.** It interprets the int as a Unicode code point. Use
`strconv.Itoa` for the number; `string()` only when you mean a code point.

**`fmt.Sprintf("k%d-%d", a, b)`** is more readable for several pieces but **slower** (reflection).
In a hot loop, concatenate with `Itoa`.

**A Go string is a `(pointer, length)` pair.** Copying one copies 16 bytes, not the text.
⚠️ In a benchmark, storing the *same* string in 200k map entries shares **one** backing array — the
payload is fake. Make the values distinct. → `leak_test.go`

**`time.Duration` is an `int64` of nanoseconds**, not a struct. Hence `50 * time.Millisecond` and
`ttl > 0`. Closer to `std::chrono::duration` than Python's `timedelta`.

**Monotonic clock.** `time.Now()` carries *two* readings: wall clock **and** a monotonic one that
only moves forward. `After`/`Before`/`Sub`/`Since` use the monotonic reading when both operands have
it — so TTLs survive an NTP correction or a VM resume, for free.

⚠️ Certain operations **strip** the monotonic reading — `t.Round(0)`, JSON marshaling, database
drivers. After that you're silently doing wall-clock arithmetic again, and clock jumps come back.
Matters when we serialize entries across nodes in Phase 3.

**`math/rand/v2`.** `rand.New(rand.NewPCG(seed, seed))` for a deterministic generator — a test that
uses the global `rand` is a test whose failures you can't reproduce. `r.IntN(n)` for a uniform draw,
`rand.NewZipf(r, s, v, imax)` for a power law.

⚠️ **`rand.NewZipf` returns `nil` when `s <= 1`**, and panics only later, when you *draw* from it.
The constructor reports the error by handing you something that looks fine. → `hitrate_test.go`

⚠️ **`time.Now()` has terrible *resolution*, whatever its precision.** It reports nanoseconds and
advances in ~541µs jumps on this Windows box: **13,397 consecutive calls returned the identical
instant.** A `Set` is ~100ns, so ~5,400 back-to-back `Set`s share one timestamp.

Consequence: **you cannot order events by asking a clock.** `lastUsed time.Time` + `Before()` made
LRU pick its victim *at random* among tied entries (and `range` randomizes the order, so the tie-break
is random too). The test failed 5 runs in 10 — flakiness, not a clean failure.

Fix: a **logical clock** — a `uint64` on the `Cache`, incremented under the lock on every access.
Two events tie only if they *are* the same event. Costs an increment instead of a clock read, and it
is the single-node case of the **Lamport clock** we'll need in Phase 3, where wall clocks on different
machines disagree and can run backwards. → `Cache.tickLocked`

---

## Hashing, sorting, slices

**Hashers are `io.Writer`s.** `h := fnv.New32a(); h.Write([]byte(s)); h.Sum32()` — feed bytes, read
the result. Same shape for `crypto/sha256`, though that also offers the one-shot `sha256.Sum256(b)`
returning a `[32]byte` array. Slice + `binary.BigEndian.Uint32(sum[:4])` to pull a `uint32` out.

⚠️ **Hash choice is not interchangeable.** `hash/fnv` and `hash/crc32` have **weak avalanche**: inputs
sharing a prefix and differing in one trailing byte (`node0`..`node9`) produce outputs that stay
clustered — on a hash ring, one node ended up owning 96% of the circle. A crypto hash randomizes every
output bit, so any truncation is uniform. → `ring/hashKey`. And **never** `hash/maphash` for anything
cross-process: it is per-process seeded on purpose, so two processes disagree.

**`sort.Search(n, f)`** is binary search over an *index range*, not a slice. `f` must be monotonic
(false…false true…true); it returns the first `i` where `f(i)` is true, or `n` if never. The clockwise
walk on a ring: `i := sort.Search(len(pts), func(i int){ return pts[i].hash >= h }); if i==len(pts) { i=0 }`.

**Filter-in-place** reuses the backing array: `kept := s[:0]; for _, x := range s { if keep(x) { kept = append(kept, x) } }; s = kept`.
`s[:0]` is length 0, full capacity, same pointer — so `append` overwrites the original as it goes.
Safe here only because the read index never lags the write index. → `Ring.Remove`

---

## Pointers and data structures

**No pointer arithmetic, no `->`.** `n.next.prev = n.prev` auto-dereferences at every step; Go has
one selector operator for both values and pointers. Nil dereference **panics** — it doesn't corrupt.

**Sentinel nodes.** Allocate two dummy nodes that hold no data and never move:
```go
c.head.next = c.tail
c.tail.prev = c.head
```
Now every real node has a non-nil `prev` and `next`, so `unlink` and `pushFront` have **zero branches**.
Without them each function needs "am I the first? the last? the only one?" — the classic linked-list
bug farm. Same trick as a C++ `std::list`'s end sentinel. → `Cache.unlink`

**`container/list` exists and we don't use it.** It's `any`-typed (pre-generics), so every element
boxes into an interface — an allocation and a type assertion per access. Hand-rolling three
four-line methods is both faster and clearer, and the algorithm is the point of this project.

**Slices are not lists.** Move-to-front on a slice shifts every element before the target: O(n).
Arrays store order **positionally** (an element's order *is* its address), lists store it in
**pointers**, so reordering a list is a local edit. That is the whole reason LRU uses a list.

---

## Packages, visibility, style

**Lowercase = package-private, uppercase = exported.** Compiler-enforced, unlike Python's `_name`.

Tests declaring `package cache` (not `cache_test`) can reach unexported identifiers. That's how
`newWithSweepInterval` gives tests a seam without widening the public API. → `cache.go`

**if-with-init:** `if v, ok := m[k]; ok { ... }` — declare and test, scoped to the `if`.

**`for i := range n`** (Go 1.22+) ranges over an integer, `0` to `n-1`. And since Go 1.22 each loop
iteration gets its **own** loop variable, so the old `go func(i int){...}(i)` capture workaround is
no longer needed.

---

## Testing and benchmarking

### Commands
```
go vet ./...              # compiles-but-wrong (copylocks, bad Printf verbs)
go test -race ./...       # runs-but-wrong (data races)
go test -count=1 ./...    # ⚠️ disable the result cache
go test -run TestName ./cache/
go test -bench . -run xxx ./cache/    # -run xxx skips tests while benchmarking
```

### Some tests have no assertions
`race_test.go` and `TestCloseIsIdempotent` assert nothing. **The runtime is the judge** — a `panic`,
a `fatal error`, a `DATA RACE` report, or a hang is the failure signal.

`TestCloseStopsSweeper` goes further: if `sweepLoop` ignored `done`, `Close()` blocks forever and the
test times out. **The hang is the assertion.**

### Benchmarks
```go
func BenchmarkGet(b *testing.B) {
    setup()
    for b.Loop() { c.Get("live") }   // resets the timer on first call
}
```
`b.Loop()` (Go 1.24+) replaced `for i := 0; i < b.N; i++`. It resets the timer automatically and
**stops the compiler eliminating the loop body** (the result is discarded, so it'd otherwise be dead
code). `b.Run(name, fn)` makes subbenchmarks — that's the `BenchmarkSweep/100000-20` in the output.
The `-20` is `GOMAXPROCS`.

### ⚠️ You cannot time one fast operation
Measured on this machine: `time.Now()` resolves to **829 µs**; a `Get` takes **67 ns** — 12,000×
below the clock. Per-operation timing prints `p50=0s`.

Two ways out:
- **fix the count, measure the batch, divide** — what `b.Loop()` does for you
- **fix the time, count the operations** — `getsIn(c, 500*time.Millisecond)`

> **When the thing you're measuring is faster than your clock, stop measuring instances and start
> counting them.**

⚠️ Also: don't `append` to a growing slice inside the measured loop. Growing a 4M-element slice
triggers a GC and you'll measure **your own measurement** (we saw a phantom 10 ms "max latency" with
nothing running). Preallocate, or don't collect.

⚠️ **Assigning a concrete value to an `any` boxes it — that allocates.** A benchmark that parks its
result in `var sink any` measures the allocator:

```go
var sink any
sink = time.Now()          // 55 ns/op  ← 8 ns of clock + 47 ns of allocator
```
```go
var sinkTime time.Time
sinkTime = time.Now()      // 8 ns/op, 0 allocs/op
```
Use **typed** sinks. The tell: a component benchmarked *slower than the whole* it belongs to
(`RawMapLookup` at 78 ns inside a 67 ns `Get`). Always pass **`-benchmem`** and demand
`0 allocs/op` before believing a microbenchmark.

The sink is still required — without storing the result somewhere, the compiler can prove the call
has no effect and delete it.

**Never `-race` a benchmark.** 5–20× overhead; every number becomes meaningless.

### Algorithms don't predict memory
`samplePass` touches exactly 20 keys at every cache size, so it "should" be constant time. Measured:
953 ns at 1k keys, 7,064 ns at 1M — **7.4× growth over 1000× the data.** Same work, different
cost: at 1k the map is in L1; at 1M every random bucket probe is a cache and TLB miss.

Still flat-*ish* against a full scan's true `O(n)` (1,128× growth over the same range), so the design
holds. But the algorithm said *constant* and the hardware said *nearly*. **Measure.**

### Formatting
`gofmt -l ./cache/` **l**ists files that aren't canonically formatted, printing nothing when clean —
a check, not a fix. `gofmt -w f.go` **w**rites the fix in place. (`go fmt ./...` wraps `-l -w`.)

**Zero configuration**, unlike `clang-format`'s hundreds of knobs. Tabs, sorted imports, aligned
struct fields, blank lines between declarations. Adding `lastUsed` to `entry` re-aligned the other
two fields because `gofmt` aligns a block around its longest name.

It catches **nothing** — layout only; correctness is `go vet` and `-race`. The payoff for giving up
the knobs: since the output is canonical, **every diff in a Go review is a semantic diff.** No line
ever changes for style.

### `runtime` introspection
```go
runtime.GC()                 // force a full, synchronous collection
runtime.ReadMemStats(&m)     // m.HeapAlloc = bytes of REACHABLE heap
runtime.NumGoroutine()       // goroutine leak checks
```
Forcing a GC before reading `HeapAlloc` is what turns "memory looks high" into "**the collector ran,
and these objects are genuinely reachable**" — i.e. a *logical* leak, which no GC in any language can
fix, because "useless" is a fact about intent, not reachability.

---

## Cross-language sanity check

Mutexes are not a Go thing: `std::mutex`, `threading.Lock`, `synchronized`, `Mutex<T>`,
`Interlocked`. What differs:

- **Consequences.** C++/Go: data race ⇒ UB. **Java defines racy reads** — no fabricated values, no
  corruption (except non-`volatile` `long`/`double`, which may tear).
- **Prevention.** **Rust makes data races a compile error** (ownership, `Send`/`Sync`), and `Mutex<T>`
  holds the data *inside* the lock so you can't reach it unlocked. Rust still permits race conditions
  and deadlocks.
- **Python's GIL half-protects.** `dict` internals stay sane, but `counter += 1` is several bytecodes
  ⇒ lost updates anyway. Free-threaded Python (PEP 703) removes that accidental safety net.
- **JS** dodges it entirely with one event loop — until `SharedArrayBuffer`.

Go's actual contributions: `-race` **in the toolchain**, a runtime that guards its own map, and
channels as an alternative discipline. **Not** the mutex.
