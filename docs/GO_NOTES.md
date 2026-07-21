# Go Notes

Go idioms and gotchas as we hit them, with C++/Python parallels. Append as new ones come up;
don't repeat what the standard library docs already say — record what *surprised* us.

**Conventions:** ⚠️ marks a trap that compiles fine and fails at runtime. `→` gives the file where
we first used it.

---

## Zero values: usable, except when they're not

Go zeroes every variable, and several stdlib types are built so the zero value is usable with **no
constructor**: `var mu sync.Mutex`, `var wg sync.WaitGroup`, `var once sync.Once`, `var t time.Time`.

⚠️ **Channels and maps are the exception.** Their zero value is `nil`:

| Zero value | Write | Read |
|---|---|---|
| `nil` map | **panics** | returns zero value, no panic |
| `nil` channel | blocks forever | **blocks forever** |

The nil channel receive is the nastier one: no panic, no error, just a goroutine that never wakes.
Both must be `make()`d. → `cache.go` `newWithSweepInterval`

```go
c := &Cache{
    data: make(map[string]entry),   // nil map write would panic
    done: make(chan struct{}),      // nil channel receive blocks forever
}
// mu, closeOnce, wg deliberately left as zero values
```

⚠️ **`time.Time`'s zero is a valid instant** (Jan 1, year 1), so it works as a "never" sentinel only if
you test it with `IsZero()` — compare it against `now` and "never" reads as "expired 2000 years ago."
→ `entry.expired`

---

## Concurrency

### goroutines
`go f()` runs `f` concurrently. Like `std::thread` / `threading.Thread`, but cheap enough to have
100,000 of them, and **genuinely parallel** — no GIL. → `race_test.go`

### `sync.WaitGroup`
A counter: `Add(1)` / `Done()` / `Wait()`. Collapses "join every thread" into one counter.

`wg.Go(func(){...})` (Go 1.25) bundles `Add(1)` + `go` + `defer Done()`. Prefer it: it makes the
classic "`Add` must precede `go`" bug **unrepresentable** — put `Add(1)` inside the goroutine and
`Wait()` can see zero and return before anything started. → `race_test.go`, `cache.go`

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
store can publish a pointer to a struct whose fields aren't visible yet. (Full argument →
`QUIZZES.md` S5 Q3.)

⚠️ **Never copy a mutex** (or a struct containing one). The copies stop excluding each other. Always
`*Cache`, never `Cache`. `go vet`'s `copylocks` catches it — but **`go test` does not run
`copylocks`**, so `go vet` must be a separate step. → `CLAUDE.md` Commands

### `sync.Once`
`once.Do(f)` runs `f` exactly once, ever, across all goroutines. Needed because closing a closed
channel panics and `Close()` gets called twice in real code. → `Cache.Close`

### `atomic.Pointer[T]`: a field you can swap without taking the lock
```go
log atomic.Pointer[slog.Logger]   // .Store(l) to swap, .Load() to read
```
A whole *pointer* replaced in one indivisible step. Readers `Load()` with no mutex, so a hot path can
reach a config/logger that a rare writer swaps out underneath it. → `Cluster.log`, `Node.log`

Why not just put it under `c.mu`: `Set`/`Get` want to log **without** holding the cluster lock — they
make a network call, and holding a lock across HTTP serializes the whole cluster onto one request.
Reading a plain `*slog.Logger` field outside the lock while `SetLogger` writes it is a **data race**,
and `-race` says so. `atomic.Pointer` makes the read legal.

⚠️ **It makes the pointer swap atomic, not the thing it points at.** Two `Load()`s can return
different loggers, and whatever `T` is must be safe for concurrent use on its own (`slog.Logger` is).
Same atomicity-≠-invariant lesson as check-then-act: the atom is the *pointer*, and the caller's
operation is usually bigger than that.

### Channels
A pipe between goroutines. `ch <- v` sends, `<-ch` receives; **the arrow points where data moves**.
A receive **blocks** until a value arrives — that's the point, it's how you wait without spinning.
`chan struct{}` carries no data (`struct{}` is zero bytes): the *arrival* is the message.

**`close(ch)` is a broadcast.** Every receive on a closed channel returns immediately, forever, for
every receiver; a *send* wakes exactly one. So `close` is the idiom for "stop everybody."

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

