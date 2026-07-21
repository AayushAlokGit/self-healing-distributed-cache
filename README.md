# Self-Healing Distributed Cache

### ▶ **[Live demo](https://self-healing-distributed-cache.vercel.app/)** · [API](https://self-healing-cache-api.onrender.com/)

> ⏳ Free tier: the backend sleeps when idle, so the first load can take **~30–60 s** while the cluster wakes.

A distributed, replicated in-memory **key-value store** in Go, hand-rolling the core distributed-systems
algorithms — consistent hashing, replication, failure detection, self-healing, vector clocks, and a tunable
consistency dial — instead of importing them. It's a leaderless, **Dynamo-style AP store**, and its dashboard
is organised around *failure modes*: each tab is a way a distributed system breaks, and how this one survives it.

> **Honest caveat — cluster-in-a-box:** the 5 nodes are goroutines in one process, with **real** HTTP message
> passing, per-node failure detection, and replication. Only the physical spread is collapsed (so it's free to
> host) — the protocol is real. Built as a learning project, phase by phase; the reasoning behind every decision
> lives in [`docs/`](docs/).

## The three demos (dashboard tabs)

1. **Replication & Self-Heal** — *a node dies → staleness.* Kill a node; its keys re-replicate onto the
   survivors and the cache keeps serving — no leader, no client, nothing lost. Revive it and a `cleanup` pass
   drops the surplus copies, but only once *every* owner confirms it holds the key.
2. **Partitions & Conflicts** — *the network splits → divergence.* Cut the cluster in two; both sides keep
   serving, so the same key can take a different write on each side. Vector clocks detect that the two writes
   never saw each other and keep **both** as siblings; a read surfaces them and you resolve in one click.
3. **Consistency Dial** — *the same cut, tuned.* Raise `W`/`R_read` so `W + R_read > R` and the ring is held:
   the losing side of a cut **refuses** rather than diverges — availability spent for consistency. A scorecard
   measures the trade (accepted vs refused vs conflicts).

## Run locally

```
go run ./cmd/server                        # API on :8080
cd frontend && npm install && npm run dev  # dashboard on :5173 (proxies /api)
```

Backend flags: `-addr :8080` (or `$PORT`), `-grace 2s` (heal grace), `-seed 12` (keys preloaded).
Control API is cluster-scoped: `GET`/`POST /api/{cluster}/…`, where `{cluster}` ∈ `replication` · `cap` · `consistency`.

## How it works

- **Consistent hashing** (`ring/`) — keys and nodes hash onto a 32-bit ring; a key belongs to the next R
  distinct nodes clockwise. Virtual points per node keep load balanced and spread a dead node's keys across
  *all* survivors. (150 points/node in the measured library; the demo ring uses 8, for legibility.)
- **Replication** (`node/`) — a write goes to all R owners and acks after a write-quorum `W`; a read tries
  owners in ring order, surviving up to R−1 deaths.
- **Failure detection** (`node/`) — every node pings every peer's `/health`; silence past a timeout drops the
  peer from *that node's* ring. Each view is its own — no central coordinator, no god's-eye view.
- **Self-heal** (`node/`) — a membership change makes each node re-check its keys and copy the missing ones onto
  their owners, restoring R. One sender per key (the first owner that actually holds it); a grace period gates
  copying against false positives; deadlines travel with the data, so a healed copy expires when the original would.
- **Vector clocks** (`vclock/`, `cache/`) — every value carries a version (one counter per node). Reconcile keeps
  the dominant version, or **both** when neither dominates (concurrent ⇒ siblings) — so a partition can't make
  last-write-wins silently destroy an acknowledged write.
- **The cut** (`node/partition.go`) — a partition fault injector living *under* the HTTP clients: a `gate` refuses
  blocked peers, so one cut drops data **and** heartbeats together and each side convicts the other.
- **Conflict handling** — reads gather every owner and surface concurrent siblings; **read-repair** writes the
  reconciled value back so a lagging replica converges on the read; a one-click resolve collapses a conflict by
  writing the merged value back (which dominates both siblings).
- **The consistency dial** (`cluster.SetQuorum`) — `W`/`R_read`, cluster-wide. `W + R_read > R` forbids stale
  reads and **holds the ring**, so a partitioned side that can't reach a quorum refuses (the CP end); otherwise
  the ring shrinks and both sides serve on (the AP end).

## Deploy (free tiers)

- **Backend → Render:** New → Blueprint → this repo → Apply (uses [`render.yaml`](render.yaml) + [`Dockerfile`](Dockerfile)).
- **Frontend → Vercel:** point at `frontend/` (Vite; build `npm run build`, output `dist`), set
  `VITE_API_URL = https://<your-render-service>.onrender.com`.

⚠️ Vite inlines `VITE_API_URL` at **build** time — change it and you must rebuild. Split across two origins, the
backend's permissive CORS (GET/POST only) is load-bearing — which is why every mutating route is a POST. And the
backend **can't be serverless**: the nodes heartbeat every 100 ms, and a platform that freezes the process makes
every node convict every other as dead on resume ([HLD §8.5](docs/HLD.md)). Optional: set `NTFY_TOPIC` for a phone
push when someone kills a node or cuts the network ([ntfy](https://ntfy.sh); off if unset — keep the topic long,
random, and out of `VITE_*`).

## Layout

| Path | What |
|---|---|
| `cache/` | Single-node store: mutex-guarded map, TTL sweeper, O(1) LRU, versioned entries (`[]Entry` + vector clocks) |
| `ring/` | Consistent-hashing ring with virtual nodes |
| `vclock/` | Vector clock: per-node counters, merge/bump, dominance vs concurrency |
| `node/` | A cache behind HTTP: coordinating role, replication, heartbeats, self-heal, the cut, read-repair |
| `cluster/` | Cluster-in-a-box manager + god's-eye state, failure injection, the consistency dial |
| `notify/` | `Notifier` interface + an ntfy push transport |
| `cmd/server/` | Backend: the JSON control API over the clusters |
| `logging/` | Structured logs: text on the console, JSON on disk |
| `frontend/` | React + Vite + TypeScript dashboard |
| `docs/` | Roadmap, HLD, CAP/dial design, progress log |

## Tests

```
go vet ./...            # copied mutexes, bad Printf verbs, …
go test -race ./...     # data races + the full suite
```

## Docs

- [`docs/ROADMAP.md`](docs/ROADMAP.md) — curriculum and phase plan
- [`docs/HLD.md`](docs/HLD.md) — architecture and the failure-mode catalog
- [`docs/CAP.md`](docs/CAP.md) — partitions, quorums, vector clocks, and the consistency dial
- [`docs/PROGRESS.md`](docs/PROGRESS.md) — running progress log
