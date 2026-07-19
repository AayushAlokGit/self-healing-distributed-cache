import type { State } from '../api'

export function Stats({ state, showDial }: { state: State; prev: State | null; showDial?: boolean }) {
  // Copies stored vs copies the ring asks for — equal in a healthy cluster, and the metric
  // cleanup exists to hold down. min(rf, alive): a key cannot have more copies than nodes.
  const stored = state.keys.reduce((n, k) => n + k.holders.length, 0)
  const wanted = state.keys.length * Math.min(state.rf, state.aliveCount)
  const surplus = stored - wanted

  // The dial. Held (W+R_read>R) forbids stale reads and refuses a partitioned side without a
  // quorum; otherwise eventual. Reported by the server, like R, so there is one source of truth.
  const sum = state.w + state.rRead
  const held = sum > state.rf

  const items: Array<{ k: string; v: string | number; title?: string; warn?: boolean; held?: boolean }> = [
    { k: 'nodes alive', v: `${state.aliveCount}/${state.nodes.length}` },
    { k: 'keys', v: state.keys.length },
    { k: 'replication', v: `R=${state.rf}` },
    // The dial tile only on the consistency tab — elsewhere it is fixed at W1·R1 and would be noise.
    ...(showDial
      ? [
          {
            k: 'dial',
            v: `W - ${state.w} · R_Read - ${state.rRead}`,
            held,
            title: held
              ? `W+R_read=${sum} > R=${state.rf}: no stale reads, and the ring is held — a partitioned side that can't reach a quorum refuses.`
              : `W+R_read=${sum} ≤ R=${state.rf}: eventual — stale reads are possible and both sides of a cut serve on.`,
          },
        ]
      : []),
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
          <div className={'v' + (it.warn ? ' surplus' : '') + (it.held ? ' held' : '')}>{it.v}</div>
        </div>
      ))}
    </div>
  )
}
