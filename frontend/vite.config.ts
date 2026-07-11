import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go backend serves the control API on :8080. In dev we proxy /api to it so
// the browser sees one origin (no CORS needed here) and HMR still works.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
