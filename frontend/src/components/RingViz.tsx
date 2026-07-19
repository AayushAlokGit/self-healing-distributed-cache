import { useEffect, useMemo, useRef } from 'react'
import type { Cut, State } from '../api'
import { COLORS, colorFor, KEY_R, NODE_R, RING, xy, keyLabels, markerAngles, ownershipArcs } from '../geometry'

// Slow enough that a key is visibly seen travelling between nodes; the stagger is capped
// so a large heal still finishes promptly.
const PACKET_MS = 1900
const PACKET_STAGGER_MS = 140
const MAX_STAGGER_MS = 2200
const SHOCK_MS = 1100

export function RingViz({ state, prev, cut }: { state: State; prev: State | null; cut?: Cut | null }) {
  const layerRef = useRef<SVGGElement>(null)
  // Which side of an active cut each node sits on, for the light on-ring tint. Empty when
  // there is no cut, so every node renders exactly as before.
  const sideOf = useMemo(() => {
    const m: Record<string, 'a' | 'b'> = {}
    cut?.sideA.forEach((id) => (m[id] = 'a'))
    cut?.sideB.forEach((id) => (m[id] = 'b'))
    return m
  }, [cut])
  // Must stay memoised: `angles` is an effect dependency, so a fresh object each render
  // would re-run the diff below and fire the same packets twice.
  const angles = useMemo(() => markerAngles(state.nodes), [state.nodes])
  const underCount = state.keys.filter((k) => k.underReplicated).length
  const arcs = useMemo(() => ownershipArcs(state.vnodes), [state.vnodes])
  const labels = useMemo(() => keyLabels(state.keys), [state.keys])

  // Transient particle layer, driven imperatively: these elements animate once and remove
  // themselves, so React never has to know they exist. Diff prev against current to fly a
  // packet when a key gains a holder, and pulse a shockwave when a node dies or returns.
  useEffect(() => {
    const layer = layerRef.current
    if (!prev || !layer) return

    const spawn = (attrs: Record<string, string | number>, frames: Keyframe[], opts: KeyframeAnimationOptions) => {
      const c = document.createElementNS('http://www.w3.org/2000/svg', 'circle')
      for (const k in attrs) c.setAttribute(k, String(attrs[k]))
      layer.appendChild(c)
      c.animate(frames, opts).onfinish = () => c.remove()
    }

    const flyPacket = (from: number, to: number, color: string, delay: number) => {
      const [x1, y1] = xy(from, NODE_R)
      const [x2, y2] = xy(to, NODE_R)
      spawn(
        // `color` feeds the drop-shadow via currentColor; opacity 0 keeps a staggered packet
        // off its source node until its turn comes.
        { cx: x1, cy: y1, r: 7, class: 'packet', fill: color, color, opacity: 0 },
        [
          { transform: 'translate(0,0)', opacity: 0 },
          { opacity: 1, offset: 0.12 },
          { opacity: 1, offset: 0.88 },
          { transform: `translate(${x2 - x1}px,${y2 - y1}px)`, opacity: 0 },
        ],
        { duration: PACKET_MS, delay, easing: 'cubic-bezier(.35,.05,.25,1)', fill: 'backwards' },
      )
    }

    const shock = (angle: number, color: string) => {
      const [cx, cy] = xy(angle, NODE_R)
      spawn({ cx, cy, r: 20, class: 'shock', stroke: color }, [
        { r: 20, opacity: 0.9, strokeWidth: 4 },
        { r: 96, opacity: 0, strokeWidth: 1 },
      ], { duration: SHOCK_MS, easing: 'ease-out' })
    }

    const heldBefore = new Map(prev.keys.map((k) => [k.key, new Set(k.holders)]))
    let launched = 0
    for (const k of state.keys) {
      const had = heldBefore.get(k.key) ?? new Set<string>()
      for (const holder of k.holders) {
        if (had.has(holder)) continue
        // Prefer a live owner as the sender; fall back to any other holder.
        const src = k.owners.find((o) => o !== holder && angles[o] !== undefined) ?? k.holders.find((o) => o !== holder)
        if (src === undefined) continue
        flyPacket(angles[src], angles[holder], colorFor(holder), Math.min(launched * PACKET_STAGGER_MS, MAX_STAGGER_MS))
        launched++
      }
    }

    const aliveBefore = new Map(prev.nodes.map((n) => [n.id, n.alive]))
    for (const n of state.nodes) {
      const was = aliveBefore.get(n.id)
      if (was === undefined || was === n.alive) continue
      shock(angles[n.id], n.alive ? '#34d399' : '#ff5470')
    }
  }, [state, prev, angles])

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

        <circle className="ringbase" cx={360} cy={360} r={RING} />

        {arcs.map((a, i) => (
          <path key={i} className="varc" d={a.d} stroke={colorFor(a.owner)} />
        ))}

        {/* Leaders first: SVG paints in document order, so dots and names sit on top. */}
        {labels.map(
          (l) => l.leader && <line key={l.key} className="keyleader" {...l.leader} />,
        )}

        {/* Key dots sit at their true hash angle, unlike the node markers. */}
        {state.keys.map((k) => {
          const [cx, cy] = xy(k.angle, KEY_R)
          return (
            <circle
              key={k.key}
              cx={cx}
              cy={cy}
              r={5}
              className={'keydot' + (k.underReplicated ? ' under' : '')}
              fill={colorFor(k.owners[0] ?? 'n0')}
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

        {state.nodes.map((n) => {
          const [x, y] = xy(angles[n.id], NODE_R)
          const color = colorFor(n.id)
          const side = sideOf[n.id]
          return (
            <g
              key={n.id}
              className={'nodeg ' + (n.alive ? 'alive' : 'dead') + (side ? ' side-' + side : '')}
              transform={`translate(${x},${y})`}
            >
              <circle className="halo" r={26} fill={n.alive ? color : 'none'} stroke={color} />
              <circle className="core" r={20} fill={n.alive ? '#0b1220' : '#171c26'} stroke={color} filter="url(#glow)" />
              <text className="label">{n.id}</text>
              <text className="sub" y={34}>
                {n.alive ? `${n.keyCount} keys` : 'dead'}
              </text>
              {/* A side tag while partitioned: which half of the cut this node can talk to. */}
              {side && (
                <text className="side-tag" x={24} y={-20}>
                  {side.toUpperCase()}
                </text>
              )}
            </g>
          )
        })}

        <g ref={layerRef} />

        <text className="center-badge" x={360} y={352}>
          {state.aliveCount}
        </text>
        <text className="center-sub" x={360} y={378}>
          nodes alive
        </text>
      </svg>

      {underCount > 0 && (
        <div className="heal-pill">
          <span className="spin">⟳</span>
          re-replicating {underCount} key{underCount > 1 ? 's' : ''}…
        </div>
      )}

      <div className="legend">
        {Object.keys(COLORS).map((id) => (
          <div className="row" key={id}>
            <span className="dot" style={{ color: COLORS[id] }} />
            {id}
          </div>
        ))}
      </div>

      <div className="caption">
        The ring is split into arcs, each coloured by the node that owns that slice. A key (dot) is
        owned by the first node clockwise. Kill a node and watch its keys jump to new owners.
      </div>
    </div>
  )
}
