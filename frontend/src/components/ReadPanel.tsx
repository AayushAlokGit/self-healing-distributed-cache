import { useState } from 'react'
import { getKey, type ReadHop, type ReadResult } from '../api'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'
import { NodeChip } from './NodeChip'

// What each outcome means, spelled out. "miss" and "unreachable" look identical to a
// client — no value, either way — and say completely different things about the node,
// so the trace is the only place the difference is ever visible.
const OUTCOME: Record<ReadHop['outcome'], string> = {
  hit: 'served the read',
  miss: 'alive, holds no copy',
  unreachable: 'never answered',
  skipped: 'not asked',
}

// What the answering node was TO THIS KEY. Read off the path rather than inferred from
// result.fallback, so the two can never disagree: the path is the record of what
// happened, and fallback is only a summary of it.
function servedRole(r: ReadResult): string {
  const hop = r.path?.find((h) => h.node === r.servedBy)
  if (!hop) return ''
  return hop.rank === 0 ? 'its primary' : `replica ${hop.rank} — the primary couldn't`
}

// The read path: every owner of the key, in ring order, and what it said. This is the
// fallback made legible — "served by n4" alone tells you a node answered, but not that
// the two owners ahead of it were asked first and could not.
function ReadPath({ path }: { path: ReadHop[] }) {
  return (
    <div className="read-path">
      {path.map((h) => (
        <div className={'hop ' + h.outcome} key={h.node}>
          <NodeChip id={h.node} />
          {/* Rank, not just the word: with R=3 there are two replicas, and which one
              answered says how far down the chain the read had to walk. */}
          <span className="role">{h.role === 'primary' ? 'primary' : `replica ${h.rank}`}</span>
          <span className="outcome">{OUTCOME[h.outcome] ?? h.outcome}</span>
        </div>
      ))}
    </div>
  )
}

// Reads get their own card. A read changes nothing in the cluster — which is why this
// panel takes no onAction: there is no new state to go and fetch. What it produces is
// not a value so much as an ACCOUNT of how the value was found, and that account is the
// self-healing story: the primary was gone, and a replica answered anyway.
export function ReadPanel() {
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<ReadResult | null>(null)
  const { err, run } = useApiError()

  // A failed read is not a miss. "no live copy" is a real answer about the cluster —
  // every replica of this key is down — while a 500 means we never got an answer at
  // all. Showing the first when the second happened would lie about the very thing
  // this demo exists to prove.
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
                {/* The role is the point: naming the node without saying what it WAS to
                    this key leaves the whole story out. A replica answering is the
                    fallback; the primary answering is the ordinary path. */}
                <span className={'served-role' + (result.fallback ? ' fallback' : '')}>{servedRole(result)}</span>
              </>
            ) : (
              <>
                <b>miss</b> — no live copy
              </>
            )}
          </div>

          {/* The coordinator is NOT the node that had the data. Any live node can take
              a read: coordinating is hashing the key and asking its owners, which needs
              no copy of anything. Saying so out loud, because it is the single thing
              people get wrong about how this cluster works. */}
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
