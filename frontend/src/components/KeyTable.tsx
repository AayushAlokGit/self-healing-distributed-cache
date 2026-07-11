import type { KeyState } from '../api'
import { colorFor } from '../geometry'

// KeyTable shows the precise key -> owner-nodes mapping, keeping that detail off
// the ring (which now shows only nodes, ownership arcs, and key movement).
//
// Owners are named, not just coloured: a row of dots forces you to look the colour
// up in the legend before you can say *which* node holds a key. The id is the fact
// you actually want; the colour just ties it back to the ring. The primary (the
// first node clockwise) is a solid pill, its replicas outlined — so replication
// factor is readable at a glance without a key.
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
                    className={'oid' + (i === 0 ? ' primary' : '')}
                    style={
                      i === 0
                        ? { background: colorFor(o), borderColor: colorFor(o), color: '#08101c' }
                        : { color: colorFor(o), borderColor: colorFor(o) }
                    }
                    title={o + (i === 0 ? ' — primary (first node clockwise)' : ' — replica')}
                  >
                    {o}
                  </span>
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
