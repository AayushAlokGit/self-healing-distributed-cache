import { useCallback, useEffect, useRef, useState } from 'react'
import { getState, type State } from './api'

// useClusterState polls /api/state on an interval and returns the latest snapshot
// plus the previous one, so components can diff them to drive animations (a key
// gaining a holder, a node dying). refresh() forces an immediate poll after an
// action so the UI reacts without waiting for the next tick.
export function useClusterState(intervalMs = 600) {
  const [state, setState] = useState<State | null>(null)
  const [connected, setConnected] = useState(true)
  const prevRef = useRef<State | null>(null)
  const curRef = useRef<State | null>(null)

  const refresh = useCallback(async () => {
    try {
      const s = await getState()
      prevRef.current = curRef.current
      curRef.current = s
      setState(s)
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

  return { state, prev: prevRef.current, connected, refresh }
}
