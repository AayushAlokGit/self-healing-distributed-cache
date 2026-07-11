import type { KeyState } from '../api'
import { colorFor } from '../geometry'

// KeyTable shows the precise key -> owner-nodes mapping, keeping that detail off
// the ring (which now shows only nodes, ownership arcs, and key movement).
export function KeyTable({ keys }: { keys: KeyState[] }) {
  const sorted = [...keys].sort((a, b) => a.key.localeCompare(b.key, undefined, { numeric: true }))
  return (
    <div className="card">
      <h2>Key ownership · {keys.length} keys</h2>
      {sorted.length === 0 ? (
        <div className="muted-note">no keys — the cluster is empty</div>
      ) : (
        <div className="keygrid">
          {sorted.map((k) => (
            <div className={'keychip' + (k.underReplicated ? ' under' : '')} key={k.key}>
              <span className="kname">{k.key}</span>
              <span className="owners">
                {k.owners.map((o, i) => (
                  <span
                    key={o}
                    className={'odot' + (i === 0 ? ' primary' : '')}
                    style={{ color: colorFor(o), background: colorFor(o) }}
                    title={o + (i === 0 ? ' (primary)' : '')}
                  />
                ))}
                {k.underReplicated && (
                  <span className="warn" title="under-replicated — re-replicating">
                    ⚠
                  </span>
                )}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
