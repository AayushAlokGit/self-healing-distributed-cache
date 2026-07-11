import { ActivityLog } from './components/ActivityLog'
import { KeysPanel } from './components/KeysPanel'
import { NodePanel } from './components/NodePanel'
import { RingViz } from './components/RingViz'
import { Stats } from './components/Stats'
import { useClusterState } from './useClusterState'

export default function App() {
  const { state, prev, connected, refresh } = useClusterState()

  return (
    <div className="wrap">
      <header>
        <div className="title">
          <h1>
            Self-Healing <span className="accent">Distributed Cache</span>
          </h1>
          <p>Consistent-hash ring · R=3 replication · heartbeat failure detection · automatic re-replication</p>
        </div>
        {state && <Stats state={state} prev={prev} />}
      </header>

      {!connected && <div className="offline-badge">⚠ backend unreachable — is `go run ./cmd/server` running?</div>}

      {state ? (
        <div className="grid">
          <RingViz state={state} prev={prev} />
          <div className="side">
            <NodePanel nodes={state.nodes} onAction={refresh} />
            <KeysPanel onAction={refresh} />
            <ActivityLog events={state.events} />
          </div>
        </div>
      ) : (
        <p style={{ color: 'var(--muted)' }}>connecting to the cluster…</p>
      )}
    </div>
  )
}
