# Self-Healing Distributed Cache

A distributed, replicated in-memory cache built in Go, hand-rolling the core distributed-systems
algorithms — **consistent hashing, replication, failure detection, and self-healing** — rather than
importing them. It ships with a live dashboard so you can *see it heal*.

**The money moment:** kill a node and watch its keys re-replicate onto the survivors while the cache
keeps serving.

> **Topology note (honest by design):** this is a *cluster-in-a-box* — the N nodes run as goroutines
> in one process with **real HTTP message passing, real per-node failure detection, and real
> replication**, collapsed onto a single container so it's free to host. The protocol is real; only
> the physical spread is collapsed.

## Architecture

- **Backend** (`cmd/server`, Go) — runs the cluster-in-a-box and exposes a JSON control API on
  `:8080` (`/api/state`, `/api/kill`, `/api/revive`, `/api/pause`, `/api/set`, `/api/get`, `/api/seed`,
  `/api/delete`, `/api/clear`).
- **Frontend** (`frontend/`, React + Vite + TypeScript) — the dashboard, talking to the API. It builds
  to static files, so in production the FE deploys to a free static host and the Go API to a free
  container — the HLD's "static frontend + one backend container."

## Run the demo

Two processes. **Terminal 1 — backend:**

```
go run ./cmd/server
```

**Terminal 2 — frontend:**

```
cd frontend
npm install     # first time only
npm run dev
```

Open **http://localhost:5173** (Vite proxies `/api` to the Go backend on `:8080`). Then:

1. **Kill a node.** It greys out; its keys drop to two copies (the ring flags them *re-replicating…*)
   and reads keep serving from the survivors via fallback.
2. A grace period later the cluster **restores R=3 on its own** — the heal-copies counter climbs and
   every key is back to three holders. No data lost, no client involved.
3. **Read a key** any time to confirm it still serves.
4. **Delete a key** (the ✕ on its chip), or **Delete all**. A delete goes to *every* node, not the
   three the ring names — otherwise a heal quietly puts the key back. See
   [the HLD](docs/HLD.md#why-delete-broadcasts-instead-of-addressing-the-owners).

`POST /api/pause` injects a *false positive* (a GC-pause stand-in): peers suspect a live node, but the
grace period withholds the expensive re-replication — resume it in time and nothing is copied. That is
the difference between a cheap reversible reroute and an expensive irreversible copy. It is API-only;
the dashboard button was dropped.

Backend flags: `-addr :8080`, `-grace 2s` (heal grace period), `-seed 12` (keys to preload).

## Status — complete

Built as a learning project, phase by phase. The reasoning behind each decision, the measurements
that drove it, and what breaks without it are written up in [`docs/`](docs/).

| Phase | Focus | State |
|---|---|---|
| 0 | Foundations (cache basics, CAP/AP, cluster-in-a-box) | ✅ done |
| 1 | Single-node cache (map + TTL sweeper + O(1) LRU, thread-safe) | ✅ done |
| 2 | Consistent hashing (the ring, virtual nodes) | ✅ done |
| 3 | Replication (factor R, primary + replicas, read fallback) | ✅ done |
| 4 | Failure detection (heartbeats, false positives) | ✅ done |
| 5 | Self-heal (re-replication on death, grace period) | ✅ done |
| 6 | Dashboard (ring viz + failure-injection controls) | ✅ done |

## How it works

- **Consistent hashing** (`ring/`) — keys and nodes hash onto a 32-bit ring; a key belongs to the
  next R distinct nodes clockwise. ~150 virtual points per node keep load balanced and spread a dead
  node's keys across *all* survivors instead of dumping them on one neighbour.
- **Replication** (`node/`) — a write goes to all R owners and acks after a write-quorum W; a read
  tries owners in ring order and returns the first reachable copy, surviving up to R−1 deaths.
- **Failure detection** (`node/`) — every node pings every peer's `/health`; silence past a timeout
  drops the peer from that node's ring. Each node's view is its own — no central coordinator.
- **Self-heal** (`node/`) — a membership change makes each node re-check the keys it holds and copy
  the missing ones onto their current owners, restoring R. Exactly one node sends each key: **the first
  owner, in ring order, that actually holds it** — permission follows the *data*, not the ring
  position, so a node promoted back to primary while holding nothing can still be repopulated. A grace
  period gates the copying against false positives: a brief GC pause recovers inside the window and
  nothing is copied. Deadlines travel with the data, so a healed copy expires when the original would.

## Layout

| Path | What |
|---|---|
| `cache/` | Single-node store: mutex-guarded map, TTL + sampling sweeper, O(1) LRU eviction |
| `ring/` | Consistent-hashing ring with virtual nodes |
| `node/` | A cache behind HTTP: coordinating role, replication, heartbeats, self-heal |
| `cluster/` | Cluster-in-a-box manager + god's-eye state and failure-injection controls |
| `cmd/server/` | Backend: the JSON control API over the cluster |
| `logging/` | Structured logs: human-readable text on the console, JSON on disk |
| `frontend/` | React + Vite + TypeScript dashboard (the animated hash-ring UI) |
| `docs/` | Roadmap, high-level design, progress log, quizzes, Go notes |

## Tests

```
go vet ./...            # copied mutexes, bad Printf verbs, …
go test -race ./...     # data races + the full suite
```

## Docs

- [`docs/ROADMAP.md`](docs/ROADMAP.md) — full curriculum and phase plan
- [`docs/HLD.md`](docs/HLD.md) — high-level design, architecture, and failure-mode catalog
- [`docs/PROGRESS.md`](docs/PROGRESS.md) — running progress log
