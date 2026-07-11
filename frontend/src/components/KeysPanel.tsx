import { useState } from 'react'
import { getKey, seedKeys, setKey } from '../api'

export function KeysPanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<{ found: boolean; value: string } | null>(null)

  const write = async () => {
    if (!k.trim()) return
    await setKey(k.trim(), v)
    setK('')
    setV('')
    onAction()
  }
  const read = async () => {
    if (!readKey.trim()) return
    setResult(await getKey(readKey.trim()))
  }
  const seed = async () => {
    await seedKeys(8)
    onAction()
  }

  return (
    <div className="card">
      <h2>Keys</h2>
      <div className="kv">
        <input placeholder="key" value={k} onChange={(e) => setK(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && write()} />
        <input placeholder="value" value={v} onChange={(e) => setV(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && write()} />
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
              <b>{result.value}</b> — served
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
    </div>
  )
}
