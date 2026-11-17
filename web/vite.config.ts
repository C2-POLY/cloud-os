/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// In development, Vite serves the front and forwards /api to the core.
const apiTarget = process.env.VITE_API_TARGET ?? 'http://localhost:8480'

export default defineConfig({
  plugins: [react()],
  server: {
    host: true,
    proxy: { '/api': apiTarget },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
  },
})
