import { describe, expect, test, vi } from 'vitest'
import { isStaleChunkError, reloadForNewVersion } from '@/lib/staleBuild'

const memory = () => {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
}

describe('reloadForNewVersion', () => {
  test('reloads once, and not again within a minute (a reload that did not help must not loop)', () => {
    const storage = memory()
    const reload = vi.fn()
    let t = 1_000_000
    expect(reloadForNewVersion({ now: () => t, storage, reload })).toBe(true)
    t += 5_000
    expect(reloadForNewVersion({ now: () => t, storage, reload })).toBe(false)
    expect(reload).toHaveBeenCalledTimes(1)
    t += 61_000 // a later deploy is a new case
    expect(reloadForNewVersion({ now: () => t, storage, reload })).toBe(true)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  test('does not reload when the browser will not remember that it did', () => {
    const reload = vi.fn()
    const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
    expect(reloadForNewVersion({ storage: broken, reload })).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })
})

describe('isStaleChunkError', () => {
  test('knows the three browsers\' words for a file that could not be fetched, and nothing else', () => {
    for (const m of ['Failed to fetch dynamically imported module: https://x/assets/a-1.js', 'error loading dynamically imported module', 'Importing a module script failed.']) expect(isStaleChunkError(new TypeError(m))).toBe(true)
    expect(isStaleChunkError(new Error('Cannot read properties of undefined'))).toBe(false)
    expect(isStaleChunkError(undefined)).toBe(false)
  })
})
