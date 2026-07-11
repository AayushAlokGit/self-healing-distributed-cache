// Ring geometry and the per-node color palette, shared across components.

import type { NodeState } from './api'

export const CX = 360
export const CY = 360
export const RING = 250
export const NODE_R = 298 // node markers, just outside the ring
export const KEY_R = 226 // key dots, just inside the ring's inner edge

// Key labels read radially, inward from their dot. Only a key's *angle* is
// meaningful (it's the hash), so a label — and, when two keys hash close
// together, its whole tier — can be pulled inward for free.
export const LABEL_R = KEY_R - 13
// A tier steps inward along the *same* spoke, so the step has to clear a whole
// label ("key:19" is ~34 units at 10px) or the stacked names run into each other.
export const LABEL_STEP = 46
export const LABEL_TIERS = 3 // 3 × 46 still stops well short of the centre badge
export const MIN_SEP_DEG = 4.5 // closer than this and two labels would touch

export const COLORS: Record<string, string> = {
  n0: '#22d3ee',
  n1: '#a78bfa',
  n2: '#fb7185',
  n3: '#fbbf24',
  n4: '#34d399',
}
export const colorFor = (id: string) => COLORS[id] ?? '#8ea3c4'

// angleDeg is measured clockwise from the top (12 o'clock).
export function xy(angleDeg: number, r: number): [number, number] {
  const a = (angleDeg * Math.PI) / 180
  return [CX + r * Math.sin(a), CY - r * Math.cos(a)]
}

// Node markers are spread evenly for legibility. A physical node has ~150
// scattered ring points (the ticks show the real spread), so no single angle is
// "true" — even spacing is the honest, readable choice.
export function markerAngles(nodes: NodeState[]): Record<string, number> {
  const m: Record<string, number> = {}
  nodes.forEach((n, i) => (m[n.id] = (i / nodes.length) * 360))
  return m
}
