import { useState } from 'react'
import { getKey, seedKeys, setKey } from '../api'
import { useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

export function KeysPanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<{ found: boolean; value: string } | null>(null)
  const { err, run } = useApiError()

  const write = async () => {
    if (!k.trim()) return
    // Only clear the inputs if it actually landed — after a failure the user wants to
    // retry, not retype.
    if (await run(() => setKey(k.trim(), v))) {
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
          <b>{result.found ? result.value : 'miss'}</b> — {result.found ? 'served' : 'no live copy'}
        </div>
      )}

      <div className="spacer" />
      <button onClick={seed}>Seed 8 more keys</button>
      <ErrorLine err={err} />
    </div>
  )
}
