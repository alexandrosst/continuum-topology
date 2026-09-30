import { afterEach, describe, expect, test, vi } from 'vitest'
import { prefetchAllRoutesWhenIdle } from '@/lib/routeLoaders'

// prefetchAllRoutesWhenIdle() is meant to be a free, after-the-fact background fetch of every route's own
// (small, built, gzipped) chunk - see its own doc comment for the "a few KB to ~120KB" reasoning that makes
// that a non-issue in production. That reasoning doesn't hold under Vite's dev server, which serves each
// route's full, unbundled, unminified dependency graph (hundreds of KB to MB for a route like Placement,
// which pulls in d3-geo/topojson-client) - so it should do nothing at all in dev, not just something smaller.

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('prefetchAllRoutesWhenIdle (background route-chunk warmup)', () => {
  test('is a no-op in dev: schedules nothing, touches neither requestIdleCallback nor setTimeout', () => {
    vi.stubEnv('DEV', true)
    const idle = vi.fn()
    const timeout = vi.fn()
    vi.stubGlobal('requestIdleCallback', idle)
    vi.stubGlobal('setTimeout', timeout)

    prefetchAllRoutesWhenIdle()

    expect(idle).not.toHaveBeenCalled()
    expect(timeout).not.toHaveBeenCalled()
  })

  test('schedules a background run in production, via requestIdleCallback when available', () => {
    vi.stubEnv('DEV', false)
    const idle = vi.fn()
    vi.stubGlobal('requestIdleCallback', idle)

    prefetchAllRoutesWhenIdle()

    expect(idle).toHaveBeenCalledTimes(1)
    expect(idle.mock.calls[0][1]).toEqual({ timeout: 3000 })
  })

  test('falls back to setTimeout in production when requestIdleCallback does not exist', () => {
    vi.stubEnv('DEV', false)
    vi.stubGlobal('requestIdleCallback', undefined)
    const timeout = vi.fn()
    vi.stubGlobal('setTimeout', timeout)

    prefetchAllRoutesWhenIdle()

    expect(timeout).toHaveBeenCalledTimes(1)
    expect(timeout.mock.calls[0][1]).toBe(1000)
  })
})
