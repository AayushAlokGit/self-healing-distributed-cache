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
  // Remaining life in ms; -1 means the key never expires. The server sends the
  // remainder rather than a deadline so the countdown never has to trust that the
  // browser's clock agrees with the cluster's. See cluster/state.go.
  ttlMs: number
}

// What a read reveals about the cluster, not just about the key. servedBy is the node
// that answered; primary is the node the ring says should have. When they differ, a
// replica covered for a primary that was down — which is the read fallback, and the
// entire reason for replicating.
export interface ReadResult {
  found: boolean
  value: string
  servedBy?: string
  primary?: string
  fallback?: boolean
}

export interface VNode {
  angle: number
  node: string
}

// One entry in the activity log. Heals share the list with the kills so that the
// order alone answers "which kill caused which copies" — from/to/keys/cause are
// present on kind === 'heal' only.
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

// fetch() rejects only on a network-level failure — as far as it is concerned a 500
// is a perfectly good response. So every call has to check res.ok itself, or a
// server-side failure resolves as *success* and the UI carries on as though the
// action landed. To a user that reads as "the button is broken", which is the worst
// kind of bug in this demo: it looks like the cluster misbehaved, not the page.
//
// The thrown error carries the server's own reason where it gave one. Our handlers
// answer failures with {"error": "..."} (see writeErr in cmd/server/server.go), so
// prefer that field — but fall back to the raw body, because the response that
// breaks us is precisely the one that didn't follow the contract (a proxy's HTML
// 502, say). Truncate either way so a stray error page can't dump markup into the UI.
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
  // Defensive: never let a null array (e.g. all nodes dead) blank the UI.
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
// ttlSeconds <= 0 means the key never expires.
export const setKey = (key: string, value: string, ttlSeconds: number) =>
  post('/api/set', { key, value, ttlSeconds }, `write ${key}`)
export const seedKeys = (n: number) => post('/api/seed', { n }, `seed ${n} keys`)

export async function getKey(key: string): Promise<ReadResult> {
  const res = await ok(await fetch('/api/get?key=' + encodeURIComponent(key)), `read ${key}`)
  return res.json()
}

// errMsg turns whatever a rejected call threw into something displayable. A caught
// value is `unknown` in TypeScript — it need not be an Error at all — so it has to be
// narrowed before you can reach for .message.
export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
