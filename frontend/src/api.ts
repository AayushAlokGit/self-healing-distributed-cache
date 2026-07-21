// Types mirror the Go cluster.State JSON (cluster/state.go).

// The server also reports `paused` (health stalled, a false-positive injection). The
// dashboard no longer offers that control, so the field is left unmodelled rather than
// carried as a value nothing reads.
export interface NodeState {
  id: string
  alive: boolean
  angle: number
  keyCount: number
  healCopies: number
}

export interface KeyState {
  key: string
  angle: number
  owners: string[]
  holders: string[]
  underReplicated: boolean
  // The key's surviving value(s), reconciled across holders: one normally, two or more when
  // concurrent writes left siblings the cache kept (a conflict).
  values: string[]
  // Remaining life in ms; -1 means the key never expires. The server sends the remainder,
  // not a deadline, so the countdown never depends on the browser's clock.
  ttlMs: number
  // Under an active cut, the owners as each side sees them — each side rings only its own
  // reachable nodes, so a key can have a different owner per side. Absent when the network
  // is whole; then `owners` (the single alive ring) is authoritative.
  ownersA?: string[]
  ownersB?: string[]
}

// One owner's outcome during a read. Rank 0 is the primary; the rest are its replicas.
// miss = the owner answered and holds no copy (a revived node is reachable but empty);
// unreachable = the owner never answered. Different facts, both "did not serve the read".
export interface ReadHop {
  node: string
  rank: number
  role: 'primary' | 'replica'
  outcome: 'hit' | 'miss' | 'unreachable' | 'skipped'
}

// coordinator took the request; servedBy had the value. The coordinator is NOT an owner:
// any live node can coordinate a read. path is every owner, in ring order, and what it said.
export interface ReadResult {
  found: boolean
  value: string
  coordinator?: string
  servedBy?: string
  primary?: string
  fallback?: boolean
  path?: ReadHop[]
  // A conflict: two writes on opposite sides of a network cut never saw each other, so the
  // cache kept BOTH as concurrent siblings (vector-clock concurrent versions) rather than
  // picking a winner. On a conflict read, `value` is empty and `siblings` holds every value;
  // absent/false means the ordinary single-value case, where `value` is authoritative.
  conflict?: boolean
  // The concurrent values, present only when conflict is true. Length is always >= 2: one
  // value is not a conflict. `found` stays true — the key exists, it just has more than one
  // value — so a conflict is a hit, not a miss.
  siblings?: string[]
}

export interface VNode {
  angle: number
  node: string
}

// A network cut splits the live nodes into two sides that cannot hear each other. This shape
// mirrors the /cut request body (what the panel sends).
export interface Cut {
  sideA: string[]
  sideB: string[]
}

// Partition is the ACTIVE cut as /state reports it, so the banner and split ring survive a
// reload — the server, not the dashboard, is now the source of truth for whether a cut is
// live. vnodesA/vnodesB are each side's ring points: a side rings only what it can reach, so
// the two disagree about who owns which arc, and that is exactly what the split ring draws.
export interface Partition {
  sideA: string[]
  sideB: string[]
  vnodesA: VNode[]
  vnodesB: VNode[]
}

// One entry in the activity log, in causal order. from/to/keys/cause are set on
// kind === 'heal' only.
export interface ClusterEvent {
  id: number
  kind: string
  msg: string
  from?: string
  to?: string
  keys?: string[]
  cause?: string
}

export interface State {
  nodes: NodeState[]
  keys: KeyState[]
  vnodes: VNode[]
  rf: number
  // The consistency dial. w + rRead > rf ⇒ no stale reads and the ring is held (a partitioned
  // side without a quorum refuses); otherwise eventual (both sides of a cut serve on).
  w: number
  rRead: number
  aliveCount: number
  totalHealCopies: number
  events: ClusterEvent[]
  // Present only while the network is cut; absent/null means whole.
  partition?: Partition | null
}

// Where the cluster lives. Empty in dev: Vite proxies /api to :8080, so it is same-origin
// and a relative path is right. In production the dashboard is on a static CDN and the
// cluster is in a container on another origin entirely, so VITE_API_URL points at it — and
// the backend's permissive CORS stops being decoration and starts being load-bearing.
//
// ⚠️ Vite inlines import.meta.env at BUILD time, not run time. Change VITE_API_URL and you
// must rebuild; there is no runtime config to edit on the CDN.
export const API_BASE = (import.meta.env.VITE_API_URL ?? '').replace(/\/+$/, '')

