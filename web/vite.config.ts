import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    // The Go binary embeds internal/webui/dist. Keep the folder so the
    // placeholder file stays put between builds.
    outDir: '../internal/webui/dist',
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: {
      '^/(skus|inventory|orders|reconciliation|auth|healthz|readyz)': 'http://localhost:8080',
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/setupTests.ts',
    css: false,
    // Playwright specs live in e2e/ and are run by Playwright, not Vitest.
    exclude: ['e2e/**', 'node_modules/**'],
  },
})
