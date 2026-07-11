// Types mirror the Go cluster.State JSON (cluster/state.go).

export interface NodeState {
  id: string
  alive: boolean
  paused: boolean
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

export async function getState(): Promise<State> {
  const res = await ok(await fetch('/api/state'), 'state')
  const s = await res.json()
  // Go marshals a nil slice as null, so default every array or the UI blanks.
  return {
    ...s,
    nodes: s.nodes ?? [],
    keys: s.keys ?? [],
    vnodes: s.vnodes ?? [],
    events: s.events ?? [],
  }
}

async function post(path: string, body: unknown, what: string): Promise<void> {
  await ok(
    await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
    what,
  )
}

export const killNode = (id: string) => post('/api/kill', { id }, `kill ${id}`)
export const reviveNode = (id: string) => post('/api/revive', { id }, `revive ${id}`)
export const pauseNode = (id: string, paused: boolean) =>
  post('/api/pause', { id, paused }, `${paused ? 'pause' : 'resume'} ${id}`)
// ttlMs <= 0 means the key never expires. Same unit as KeyState.ttlMs.
export const setKey = (key: string, value: string, ttlMs: number) =>
  post('/api/set', { key, value, ttlMs }, `write ${key}`)
export const seedKeys = (n: number) => post('/api/seed', { n }, `seed ${n} keys`)

export async function getKey(key: string): Promise<ReadResult> {
  const res = await ok(await fetch('/api/get?key=' + encodeURIComponent(key)), `read ${key}`)
  return res.json()
}

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
