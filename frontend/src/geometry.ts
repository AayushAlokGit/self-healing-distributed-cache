// Ring geometry and the per-node colour palette. Everything positional lives here so
// RingViz can stay a view: it maps over what these functions return and draws it.

import type { KeyState, NodeState } from './api'

const CX = 360
const CY = 360
export const RING = 250
export const NODE_R = 298 // node markers, just outside the ring
export const KEY_R = 226 // key dots, just inside the ring's inner edge

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

// Node markers are spread evenly for legibility. A physical node has ~150 scattered
// ring points (the arcs show the real spread), so no single angle is "true" — even
// spacing is the honest, readable choice.
export function markerAngles(nodes: NodeState[]): Record<string, number> {
  const m: Record<string, number> = {}
  nodes.forEach((n, i) => (m[n.id] = (i / nodes.length) * 360))
  return m
}

// arcPath draws the ring segment from angle a1 clockwise to a2 at radius r.
function arcPath(a1: number, a2: number, r: number): string {
  const [x1, y1] = xy(a1, r)
  const [x2, y2] = xy(a2, r)
  const delta = (a2 - a1 + 360) % 360
  return `M ${x1} ${y1} A ${r} ${r} 0 ${delta > 180 ? 1 : 0} 1 ${x2} ${y2}`
}

// ownershipArcs: the segment from one virtual point to the next clockwise belongs to
// the node the *next* point belongs to — so the ring literally shows whose slice is
// whose. vnodes arrive sorted by angle.
export function ownershipArcs(vnodes: { angle: number; node: string }[]) {
  return vnodes.map((v, i) => {
    const next = vnodes[(i + 1) % vnodes.length]
    return { d: arcPath(v.angle, next.angle, RING), owner: next.node }
  })
}

const LABEL_R = KEY_R - 13
const LABEL_STEP = 46 // a tier steps inward along the same spoke, so it must clear a whole label
const LABEL_TIERS = 3 // 3 x 46 still stops well short of the centre badge
const MIN_SEP_DEG = 4.5 // closer than this and two labels would touch

export interface Label {
  key: string
  x: number
  y: number
  rot: number
  anchor: 'start' | 'end'
  leader?: { x1: number; y1: number; x2: number; y2: number }
}

// keyLabels places each key's name on its own radial spoke.
//
// Drawn horizontally, "key:12" is ~34 units wide — ~9 degrees of arc at this radius,
// while keys hashed at random average only ~15 degrees apart, so nearly every label
// overlapped its neighbour. Rotated onto a spoke, a label's angular footprint is its
// *height* (~2.5 degrees) and most collisions simply vanish.
//
// The survivors — keys hashing within a few degrees of each other — step inward a tier
// at a time, with a leader line back to the dot. That is honest: on this ring only the
// angle carries meaning (it *is* the hash), so radius is free real estate.
//
// A label on the left half would come out upside-down, so it is flipped and anchored
// from the other end; both halves then read left-to-right, inward.
export function keyLabels(keys: KeyState[]): Label[] {
  const sorted = [...keys].sort((a, b) => a.angle - b.angle)
  let prevAngle = -Infinity
  let tier = 0

  return sorted.map((k) => {
    tier = k.angle - prevAngle < MIN_SEP_DEG ? (tier + 1) % LABEL_TIERS : 0
    prevAngle = k.angle

    const r = LABEL_R - tier * LABEL_STEP
    const [x, y] = xy(k.angle, r)
    const flip = k.angle > 180
    const label: Label = {
      key: k.key,
      x,
      y,
      rot: flip ? k.angle + 90 : k.angle - 90,
      anchor: flip ? 'start' : 'end',
    }

    if (tier > 0) {
      const [x1, y1] = xy(k.angle, KEY_R - 6)
      const [x2, y2] = xy(k.angle, r + 4)
      label.leader = { x1, y1, x2, y2 }
    }
    return label
  })
}
