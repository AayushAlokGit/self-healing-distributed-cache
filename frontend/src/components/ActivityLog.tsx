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

function group(events: ClusterEvent[]): Block[] {
  const blocks: Block[] = []
  for (const e of events) {
    if (e.kind === 'heal' && blocks.length > 0 && FAULTS.has(blocks[blocks.length - 1].header.kind)) {
      blocks[blocks.length - 1].heals.push(e)
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

export function ActivityLog({ events }: { events: ClusterEvent[] }) {
  const blocks = group(events).reverse() // newest block first

  return (
    <div className="card">
      <h2>Activity log</h2>
      <div className="log">
        {blocks.map((b) => {
          const copies = b.heals.reduce((n, h) => n + (h.keys?.length ?? 0), 0)
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
