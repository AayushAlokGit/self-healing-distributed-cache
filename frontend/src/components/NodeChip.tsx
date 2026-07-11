import { colorFor } from '../geometry'

// A node id in that node's ring colour, so a name in a log or trace is findable on the ring.
export function NodeChip({ id, title }: { id: string; title?: string }) {
  const c = colorFor(id)
  return (
    <span className="node-chip" style={{ color: c, borderColor: c }} title={title}>
      {id}
    </span>
  )
}
