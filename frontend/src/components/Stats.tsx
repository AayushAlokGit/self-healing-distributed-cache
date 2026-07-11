import type { State } from '../api'

export function Stats({ state }: { state: State; prev: State | null }) {
  const items: Array<{ k: string; v: string | number; pop?: boolean }> = [
    { k: 'nodes alive', v: `${state.aliveCount}/${state.nodes.length}` },
    { k: 'keys', v: state.keys.length },
    { k: 'replication', v: `R=${state.rf}` },
  ]
  return (
    <div className="stats">
      {items.map((it) => (
        <div className="stat" key={it.k}>
          <div className="k">{it.k}</div>
          <div className={'v' + (it.pop ? ' pop' : '')}>{it.v}</div>
        </div>
      ))}
    </div>
  )
}
