import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev only: proxy /api to the Go backend on :8080 so the browser sees one origin.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