### Coalescing signal: a buffered-1 channel + non-blocking send
A **flag**, not a queue. Buffer 1 holds "there is work pending"; the non-blocking send drops the
signal if one is already buffered, so a burst of events schedules exactly **one** wakeup:

```go
healTrigger := make(chan struct{}, 1)   // buffered to 1
// producer (must never block):
select {
case healTrigger <- struct{}{}:  // signal pending
default:                          // already pending — drop, don't queue
}
// consumer: for { select { case <-done: return; case <-healTrigger: heal() } }
```

Use it when the reaction is **idempotent / re-derives full state** — one heal pass re-asserts the
whole invariant, so collapsing ten triggers into one loses nothing. A buffered *queue* is what you
want only if each event carries distinct work. → `node.healTrigger`

### `time.Ticker` vs `time.Sleep`
Use a `Ticker` when a goroutine must stay interruptible. **`Sleep` cannot be interrupted**, so a
`Close()` would block for up to a full interval. `select` can only race *channels*, and `ticker.C`
is `Sleep` reshaped into a channel. → `sweepLoop`

### Request ≠ confirmation
Two separate signals, always:
```go
close(stop)   // ask it to stop
<-swept       // wait until it HAS stopped
```
`Cache.Close()` does this with `close(done)` + `wg.Wait()`. Skip the second and you return while a
goroutine is still touching your data.

### ⚠️ Lock-order inversion: the callback that deadlocks
A callback is the obvious way to let a worker report progress upward, and the classic way to
deadlock — and **`-race` will not warn you**, because a deadlock is not a data race.

`Cluster.Kill` holds `c.mu` and calls `node.Close()`, which blocks on `n.wg.Wait()` until the heal
goroutine exits. So if the heal goroutine reported each copy by calling back into the cluster:

```go
// heal goroutine                 // Kill goroutine
n.onHealCopy(key, to)             c.mu.Lock()
  └─ c.mu.Lock()   ← blocked      n.Close()
                                    └─ n.wg.Wait()  ← blocked on the heal goroutine
```

Each waits for a lock the other will never release. **Two locks acquired in opposite orders by two
goroutines is all a deadlock needs** — here the second "lock" is `wg.Wait()`, easy to miss because it
isn't spelled `Lock`.

**The fix is structural, not a smarter lock:** invert the direction. The node writes what it did into
its *own* buffer under its *own* mutex; the manager **drains** it on its next poll. Nobody calls
upward, so no cycle can form. → `node.healLog` / `node.DrainHealLog`

Rule of thumb: **a lower layer must never call up into the layer that owns it.** Let the upper layer
pull. (Same instinct as `Close()` ownership: whoever constructs it, drives it.)

### Goroutines and the GC
**Every running goroutine's stack is a GC root.** A goroutine that never returns keeps everything it
references reachable *forever* — and since it never returns, its stack never stops being a root: two
leaks holding each other up. `runtime.SetFinalizer` can't save you, because a finalizer runs when an
object becomes unreachable, which is exactly what can't happen.

**Consequence:** a type owning a background goroutine is a **resource, not a value**. It needs
`Close()`, and callers who forget it leak — no compiler, vet, or `-race` check will tell them.

⚠️ **`context` is not a lifetime.** The tempting `New(ctx)` — cancel the ctx, kill the cache — is
wrong: `context` is for **request scope and in-flight cancellation**, `Close()` is for **resource
lifetime**. Tying a cache's lifetime to a shared context makes it impossible to kill *one* node,
which is exactly what our demo does forty times an evening. And `Close()` returns **nothing**, not
`error`: it cannot fail, and inventing an always-nil error to satisfy `io.Closer` is a lie about the
API.

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

**`clear(m)` (Go 1.21) empties a map in place** — and, per the note above, keeps the bucket array. So
`clear` when you want to reuse the map, `m = make(...)` when you want the memory back. `Cache.Clear`
reallocates for exactly that reason; `cluster.Clear` uses `clear(c.deadlines)`, which is small and
refills immediately. → `Cache.Clear`

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

⚠️ **A map field survives a struct copy as a shared reference.** Copying a struct copies the map
*header*, not the entries — `b := a` gives two structs pointing at **one** table, so a write through
either is seen by both. Bit us when `cache.Entry` gained a `vclock.Clock` (`map[string]uint64`): an
`Entry` handed out of the cache aliases the version *still stored inside*. The fix is a discipline, not
a type — `vclock` treats a `Clock` as **immutable**: `Bump`/`Merge` `Clone` first and return a new map,
so the stored version is never mutated in passing. Same family as the S17 `peers`-map aliasing race
(`maps.Clone` per node). → `vclock`, `cache.Entry`

