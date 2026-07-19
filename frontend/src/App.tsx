import { useMemo, useState } from 'react'
import { API_BASE, createApi, type Partition, type State } from './api'
import { ActivityLog } from './components/ActivityLog'
import { DialPanel } from './components/DialPanel'
import { KeyTable } from './components/KeyTable'
import { NodePanel } from './components/NodePanel'
import { PartitionPanel } from './components/PartitionPanel'
import { ReadPanel } from './components/ReadPanel'
import { Scorecard } from './components/Scorecard'
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
      `Failure mode: nodes die. Heartbeats give each node its own view of who is alive; a heal loop restores R=${s.rf} copies. A death costs only staleness — there is one truth to copy.`,
  },
  {
    id: 'cap',
    label: 'CAP & Partitions',
    blurb: () =>
      'Failure mode: the network splits — both sides keep serving, so one key can take two concurrent writes (divergence, not staleness). Vector clocks detect the clash and keep both as siblings for the client to resolve.',
  },
]

// sideState carves one side of a cut out of the god's-eye snapshot into a self-contained State,
// so a plain RingViz can draw that side as the independent cluster it actually is. A side sees
// every alive node EXCEPT the far side (an unassigned bridge node is reachable by both, so it
// shows on both) — matching the backend's per-side ring. Owners come from that side's ring
// (ownersA/ownersB), holders are filtered to nodes this side can reach, and only keys this side
// actually holds are shown. aliveCount is the side's own count: from here, the far side is dead.
function sideState(state: State, partition: Partition, side: 'a' | 'b'): State {
  const far = new Set(side === 'a' ? partition.sideB : partition.sideA)
  const nodes = state.nodes.filter((n) => n.alive && !far.has(n.id))
  const near = new Set(nodes.map((n) => n.id))
  const vnodes = side === 'a' ? partition.vnodesA : partition.vnodesB
  const want = Math.min(state.rf, nodes.length)
  const keys = state.keys
    .map((k) => {
      const owners = (side === 'a' ? k.ownersA : k.ownersB) ?? k.owners
      const holders = k.holders.filter((h) => near.has(h))
      return { ...k, owners, holders, ownersA: undefined, ownersB: undefined, underReplicated: holders.length > 0 && holders.length < want }
    })
    .filter((k) => k.holders.length > 0)
  return { ...state, nodes, keys, vnodes, aliveCount: nodes.length }
}

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

  // The active cut now comes from /state: the server is the source of truth, so a reload
  // keeps the banner and the split ring. (It used to be client-only React state, which a
  // refresh forgot while the backend cut stayed live.) Null on every non-CAP tab, since only
  // the CAP demo issues cuts.
  const partition = state?.partition ?? null
  const isCap = tab.id === 'cap'

  return (
    <>
      <header>
        <div className="title">
          <h1>
            Self-Healing <span className="accent">Distributed Cache</span>
          </h1>
          <p className="identity">
            A leaderless, <strong>Dynamo-style AP (prioritizes availability) key value store</strong>. Each tab demos a distributed-systems failure
            mode — and how the cluster survives it.
          </p>
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

      {partition && (
        <div className="partition-banner">
          <span className="bolt">✂</span>
          <b>NETWORK PARTITIONED</b>
          <span className="detail">
            Side A ({partition.sideA.join(', ')}) and Side B ({partition.sideB.join(', ')}) cannot
            hear each other — both keep serving.
          </span>
        </div>
      )}

      {state ? (
        <div className="grid">
          <div className="left">
            {partition ? (
              <div className="split-stage">
                <RingViz
                  state={sideState(state, partition, 'a')}
                  prev={prev ? sideState(prev, partition, 'a') : null}
                  sideLabel="Side A"
                />
                <RingViz
                  state={sideState(state, partition, 'b')}
                  prev={prev ? sideState(prev, partition, 'b') : null}
                  sideLabel="Side B"
                />
              </div>
            ) : (
              <RingViz state={state} prev={prev} />
            )}
            <div className="io-row">
              <WritePanel nodes={state.nodes} onAction={refresh} />
              <ReadPanel nodes={state.nodes} />
            </div>
            <KeyTable keys={state.keys} partition={partition} onAction={refresh} />
          </div>
          <div className="side">
            <NodePanel nodes={state.nodes} onAction={refresh} />
            {isCap && (
              <>
                <PartitionPanel nodes={state.nodes} partition={partition} onAction={refresh} />
                <DialPanel state={state} onAction={refresh} />
                <Scorecard state={state} onAction={refresh} />
              </>
            )}
            <ActivityLog events={state.events} />
          </div>
        </div>
      ) : (
        <p style={{ color: 'var(--muted)' }}>connecting to the cluster…</p>
      )}
    </>
  )
}
