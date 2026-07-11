import { killNode, pauseNode, reviveNode, type NodeState } from '../api'
import { colorFor } from '../geometry'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

export function NodePanel({ nodes, onAction }: { nodes: NodeState[]; onAction: () => void }) {
  const { err, run } = useApiError()

  // Refresh whether or not the call worked: on failure that re-syncs the UI with the
  // truth instead of leaving a stale guess on screen.
  const act = async (fn: () => Promise<void>) => {
    await run(fn)
    onAction()
  }

  return (
    <div className="card">
      <h2>Failure injection</h2>
      <div className="nodes-ctl">
        {nodes.map((n) => (
          <div className={'nrow' + (!n.alive ? ' dead' : n.paused ? ' paused' : '')} key={n.id}>
            <div className="id">
              {/* one colour in, and CSS derives both the fill and the glow from it */}
              <span className="swatch" style={{ color: colorFor(n.id) }} />
              {n.id}
            </div>
            <div className="meta">{n.alive ? `${n.keyCount} keys · ${n.healCopies} pushed` : 'offline'}</div>
            <div className="btns">
              {n.alive ? (
                <>
                  <button className={'pause' + (n.paused ? ' on' : '')} onClick={() => act(() => pauseNode(n.id, !n.paused))}>
                    {n.paused ? 'Resume' : 'Pause'}
                  </button>
                  <button className="kill" onClick={() => act(() => killNode(n.id))}>
                    Kill
                  </button>
                </>
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
