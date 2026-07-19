// Ring geometry and the per-node colour palette.

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

// Marker angles are display-only, spread evenly. A node has many scattered ring points
// (8 in the demo ring), so no single angle is its real position; the ownership arcs show
// the true spread.
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

// The segment from one virtual point to the next clockwise belongs to the NEXT point's
// node. Requires vnodes sorted by angle. r defaults to the ring radius; a cut draws each
// side's ownership as its own concentric band, so it passes two different radii.
export function ownershipArcs(vnodes: { angle: number; node: string }[], r: number = RING) {
  return vnodes.map((v, i) => {
    const next = vnodes[(i + 1) % vnodes.length]
    return { d: arcPath(v.angle, next.angle, r), owner: next.node }
  })
}

// splitDot returns the left and right semicircle paths of a radius-r dot at (cx, cy), for a
// key owned by a different node on each side of a cut — one half in each side's owner colour.
export function splitDot(cx: number, cy: number, r: number) {
  return {
    left: `M ${cx} ${cy - r} A ${r} ${r} 0 0 0 ${cx} ${cy + r} Z`,
    right: `M ${cx} ${cy - r} A ${r} ${r} 0 0 1 ${cx} ${cy + r} Z`,
  }
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

// keyLabels places each key's name on its own radial spoke. Keys closer than MIN_SEP_DEG
// step inward a tier, with a leader line back to the dot; labels past 180 degrees are
// flipped and re-anchored so they don't read upside-down.
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
