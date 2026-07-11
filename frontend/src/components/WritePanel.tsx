import { useState } from 'react'
import { seedKeys, setKey } from '../api'
import { ttlText } from '../format'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

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

// Everything that PUTS data into the cluster. Split from the read panel because the two
// are different questions — "put this somewhere" and "where did this come from" — and
// sharing one card made them share an error line too, so a failed write left its
// complaint sitting above an unrelated read result.
export function WritePanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [ttl, setTtl] = useState(0) // ms; 0 = never
  const [custom, setCustom] = useState(false)
  const [customMs, setCustomMs] = useState('')
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

  const seed = async () => {
    await run(() => seedKeys(8))
    onAction()
  }

  return (
    <div className="card">
      <h2>Write · SET</h2>
      <p className="panel-hint">
        Any node can take the write. It hashes the key, then writes to all R owners.
      </p>

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
      <button onClick={seed}>Seed 8 more keys</button>
      <ErrorLine err={err} />
    </div>
  )
}
