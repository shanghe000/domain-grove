import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Keep the browser-visible Host so Go can verify same-origin mutations through Vite.
const backend = { target: 'http://127.0.0.1:8080', changeOrigin: false }

export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': backend, '/auth': backend, '/health': backend } },
  build: { target: 'es2022' },
})
