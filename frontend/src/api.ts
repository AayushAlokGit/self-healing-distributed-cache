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
  // Remaining life in ms; -1 means the key never expires. The server sends the remainder,
  // not a deadline, so the countdown never depends on the browser's clock.
  ttlMs: number
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
  aliveCount: number
  totalHealCopies: number
  events: ClusterEvent[]
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
      return { ...s, nodes: s.nodes ?? [], keys: s.keys ?? [], vnodes: s.vnodes ?? [], events: s.events ?? [] }
    },

    async getKey(key: string): Promise<ReadResult> {
      const res = await ok(await fetch(base + '/get?key=' + encodeURIComponent(key)), `read ${key}`)
      return res.json()
    },

    killNode: (id: string) => post<void>('/kill', { id }, `kill ${id}`),
    reviveNode: (id: string) => post<void>('/revive', { id }, `revive ${id}`),

    // ttlMs <= 0 means the key never expires. Same unit as KeyState.ttlMs.
    setKey: (key: string, value: string, ttlMs: number) => post<void>('/set', { key, value, ttlMs }, `write ${key}`),
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
