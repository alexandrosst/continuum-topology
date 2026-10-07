/**
 * After a new version of the server is deployed, a tab that was opened before still runs the old page, which asks for files
 * (named by a hash of their content) that the new server no longer has: loading a page's code then fails with "Failed to fetch
 * dynamically imported module". The cure is to load the new page, so the app does that once by itself instead of asking the person to.
 */

const KEY = 'ikhnos-reloaded-for-update'
/** A second failure inside this window is not a stale tab (a reload would not help, and would loop). */
const WINDOW_MS = 60_000

/** The messages browsers give when a lazily loaded file cannot be fetched (Chrome, Firefox, Safari). */
const MESSAGES = /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|Unable to preload CSS/i

export function isStaleChunkError(e: unknown): boolean {
  return MESSAGES.test(e instanceof Error ? e.message : typeof e === 'string' ? e : '')
}

/**
 * Reloads the page once for a new version. Says whether it did: false when it already did a moment ago (the files are really
 * missing, so the person is told instead of being looped), or when the browser would not let it remember that.
 */
export function reloadForNewVersion(opts: { now?: () => number; storage?: Pick<Storage, 'getItem' | 'setItem'>; reload?: () => void } = {}): boolean {
  const now = (opts.now ?? Date.now)()
  try {
    const store = opts.storage ?? window.sessionStorage
    const last = Number(store.getItem(KEY) ?? 0)
    if (last && now - last < WINDOW_MS) return false
    store.setItem(KEY, String(now))
  } catch {
    return false // cannot tell whether it already tried: do not risk a loop
  }
  ;(opts.reload ?? (() => window.location.reload()))()
  return true
}
