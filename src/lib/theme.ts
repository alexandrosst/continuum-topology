/**
 * Theme preference: 'system' follows the OS (the default - no explicit choice has been made), 'light'
 * and 'dark' pin it. This is a per-browser convenience (like the sidebar's collapsed state), not an
 * account setting: it lives in localStorage only, the same pattern src/store/server.ts and the 2FA
 * setup nudge already use, and is applied by setting (or removing) documentElement's data-theme
 * attribute, which src/index.css's [data-theme='light'|'dark'] blocks key off. index.html carries a
 * tiny inline script that applies the stored preference before first paint, so switching pages or
 * reloading never flashes the wrong theme.
 */
export type ThemePreference = 'system' | 'light' | 'dark'

const KEY = 'continuum:theme'

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

/** Applies pref to the page immediately and remembers it (best-effort) for next time. */
export function setThemePreference(pref: ThemePreference): void {
  try {
    if (pref === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, pref)
  } catch {
    // storage may be unavailable; the page still reflects the choice below for this load.
  }
  const root = document.documentElement
  if (pref === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', pref)
}
