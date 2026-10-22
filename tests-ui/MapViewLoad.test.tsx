import { afterEach, describe, expect, test, vi } from 'vitest'

afterEach(() => {
  vi.doUnmock('world-atlas/countries-110m.json')
  vi.resetModules()
})

describe('MapView land loaders', () => {
  test('a failed load is not cached: the next call retries instead of replaying the rejection', async () => {
    vi.resetModules()
    let calls = 0
    vi.doMock('world-atlas/countries-110m.json', () => {
      calls++
      if (calls === 1) throw new Error('chunk failed')
      return { default: { type: 'Topology', objects: { countries: { type: 'GeometryCollection', geometries: [] } }, arcs: [] } }
    })
    const { loadCoarse } = await import('@/features/topology/components/MapView')
    await expect(loadCoarse()).rejects.toThrow()
    const land = await loadCoarse()
    expect(land.type).toBe('FeatureCollection')
    expect(loadCoarse()).toBe(loadCoarse()) // once it works it is cached again
  })
})
