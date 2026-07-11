import { useCallback, useEffect, useState } from 'react'
import { errMsg, getState, type State } from './api'

// useClusterState polls /api/state and hands back the latest snapshot and the one before
// it, so components can diff the two to drive animations. Both live in one piece of state
// because they must always describe the same instant. refresh() forces an immediate poll.
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

// useApiError runs an API call and remembers why it failed. run() returns whether the call
// succeeded, so a caller can decide whether to clear its inputs; fail() reports a failure
// found locally (bad input) on the same error line.
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
