# Self-Healing Distributed Cache

A distributed in-memory cache built in Go, hand-rolling the core distributed-systems
algorithms — **consistent hashing, replication, failure detection, and self-healing** —
rather than importing them.

**The money moment:** kill a node and watch its keys re-replicate onto the survivors while
the cache keeps serving.

> **Topology note (honest by design):** this is a *cluster-in-a-box* — the N nodes run as
> separate processes/goroutines with **real message passing, real failure detection, and real
> replication**, but collapsed onto a single container so it's free to host. The protocol is
> real; only the physical spread is collapsed.

## Status

Early development. Built as a learning project, phase by phase:

| Phase | Focus | State |
|---|---|---|
| 0 | Foundations (cache basics, CAP/AP, cluster-in-a-box) | ✅ done |
| 1 | Naive single-node cache (map + TTL + LRU, thread-safe) | 🔨 in progress |
| 2 | Consistent hashing (the ring, virtual nodes) | ⏳ |
| 3 | Replication (factor R, primary + replicas) | ⏳ |
| 4 | Failure detection (heartbeats, false positives) | ⏳ |
| 5 | Self-heal (re-replication on membership change) | ⏳ |
| 6 | Dashboard (ring viz + failure-injection controls) | ⏳ |

## Docs

- [`docs/ROADMAP.md`](docs/ROADMAP.md) — full curriculum and phase plan
- [`docs/HLD.md`](docs/HLD.md) — high-level design, architecture, and failure-mode catalog
- [`docs/PROGRESS.md`](docs/PROGRESS.md) — running progress log
