import { describe, expect, test, vi } from 'vitest'
import { render } from '@testing-library/react'
import { holdReload, isReloadHeld, isStaleChunkError, reloadForNewVersion } from '@/lib/staleBuild'
import { useHoldReload } from '@/lib/useHoldReload'

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

describe('holdReload', () => {
  test('no automatic reload while something holds it, and no attempt is recorded, so the one try is still there afterwards', () => {
    const storage = memory()
    const reload = vi.fn()
    const release = holdReload()
    expect(isReloadHeld()).toBe(true)
    expect(reloadForNewVersion({ now: () => 1_000_000, storage, reload })).toBe(false)
    expect(reload).not.toHaveBeenCalled()
    expect(storage.getItem('ikhnos-reloaded-for-update')).toBeNull()
    release()
    expect(isReloadHeld()).toBe(false)
    expect(reloadForNewVersion({ now: () => 1_000_001, storage, reload })).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  test('two holds both have to be released, and a release is safe to call twice', () => {
    const a = holdReload()
    const b = holdReload()
    a()
    a()
    expect(isReloadHeld()).toBe(true)
    b()
    expect(isReloadHeld()).toBe(false)
  })

  test('useHoldReload holds while the component is mounted and active', () => {
    const Held = ({ active }: { active: boolean }) => {
      useHoldReload(active)
      return null
    }
    const { rerender, unmount } = render(<Held active={false} />)
    expect(isReloadHeld()).toBe(false)
    rerender(<Held active />)
    expect(isReloadHeld()).toBe(true)
    unmount()
    expect(isReloadHeld()).toBe(false)
  })
})
