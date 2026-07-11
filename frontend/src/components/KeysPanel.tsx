import { useState } from 'react'
import { getKey, seedKeys, setKey, type ReadResult } from '../api'
import { colorFor } from '../geometry'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

// Presets rather than a free-text box: the point of a TTL here is to *watch* a key
// die, so the useful values are the ones short enough to sit through. "never" is the
// default because it is what every other write in the demo does.
const TTLS = [
  { label: 'never', secs: 0 },
  { label: '10s', secs: 10 },
  { label: '30s', secs: 30 },
  { label: '2m', secs: 120 },
]

export function KeysPanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [ttl, setTtl] = useState(0)
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<ReadResult | null>(null)
  const { err, run } = useApiError()

  const write = async () => {
    if (!k.trim()) return
    // Only clear the inputs if it actually landed — after a failure the user wants to
    // retry, not retype. The TTL choice sticks, so writing several expiring keys in a
    // row doesn't mean re-picking it every time.
    if (await run(() => setKey(k.trim(), v, ttl))) {
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
            className={'ttl-opt' + (ttl === t.secs ? ' on' : '')}
            onClick={() => setTtl(t.secs)}
            aria-pressed={ttl === t.secs}
          >
            {t.label}
          </button>
        ))}
      </div>

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
          {result.found ? (
            <>
              <b>{result.value}</b> — served by{' '}
              <span className="node-chip" style={{ color: colorFor(result.servedBy ?? ''), borderColor: colorFor(result.servedBy ?? '') }}>
                {result.servedBy}
              </span>
              {/* The fallback is the payoff: the primary could not serve, and a replica
                  did anyway. Deliberately NOT phrased as "the primary is down" — it may
                  be perfectly alive and simply not hold the key yet, which is precisely
                  the state a revived node is in before the heal refills it. */}
              {result.fallback && (
                <div className="fallback-note">
                  ↳ primary <b>{result.primary}</b> couldn't serve it — a replica answered
                </div>
              )}
            </>
          ) : (
            <>
              <b>miss</b> — no live copy
            </>
          )}
        </div>
      )}

      <div className="spacer" />
      <button onClick={seed}>Seed 8 more keys</button>
      <ErrorLine err={err} />
    </div>
  )
}
