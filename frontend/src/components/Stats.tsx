import type { State } from '../api'

export function Stats({ state, prev }: { state: State; prev: State | null }) {
  const healPopped = prev != null && state.totalHealCopies !== prev.totalHealCopies
  const items: Array<{ k: string; v: string | number; pop?: boolean }> = [
    { k: 'nodes alive', v: `${state.aliveCount}/${state.nodes.length}` },
    { k: 'keys', v: state.keys.length },
    { k: 'replication', v: `R=${state.rf}` },
    { k: 'heal copies', v: state.totalHealCopies, pop: healPopped },
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
