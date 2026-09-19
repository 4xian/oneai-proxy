import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  root: fileURLToPath(new URL('./web', import.meta.url)),
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5188,
    strictPort: true,
    proxy: {
      '/api': { target: 'http://127.0.0.1:9989', changeOrigin: true },
      '/healthz': { target: 'http://127.0.0.1:9989', changeOrigin: true },
    },
  },
  build: {
    outDir: fileURLToPath(new URL('./internal/server/web', import.meta.url)),
    emptyOutDir: true,
  },
})
