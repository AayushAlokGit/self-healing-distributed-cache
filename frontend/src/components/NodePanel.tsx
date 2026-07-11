import { killNode, reviveNode, type NodeState } from '../api'
import { colorFor } from '../geometry'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

export function NodePanel({ nodes, onAction }: { nodes: NodeState[]; onAction: () => void }) {
  const { err, run } = useApiError()

  // Refresh even when the call failed, so the UI re-syncs instead of showing a stale guess.
  const act = async (fn: () => Promise<void>) => {
    await run(fn)
    onAction()
  }

  return (
    <div className="card">
      <h2>Failure injection</h2>
      <div className="nodes-ctl">
        {nodes.map((n) => (
          <div className={'nrow' + (n.alive ? '' : ' dead')} key={n.id}>
            <div className="id">
              {/* CSS derives fill and glow from currentColor, so only `color` is set here. */}
              <span className="swatch" style={{ color: colorFor(n.id) }} />
              {n.id}
            </div>
            <div className="meta">{n.alive ? `${n.keyCount} keys · ${n.healCopies} pushed` : 'offline'}</div>
            <div className="btns">
              {n.alive ? (
                <button className="kill" onClick={() => act(() => killNode(n.id))}>
                  Kill
                </button>
              ) : (
                <button className="revive" onClick={() => act(() => reviveNode(n.id))}>
                  Revive
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
      <ErrorLine err={err} />
    </div>
  )
}
