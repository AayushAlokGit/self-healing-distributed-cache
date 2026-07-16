import { useEffect, useState } from 'react'
import { type KeyState } from '../api'
import { ttlText } from '../format'
import { colorFor } from '../geometry'
import { useApi, useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

// owners[0] is the primary (the first node clockwise); the rest are its replicas.
export function KeyTable({ keys, onAction }: { keys: KeyState[]; onAction: () => void }) {
  const sorted = [...keys].sort((a, b) => a.key.localeCompare(b.key, undefined, { numeric: true }))
  const expiring = keys.filter((k) => k.ttlMs >= 0).length

  const { clearKeys, deleteKey } = useApi()
  const { err, run } = useApiError()
  const [armed, setArmed] = useState(false) // "delete all" wants a second click
  const [busy, setBusy] = useState<string | null>(null)

  // Disarm on a timer: an armed-and-forgotten "delete all" must not fire on a stray click
  // a minute later.
  useEffect(() => {
    if (!armed) return
    const id = setTimeout(() => setArmed(false), 4000)
    return () => clearTimeout(id)
  }, [armed])

  // Refresh even when the call failed, so the UI re-syncs instead of showing a stale guess.
  const del = async (key: string) => {
    setBusy(key)
    await run(() => deleteKey(key))
    setBusy(null)
    onAction()
  }

  const clearAll = async () => {
    if (!armed) {
      setArmed(true)
      return
    }
    setArmed(false)
    await run(clearKeys)
    onAction()
  }

  return (
    <div className="card">
      <div className="card-head">
        <h2>
          Key ownership · {keys.length} keys
          {expiring > 0 && ` · ${expiring} expiring`}
        </h2>
        {keys.length > 0 && (
          <button
            className={'clear-all' + (armed ? ' armed' : '')}
            onClick={clearAll}
            title="delete every key from every node"
          >
            {armed ? 'Delete all — sure?' : 'Delete all'}
          </button>
        )}
      </div>
      <ErrorLine err={err} />
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
              <button
                className="key-del"
                onClick={() => del(k.key)}
                disabled={busy === k.key}
                title={`delete ${k.key} from every node, owner or not`}
                aria-label={`delete ${k.key}`}
              >
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
