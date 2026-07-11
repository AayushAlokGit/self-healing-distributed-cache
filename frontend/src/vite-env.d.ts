/// <reference types="vite/client" />

// Vite exposes only vars prefixed VITE_ to the client, and inlines them at build time.
// Declaring it optional is honest: in dev it is unset and the API is same-origin via the
// proxy (see API_BASE in api.ts).
interface ImportMetaEnv {
  readonly VITE_API_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