**And the pointer was free.** `BenchmarkGet`: 66.99 → 61.31 → **52.52 ns** across the two rewrites.
The expected cost was a pointer chase; instead it *paid* for one, because a `*node` is addressable
and `Get` stopped hashing the key a second time to store the entry back.

---

## Strings, numbers, time

**`strconv.Itoa(n)`** — int → string. `std::to_string` / `str()`. Go has no implicit numeric→string
coercion, so `"k" + i` is a compile error.

⚠️ **`string(65)` is `"A"`, not `"65"`.** It reads the int as a Unicode code point. Use
`strconv.Itoa` for the number; `string()` only when you mean a code point.

**`fmt.Sprintf("k%d-%d", a, b)`** reads better for several pieces but is **slower** (reflection). In
a hot loop, concatenate with `Itoa`.

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
Matters wherever we serialize entries across nodes.

⚠️ **`time.Now()` has terrible *resolution*, whatever its precision.** It reports nanoseconds and
advances in ~541µs jumps on this Windows box: **13,397 consecutive calls returned the identical
instant.** A `Set` is ~100ns, so ~5,400 back-to-back `Set`s share one timestamp.

Consequence: **you cannot order events by asking a clock.** `lastUsed time.Time` + `Before()` made
LRU pick its victim *at random* among tied entries (and `range` randomizes the order, so the
tie-break is random too). The test failed 5 runs in 10 — flakiness, not a clean failure. Fix: a
**logical clock** — a `uint64` on the `Cache`, incremented under the lock on every access, so two
events tie only if they *are* the same event. Costs an increment instead of a clock read, and it is
the single-node case of the **Lamport clock** problem, where wall clocks on different machines
disagree and can run backwards. → `Cache.tickLocked`
(Same clock seen from the measurement side — *you cannot time one fast operation* — under Testing.)

**`math/rand/v2`.** `rand.New(rand.NewPCG(seed, seed))` for a deterministic generator — a test that
uses the global `rand` is a test whose failures you can't reproduce. `r.IntN(n)` for a uniform draw,
`rand.NewZipf(r, s, v, imax)` for a power law.

⚠️ **`rand.NewZipf` returns `nil` when `s <= 1`**, and panics only later, when you *draw* from it.
The constructor reports the error by handing you something that looks fine. → `hitrate_test.go`

**A `time.Duration` round-trips through a string.** `d.String()` gives `"250ms"` / `"2m0s"`;
`time.ParseDuration` reads it back. Put *that* on the wire, not a float of seconds: `0.001` loses
precision at one end of the scale and legibility at the other, while `"250ms"` is exact at any scale
and readable in a log. → `Cluster.Set` → `node.parseTTL`

**`time.Time` is the right type for a *deadline*; `time.Duration` is the right type for a *TTL*.**
Not interchangeable across a network hop: a duration re-based at every hop drifts later on each one;
an absolute instant, decided once and carried, cannot. (The bug we shipped and fixed → PROGRESS,
Session 10.) In JSON, though, send the browser the **remaining duration**, not the instant — an
instant is read against *the reader's* clock, and a countdown that disagrees between two laptops gets
blamed on the server.

---

## Hashing, sorting, slices

**Hashers are `io.Writer`s.** `h := fnv.New32a(); h.Write([]byte(s)); h.Sum32()` — feed bytes, read
the result. Same shape for `crypto/sha256`, which also offers the one-shot `sha256.Sum256(b)`
returning a `[32]byte`. Slice + `binary.BigEndian.Uint32(sum[:4])` to pull a `uint32` out.

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
Safe only because the read index never lags the write index. → `Ring.Remove`

**Bounded (ring-buffer-ish) log:** `if over := len(s) - maxN; over > 0 { s = slices.Delete(s, 0, over) }`
drops the oldest and keeps the newest `maxN`. ⚠️ Prefer it to the older `s = s[len(s)-maxN:]`, which
quietly **retains the whole backing array** — the dropped elements stay reachable through it, so a
"bounded" log leaks unboundedly. `slices.Delete` shifts and zeroes, so the dropped ones are
collectable. → `Cluster.appendEvent`, `Node.recordHeal`

