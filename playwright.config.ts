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
 * user actually hits, and it's what originally crashed (the user's own report was the minified production
 * error text, not the verbose dev-mode one).
 *
 * `webServer.command` only builds if `dist/` doesn't exist yet, then always runs `preview` against whatever
 * is there - so `npm run test:e2e` stays self-contained standalone (a missing `dist/` gets built first), but
 * in CI, which already built `dist/` from this exact checkout one step earlier ("Type-check + build"), it
 * skips straight to `preview` instead of paying for a second full `tsc -b && vite build`. That redundant
 * second build - landing right after the heavy `playwright install --with-deps chromium` step, on a shared,
 * variable-speed CI runner - is what blew past this file's `webServer.timeout` and failed the job with
 * "Timed out waiting ...ms from config.webServer" even though nothing was actually broken. There's no
 * staleness risk from skipping it in CI: nothing changes `dist/` between that build step and this one within
 * the same job run. A genuinely stale local `dist/` (edited source, forgot to rebuild) is still on the
 * person to rebuild themselves - same as running the built app any other way - `npm run build` before
 * `npm run test:e2e`, or just delete `dist/` and let this command rebuild it.
 *
 * No backend is needed: every test here uses the "Load sample" flow (Settings page), which seeds the built-in
 * demo topology entirely into browser state - see the README's "frontend only... no server needed" dev note.
 *
 * `vite preview` is started with `--host 127.0.0.1` explicitly, matching `baseURL`/`webServer.url` above
 * exactly, instead of relying on its default `localhost` host. On some CI runners Node resolves the bare
 * hostname `localhost` to the IPv6 loopback (`::1`) rather than `127.0.0.1`, so the preview server ends up
 * listening on an address Playwright's own health check never connects to - the server is actually up the
 * whole time, but every check against `127.0.0.1` gets refused, and this file's `webServer.timeout` is what
 * eventually reports it as "Timed out waiting ...ms from config.webServer." Binding both sides to the same
 * literal IP removes the ambiguity outright rather than hoping the runner's resolver order cooperates.
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
    command: '(test -d dist && test -n "$(ls -A dist 2>/dev/null)") || npm run build && npm run preview -- --host 127.0.0.1 --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    // Generous on purpose: the common CI path above only needs to start `vite preview`, which is fast, but
    // the fallback (no `dist/` yet - a fresh local checkout, or CI's own build step is ever skipped/reordered)
    // still has to run the full `tsc -b && vite build` first, and a shared CI runner's speed can vary. Costs
    // nothing on the fast path; only matters, in either path, when something's actually slow enough to be
    // worth knowing about on its own terms rather than via an arbitrary timeout.
    timeout: 180_000,
  },
})
