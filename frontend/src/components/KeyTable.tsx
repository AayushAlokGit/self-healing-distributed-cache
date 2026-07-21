import { useEffect, useState } from 'react'
import { type KeyState, type Partition } from '../api'
import { ttlText } from '../format'
import { colorFor } from '../geometry'
import { useApi, useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

// One owner run: owners[0] is the primary (first node clockwise), the rest replicas.
function OwnerChips({ owners }: { owners: string[] }) {
  return (
    <>
      {owners.map((o, i) => (
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
    </>
  )
}

// owners[0] is the primary (the first node clockwise); the rest are its replicas. Under a cut
// each side has its OWN owners (each rings only what it can reach), so the table shows both —
// mirroring the split ring, where the god's-eye single-ring `owners` would be a fiction no node holds.
export function KeyTable({ keys, partition, onAction }: { keys: KeyState[]; partition: Partition | null; onAction: () => void }) {
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
              <div className="key-head">
                <span className="kname">{k.key}</span>
                <span className="key-head-right">
                  {k.ttlMs >= 0 && (
                    <span
                      className={'ttl' + (k.ttlMs < 10_000 ? ' soon' : '')}
                      title={`expires in ${ttlText(k.ttlMs)} — every replica dies at the same instant`}
                    >
                      ⏳ {ttlText(k.ttlMs)}
                    </span>
                  )}
                  {k.underReplicated && (
                    <span className="warn" title="under-replicated — re-replicating">
                      ⚠
                    </span>
                  )}
                  <button
                    className="key-del"
                    onClick={() => del(k.key)}
                    disabled={busy === k.key}
                    title={`delete ${k.key} from every node, owner or not`}
                    aria-label={`delete ${k.key}`}
                  >
                    ✕
                  </button>
                </span>
              </div>
              {(k.values ?? []).length > 0 && (
                <div className={'kvals' + (k.values.length > 1 ? ' conflict' : '')}>
                  {k.values.length > 1 && (
                    <span className="kvals-badge" title="concurrent siblings — two writes the cache kept, resolve in Read · GET">
                      conflict
                    </span>
                  )}
                  {k.values.map((v, i) => (
                    <span className="kval" key={i}>
                      {v}
                    </span>
                  ))}
                </div>
              )}
              <div className="owners">
                {partition && k.ownersA && k.ownersB ? (
                  <>
                    <span className="owner-run">
                      <span className="side-badge side-a" title="side A owners — A rings only its own reachable nodes">A</span>
                      <OwnerChips owners={k.ownersA} />
                    </span>
                    <span className="owner-run">
                      <span className="side-badge side-b" title="side B owners — B rings only its own reachable nodes">B</span>
                      <OwnerChips owners={k.ownersB} />
                    </span>
                  </>
                ) : (
                  <OwnerChips owners={k.owners} />
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
