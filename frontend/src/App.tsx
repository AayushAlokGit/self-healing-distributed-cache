import { ActivityLog } from './components/ActivityLog'
import { KeyTable } from './components/KeyTable'
import { NodePanel } from './components/NodePanel'
import { ReadPanel } from './components/ReadPanel'
import { RingViz } from './components/RingViz'
import { Stats } from './components/Stats'
import { WritePanel } from './components/WritePanel'
import { useClusterState } from './hooks'

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
          <div className="left">
            <RingViz state={state} prev={prev} />
            <KeyTable keys={state.keys} />
          </div>
          <div className="side">
            <NodePanel nodes={state.nodes} onAction={refresh} />
            <WritePanel onAction={refresh} />
            <ReadPanel />
            <ActivityLog events={state.events} />
          </div>
        </div>
      ) : (
        <p style={{ color: 'var(--muted)' }}>connecting to the cluster…</p>
      )}
    </div>
  )
}
