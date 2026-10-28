import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// Where the dev server forwards /api. The default suits `npm run dev` on the
// host, where the local stack publishes the management API on 127.0.0.1:3000.
// The dev container in docker-compose.ui.yml sets it to
// http://management-api:3000, because inside a container localhost is the
// container itself (#399). Not VITE_-prefixed: only this Node process reads
// it, and Vite hands every VITE_* variable to the browser bundle.
const apiProxyTarget = process.env.API_PROXY_TARGET || 'http://localhost:3000'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '@components': path.resolve(__dirname, './src/components'),
      '@pages': path.resolve(__dirname, './src/pages'),
      '@services': path.resolve(__dirname, './src/services'),
      '@store': path.resolve(__dirname, './src/store'),
      '@hooks': path.resolve(__dirname, './src/hooks'),
      '@types': path.resolve(__dirname, './src/types'),
      '@utils': path.resolve(__dirname, './src/utils'),
      '@config': path.resolve(__dirname, './src/config'),
    },
  },
  server: {
    proxy: {
      '/api': {
        target: apiProxyTarget,
        changeOrigin: true,
        rewrite: (path) => path,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    target: 'esnext',
  },
})
