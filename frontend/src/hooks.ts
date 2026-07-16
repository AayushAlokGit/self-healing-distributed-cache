import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { errMsg, type Api, type State } from './api'

// ClusterContext carries the api for the tab you are looking at. It is deliberately the
// ONLY way a component gets one: nothing below takes a clusterId, so no component can name
// a cluster, let alone the wrong one. The provider sits at the tab boundary (App.tsx), so
// cross-talk is impossible by construction rather than by everyone remembering to thread
// the right id through — which is the same guarantee the server has, where two Cluster
// values share no state at all (docs/HLD.md §4).
const ClusterContext = createContext<Api | null>(null)

export const ClusterProvider = ClusterContext.Provider

// useApi is the api for the current tab. Throwing beats a default: a silent fallback to
// "some cluster" is how you kill a node on the wrong demo and spend an hour on it.
export function useApi(): Api {
  const api = useContext(ClusterContext)
  if (!api) throw new Error('useApi() used outside a ClusterProvider')
  return api
}

// useClusterState polls this tab's cluster and hands back the latest snapshot and the one
// before it, so components can diff the two to drive animations. Both live in one piece of
// state because they must always describe the same instant. refresh() forces an immediate poll.
export function useClusterState(intervalMs = 600) {
  const api = useApi()
  const [snap, setSnap] = useState<{ cur: State | null; prev: State | null }>({ cur: null, prev: null })
  const [connected, setConnected] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const s = await api.getState()
      setSnap((p) => ({ cur: s, prev: p.cur }))
      setConnected(true)
    } catch {
      setConnected(false)
    }
  }, [api])

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, intervalMs)
    return () => clearInterval(id)
  }, [refresh, intervalMs])

  return { state: snap.cur, prev: snap.prev, connected, refresh }
}

// useApiError runs an API call and remembers why it failed. run() returns whether the call
// succeeded, so a caller can decide whether to clear its inputs; fail() reports a failure
// found locally (bad input) on the same error line.
export function useApiError() {
  const [err, setErr] = useState<string | null>(null)

  const run = useCallback(async (fn: () => Promise<unknown>): Promise<boolean> => {
    setErr(null)
    try {
      await fn()
      return true
    } catch (e) {
      setErr(errMsg(e))
      return false
    }
  }, [])

  const fail = useCallback((msg: string) => setErr(msg), [])

  return { err, run, fail }
}
