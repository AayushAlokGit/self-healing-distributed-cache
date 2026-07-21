# Self-Healing Distributed Cache

### ▶ **[Live demo](https://self-healing-distributed-cache.vercel.app/)** · [API](https://self-healing-cache-api.onrender.com/)

> ⏳ The backend sleeps when idle on its free tier, so **the first load can take ~30–60 s** while the
> cluster wakes. The dashboard says so instead of looking broken. Everything after that is instant.

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

- **Backend** (`cmd/server`, Go) — runs the cluster-in-a-box and exposes a JSON control API on `:8080`.
  Every route lives under `/api`: `GET /api/state`, `GET /api/get`, and `POST /api/{kill,revive,pause,set,seed,delete,clear}`.
- **Frontend** (`frontend/`, React + Vite + TypeScript) — the dashboard. It builds to static files, so
  in production the FE deploys to a free static host and the Go API to a free container — the HLD's
  "static frontend + one backend container."

## Run the demo

Two processes: `go run ./cmd/server`, and `cd frontend && npm install && npm run dev`.

Open **http://localhost:5173** (Vite proxies `/api` to the Go backend on `:8080`). Then:

1. **Kill a node.** It greys out; its keys drop to two copies (the ring flags them *re-replicating…*)
   and reads keep serving from the survivors via fallback.
2. A grace period later the cluster **restores R=3 on its own** — the heal-copies counter climbs and
   every key is back to three holders. No data lost, no client involved.
3. **Revive it**, and watch the **copies** counter. The heal only ever *copies*, so reviving would leave
   surplus copies stranded on nodes that no longer own those keys — R quietly creeping toward N, one
   outage at a time. A `cleanup` pass drops them, but only once **every** owner confirms it holds the
   key: from a non-owner's side, a surplus copy and the last copy alive look identical.
4. **Read a key** any time to confirm it still serves.
5. **Delete a key** (the ✕ on its chip), or **Delete all**. A delete goes to *every* node, not the
   three the ring names — otherwise a heal quietly puts the key back. See
   [the HLD](docs/HLD.md#why-delete-broadcasts-instead-of-addressing-the-owners).

`POST /api/pause` injects a *false positive* (a GC-pause stand-in): peers suspect a live node, but the
grace period withholds the expensive re-replication — resume it in time and nothing is copied. That is
the difference between a cheap reversible reroute and an expensive irreversible copy. It is API-only;
the dashboard button was dropped.

Backend flags: `-addr :8080` (or `$PORT`), `-grace 2s` (heal grace period), `-seed 12` (keys to preload).

## Deploy

Two pieces, two hosts, both free: the **dashboard** is static files on a CDN, the **cluster** is a
long-running container.

**1 — Backend → Render** (free, no credit card). The repo carries a [`render.yaml`](render.yaml)
Blueprint, so: New → Blueprint → pick this repo → Apply. Render builds the [`Dockerfile`](Dockerfile),
injects `$PORT`, and health-checks `/`. Note the service URL it gives you.

**2 — Frontend → Vercel** (or Netlify/Pages). Point it at the **`frontend/`** directory (framework:
Vite, build `npm run build`, output `dist`) and set `VITE_API_URL = https://<your-render-service>.onrender.com`.

⚠️ **Vite inlines `VITE_API_URL` at build time, not run time.** Change the backend URL and you must
*rebuild* the frontend — there is no runtime config to edit on the CDN.

⚠️ Once split across two origins, the backend's permissive CORS (`cmd/server/server.go`) stops being
decoration and becomes **load-bearing**. It also allows only `GET`/`POST` — which is why every mutating
control-API route is a `POST`, including the deletes. (The node↔node protocol, which no browser touches,
uses real `PUT`/`DELETE` verbs.)

**3 — Get pinged when someone breaks the cluster** (optional). Set `NTFY_TOPIC` on the backend to any
unguessable string, install [ntfy](https://ntfy.sh) on your phone, and subscribe. Unset, the feature is off.

⚠️ **The topic name is the only secret ntfy has** — no key, no account. Anyone who knows it can read your
notifications *and* send you some. Keep it in an env var (`render.yaml` marks it `sync: false`, so it never
enters git), and **never** as a `VITE_*` variable — those are inlined into the bundle every visitor
downloads. The push names the clicker's IP, browser, and where they came from — so the topic name is
guarding *visitor IPs*. Make it long and random.

It fires on exactly two things: **kill a node** and **cut the network** — the demo's money moments.
Everything else stays silent, including revive (the fix is not the fault) and the state poll the dashboard
makes every 600 ms. `cmd/server/faults.go` is deliberately **not** a middleware: only the handler knows
*which* node (the id is in the JSON body) and whether the cluster actually accepted the kill — a push
saying "n7 killed" for a request that 400'd is worse than no push. It caps at 30/hour, because the API is
public and a script holding the button down must not turn into a DoS on your own phone. The transport sits
behind [`notify.Notifier`](notify/notify.go), so swapping ntfy for mail or Slack is a new type in that
package and nothing else.

⚠️ **The backend cannot be serverless**, and that is a property of the design. The five nodes are goroutines
holding in-memory state and heartbeating every 100 ms; a platform that freezes the process between requests
stops the beats, and when traffic resumes **every node falsely convicts every other node as dead**. (Same
reason Google Cloud Run needs instance-based billing rather than its default request-based mode.) Render's
free tier sleeps after ~15 min idle and pays **~30–60 s** to wake — and sleeping *terminates the process*,
so the cache is wiped and re-seeds on boot. Accepted, and surfaced in the UI as *"waking the cluster…"*
rather than an error. Full reasoning and the rejected alternatives: [HLD §8.5](docs/HLD.md).

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
  next R distinct nodes clockwise. Each node holds many *virtual points* rather than one, which is what
  keeps load balanced and spreads a dead node's keys across *all* survivors instead of dumping them on one
  neighbour. The library default is **150 points per node** (`ring.defaultReplicas`), and that is what the
  Phase-2 numbers were measured at; the **demo cluster deliberately drops to 8** (`cluster.demoRingReplicas`)
  so the ring on screen is legible — fewer, bigger arcs you can actually see a key land in. Legibility
  bought at the cost of balance: the property is real, the demo just shows a coarser version of it.
- **Replication** (`node/`) — a write goes to all R owners and acks after a write-quorum W; a read
  tries owners in ring order and returns the first reachable copy, surviving up to R−1 deaths.
- **Failure detection** (`node/`) — every node pings every peer's `/health`; silence past a timeout
  drops the peer from that node's ring. Each node's view is its own — no central coordinator.
- **Self-heal** (`node/`) — a membership change makes each node re-check the keys it holds and copy
  the missing ones onto their current owners, restoring R. Exactly one node sends each key: **the first
  owner, in ring order, that actually holds it** — permission follows the *data*, not the ring
  position, so a node promoted back to primary while holding nothing can still be repopulated. A grace
  period gates the copying against false positives. Deadlines travel with the data, so a healed copy
  expires when the original would.

## Layout

| Path | What |
|---|---|
| `cache/` | Single-node store: mutex-guarded map, TTL + sampling sweeper, O(1) LRU eviction |
| `ring/` | Consistent-hashing ring with virtual nodes |
| `node/` | A cache behind HTTP: coordinating role, replication, heartbeats, self-heal |
| `cluster/` | Cluster-in-a-box manager + god's-eye state and failure-injection controls |
| `notify/` | `Notifier` interface + an ntfy transport (push when somebody kills a node or cuts the network) |
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