**`min`/`max` are builtins since Go 1.21** — no import, any ordered type:
`owners[:min(rank, len(owners))]`. (Not `math.Min`, which is `float64`-only and needs casts.)

**`maps.Keys` returns an iterator, not a slice** (Go 1.23+). `slices.Sorted(maps.Keys(m))` is the
idiom for "iterate a map in a stable order" — needed because Go randomizes map iteration, so an
unsorted render order flickers on every poll. → `Node.recordHeal`

---

## net/http (server side)

**A handler is `func(w http.ResponseWriter, r *http.Request)`.** `w.Write` / `io.WriteString(w, s)`
sends the body; the first write implies `200`, so call `w.WriteHeader(code)` *before* writing if you
want another. `http.Error(w, msg, code)` does both.

**`ServeMux` with method+path patterns (Go 1.22+).** `mux.HandleFunc("GET /kv/{key}", h)` — the method
is part of the pattern, and `{key}` is a wildcard read back with `r.PathValue("key")`. `"DELETE /kv"`
and `"DELETE /kv/{key}"` are two different patterns: the first matches `/kv` exactly and does **not**
swallow `/kv/foo`. That is how one verb serves both "delete this key" and "wipe everything". → `node.New`

⚠️ **A `nil` slice marshals to JSON `null`, not `[]`.** `var xs []string` → `null`; `xs := []string{}`
→ `[]`. So a Go API that "returns a list" hands JS a `null` the moment the list is empty, and the first
`.map`/`.filter` on the other side throws. Return the empty literal on every path that can be empty.
→ `cluster.Delete`, `cluster.State`

**The server blocks, so launch it with `go`.** `srv.Serve(ln)` runs until shutdown. Bind the listener
separately (`net.Listen("tcp", ":0")`) so the OS picks a free port and you can read the real one back
via `ln.Addr()` — essential for tests, no port collisions. → `node.Start`

**`srv.Shutdown(ctx)` is the graceful Close.** Stops accepting, lets in-flight handlers finish. Same
resource-not-value lesson as the cache sweeper: an `http.Server` owns a goroutine and must be stopped.

⚠️ **Always `resp.Body.Close()`** on a client response, even if you ignore the body — the connection
leaks otherwise. **Close is not enough to *reuse* it**: the transport only returns a connection to the
keep-alive pool once the body is read to EOF, so a response you don't care about still wants
`io.Copy(io.Discard, resp.Body)` before the Close. Skip it and every call dials a fresh TCP connection.
→ `notify.Ntfy.Notify`

⚠️⚠️ **`*http.Request` is dead the moment the handler returns, and so is its context.** Two separate traps,
both fatal to fire-and-forget work (`go doSomething(r)`):
1. **The request is not yours to keep.** After the handler returns the server may recycle it; reading `r`
   from a goroutine that outlived the handler is a race. Pull out the strings you need *before* the `go`.
2. **`r.Context()` is cancelled when the response is written.** So the very natural
   `go post(r.Context(), ...)` gets cancelled a microsecond after it starts — and it fails *sometimes*,
   depending on which won the race, which is the worst kind of bug. Background work needs
   `context.Background()` and a timeout of its own.
```go
msg := build(r)                    // read r HERE, while it is still alive
go func() {
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)  // NOT r.Context()
    defer cancel()
    send(ctx, msg)
}()
```
→ `faults.announce`

**Behind a proxy, `r.RemoteAddr` is the proxy.** On any PaaS every visitor looks like the same client
unless you read `X-Forwarded-For` — `"client, proxy1, proxy2"`, so the client is the *first* entry.
⚠️ It is a header the caller writes, so it is trivially spoofed: fine for a metric, never for a decision
that matters. → `clientIP`

**`//go:embed` bakes files into the binary.** A magic comment directly above a package-level var:
```go
//go:embed web
var webFS embed.FS
```
The directive must *touch* the var (no blank line), and the path is relative to the `.go` file —
you can't reach `../`. `fs.Sub(webFS, "web")` strips the prefix so `http.FileServer(http.FS(sub))`
serves `web/index.html` at `/`. Result: one self-contained binary, no asset directory to ship — but
**editing the embedded file means rebuilding**, which is the iteration cost. → `cmd/server`

