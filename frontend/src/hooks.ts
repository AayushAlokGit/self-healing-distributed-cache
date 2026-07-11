import { useCallback, useEffect, useState } from 'react'
import { errMsg, getState, type State } from './api'

// useClusterState polls /api/state and hands back the latest snapshot *and* the one
// before it, so components can diff the two to drive animations (a key gaining a
// holder, a node dying). Both live in one piece of state: they must always describe
// the same instant, and a single setState makes that impossible to get wrong.
// refresh() forces an immediate poll after an action, so the UI reacts at once.
export function useClusterState(intervalMs = 600) {
  const [snap, setSnap] = useState<{ cur: State | null; prev: State | null }>({ cur: null, prev: null })
  const [connected, setConnected] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const s = await getState()
      setSnap((p) => ({ cur: s, prev: p.cur }))
      setConnected(true)
    } catch {
      setConnected(false)
    }
  }, [])

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, intervalMs)
    return () => clearInterval(id)
  }, [refresh, intervalMs])

  return { state: snap.cur, prev: snap.prev, connected, refresh }
}

// useApiError runs an API call and remembers why it failed. Every control panel needs
// exactly this: a request that can fail, and a place to say so — because a kill that
// quietly did nothing is indistinguishable from a cluster that ignored the kill.
// run() reports whether the call succeeded, so a caller can decide what to do next
// (clear the inputs, or leave them alone so the user can retry).
//
// fail() reports a failure we found ourselves, before any request went out — bad
// input, say. It shares the error line rather than inventing a second one: to the
// user, "that TTL isn't a number" and "the server rejected the write" are the same
// event, namely "my write did not happen and here is why".
export function useApiError() {
  const [err, setErr] = useState<string | null>(null)

  const run = useCallback(async (fn: () => Promise<void>): Promise<boolean> => {
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
