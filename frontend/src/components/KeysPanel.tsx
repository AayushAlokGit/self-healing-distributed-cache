import { useState } from 'react'
import { getKey, seedKeys, setKey, type ReadHop, type ReadResult } from '../api'
import { ttlText } from '../format'
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

// Presets for the common case — the point of a TTL here is to *watch* a key die, so
// the useful values are the ones short enough to sit through — plus "custom" for an
// exact figure. "never" is the default, because it is what every other write does.
//
// Milliseconds throughout, matching the ttlMs the dashboard reports back: a lifetime
// read off the key panel can be typed straight back into this box.
const TTLS = [
  { label: 'never', ms: 0 },
  { label: '10s', ms: 10_000 },
  { label: '30s', ms: 30_000 },
  { label: '2m', ms: 120_000 },
]

// parseCustomMs returns the ms, or an error string. Rejecting junk here rather than
// letting Number('') quietly become 0 — which would silently write a permanent key
// when the user asked for an expiring one, the worst possible way to be wrong.
function parseCustomMs(raw: string): { ms?: number; error?: string } {
  const s = raw.trim()
  if (s === '') return { error: 'enter a TTL in milliseconds, or pick a preset' }
  const ms = Number(s)
  if (!Number.isFinite(ms)) return { error: `"${s}" is not a number of milliseconds` }
  if (ms < 0) return { error: 'a TTL cannot be negative' }
  if (ms === 0) return { error: 'a TTL of 0ms never expires — pick "never" if you meant that' }
  return { ms: Math.round(ms) }
}

export function KeysPanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [ttl, setTtl] = useState(0) // ms; 0 = never
  const [custom, setCustom] = useState(false)
  const [customMs, setCustomMs] = useState('')
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<ReadResult | null>(null)
  const { err, run, fail } = useApiError()

  const write = async () => {
    if (!k.trim()) return

    let ms = ttl
    if (custom) {
      const parsed = parseCustomMs(customMs)
      if (parsed.error !== undefined) {
        fail(parsed.error)
        return
      }
      ms = parsed.ms!
    }

    // Only clear the inputs if it actually landed — after a failure the user wants to
    // retry, not retype. The TTL choice sticks, so writing several expiring keys in a
    // row doesn't mean re-picking it every time.
    if (await run(() => setKey(k.trim(), v, ms))) {
      setK('')
      setV('')
      onAction()
    }
  }

  // A failed read is not a miss. "no live copy" is a real answer about the cluster —
  // every replica of this key is down — while a 500 means we never got an answer at
  // all. Showing the first when the second happened would lie about the very thing
  // this demo exists to prove.
  const read = async () => {
    if (!readKey.trim()) return
    setResult(null)
    await run(async () => setResult(await getKey(readKey.trim())))
  }

  const seed = async () => {
    await run(() => seedKeys(8))
    onAction()
  }

  return (
    <div className="card">
      <h2>Keys</h2>
      <div className="kv">
        <input placeholder="key" value={k} onChange={(e) => setK(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && write()} />
        <input placeholder="value" value={v} onChange={(e) => setV(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && write()} />
      </div>

      <div className="ttl-row">
        <span className="ttl-label">expires in</span>
        {TTLS.map((t) => (
          <button
            key={t.label}
            className={'ttl-opt' + (!custom && ttl === t.ms ? ' on' : '')}
            onClick={() => {
              setCustom(false)
              setTtl(t.ms)
            }}
            aria-pressed={!custom && ttl === t.ms}
          >
            {t.label}
          </button>
        ))}
        <button className={'ttl-opt' + (custom ? ' on' : '')} onClick={() => setCustom(true)} aria-pressed={custom}>
          custom
        </button>
      </div>

      {custom && (
        <div className="ttl-custom">
          <input
            type="number"
            min={1}
            step={1}
            placeholder="e.g. 1500"
            value={customMs}
            onChange={(e) => setCustomMs(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && write()}
            aria-label="custom TTL in milliseconds"
          />
          <span className="unit">ms</span>
          {/* Say what they actually asked for, in the same words the key panel will use
              to count it down — so a typo is caught before the key is written, not after
              it has already quietly vanished. */}
          <span className="ttl-preview">
            {(() => {
              const p = parseCustomMs(customMs)
              return p.error !== undefined ? '—' : `dies in ${ttlText(p.ms!)}`
            })()}
          </span>
        </div>
      )}

      <button className="primary" onClick={write}>
        Write key
      </button>

      <div className="spacer" />
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
                <span className={'served-role' + (result.fallback ? ' fallback' : '')}>
                  {servedRole(result)}
                </span>
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
              request taken by <NodeChip id={result.coordinator} /> — it owns nothing here; it
              hashed the key and asked the owners
            </div>
          )}

          {result.path && result.path.length > 0 && <ReadPath path={result.path} />}
        </div>
      )}

      <div className="spacer" />
      <button onClick={seed}>Seed 8 more keys</button>
      <ErrorLine err={err} />
    </div>
  )
}
