import { useState } from 'react'
import { errMsg, getKey, seedKeys, setKey } from '../api'

export function KeysPanel({ onAction }: { onAction: () => void }) {
  const [k, setK] = useState('')
  const [v, setV] = useState('')
  const [readKey, setReadKey] = useState('')
  const [result, setResult] = useState<{ found: boolean; value: string } | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const write = async () => {
    if (!k.trim()) return
    setErr(null)
    try {
      await setKey(k.trim(), v)
    } catch (e) {
      setErr(errMsg(e)) // keep the inputs: the user will want to retry, not retype
      return
    }
    setK('')
    setV('')
    onAction()
  }

  // A failed read is not a miss. "no live copy" is a real answer about the cluster —
  // every replica of this key is down — while a 500 means the request never got an
  // answer at all. Showing the first when the second happened would be a lie about
  // the very thing this demo is meant to prove.
  const read = async () => {
    if (!readKey.trim()) return
    setErr(null)
    try {
      setResult(await getKey(readKey.trim()))
    } catch (e) {
      setResult(null)
      setErr(errMsg(e))
    }
  }

  const seed = async () => {
    setErr(null)
    try {
      await seedKeys(8)
    } catch (e) {
      setErr(errMsg(e))
    }
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
      {err && <div className="api-err">⚠ {err}</div>}
    </div>
  )
}
