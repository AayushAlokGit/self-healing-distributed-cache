import type { ClusterEvent } from '../api'

export function ActivityLog({ events }: { events: ClusterEvent[] }) {
  // Newest first.
  const ordered = [...events].reverse()
  return (
    <div className="card">
      <h2>Activity</h2>
      <div className="log">
        {ordered.map((e) => (
          <div className="ev" key={e.id}>
            <span className={'tag ' + e.kind}>{e.kind}</span>
            <span className="msg">{e.msg}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
