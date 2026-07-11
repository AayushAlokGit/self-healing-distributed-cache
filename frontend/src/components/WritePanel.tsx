import { useState } from 'react'
import { seedKeys, setKey } from '../api'
import { ttlText } from '../format'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

// TTL presets, in ms to match the ttlMs the dashboard reports back. 0 = never expires.
const TTLS = [
  { label: 'never', ms: 0 },
  { label: '10s', ms: 10_000 },
  { label: '30s', ms: 30_000 },
  { label: '2m', ms: 120_000 },
]

// parseCustomMs returns the ms, or an error string. Junk is rejected here because
// Number('') is 0, which would silently write a permanent key instead of an expiring one.
function parseCustomMs(raw: string): { ms?: number; error?: string } {
  const s = raw.trim()
  if (s === '') return { error: 'enter a TTL in milliseconds, or pick a preset' }
  const ms = Number(s)
  if (!Number.isFinite(ms)) return { error: `"${s}" is not a number of milliseconds` }
  if (ms < 0) return { error: 'a TTL cannot be negative' }
  if (ms === 0) return { error: 'a TTL of 0ms never expires — pick "never" if you meant that' }
  return { ms: Math.round(ms) }
}

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

    // Only clear the inputs if the write landed, so a failure can be retried without retyping.
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
