export function ErrorLine({ err }: { err: string | null }) {
  if (!err) return null
  return <div className="api-err">⚠ {err}</div>
}
