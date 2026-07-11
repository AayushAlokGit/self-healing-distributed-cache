import { useEffect, useRef } from 'react'
import type { State } from '../api'
import { COLORS, colorFor, KEY_R, NODE_R, RING, xy, markerAngles } from '../geometry'

const SVGNS = 'http://www.w3.org/2000/svg'
function svgEl(tag: string, attrs: Record<string, string | number>): SVGElement {
  const e = document.createElementNS(SVGNS, tag)
  for (const k in attrs) e.setAttribute(k, String(attrs[k]))
  return e
}

export function RingViz({ state, prev }: { state: State; prev: State | null }) {
  const packetsRef = useRef<SVGGElement>(null)
  const mA = markerAngles(state.nodes)
  const underCount = state.keys.filter((k) => k.underReplicated).length

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

        <circle className="ringline" cx="360" cy="360" r={RING} />

        {/* virtual points, colored by node — the real load spread */}
        <g>
          {state.vnodes.map((v, i) => {
            const [x1, y1] = xy(v.angle, RING - 8)
            const [x2, y2] = xy(v.angle, RING + 8)
            return <line key={i} x1={x1} y1={y1} x2={x2} y2={y2} className="vtick" stroke={colorFor(v.node)} />
          })}
        </g>

        {/* ownership links: key -> each current holder */}
        <g>
          {state.keys.flatMap((k) => {
            const [kx, ky] = xy(k.angle, KEY_R)
            const primary = k.owners[0]
            return k.holders
              .filter((h) => mA[h] !== undefined)
              .map((h) => {
                const [hx, hy] = xy(mA[h], NODE_R)
                return (
                  <line
                    key={k.key + '-' + h}
                    x1={kx}
                    y1={ky}
                    x2={hx}
                    y2={hy}
                    className="link"
                    stroke={colorFor(h)}
                    strokeWidth={h === primary ? 2 : 1.1}
                    opacity={h === primary ? 0.5 : 0.22}
                  />
                )
              })
          })}
        </g>

        {/* key dots on their true hash angle */}
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
        Each node scatters ~150 <b>virtual points</b> (ticks) around the ring so load spreads evenly. A
        key belongs to the next 3 distinct nodes clockwise. Kill one and watch its keys re-replicate.
      </div>
    </div>
  )
}
