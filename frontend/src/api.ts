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
}

export interface VNode {
  angle: number
  node: string
}

export interface ClusterEvent {
  id: number
  kind: string
  msg: string
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

export async function getState(): Promise<State> {
  const res = await fetch('/api/state')
  if (!res.ok) throw new Error(`state ${res.status}`)
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

async function post(path: string, body: unknown): Promise<void> {
  await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export const killNode = (id: string) => post('/api/kill', { id })
export const reviveNode = (id: string) => post('/api/revive', { id })
export const pauseNode = (id: string, paused: boolean) => post('/api/pause', { id, paused })
export const setKey = (key: string, value: string) => post('/api/set', { key, value })
export const seedKeys = (n: number) => post('/api/seed', { n })

export async function getKey(key: string): Promise<{ found: boolean; value: string }> {
  const res = await fetch('/api/get?key=' + encodeURIComponent(key))
  return res.json()
}
