import { useState } from 'react'
import { type NodeState, type Partition } from '../api'
import { colorFor } from '../geometry'
import { useApi, useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

type Side = 'A' | 'B'

// The two sides of an active cut, each node in its ring colour so a name is findable on the
// ring. Shown only while partitioned.
function SideList({ label, ids }: { label: string; ids: string[] }) {
  return (
    <div className="cut-side">
      <div className="cut-side-label">{label}</div>
      <div className="cut-chips">
        {ids.map((id) => (
          <span className="node-chip" key={id} style={{ color: colorFor(id), borderColor: colorFor(id) }}>
            {id}
          </span>
        ))}
      </div>
    </div>
  )
}

// PartitionPanel drives THE CUT. Assignment (which node is on which side) lives in local
// state until you cut; the *active* cut comes from /state (the `partition` prop), so the panel
// reflects the real backend cut and a reload is faithful. partition !== null means a cut is
// live — the editor is replaced by the two sides plus a Mend button, since re-cutting on top
// of a cut is not a case the demo needs.
export function PartitionPanel({ nodes, partition, onAction }: {
  nodes: NodeState[]
  partition: Partition | null
  onAction: () => void
}) {
  const { cut: cutApi, mend } = useApi()
  const { err, run, fail } = useApiError()
  const [sides, setSides] = useState<Record<string, Side>>({})

  // Only live nodes can be cut — the backend 400s on a dead one ("no such live node").
  const alive = nodes.filter((n) => n.alive)
  const sideA = alive.filter((n) => sides[n.id] === 'A').map((n) => n.id)
  const sideB = alive.filter((n) => sides[n.id] === 'B').map((n) => n.id)

  // Clicking a node's current side again unassigns it (back to reachable-by-both).
  const assign = (id: string, side: Side) =>
    setSides((s) => {
      const next = { ...s }
      if (next[id] === side) delete next[id]
      else next[id] = side
      return next
    })

  const splitEvenly = () => {
    const next: Record<string, Side> = {}
    alive.forEach((n, i) => (next[n.id] = i % 2 === 0 ? 'A' : 'B'))
    setSides(next)
  }

  // On success just refresh — the next /state poll carries the active cut (or its absence),
  // so the panel, banner and ring all update from one source of truth.
  const doCut = async () => {
    if (sideA.length === 0 || sideB.length === 0) {
      fail('each side needs at least one live node')
      return
    }
    if (await run(() => cutApi(sideA, sideB))) {
      onAction()
    }
  }

  const doMend = async () => {
    if (await run(() => mend())) {
      onAction()
    }
  }

  return (
    <div className="card">
      <h2>Network partition · THE CUT</h2>

      {partition ? (
        <>
          <p className="panel-hint">
            The cluster is split. Each side serves alone; a write to the same key on both sides
            becomes a conflict the cache keeps both of (see Read · GET).
          </p>
          <div className="cut-sides">
            <SideList label="Side A" ids={partition.sideA} />
            <div className="cut-divider" aria-hidden>✂</div>
            <SideList label="Side B" ids={partition.sideB} />
          </div>
          <button className="primary" onClick={doMend}>
            Mend network
          </button>
        </>
      ) : (
        <>
          <p className="panel-hint">
            Assign live nodes to Side A or Side B, then cut. A node left off both sides stays
            reachable to everyone. Each side needs at least one node.
          </p>
          <div className="cut-assign">
            {alive.map((n) => (
              <div className="cut-row" key={n.id}>
                <span className="id">
                  <span className="swatch" style={{ color: colorFor(n.id) }} />
                  {n.id}
                </span>
                <div className="cut-seg">
                  <button
                    className={'seg' + (sides[n.id] === 'A' ? ' on-a' : '')}
                    onClick={() => assign(n.id, 'A')}
                    aria-pressed={sides[n.id] === 'A'}
                  >
                    A
                  </button>
                  <button
                    className={'seg' + (sides[n.id] === 'B' ? ' on-b' : '')}
                    onClick={() => assign(n.id, 'B')}
                    aria-pressed={sides[n.id] === 'B'}
                  >
                    B
                  </button>
                </div>
              </div>
            ))}
            {alive.length === 0 && <p className="panel-hint">No live nodes to partition.</p>}
          </div>
          <div className="cut-actions">
            <button onClick={splitEvenly} disabled={alive.length < 2}>
              Split evenly
            </button>
            <button className="primary" onClick={doCut} disabled={sideA.length === 0 || sideB.length === 0}>
              Cut network
            </button>
          </div>
        </>
      )}

      <ErrorLine err={err} />
    </div>
  )
}
