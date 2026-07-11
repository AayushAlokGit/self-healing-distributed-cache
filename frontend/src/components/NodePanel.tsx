import { killNode, pauseNode, reviveNode, type NodeState } from '../api'
import { colorFor } from '../geometry'

export function NodePanel({ nodes, onAction }: { nodes: NodeState[]; onAction: () => void }) {
  const act = async (fn: () => Promise<void>) => {
    await fn()
    onAction() // force an immediate refresh so the UI reacts at once
  }
  return (
    <div className="card">
      <h2>Failure injection</h2>
      <div className="nodes-ctl">
        {nodes.map((n) => (
          <div className={'nrow' + (!n.alive ? ' dead' : n.paused ? ' paused' : '')} key={n.id}>
            <div className="id">
              <span className="swatch" style={{ color: colorFor(n.id), background: colorFor(n.id) }} />
              {n.id}
            </div>
            <div className="meta">
              {n.alive ? `${n.keyCount} keys · ${n.healCopies} pushed` : 'offline'}
            </div>
            <div className="btns">
              {n.alive ? (
                <>
                  <button
                    className={'pause' + (n.paused ? ' on' : '')}
                    onClick={() => act(() => pauseNode(n.id, !n.paused))}
                  >
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
    </div>
  )
}
