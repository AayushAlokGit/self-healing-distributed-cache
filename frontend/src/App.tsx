import { useMemo, useState } from 'react'
import { API_BASE, createApi, type Cut, type State } from './api'
import { ActivityLog } from './components/ActivityLog'
import { KeyTable } from './components/KeyTable'
import { NodePanel } from './components/NodePanel'
import { PartitionPanel } from './components/PartitionPanel'
import { ReadPanel } from './components/ReadPanel'
import { RingViz } from './components/RingViz'
import { Stats } from './components/Stats'
import { WritePanel } from './components/WritePanel'
import { ClusterProvider, useClusterState } from './hooks'

// One tab per demo, each on its own cluster inside the same container (demoClusters in
// cmd/server/main.go). They are separate clusters because they want the ring in states that
// cannot both be true: the CAP demo leaves the network cut for minutes at a time while
// writing to both sides, which reads as a broken cluster to anyone on the replication tab,
// and a node killed there would quietly corrupt a run here.
//
// ⚠️ `id` must name a cluster the server knows, or every call under that tab 404s.
//
// blurb takes the state rather than being a string, because it describes the cluster you are
// looking at — so it cannot be written until there is one to describe. R comes from the
// server for the same reason it does in Stats: the number is the cluster's to report, and a
// second hardcoded copy here is just a copy that can be wrong.
type Tab = {
  readonly id: string
  readonly label: string
  readonly blurb: (s: State) => string
}

const TABS: readonly Tab[] = [
  {
    id: 'replication',
    label: 'Replication & Self-Heal',
    blurb: (s) =>
      `Consistent-hash ring · R=${s.rf} replication · heartbeat failure detection · automatic re-replication`,
  },
  {
    id: 'cap',
    label: 'CAP & Partitions',
    blurb: () =>
      'The same ring, on its own cluster. Cut the network in two and each side keeps serving — a write to the same key on both sides becomes a conflict the cache keeps both of.',
  },
]

export default function App() {
  const [activeId, setActiveId] = useState<string>(TABS[0].id)
  const tab = TABS.find((t) => t.id === activeId) ?? TABS[0]

  // ⚠️ useMemo is load-bearing here, not an optimisation. createApi returns a NEW object
  // every call, and useClusterState depends on it — unmemoised, every render would be a new
  // api value and would restart the polling effect.
  const api = useMemo(() => createApi(tab.id), [tab.id])

  // The provider sits at the tab boundary, so nothing below can reach another cluster: no
  // component takes a cluster id, it only asks useApi() for the one it was handed.
  // Cross-talk is impossible by construction, the same way it is on the server, where two
  // Cluster values share no state at all (docs/HLD.md §4).
  //
  // key={tab.id} is also load-bearing. It throws away every piece of per-tab state on a
  // switch, and three of them are wrong to keep:
  //   · the cluster snapshot — otherwise the previous cluster's ring stays on screen until
  //     the new one's first poll lands ~600ms later, under the tab you just clicked;
  //   · ReadPanel's result — a read of the other cluster, captioned as this one;
  //   · KeyTable's armed "delete all" — arm it here, switch, click, and you wipe the OTHER
  //     demo's keys.
  // Resetting each by hand would be whack-a-mole; a key says the honest thing, which is that
  // this is a different cluster and nothing about the old one still applies.
  return (
    <div className="wrap">
      <ClusterProvider value={api}>
        <Dashboard key={tab.id} tab={tab} onSelect={setActiveId} />
      </ClusterProvider>
    </div>
  )
}

function Dashboard({ tab, onSelect }: { tab: Tab; onSelect: (id: string) => void }) {
  const { state, prev, connected, refresh } = useClusterState()

  // The active cut is client-only: /state does not report it (state.go was untouched), so
  // the dashboard — the only issuer of cuts — is the sole source of truth. A reload forgets
  // it (a known limitation until /state carries the partition). Only the CAP tab issues cuts;
  // Dashboard remounts per tab (key={tab.id} in App), so this is null everywhere else.
  const [cut, setCut] = useState<Cut | null>(null)
  const isCap = tab.id === 'cap'

  return (
    <>
      <header>
        <div className="title">
          <h1>
            Self-Healing <span className="accent">Distributed Cache</span>
          </h1>
          <nav className="tabs" role="tablist">
            {TABS.map((t) => (
              <button
                key={t.id}
                role="tab"
                aria-selected={t.id === tab.id}
                className={'tab' + (t.id === tab.id ? ' active' : '')}
                onClick={() => onSelect(t.id)}
              >
                {t.label}
              </button>
            ))}
          </nav>
          {state && <p>{tab.blurb(state)}</p>}
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

      {cut && (
        <div className="partition-banner">
          <span className="bolt">✂</span>
          <b>NETWORK PARTITIONED</b>
          <span className="detail">
            Side A ({cut.sideA.join(', ')}) and Side B ({cut.sideB.join(', ')}) cannot hear each
            other — both keep serving.
          </span>
        </div>
      )}

      {state ? (
        <div className="grid">
          <div className="left">
            <RingViz state={state} prev={prev} cut={cut} />
            <KeyTable keys={state.keys} onAction={refresh} />
          </div>
          <div className="side">
            <NodePanel nodes={state.nodes} onAction={refresh} />
            {isCap && (
              <PartitionPanel
                nodes={state.nodes}
                cut={cut}
                onCut={setCut}
                onMend={() => setCut(null)}
                onAction={refresh}
              />
            )}
            <WritePanel nodes={state.nodes} onAction={refresh} />
            <ReadPanel nodes={state.nodes} />
            <ActivityLog events={state.events} />
          </div>
        </div>
      ) : (
        <p style={{ color: 'var(--muted)' }}>connecting to the cluster…</p>
      )}
    </>
  )
}
