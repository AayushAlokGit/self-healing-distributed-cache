import type { ClusterEvent } from '../api'
import { colorFor } from '../geometry'

// Faults are the events that move ownership, so they are the ones a heal can be a
// consequence of. Everything else (a write, a seed) just sits in the timeline.
const FAULTS = new Set(['kill', 'revive', 'pause', 'resume'])

// A block is one fault and the heal copies that followed it. The backend appends
// heals to the same list as the kills at the moment they happen, so the order is
// already causal — grouping here is just reading that order, not reconstructing it.
interface Block {
  header: ClusterEvent
  heals: ClusterEvent[]
}

function group(events: ClusterEvent[]): Block[] {
  const blocks: Block[] = []
  for (const e of events) {
    if (e.kind === 'heal' && blocks.length > 0 && FAULTS.has(blocks[blocks.length - 1].header.kind)) {
      // Attach to the fault above it: that is the one it followed.
      blocks[blocks.length - 1].heals.push(e)
    } else {
      blocks.push({ header: e, heals: [] })
    }
  }
  return blocks
}

function NodeChip({ id }: { id: string }) {
  return (
    <span className="node-chip" style={{ color: colorFor(id), borderColor: colorFor(id) }}>
      {id}
    </span>
  )
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
      {/* The sender's OWN reason for healing — not the manager's. Two nodes can
          disagree about who died, and this is where that would show. */}
      {e.cause && <div className="cause">because {e.from} saw {e.cause}</div>}
    </div>
  )
}

export function ActivityLog({ events }: { events: ClusterEvent[] }) {
  const blocks = group(events).reverse() // newest block first

  return (
    <div className="card">
      <h2>Activity &amp; re-replication</h2>
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
