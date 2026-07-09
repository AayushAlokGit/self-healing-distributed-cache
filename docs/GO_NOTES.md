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