**`signal.NotifyContext`** turns Ctrl-C into a cancelled `context`:
`ctx, stop := signal.NotifyContext(ctx, os.Interrupt)`, then `<-ctx.Done()` blocks until the signal.
Cleaner than a raw `signal.Notify` channel, and it composes with `srv.Shutdown(ctx)`. "Stop the users,
then stop the thing they use": `srv.Shutdown` first, then `cluster.Close()`. → `cmd/server/main`

**Headers carry metadata *about* the body; the body is the value.** `PUT /kv/{key}` sends the value as
the raw body and its deadline as `X-Expires-At`; a `GET` answers with the value plus `X-Coordinator`,
`X-Served-By` and `X-Read-Path`. ⚠️ `w.Header().Set(k, v)` must come **before** the first write — the first write flushes
the headers and a later `Set` is silently ignored. `r.Header.Get(k)` is case-insensitive and returns
`""` when absent, so "no header" and "empty header" are the same thing: an optional field needs a
value that means *absent*. → `node.storeOn`, `node.handleClientGet`

---

## Logging (`log/slog`)

**A library defaults to silence.** `cluster` and `node` hold a logger and *discard* until someone calls
`SetLogger` — `slog.New(slog.DiscardHandler)`. A package that logs on its own terms is one you cannot
shut up, and heartbeats at 100ms would spray through every `go test` run. The application (`cmd/server`)
owns the logger; the libraries only accept one. → `logging.Discard`

**`slog.SetDefault(l)` does more than set slog's default.** It also redirects the *old* `log` package
through your handler — so a stray `log.Printf`, **including the ones inside `net/http`** when it recovers
a handler panic, lands in your JSON file instead of vanishing to stderr in a format nothing can parse.

**To write two formats at once, fan out at the `Handler`, not the `io.Writer`.** `io.MultiWriter`
duplicates **bytes** — that is *after* formatting, so both destinations get the same format. A
`multiHandler` holding `[]slog.Handler` formats each record once per destination: text on the console
for a human watching the demo, JSON on disk for `jq` afterwards. → `logging/logging.go`

Three traps in writing that handler, all of which compile fine:
- ⚠️ **`Clone()` the `Record` per handler.** A `Record` keeps its attrs in a slice a handler may
  `append` to; hand the same one to two handlers and the first can mutate what the second sees.
- ⚠️ **`WithAttrs`/`WithGroup` must return a NEW handler.** slog treats handlers as immutable, so
  mutating in place means one caller's `logger.With(...)` silently rewrites every other holder's logger.
- ⚠️ **`Enabled` must be true if *any* destination wants the level** — otherwise a record bound for the
  debug file is dropped because the console is at info.

**`os.O_APPEND` is what makes concurrent writes to one log file safe.** The OS positions every write at
the current end, so records never interleave mid-line. And append (not truncate) on open: a restart must
not erase the run that explains why you restarted.

**Compile-time interface check:** `var _ slog.Handler = (*multiHandler)(nil)`. A nil pointer assigned to
an interface variable nobody reads — free at runtime, and it turns "missing method" into an error *here*
rather than a confusing one at the `slog.New` call site. The Go idiom for "I meant to implement this."

---

## Pointers and data structures

**No pointer arithmetic, no `->`.** `n.next.prev = n.prev` auto-dereferences at every step; Go has one
selector operator for both values and pointers. Nil dereference **panics** — it doesn't corrupt.

**Sentinel nodes.** Allocate two dummy nodes that hold no data and never move:
```go
c.head.next = c.tail
c.tail.prev = c.head
```
Now every real node has a non-nil `prev` and `next`, so `unlink` and `pushFront` have **zero branches**.
Without them each function needs "am I the first? the last? the only one?" — the classic linked-list bug
farm. Same trick as a C++ `std::list`'s end sentinel. → `Cache.unlink`

**`container/list` exists and we don't use it.** It's `any`-typed (pre-generics), so every element boxes
into an interface — an allocation and a type assertion per access. Hand-rolling three four-line methods
is both faster and clearer, and the algorithm is the point of this project.

