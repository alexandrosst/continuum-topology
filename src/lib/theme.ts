/**
 * Theme preference: 'system' follows the OS (the default - no explicit choice has been made), 'light'
 * and 'dark' pin it. This is a per-browser convenience (like the sidebar's collapsed state), not an
 * account setting: it lives in localStorage only, the same pattern src/store/server.ts and the 2FA
 * setup nudge already use.
 *
 * The OS preference is resolved to a concrete 'light' | 'dark' here in JS, and documentElement's
 * data-theme attribute is ALWAYS set to that concrete value, rather than left absent for a
 * prefers-color-scheme media rule in index.css to pick up. That is not just simpler: an earlier version
 * of this file relied on such a media rule for the no-explicit-choice case, and the light theme visibly
 * did nothing when chosen. The actual cause turned out to be unrelated to media queries at all - a
 * documentation comment in index.css, describing the light-mode block, listed the utility names it
 * affects in a slash-separated shorthand, and one of those slashes landed right after an asterisk that
 * was already there for an unrelated reason (an emphasis mark around a single word), spelling out the
 * two-character sequence that closes a CSS comment. Everything after that point - the rest of the
 * intended comment, and then real CSS - was silently swallowed as invalid content up to the next
 * legitimate comment closer, corrupting the parse of the light-theme override rule that followed it.
 * Its declarations were dropped from the production build with no warning, which is what made this so
 * slow to track down. That comment has been reworded to avoid the collision, and resolving the OS
 * preference here in JS (rather than through a media query) is kept anyway since it centralizes the
 * logic and is easier to test.
 *
 * index.html carries a tiny inline script that applies the stored (or OS-resolved) preference before
 * first paint, so switching pages or reloading never flashes the wrong theme. Keep it in sync with the
 * logic here (it's duplicated on purpose: that one has to run synchronously, before any module loads).
 */
export type ThemePreference = 'system' | 'light' | 'dark'

const KEY = 'continuum:theme'

function systemPrefersLight(): boolean {
  try {
    return window.matchMedia('(prefers-color-scheme: light)').matches
  } catch {
    return false // matchMedia unavailable (very old browser, some test environments): dark is the default look.
  }
}

export function getThemePreference(): ThemePreference {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // a private window or blocked storage just means the preference cannot be remembered: 'system' is
    // the correct answer either way, not an error.
  }
  return 'system'
}

/** Resolves 'system' against the OS's current preference; 'light'/'dark' pass through unchanged. */
function resolve(pref: ThemePreference): 'light' | 'dark' {
  return pref === 'system' ? (systemPrefersLight() ? 'light' : 'dark') : pref
}

function apply(pref: ThemePreference): void {
  const resolved = resolve(pref)
  const root = document.documentElement
  root.setAttribute('data-theme', resolved)
  // Set directly on the element rather than as a color-scheme declaration in index.css: this was
  // already how it worked while tracking down the bug described above, and there is no reason to move
  // it back now that the real cause is fixed - it gets the same native scrollbar/form-control theming
  // either way.
  root.style.colorScheme = resolved
}

/** Applies pref to the page immediately and remembers it (best-effort) for next time. */
export function setThemePreference(pref: ThemePreference): void {
  try {
    if (pref === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, pref)
  } catch {
    // storage may be unavailable; the page still reflects the choice below for this load.
  }
  apply(pref)
}

let watching = false

/**
 * Keeps the applied theme in sync with OS-level changes (e.g. the system switching to Dark Mode at
 * sunset) while the preference is 'system'. Idempotent - safe to call from more than one mount. Call
 * once near app startup; there's nothing to clean up (it outlives any single component).
 */
export function watchSystemTheme(): void {
  if (watching) return
  watching = true
  try {
    window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
      if (getThemePreference() === 'system') apply('system')
    })
  } catch {
    // matchMedia/addEventListener unavailable: the theme just won't live-update on OS changes; the
    // resolved value from the last full load (see index.html's inline script) still applies.
  }
}
