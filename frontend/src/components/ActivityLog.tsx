import type { ClusterEvent } from '../api'
import { NodeChip } from './NodeChip'

// Only a fault moves ownership, so only a fault can be the cause of a heal.
const FAULTS = new Set(['kill', 'revive', 'pause', 'resume'])

// A block is one fault plus the heal copies that followed it. Events arrive in causal
// order from the backend, so grouping only reads that order rather than reconstructing it.
interface Block {
  header: ClusterEvent
  heals: ClusterEvent[]
}

// Heals attach to the open fault — the last fault seen — and NOT merely to the block
// immediately behind them. Those are the same thing only while nothing else can land in
// between, which stopped being true the moment expiries and reclamations existed: an
// expire event between a kill and its heals would take the last-block slot, and every
// heal behind it would silently detach from the kill that caused it. Nothing would throw;
// the log would just quietly stop showing causation, which is the one thing it is for.
//
// A heal with no fault ahead of it stays its own block. That is deliberate: a UI that
// glues every heal to the nearest kill is structurally incapable of showing a heal that
// happened without one — which is exactly what a false-positive failure detection is.
function group(events: ClusterEvent[]): Block[] {
  const blocks: Block[] = []
  let openFault: Block | null = null
  for (const e of events) {
    if (FAULTS.has(e.kind)) {
      openFault = { header: e, heals: [] }
      blocks.push(openFault)
    } else if (e.kind === 'heal' && openFault) {
      openFault.heals.push(e)
    } else {
      blocks.push({ header: e, heals: [] })
    }
  }
  return blocks
}

function HealRow({ e }: { e: ClusterEvent }) {
  const keys = e.keys ?? []
  return (
    <div className="heal-row">
      <div className="hop">
        <NodeChip id={e.from ?? '?'} />
        <span className="arrow">→</span>
        <NodeChip id={e.to ?? '?'} />
        <span className="count">
          {keys.length} key{keys.length === 1 ? '' : 's'}
        </span>
      </div>
      <div className="keys">
        {keys.map((k) => (
          <span className="key-chip" key={k}>
            {k}
          </span>
        ))}
      </div>
      {/* The sender's own reason for healing, not the manager's: two nodes can disagree
          about who died, and this is where that shows. */}
      {e.cause && <div className="cause">because {e.from} saw {e.cause}</div>}
    </div>
  )
}

// Kinds whose keys are worth naming on the header line itself. A heal's keys live in
// its own rows; these have no rows of their own.
const NAMES_KEYS = new Set(['expire', 'reclaim', 'cleanup'])

export function ActivityLog({ events }: { events: ClusterEvent[] }) {
  const blocks = group(events).reverse() // newest block first

  return (
    <div className="card">
      <h2>Activity log</h2>
      <div className="log">
        {blocks.map((b) => {
          const copies = b.heals.reduce((n, h) => n + (h.keys?.length ?? 0), 0)
          const named = NAMES_KEYS.has(b.header.kind) ? (b.header.keys ?? []) : []
          return (
            <div className={'block' + (b.heals.length ? ' has-heals' : '')} key={b.header.id}>
              <div className="ev">
                <span className={'tag ' + b.header.kind}>{b.header.kind}</span>
                <span className="msg">{b.header.msg}</span>
                {copies > 0 && (
                  <span className="rollup">
                    ↳ {copies} cop{copies === 1 ? 'y' : 'ies'}
                  </span>
                )}
              </div>
              {named.length > 0 && (
                <div className="keys">
                  {named.map((k) => (
                    <span className="key-chip dead" key={k}>
                      {k}
                    </span>
                  ))}
                </div>
              )}
              {b.heals.length > 0 && (
                <div className="heals">
                  {b.heals.map((h) => (
                    <HealRow e={h} key={h.id} />
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