// fetch() rejects only on a network failure: a 500 resolves as success, so res.ok must be
// checked here or the UI carries on as though the action landed. Prefer the server's
// {"error": ...}, else the raw body — truncated, so a proxy's HTML 502 can't dump markup.
async function ok(res: Response, what: string): Promise<Response> {
  if (res.ok) return res
  const raw = (await res.text().catch(() => '')).trim()
  let reason = raw
  try {
    const parsed = JSON.parse(raw)
    if (typeof parsed?.error === 'string') reason = parsed.error
  } catch {
    // not JSON — the raw body is the best we have
  }
  throw new Error(`${what} failed (${res.status})` + (reason ? `: ${reason.slice(0, 200)}` : ''))
}

// createApi binds every call to ONE cluster (the backend runs several — see demoClusters in
// cmd/server/main.go). Nothing here can reach another cluster, because the id is captured
// once here and never passed around afterwards: components ask for their api via useApi()
// and cannot name a cluster at all. That is the whole isolation story on the client — a
// component holding a clusterId string could be handed a stale or wrong one and would kill
// a node on the wrong demo, and it would look like a real bug rather than a typo.
//
// ⚠️ It returns a NEW object per call, so callers must useMemo it — see App.tsx.
export function createApi(cluster: string) {
  const base = `${API_BASE}/api/${cluster}`

  const post = async <T,>(path: string, body: unknown, what: string): Promise<T> => {
    const res = await ok(
      await fetch(base + path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }),
      what,
    )
    return res.json()
  }

  return {
    cluster,

    async getState(): Promise<State> {
      const res = await ok(await fetch(base + '/state'), 'state')
      const s = await res.json()
      // Go marshals a nil slice as null, so default every array or the UI blanks.
      return { ...s, nodes: s.nodes ?? [], keys: s.keys ?? [], vnodes: s.vnodes ?? [], events: s.events ?? [], w: s.w ?? 1, rRead: s.rRead ?? 1 }
    },

    // via picks the coordinator node; omit or empty means auto (the backend picks any live node).
    async getKey(key: string, via?: string): Promise<ReadResult> {
      let q = '/get?key=' + encodeURIComponent(key)
      if (via) q += '&via=' + encodeURIComponent(via)
      const res = await ok(await fetch(base + q), `read ${key}`)
      return res.json()
    },

    killNode: (id: string) => post<void>('/kill', { id }, `kill ${id}`),
    reviveNode: (id: string) => post<void>('/revive', { id }, `revive ${id}`),

    // Split the cluster in two: neither side hears the other, both keep serving. The backend
    // 400s on an empty side, a node that isn't currently alive/known, or one named on both
    // sides — surfaced verbatim on the panel's error line. mend clears whatever cut is active.
    cut: (sideA: string[], sideB: string[]) => post<void>('/cut', { sideA, sideB }, 'cut network'),
    mend: () => post<void>('/mend', {}, 'mend network'),

    // The consistency dial: W (write quorum) and R_read (read quorum), cluster-wide. The backend
    // 400s an out-of-range pair (each must be in [1, R]). w+rRead>R holds the ring, no stale reads.
    setQuorum: (w: number, rRead: number) => post<void>('/quorum', { w, rRead }, 'set quorum'),

    // ttlMs <= 0 means the key never expires. Same unit as KeyState.ttlMs. via picks the
    // coordinator node; omit or empty means auto (the backend picks any live node).
    setKey: (key: string, value: string, ttlMs: number, via?: string) =>
      post<void>('/set', via ? { key, value, ttlMs, via } : { key, value, ttlMs }, `write ${key}`),
    seedKeys: (n: number) => post<void>('/seed', { n }, `seed ${n} keys`),

    // POST, not DELETE: the control API allows GET/POST only, so a DELETE fails the browser's
    // preflight. dropped is the nodes that held the key — empty means nobody did, which is a
    // successful delete, not an error.
    deleteKey: (key: string) => post<{ dropped: string[] }>('/delete', { key }, `delete ${key}`),

    // keys, not copies: one key on three replicas counts once.
    clearKeys: () => post<{ keys: number }>('/clear', {}, 'delete all keys'),
  }
}

// Api is derived from createApi rather than declared, so the two cannot drift apart.
export type Api = ReturnType<typeof createApi>

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
