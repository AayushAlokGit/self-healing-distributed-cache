import { type NodeState } from '../api'

// liveVia collapses a stale selection back to auto: if the chosen coordinator has since died
// (its option is gone), the id we still hold in state must not be sent — a dead node cannot
// coordinate. Both the rendered value and the request go through this, so they can't disagree.
export function liveVia(nodes: NodeState[], via: string): string {
  return nodes.some((n) => n.alive && n.id === via) ? via : ''
}

// Which node takes the request. auto (empty) lets the backend pick any live node — the default,
// current behaviour. Only alive nodes are offered, since a dead one cannot coordinate.
export function CoordinatorSelect({ nodes, via, onVia }: { nodes: NodeState[]; via: string; onVia: (v: string) => void }) {
  return (
    <div className="via-row">
      <span className="ttl-label">via</span>
      <select className="via-select" value={liveVia(nodes, via)} onChange={(e) => onVia(e.target.value)} aria-label="coordinator node">
        <option value="">auto (any live node)</option>
        {nodes
          .filter((n) => n.alive)
          .map((n) => (
            <option key={n.id} value={n.id}>
              {n.id}
            </option>
          ))}
      </select>
    </div>
  )
}
