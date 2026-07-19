import { useState } from 'react'
import { type State } from '../api'
import { useApi } from '../hooks'

type Row = { w: number; rRead: number; accepted: number; refused: number; conflicts: number }

const N = 5 // keys; each is written via BOTH sides, so 2N write attempts per probe

// Scorecard runs a controlled experiment under the current cut: the same 2N writes (N keys ×
// both sides) at the CURRENT dial, tallying accepted (204) vs refused (503), plus conflicts —
// a key BOTH sides accepted, i.e. divergence in waiting. Flip the dial above and probe again to
// stack a second row: at ONE both sides accept (conflicts), at QUORUM one side refuses (no
// divergence). The refusals and the conflicts are the same writes — QUORUM trades one for the
// other. Honest by construction: every number is a real response to a real write.
export function Scorecard({ state, onAction }: { state: State; onAction: () => void }) {
  const { setKey } = useApi()
  const [rows, setRows] = useState<Row[]>([])
  const [running, setRunning] = useState(false)
  const partition = state.partition

  const tryWrite = async (key: string, val: string, via: string): Promise<boolean> => {
    try {
      await setKey(key, val, 0, via) // 0 = never expire: a demo key must not die mid-run
      return true
    } catch {
      return false // a 503 (quorum refused) or any non-2xx throws — count it as refused
    }
  }

  const probe = async () => {
    if (!partition) return
    setRunning(true)
    const a = partition.sideA[0]
    const b = partition.sideB[0]
    let accepted = 0
    let refused = 0
    let conflicts = 0
    for (let i = 0; i < N; i++) {
      const key = `sc:${i}`
      const okA = await tryWrite(key, `A-${i}`, a)
      const okB = await tryWrite(key, `B-${i}`, b)
      accepted += (okA ? 1 : 0) + (okB ? 1 : 0)
      refused += (okA ? 0 : 1) + (okB ? 0 : 1)
      if (okA && okB) conflicts++ // both sides took it → a conflict once the network mends
    }
    // Label the row with the dial as it was for this run (server truth this render).
    setRows((r) => [...r, { w: state.w, rRead: state.rRead, accepted, refused, conflicts }])
    setRunning(false)
    onAction()
  }

  return (
    <div className="card">
      <h2>Scorecard · same writes, each dial</h2>
      <p className="panel-hint">
        {partition
          ? `Under the current cut, write ${N} keys via BOTH sides (${2 * N} attempts) at the dial set above. Flip the dial and probe again to compare.`
          : 'Cut the network first, then set a dial and probe. The scorecard needs two sides to write to.'}
      </p>

      <div className="scorecard-actions">
        <button className="primary" onClick={probe} disabled={!partition || running}>
          {running ? 'probing…' : `Probe at W${state.w} R${state.rRead}`}
        </button>
        {rows.length > 0 && (
          <button onClick={() => setRows([])} disabled={running}>
            Clear
          </button>
        )}
      </div>

      {rows.length > 0 && (
        <>
          <table className="scorecard">
            <thead>
              <tr>
                <th>dial</th>
                <th>accepted</th>
                <th>refused</th>
                <th>conflicts</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i}>
                  <td>
                    W{r.w} R{r.rRead}
                    {r.w + r.rRead > state.rf ? ' · held' : ''}
                  </td>
                  <td className="ok">{r.accepted}</td>
                  <td className={r.refused > 0 ? 'no' : ''}>{r.refused}</td>
                  <td className={r.conflicts > 0 ? 'warn' : ''}>{r.conflicts}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="panel-hint scorecard-note">
            A conflict is a key both sides accepted — divergence the mend must reconcile. QUORUM
            turns each into a refusal instead: the refused writes and the conflicts are the same
            writes.
          </p>
        </>
      )}
    </div>
  )
}
