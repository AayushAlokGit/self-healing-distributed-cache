import { type State } from '../api'
import { useApi, useApiError } from '../hooks'
import { ErrorLine } from './ErrorLine'

// DialPanel drives the consistency dial: W (write quorum) and R_read (read quorum), cluster-wide.
// The current setting is server truth (state.w / state.rRead), not local — the same reason R is.
// The readout is the teaching: a stranger finds the threshold by clicking, not by being shown a
// formula. W+R_read>R holds the ring and forbids stale reads; at or below R it is eventual.
export function DialPanel({ state, onAction }: { state: State; onAction: () => void }) {
  const { setQuorum } = useApi()
  const { err, run } = useApiError()
  const { w, rRead, rf } = state
  const sum = w + rRead
  const held = sum > rf
  const majority = Math.floor(rf / 2) + 1

  const apply = async (nw: number, nr: number) => {
    if (nw === w && nr === rRead) return
    if (await run(() => setQuorum(nw, nr))) onAction()
  }

  const levels = Array.from({ length: rf }, (_, i) => i + 1)

  return (
    <div className="card">
      <h2>Consistency dial · W / R_read</h2>
      <p className="panel-hint">
        How many owners must ack a write (W) and answer a read (R_read). The <em>pair</em> decides
        consistency — neither number alone.
      </p>

      <div className="dial-presets">
        <button className={'preset' + (w === 1 && rRead === 1 ? ' on' : '')} onClick={() => apply(1, 1)}>
          ONE · W1 R_Read1
        </button>
        <button
          className={'preset' + (w === majority && rRead === majority ? ' on' : '')}
          onClick={() => apply(majority, majority)}
        >
          QUORUM · W{majority} R_Read{majority}
        </button>
      </div>

      {(['W', 'R_read'] as const).map((which) => {
        const cur = which === 'W' ? w : rRead
        return (
          <div className="dial-row" key={which}>
            <span className="dial-label">{which}</span>
            <div className="dial-seg">
              {levels.map((l) => (
                <button
                  key={l}
                  className={'seg' + (cur === l ? ' on' : '')}
                  aria-pressed={cur === l}
                  onClick={() => (which === 'W' ? apply(l, rRead) : apply(w, l))}
                >
                  {l}
                </button>
              ))}
            </div>
          </div>
        )
      })}

      <div className={'dial-readout' + (held ? ' held' : '')}>
        <b>
          W + R_read = {sum} {held ? '>' : '≤'} R = {rf}
        </b>
        <span>
          {held
            ? 'no stale reads — the ring is held, so a partitioned side without a quorum refuses.'
            : 'eventual — stale reads possible, and both sides of a cut serve on.'}
        </span>
      </div>

      <ErrorLine err={err} />
    </div>
  )
}
