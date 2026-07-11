import { useEffect, useMemo, useRef } from 'react'
import type { KeyState, State } from '../api'
import {
  COLORS,
  colorFor,
  KEY_R,
  LABEL_R,
  LABEL_STEP,
  LABEL_TIERS,
  MIN_SEP_DEG,
  NODE_R,
  RING,
  xy,
  markerAngles,
} from '../geometry'

// Animation timing. Deliberately unhurried: a viewer needs to see a key leave one
// node and arrive at another, and the whole point of the demo is that they believe
// it. PACKET_MS is one key's flight; the stagger spaces a heal's packets out so
// they read as a stream rather than a burst, and MAX_STAGGER_MS keeps a 24-key
// heal from taking half a minute to finish drawing.
const PACKET_MS = 1900
const PACKET_STAGGER_MS = 140
const MAX_STAGGER_MS = 2200
const SHOCK_MS = 1100

const SVGNS = 'http://www.w3.org/2000/svg'
function svgEl(tag: string, attrs: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(SVGNS, tag)
  for (const k in attrs) e.setAttribute(k, String(attrs[k]))
  return e
}

// layoutKeyLabels decides where each key's name is drawn.
//
// Horizontal labels on a ring collide almost immediately: "key:12" is ~34 units
// wide, which at this radius spans ~9° of arc, while 24 keys hashed at random
// average only 15° apart. Rotating each label onto its own radial spoke shrinks
// its angular footprint to its *height* (~2.5°), so most collisions vanish.
//
// The ones that survive — keys whose hashes land within a few degrees of each
// other — are stepped inward one tier at a time and joined back to their dot by a
// leader line. That is honest: on this ring only the angle carries meaning (it is
// the hash), so radius is free real estate.
//
// A label on the left half of the ring would come out upside-down, so it is
// flipped 180° and anchored from the other end; both halves end up reading
// left-to-right, inward toward the centre.
interface Label {
  key: string
  angle: number
  tier: number
  x: number
  y: number
  rot: number
  anchor: 'start' | 'end'
}

function layoutKeyLabels(keys: KeyState[]): Label[] {
  const sorted = [...keys].sort((a, b) => a.angle - b.angle)
  let prevAngle = -Infinity
  let tier = 0
  return sorted.map((k) => {
    tier = k.angle - prevAngle < MIN_SEP_DEG ? (tier + 1) % LABEL_TIERS : 0
    prevAngle = k.angle
    const [x, y] = xy(k.angle, LABEL_R - tier * LABEL_STEP)
    const flip = k.angle > 180
    return {
      key: k.key,
      angle: k.angle,
      tier,
      x,
      y,
      rot: flip ? k.angle + 90 : k.angle - 90,
      anchor: flip ? 'start' : 'end',
    }
  })
}

// arcPath draws the ring segment from angle a1 clockwise to a2 at radius r.
function arcPath(a1: number, a2: number, r: number): string {
  const [x1, y1] = xy(a1, r)
  const [x2, y2] = xy(a2, r)
  let delta = a2 - a1
  if (delta < 0) delta += 360
  const largeArc = delta > 180 ? 1 : 0
  return `M ${x1} ${y1} A ${r} ${r} 0 ${largeArc} 1 ${x2} ${y2}`
}

