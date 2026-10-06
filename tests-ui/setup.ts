import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// jsdom doesn't implement scrollIntoView at all (it's a real browser layout API, out of scope for a DOM
// simulator) - Select's keyboard-nav effect calls it on the active option, so without a stub every test
// that opens a Select-based dropdown (ComboField included) throws. Not a product bug, a test-env gap.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}

// Unmounts whatever the previous test rendered - without this, DOM from one test leaks into the next
// and queries like getByRole start matching the wrong test's elements.
afterEach(() => {
  cleanup()
  // An unfinished telemetry draft is kept for the browser session (lib/draftStore.ts): one test's must not come back in the next.
  try {
    sessionStorage.clear()
  } catch {
    // no storage in this environment
  }
})
