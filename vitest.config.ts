import react from '@vitejs/plugin-react'
import path from 'node:path'
import { defineConfig } from 'vitest/config'

// Separate from vite.config.ts on purpose: this only ever runs under `vitest`, never dev/build, so it stays
// out of the app's own bundling path. Pairs with tests/ (pure-logic, node:test, no DOM) rather than replacing
// it - tests-ui/ is for anything that needs to actually render a component and interact with the result.
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./tests-ui/setup.ts'],
    include: ['tests-ui/**/*.test.tsx'],
    css: false,
  },
})
