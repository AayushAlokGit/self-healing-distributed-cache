import { useState } from 'react'
import { ttlText } from '../format'
import { useApi, useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

// TTL presets, in ms to match the ttlMs the dashboard reports back. 0 = never expires.
const TTLS = [
  { label: 'never', ms: 0 },
  { label: '10s', ms: 10_000 },
  { label: '30s', ms: 30_000 },
  { label: '2m', ms: 120_000 },
]

// The server rejects a bigger batch than this; keep the two in step (cmd/server/server.go).
const MAX_SEED = 5000

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

// parseSeedCount returns how many keys to seed, or an error string. Number('') is 0 here
// too, and the server turns an n of 0 into a default batch of 12 — so an empty box would
// seed 12 keys while the button claimed otherwise. Reject it instead.
function parseSeedCount(raw: string): { n?: number; error?: string } {
  const s = raw.trim()
  if (s === '') return { error: 'enter how many keys to seed' }
  const n = Number(s)
  if (!Number.isInteger(n)) return { error: `"${s}" is not a whole number of keys` }
  if (n < 1) return { error: 'seed at least one key' }
  if (n > MAX_SEED) return { error: `${MAX_SEED} keys is the most one batch will seed` }
  return { n }
}

export function WritePanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [ttl, setTtl] = useState(0) // ms; 0 = never
  const [custom, setCustom] = useState(false)
  const [customMs, setCustomMs] = useState('')
  const [seedCount, setSeedCount] = useState('8')
  const { seedKeys, setKey } = useApi()
  const { err, run, fail } = useApiError()

  const seedN = parseSeedCount(seedCount)

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
    if (seedN.error !== undefined) {
      fail(seedN.error)
      return
    }
    // Refresh even if the seed failed: Seed stops at the first bad write, so some of the
    // batch may already be on the ring.
    await run(() => seedKeys(seedN.n!))
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
      <div className="seed-row">
        <input
          type="number"
          min={1}
          max={MAX_SEED}
          step={1}
          value={seedCount}
          onChange={(e) => setSeedCount(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && seed()}
          aria-label="number of keys to seed"
        />
        <button onClick={seed}>
          {seedN.error !== undefined ? 'Seed keys' : `Seed ${seedN.n} ${seedN.n === 1 ? 'key' : 'keys'}`}
        </button>
      </div>
      <ErrorLine err={err} />
    </div>
  )
}