export function RingViz({ state, prev }: { state: State; prev: State | null }) {
  const packetsRef = useRef<SVGGElement>(null)
  const mA = markerAngles(state.nodes)
  const underCount = state.keys.filter((k) => k.underReplicated).length
  const labels = useMemo(() => layoutKeyLabels(state.keys), [state.keys])

  // Colored arcs: the segment from one virtual point to the next clockwise is
  // owned by the node the next point belongs to — so the ring literally shows
  // which slice belongs to whom. vnodes arrive sorted by angle.
  const arcs = state.vnodes.map((v, i) => {
    const next = state.vnodes[(i + 1) % state.vnodes.length]
    return { d: arcPath(v.angle, next.angle, RING), owner: next.node }
  })

  // Imperative particle layer: diff prev vs current to fly a packet when a key
  // gains a holder (re-replication) and pulse a shockwave when a node dies/returns.
  useEffect(() => {
    const layer = packetsRef.current
    if (!prev || !layer) return
    const angle = markerAngles(state.nodes)

    // A packet is a key in flight. Slow enough to actually follow with your eyes —
    // this is the money moment, and at 750ms it read as a flicker. Packets are also
    // staggered rather than fired at once: a heal moves many keys, and twenty dots
    // launching on the same frame is a blur, not a story. Stagger is capped so a
    // large heal still finishes promptly.
    const flyPacket = (a1: number, a2: number, color: string, delay: number) => {
      const [x1, y1] = xy(a1, NODE_R)
      const [x2, y2] = xy(a2, NODE_R)
      const p = svgEl('circle', { cx: x1, cy: y1, r: 7, class: 'packet', fill: color }) as SVGCircleElement
      p.style.color = color
      p.style.opacity = '0' // hidden until its turn, else it sits on the source node
      layer.appendChild(p)
      p.animate(
        [
          { transform: 'translate(0,0)', opacity: 0 },
          { opacity: 1, offset: 0.12 },
          { opacity: 1, offset: 0.88 },
          { transform: `translate(${x2 - x1}px,${y2 - y1}px)`, opacity: 0 },
        ],
        { duration: PACKET_MS, delay, easing: 'cubic-bezier(.35,.05,.25,1)', fill: 'backwards' },
      ).onfinish = () => p.remove()
    }
    const shock = (a: number, color: string) => {
      const [x, y] = xy(a, NODE_R)
      const c = svgEl('circle', { cx: x, cy: y, r: 20, class: 'shock', stroke: color }) as SVGCircleElement
      layer.appendChild(c)
      c.animate(
        [
          { r: 20, opacity: 0.9, strokeWidth: 4 },
          { r: 96, opacity: 0, strokeWidth: 1 },
        ],
        { duration: SHOCK_MS, easing: 'ease-out' },
      ).onfinish = () => c.remove()
    }

    const before: Record<string, Set<string>> = {}
    prev.keys.forEach((k) => (before[k.key] = new Set(k.holders)))
    let launched = 0
    for (const k of state.keys) {
      const had = before[k.key] ?? new Set<string>()
      for (const h of k.holders) {
        if (!had.has(h)) {
          const src = k.owners.find((o) => o !== h && angle[o] !== undefined) ?? k.holders.find((o) => o !== h)
          if (src !== undefined) {
            flyPacket(angle[src], angle[h], colorFor(h), Math.min(launched * PACKET_STAGGER_MS, MAX_STAGGER_MS))
            launched++
          }
        }
      }
    }

    const was: Record<string, boolean> = {}
    prev.nodes.forEach((n) => (was[n.id] = n.alive))
    for (const n of state.nodes) {
      if (n.id in was && was[n.id] && !n.alive) shock(angle[n.id], '#ff5470')
      if (n.id in was && !was[n.id] && n.alive) shock(angle[n.id], '#34d399')
    }
  }, [state, prev])

  return (
    <div className="stage">
      <svg viewBox="0 0 720 720" aria-label="hash ring">
        <defs>
          <filter id="glow" x="-60%" y="-60%" width="220%" height="220%">
            <feGaussianBlur stdDeviation="4" result="b" />
            <feMerge>
              <feMergeNode in="b" />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
        </defs>

        <circle className="ringbase" cx="360" cy="360" r={RING} />

        {/* the ring itself, as arcs colored by the node that owns each slice */}
        <g>
          {arcs.map((a, i) => (
            <path key={i} className="varc" d={a.d} stroke={colorFor(a.owner)} />
          ))}
        </g>

        {/* leader lines, drawn first so the dots and labels sit on top of them */}
        <g>
          {labels
            .filter((l) => l.tier > 0)
            .map((l) => {
              const [x1, y1] = xy(l.angle, KEY_R - 6)
              const [x2, y2] = xy(l.angle, LABEL_R - l.tier * LABEL_STEP + 4)
              return <line key={l.key} className="keyleader" x1={x1} y1={y1} x2={x2} y2={y2} />
            })}
        </g>

        {/* key dots on their true hash angle, colored by primary owner */}
        <g>
          {state.keys.map((k) => {
            const [kx, ky] = xy(k.angle, KEY_R)
            return (
              <circle
                key={k.key}
                cx={kx}
                cy={ky}
                r={5}
                className={'keydot' + (k.underReplicated ? ' under' : '')}
                fill={colorFor(k.owners[0] ?? 'n0')}
                stroke="#0b1220"
                strokeWidth={1.5}
                style={{ color: '#ff5470' }}
              >
                <title>
                  {k.key}
                  {'\n'}owners: {k.owners.join(', ')}
                  {'\n'}holders: {k.holders.join(', ')}
                  {k.underReplicated ? '  ⚠ under-replicated' : ''}
                </title>
              </circle>
            )
          })}
        </g>

        {/* key names, each on its own radial spoke */}
        <g>
          {labels.map((l) => (
            <text
              key={l.key}
              className="keylabel"
              textAnchor={l.anchor}
              transform={`translate(${l.x},${l.y}) rotate(${l.rot})`}
            >
              {l.key}
            </text>
          ))}
        </g>

        {/* node markers, evenly spaced */}
        <g>
          {state.nodes.map((n) => {
            const [x, y] = xy(mA[n.id], NODE_R)
            const cls = 'nodeg ' + (!n.alive ? 'dead' : n.paused ? 'paused' : 'alive')
            const color = colorFor(n.id)
            return (
              <g key={n.id} className={cls} transform={`translate(${x},${y})`}>
                <circle className="halo" r={26} fill={n.alive ? color : 'none'} stroke={color} strokeWidth={2} />
                <circle className="core" r={20} fill={n.alive ? '#0b1220' : '#171c26'} stroke={color} strokeWidth={2.5} filter="url(#glow)" />
                <text className="label">{n.id}</text>
                <text className="sub" y={34}>
                  {n.alive ? (n.paused ? 'paused' : `${n.keyCount} keys`) : 'dead'}
                </text>
              </g>
            )
          })}
        </g>

        <g ref={packetsRef} />

        <text className="center-badge" x="360" y="352" fontSize="30">
          {state.aliveCount}
        </text>
        <text className="center-sub" x="360" y="378">
          nodes alive
        </text>
      </svg>

      {underCount > 0 && (
        <div className="heal-pill">
          <span className="spin">⟳</span>
          <span>
            re-replicating {underCount} key{underCount > 1 ? 's' : ''}…
          </span>
        </div>
      )}

      <div className="legend">
        {Object.keys(COLORS).map((id) => (
          <div className="row" key={id}>
            <span className="dot" style={{ background: COLORS[id], boxShadow: `0 0 8px ${COLORS[id]}` }} />
            {id}
          </div>
        ))}
      </div>

      <div className="caption">
        The ring is split into arcs, each colored by the node that owns that slice. A key (dot) is
        owned by the first node clockwise. Kill a node and watch its keys jump to new owners.
      </div>
    </div>
  )
}
