import { useEffect, useRef } from 'react'
import type { State } from '../api'
import { COLORS, colorFor, KEY_R, NODE_R, RING, xy, markerAngles } from '../geometry'

const SVGNS = 'http://www.w3.org/2000/svg'
function svgEl(tag: string, attrs: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(SVGNS, tag)
  for (const k in attrs) e.setAttribute(k, String(attrs[k]))
  return e
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

    const flyPacket = (a1: number, a2: number, color: string) => {
      const [x1, y1] = xy(a1, NODE_R)
      const [x2, y2] = xy(a2, NODE_R)
      const p = svgEl('circle', { cx: x1, cy: y1, r: 6, class: 'packet', fill: color }) as SVGCircleElement
      p.style.color = color
      layer.appendChild(p)
      p.animate(
        [
          { transform: 'translate(0,0)', opacity: 0 },
          { opacity: 1, offset: 0.15 },
          { transform: `translate(${x2 - x1}px,${y2 - y1}px)`, opacity: 1 },
        ],
        { duration: 750, easing: 'cubic-bezier(.4,.1,.2,1)' },
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
        { duration: 720, easing: 'ease-out' },
      ).onfinish = () => c.remove()
    }

    const before: Record<string, Set<string>> = {}
    prev.keys.forEach((k) => (before[k.key] = new Set(k.holders)))
    for (const k of state.keys) {
      const had = before[k.key] ?? new Set<string>()
      for (const h of k.holders) {
        if (!had.has(h)) {
          const src = k.owners.find((o) => o !== h && angle[o] !== undefined) ?? k.holders.find((o) => o !== h)
          if (src !== undefined) flyPacket(angle[src], angle[h], colorFor(h))
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

        {/* key dots on their true hash angle, colored by primary owner, labeled */}
        <g>
          {state.keys.map((k) => {
            const [kx, ky] = xy(k.angle, KEY_R)
            const [lx, ly] = xy(k.angle, KEY_R - 15)
            return (
              <g key={k.key}>
                <circle
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
                <text x={lx} y={ly} className="keylabel">
                  {k.key}
                </text>
              </g>
            )
          })}
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
