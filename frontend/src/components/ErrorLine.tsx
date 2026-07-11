// The one place a failed request is shown to the user. Rendering null for "no error"
// keeps every caller down to a single line: <ErrorLine err={err} />
export function ErrorLine({ err }: { err: string | null }) {
  if (!err) return null
  return <div className="api-err">⚠ {err}</div>
}
