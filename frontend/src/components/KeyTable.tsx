import type { KeyState } from '../api'
import { colorFor } from '../geometry'

// ttlText renders a key's remaining life. The value is the server's own remainder, so
// no clock comparison happens here; the dashboard re-polls several times a second and
// simply re-reads it, which is what makes the countdown tick.
function ttlText(ms: number): string {
  const s = Math.ceil(ms / 1000)
  if (s >= 60) return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`
  return `${s}s`
}

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
              {/* Under 10s left, it turns red: the last few ticks before a key dies are
                  the ones worth watching, and this is a demo about watching. */}
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
