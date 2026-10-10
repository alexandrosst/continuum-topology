// A per-viewer convenience kept in this browser (a panel left open or closed). Never state that matters: a private window or blocked
// storage just means the default comes back, so every read and write is allowed to fail.
export function readFlag(key: string, fallback: boolean): boolean {
  try {
    const v = localStorage.getItem(key)
    return v === null ? fallback : v === '1'
  } catch {
    return fallback
  }
}

export function writeFlag(key: string, value: boolean): void {
  try {
    localStorage.setItem(key, value ? '1' : '0')
  } catch {
    /* storage blocked: the choice lasts until the page is closed */
  }
}

/** A short text choice (a presentation, not data) kept in this browser: null when none was made or storage is blocked. */
export function readText(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function writeText(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* storage blocked: the choice lasts until the page is closed */
  }
}
