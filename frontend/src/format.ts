// ttlText renders a key's remaining life. The number it is given is the server's own
// remainder (see cluster/state.go), so no clock comparison happens here — the
// dashboard re-polls several times a second and simply re-reads it, and that is what
// makes the countdown tick.
//
// Three bands, because one rounding rule cannot serve the whole range honestly. Now
// that a TTL can be given in milliseconds, the display has to be truthful AT the
// precision the user chose: ceiling everything to whole seconds would round 1500ms up
// to "2s" and 400ms up to "1s" — the countdown lying at exactly the scale someone
// picked milliseconds in order to see.
//
//	  <1s    620ms     exact
//	 <10s    1.5s      one decimal, so a sub-second choice survives
//	  <1m    45s       whole seconds; a decimal here is just noise
//	 >=1m    2m05s
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
