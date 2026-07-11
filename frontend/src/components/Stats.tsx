import type { State } from '../api'

export function Stats({ state }: { state: State; prev: State | null }) {
  // Copies stored vs copies the ring asks for — equal in a healthy cluster, and the metric
  // cleanup exists to hold down. min(rf, alive): a key cannot have more copies than nodes.
  const stored = state.keys.reduce((n, k) => n + k.holders.length, 0)
  const wanted = state.keys.length * Math.min(state.rf, state.aliveCount)
  const surplus = stored - wanted

  const items: Array<{ k: string; v: string | number; title?: string; warn?: boolean }> = [
    { k: 'nodes alive', v: `${state.aliveCount}/${state.nodes.length}` },
    { k: 'keys', v: state.keys.length },
    { k: 'replication', v: `R=${state.rf}` },
    {
      k: 'copies',
      v: surplus > 0 ? `${stored}/${wanted}` : stored,
      warn: surplus > 0,
      title:
        surplus > 0
          ? `${surplus} surplus cop${surplus === 1 ? 'y' : 'ies'} — copies a heal left on nodes that no longer own the key. Cleanup drops them once every owner confirms it has the key.`
          : 'every key is on exactly the nodes that own it',
    },
  ]
  return (
    <div className="stats">
      {items.map((it) => (
        <div className="stat" key={it.k} title={it.title}>
          <div className="k">{it.k}</div>
          <div className={'v' + (it.warn ? ' surplus' : '')}>{it.v}</div>
        </div>
      ))}
    </div>
  )
}
