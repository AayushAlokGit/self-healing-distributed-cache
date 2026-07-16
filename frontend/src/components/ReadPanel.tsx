import { useState } from 'react'
import { type ReadHop, type ReadResult } from '../api'
import { useApi, useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'
import { NodeChip } from './NodeChip'

const OUTCOME: Record<ReadHop['outcome'], string> = {
  hit: 'served the read',
  miss: 'alive, holds no copy',
  unreachable: 'never answered',
  skipped: 'not asked',
}

// The answering node's role, read off the path rather than inferred from result.fallback,
// so the two can never disagree.
function servedRole(r: ReadResult): string {
  const hop = r.path?.find((h) => h.node === r.servedBy)
  if (!hop) return ''
  return hop.rank === 0 ? 'its primary' : `replica ${hop.rank} — the primary couldn't`
}

// Every owner of the key, in ring order, and what it said.
function ReadPath({ path }: { path: ReadHop[] }) {
  return (
    <div className="read-path">
      {path.map((h) => (
        <div className={'hop ' + h.outcome} key={h.node}>
          <NodeChip id={h.node} />
          <span className="role">{h.role === 'primary' ? 'primary' : `replica ${h.rank}`}</span>
          <span className="outcome">{OUTCOME[h.outcome] ?? h.outcome}</span>
        </div>
      ))}
    </div>
  )
}

// No onAction: a read changes nothing in the cluster, so there is no new state to fetch.
export function ReadPanel() {
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<ReadResult | null>(null)
  const { getKey } = useApi()
  const { err, run } = useApiError()

  // A failed request is not a miss: clear the old result so an error never renders as
  // "no live copy", which is a real answer about the cluster.
  const read = async () => {
    if (!readKey.trim()) return
    setResult(null)
    await run(async () => setResult(await getKey(readKey.trim())))
  }

  return (
    <div className="card">
      <h2>Read · GET</h2>
      <p className="panel-hint">
        Owners are tried in ring order. The first one holding a copy answers.
      </p>

      <div className="kv">
        <input placeholder="key to read" value={readKey} onChange={(e) => setReadKey(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && read()} />
        <button onClick={read}>Read</button>
      </div>

      {result && (
        <div className={'result' + (result.found ? '' : ' miss')}>
          <div className="headline">
            {result.found ? (
              <>
                <b>{result.value}</b> — served by <NodeChip id={result.servedBy ?? '?'} />
                <span className={'served-role' + (result.fallback ? ' fallback' : '')}>{servedRole(result)}</span>
              </>
            ) : (
              <>
                <b>miss</b> — no live copy
              </>
            )}
          </div>

          {/* The coordinator is not an owner: any live node can coordinate a read. */}
          {result.coordinator && (
            <div className="coord">
              request taken by <NodeChip id={result.coordinator} /> — it owns nothing here; it hashed the
              key and asked the owners
            </div>
          )}

          {result.path && result.path.length > 0 && <ReadPath path={result.path} />}
        </div>
      )}

      <ErrorLine err={err} />
    </div>
  )
}