**Slices are not lists.** Move-to-front on a slice shifts every element before the target: O(n). Arrays
store order **positionally** (an element's order *is* its address), lists store it in **pointers**, so
reordering a list is a local edit. That is the whole reason LRU uses a list.

---

## Interfaces

**Satisfaction is implicit — there is no `implements`.** A type has the methods or it doesn't; it never
names the interface, and it can satisfy one written *after* it, in a package it has never heard of. C++
and Java both make you declare the relationship up front; Go infers it. → `notify.Notifier`

**Keep them one method wide.** `Notify(ctx, Notification) error` is all any caller needs, so it is all the
interface says. Every method you add is a method every future implementation must write — a wide interface
taxes implementors to serve callers who don't exist yet. (`io.Reader`, `io.Writer`, `error`: all one.)

**The zero implementation beats `nil`.** An unconfigured notifier is a `Nop{}` that discards, *not* a nil
interface — so no call site needs `if n != nil`. Null-object pattern; the nil check you don't write is the
nil check that can't be forgotten. ⚠️ And in Go a nil interface is subtle anyway: a nil *pointer* stored in
an interface is **not** `== nil`, because the interface holds `(type, value)` and the type is non-nil.

**Compile-time check:** `var _ Notifier = (*Ntfy)(nil)` — turns "forgot a method" into an error *here*,
not at the call site. Free at runtime.

**Accept interfaces, return structs.** `newFaults` takes a `Notifier` (any transport); `NewNtfy` returns a
`*Ntfy` (everything it has). The caller can always narrow; it can never widen.

**Take the dependency as a parameter, or you have no seam to test through.** `routes()` used to call
`notify.FromEnv()` itself, and its handlers close over the result — from a test there is no way to reach
inside a closure and swap it. Hoisting the call to `main()` and passing the `Notifier` in makes the fake a
one-line argument. ⚠️ The smell to watch for: a constructor that *reads the environment* is a constructor
whose behaviour you cannot choose.

### Method *values* vs method *expressions* — the receiver as an argument

`c.Kill` is a **method value**: the receiver `c` is captured *now*, and you get a `func(string) error`.
`(*Cluster).Kill` is a **method expression**: nothing is captured, and you get a plain
`func(*Cluster, string) error` — **the receiver becomes the first parameter.**

That distinction did real work in `cmd/server`. `nodeAction` used to take `c.Kill`, which only worked while
there was exactly one cluster to bind at wiring time. With several, the cluster arrives *per request*:

```go
// before — the receiver is baked in at startup
mux.HandleFunc("POST /api/kill", nodeAction(c.Kill))          // func(string) error

// after — no receiver yet; handle() supplies it per request
handle("POST /api/{cluster}/kill", nodeAction((*cluster.Cluster).Kill))  // func(*Cluster, string) error
```

**Python parallel, and it's exact.** `instance.method` is a bound method; `Class.method` is a plain function
whose first parameter is `self`, so `Class.method(instance, arg)` works. Go's two forms are the same idea
with static types. **C++ has no equivalent** — `&Class::method` is a pointer-to-member, which needs an
object *and* `.*`/`->*` to invoke; it is not a plain callable.

Reach for the expression when the receiver is **data**, chosen later — not when it's fixed at wiring time.

---

## Packages, visibility, style

**Lowercase = package-private, uppercase = exported.** Compiler-enforced, unlike Python's `_name`.
Tests declaring `package cache` (not `cache_test`) can reach unexported identifiers — that's how
`newWithSweepInterval` gives tests a seam without widening the public API. → `cache.go`

**if-with-init:** `if v, ok := m[k]; ok { ... }` — declare and test, scoped to the `if`.

**`for i := range n`** (Go 1.22+) ranges over an integer, `0` to `n-1`. And since Go 1.22 each loop
iteration gets its **own** loop variable, so the old `go func(i int){...}(i)` capture workaround is no
longer needed.

---

## Testing and benchmarking

The commands, their flags, and why each is not optional → `CLAUDE.md` Commands.

### Some tests have no assertions
`race_test.go` and `TestCloseIsIdempotent` assert nothing. **The runtime is the judge** — a `panic`, a
`fatal error`, a `DATA RACE` report, or a hang is the failure signal.

`TestCloseStopsSweeper` goes further: if `sweepLoop` ignored `done`, `Close()` blocks forever and the
test times out. **The hang is the assertion.**

### Benchmarks
```go
func BenchmarkGet(b *testing.B) {
    setup()
    for b.Loop() { c.Get("live") }   // resets the timer on first call
}
```
`b.Loop()` (Go 1.24+) replaced `for i := 0; i < b.N; i++`. It resets the timer automatically and **stops
the compiler eliminating the loop body** (the result is discarded, so it'd otherwise be dead code).
`b.Run(name, fn)` makes subbenchmarks — that's the `BenchmarkSweep/100000-20` in the output; the `-20` is
`GOMAXPROCS`.

### ⚠️ You cannot time one fast operation
Measured on this machine: `time.Now()` resolves to **829 µs**; a `Get` takes **67 ns** — 12,000× below
the clock. Per-operation timing prints `p50=0s`. Two ways out:
- **fix the count, measure the batch, divide** — what `b.Loop()` does for you
- **fix the time, count the operations** — `getsIn(c, 500*time.Millisecond)`

> **When the thing you're measuring is faster than your clock, stop measuring instances and start
> counting them.**

⚠️ Don't `append` to a growing slice inside the measured loop. Growing a 4M-element slice triggers a GC
and you'll measure **your own measurement** — we saw a phantom 10 ms "max latency" with nothing running.
Preallocate, or don't collect.

⚠️ **Assigning a concrete value to an `any` boxes it — that allocates.** A benchmark that parks its
result in `var sink any` measures the allocator:

```go
var sink any
sink = time.Now()          // 55 ns/op  ← 8 ns of clock + 47 ns of allocator
var sinkTime time.Time
sinkTime = time.Now()      // 8 ns/op, 0 allocs/op
```
Use **typed** sinks. The tell: a component benchmarked *slower than the whole* it belongs to
(`RawMapLookup` at 78 ns inside a 67 ns `Get`). Always pass **`-benchmem`** and demand `0 allocs/op`
before believing a microbenchmark. The sink is still required — without storing the result somewhere, the
compiler can prove the call has no effect and delete it.

⚠️ **Never `-race` a benchmark.** 5–20× overhead; every number becomes meaningless.

### Algorithms don't predict memory
`samplePass` touches exactly 20 keys at every cache size, so it "should" be constant time. Measured:
953 ns at 1k keys, 7,064 ns at 1M — **7.4× growth over 1000× the data.** Same work, different cost: at 1k
the map is in L1; at 1M every random bucket probe is a cache and TLB miss.

Still flat-*ish* against a full scan's true `O(n)` (1,128× growth over the same range), so the design
holds. But the algorithm said *constant* and the hardware said *nearly*. **Measure.**

### Formatting
`gofmt -l ./cache/` **l**ists files that aren't canonically formatted, printing nothing when clean — a
check, not a fix. `gofmt -w f.go` **w**rites the fix in place. (`go fmt ./...` wraps `-l -w`.)

**Zero configuration**, unlike `clang-format`'s hundreds of knobs. It catches **nothing** — layout only;
correctness is `go vet` and `-race`. The payoff for giving up the knobs: since the output is canonical,
**every diff in a Go review is a semantic diff.** No line ever changes for style.

### `runtime` introspection
```go
runtime.GC()                 // force a full, synchronous collection
runtime.ReadMemStats(&m)     // m.HeapAlloc = bytes of REACHABLE heap
runtime.NumGoroutine()       // goroutine leak checks
```
Forcing a GC before reading `HeapAlloc` turns "memory looks high" into "**the collector ran, and these
objects are genuinely reachable**" — i.e. a *logical* leak, which no GC in any language can fix, because
"useless" is a fact about intent, not reachability.

---

## Cross-language sanity check

Mutexes are not a Go thing: `std::mutex`, `threading.Lock`, `synchronized`, `Mutex<T>`, `Interlocked`.
What differs:

- **Consequences.** C++/Go: data race ⇒ UB. **Java defines racy reads** — no fabricated values, no
  corruption (except non-`volatile` `long`/`double`, which may tear).
- **Prevention.** **Rust makes data races a compile error** (ownership, `Send`/`Sync`), and `Mutex<T>`
  holds the data *inside* the lock so you can't reach it unlocked. Rust still permits race conditions
  and deadlocks.
- **Python's GIL half-protects.** `dict` internals stay sane, but `counter += 1` is several bytecodes
  ⇒ lost updates anyway. Free-threaded Python (PEP 703) removes that accidental safety net.
- **JS** dodges it entirely with one event loop — until `SharedArrayBuffer`.

Go's actual contributions: `-race` **in the toolchain**, a runtime that guards its own map, and channels
as an alternative discipline. **Not** the mutex.
