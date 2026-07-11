import { API_BASE } from './api'
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

      {/* Two different readers. Locally, "unreachable" means you forgot to start the backend.
          Deployed, it almost always means the free container is cold-starting — and telling a
          stranger to run `go run` would just look broken. */}
      {!connected &&
        (API_BASE ? (
          <div className="offline-badge waking">
            ⏳ waking the cluster… the backend sleeps when idle on its free host, so the first load can
            take up to a minute. This page will fill in on its own.
          </div>
        ) : (
          <div className="offline-badge">⚠ backend unreachable — is `go run ./cmd/server` running?</div>
        ))}

      {state ? (
        <div className="grid">
          <div className="left">
            <RingViz state={state} prev={prev} />
            <KeyTable keys={state.keys} onAction={refresh} />
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
