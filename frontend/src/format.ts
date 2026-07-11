// ttlText renders a key's remaining life. Banded so the display stays truthful at the
// precision chosen: <1s exact ms, <10s one decimal, <1m whole seconds, then 2m05s.
export function ttlText(ms: number): string {
  const clamped = Math.max(ms, 0)
  if (clamped < 1000) return `${Math.round(clamped)}ms`
  if (clamped < 10_000) {
    const s = clamped / 1000
    // 2.0 -> "2s", 1.5 -> "1.5s": no trailing ".0" on a value that is a whole second.
    return `${Number(s.toFixed(1))}s`
  }
  const s = Math.ceil(clamped / 1000)
  if (s >= 60) return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`
  return `${s}s`
}
