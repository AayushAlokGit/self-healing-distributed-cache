import type { KeyState } from '../api'
import { ttlText } from '../format'
import { colorFor } from '../geometry'

// owners[0] is the primary (the first node clockwise); the rest are its replicas.
export function KeyTable({ keys }: { keys: KeyState[] }) {
  const sorted = [...keys].sort((a, b) => a.key.localeCompare(b.key, undefined, { numeric: true }))
  const expiring = keys.filter((k) => k.ttlMs >= 0).length

  return (
    <div className="card">
      <h2>
        Key ownership · {keys.length} keys
        {expiring > 0 && ` · ${expiring} expiring`}
      </h2>
      {sorted.length === 0 ? (
        <div className="muted-note">no keys — the cluster is empty</div>
      ) : (
        <div className="keygrid">
          {sorted.map((k) => (
            <div className={'keychip' + (k.underReplicated ? ' under' : '')} key={k.key}>
              <span className="kname">{k.key}</span>
              {k.ttlMs >= 0 && (
                <span
                  className={'ttl' + (k.ttlMs < 10_000 ? ' soon' : '')}
                  title={`expires in ${ttlText(k.ttlMs)} — every replica dies at the same instant`}
                >
                  ⏳ {ttlText(k.ttlMs)}
                </span>
              )}
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
