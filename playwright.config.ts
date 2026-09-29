import { defineConfig, devices } from '@playwright/test'

/**
 * End-to-end smoke/regression tests. Deliberately separate from tests/ (pure logic, node:test) and tests-ui/
 * (component-level, jsdom + vitest): this suite drives a *real* browser against the *real*, built app, which
 * is the only way to catch a bug that only exists in the interaction between our code and a UI library's own
 * internals (React Flow's SelectionListener/StoreUpdater effects, React's own render-loop guard) - exactly
 * the class of bug that produced two real "Maximum update depth exceeded" (React error #185) production
 * crashes that neither the unit tests nor the component tests had any way to catch.
 *
 * Runs against the PRODUCTION build (`vite build` + `vite preview`), not the dev server: that's what a real
 * user actually hits, it's what originally crashed (the user's own report was the minified production error
 * text, not the verbose dev-mode one), and it's what CI already builds in the "Type-check + build" step.
 * `webServer.command` builds first on its own so `npm run test:e2e` is self-contained locally too, even if
 * `dist/` is stale or missing.
 *
 * No backend is needed: every test here uses the "Load sample" flow (Settings page), which seeds the built-in
 * demo topology entirely into browser state - see the README's "frontend only... no server needed" dev note.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // Deliberately 0, not CI's usual retry cushion: the bugs this suite exists to catch are timing-sensitive
  // render loops - a retry that happens to land differently could pass by luck and quietly mask a real
  // regression. Each test instead repeats its own risky interaction sequence a few times internally (see
  // topology-crash.spec.ts) to raise the odds of tripping a timing-dependent loop within a single run,
  // rather than leaning on the runner to paper over a flaky failure.
  retries: 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    command: 'npm run build && npm run preview -- --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
