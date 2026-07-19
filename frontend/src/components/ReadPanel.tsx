import { type CSSProperties, useState } from 'react'
import { type NodeState, type ReadHop, type ReadResult } from '../api'
import { useApi, useApiError } from '../hooks'
import { CoordinatorSelect, liveVia } from './CoordinatorSelect'
import { ErrorLine } from './ErrorLine'
import { NodeChip } from './NodeChip'

const OUTCOME: Record<ReadHop['outcome'], string> = {
  hit: 'served the read',
  miss: 'alive, holds no copy',
  unreachable: 'never answered',
  skipped: 'not asked',
}

// One accent per sibling, cycled by index. The point is that two collided writes read as two
// *different* colours at a glance — cyan / violet / amber / green, the dashboard's own tokens
// as space-separated RGB triples so a chip can mix its own alpha (see styles.css `--sib`).
const SIBLING_ACCENTS = ['34 211 238', '167 139 250', '251 191 36', '52 211 153']

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

// The concurrent versions the cache kept because two writes never saw each other. Not a
// winner-picking merge and not corruption: both writes were accepted, and the next write that
// sees both is what reconciles them. Each value gets its own accent so "the cache kept BOTH"
// reads without reading the words.
function Siblings({ values }: { values: string[] }) {
  return (
    <>
      <div className="headline">
        <b className="conflict-badge">conflict</b> — {values.length} concurrent versions, the cache
        kept them all
      </div>
      <p className="conflict-note">
        Two writes landed on opposite sides of a network cut and never saw each other. Neither is
        "wrong," so the cache kept both instead of silently dropping one. The next write that sees
        both reconciles them.
      </p>
      <div className="siblings">
        {values.map((v, i) => (
          // index key: siblings are an unordered set of concurrent values with no id, and the
          // list is replaced wholesale on each read, so position is a stable enough key.
          <div className="sibling" key={i} style={{ '--sib': SIBLING_ACCENTS[i % SIBLING_ACCENTS.length] } as CSSProperties}>
            <span className="sib-tag">v{i + 1}</span>
            <span className="sib-val">{v}</span>
          </div>
        ))}
      </div>
    </>
  )
}

// No onAction: a read changes nothing in the cluster, so there is no new state to fetch.
export function ReadPanel({ nodes }: { nodes: NodeState[] }) {
  const [readKey, setReadKey] = useState('')
  const [via, setVia] = useState('') // coordinator node id; '' = auto
  const [result, setResult] = useState<ReadResult | null>(null)
  const { getKey } = useApi()
  const { err, run } = useApiError()

  // A failed request is not a miss: clear the old result so an error never renders as
  // "no live copy", which is a real answer about the cluster.
  const read = async () => {
    if (!readKey.trim()) return
    setResult(null)
    await run(async () => setResult(await getKey(readKey.trim(), liveVia(nodes, via))))
  }

  // A conflict is still a hit — the key exists, it just carries more than one value — so the
  // container is never the `miss` variant here.
  const isConflict = result?.conflict === true

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

      <CoordinatorSelect nodes={nodes} via={via} onVia={setVia} />

      {result && (
        <div className={'result' + (result.found ? '' : ' miss') + (isConflict ? ' conflict' : '')}>
          {isConflict ? (
            <Siblings values={result.siblings ?? []} />
          ) : (
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
          )}

          {/* The coordinator is not an owner: any live node can coordinate a read. Shown for a
              conflict too — some node still took the request and gathered the versions. */}
          {result.coordinator && (
            <div className="coord">
              request taken by <NodeChip id={result.coordinator} /> — it owns nothing here; it hashed the
              key and asked the owners
            </div>
          )}

          {/* On a conflict the trace shows every owner that answered, which is where the
              divergent versions came from — so it stays useful in both cases. */}
          {result.path && result.path.length > 0 && <ReadPath path={result.path} />}
        </div>
      )}

      <ErrorLine err={err} />

      {/* DEV-ONLY conflict preview. import.meta.env.DEV is statically `false` in `npm run
          build`, so Vite tree-shakes this whole block (and MOCK_CONFLICT) out of the bundle —
          it cannot ship fake data to users. It exists only so the card can be eyeballed before
          the backend sends real conflict reads; delete it once that lands. */}
      {/* {import.meta.env.DEV && (
        <button className="ttl-opt" style={{ marginTop: 8 }} onClick={() => setResult(MOCK_CONFLICT)} title="preview the conflict card with mock data">
          preview conflict (dev)
        </button>
      )} */}
    </div>
  )
}

// DEV-ONLY mock — a real ReadResult of the exact shape the backend will send, so the card is
// still driven by the contract and not by this literal. Referenced only inside the
// import.meta.env.DEV guard above, so it never reaches a production bundle.
const MOCK_CONFLICT: ReadResult = {
  found: true,
  value: '',
  conflict: true,
  siblings: ['cart=[milk, eggs]', 'cart=[milk, bread]'],
  coordinator: 'n2',
  path: [
    { node: 'n0', rank: 0, role: 'primary', outcome: 'hit' },
    { node: 'n1', rank: 1, role: 'replica', outcome: 'hit' },
  ],
}
