import { describe, expect, test } from 'vitest'
import {
  entityLabel,
  fieldEverReported,
  freshnessOf,
  latestDefined,
  niceDomain,
  toPoints,
  type SelfTelemetryEntity,
} from '@/lib/selfHealth'

const agent = (over: Partial<SelfTelemetryEntity> = {}): SelfTelemetryEntity => ({
  id: 'a1',
  kind: 'agent',
  clusterName: 'edge-1',
  samples: [],
  ...over,
})

describe('entityLabel', () => {
  test('names the server entity plainly, since it has no clusterName of its own', () => {
    expect(entityLabel({ id: 'server', kind: 'server', samples: [] })).toBe('Ikhnos server')
  })
  test('uses the agent\'s own clusterName when it has one', () => {
    expect(entityLabel(agent({ clusterName: 'prod-east' }))).toBe('prod-east')
  })
  test('falls back to the raw id for an agent with no clusterName', () => {
    expect(entityLabel(agent({ clusterName: undefined, id: 'agent-42' }))).toBe('agent-42')
  })
})

describe('freshnessOf', () => {
  test('null for an entity with no samples at all (defensive - the backend never actually sends one)', () => {
    expect(freshnessOf(agent({ samples: [] }))).toBeNull()
  })
  test('live when the last sample is within the threshold', () => {
    const now = Date.parse('2026-01-01T00:01:00Z')
    const f = freshnessOf(agent({ samples: [{ t: '2026-01-01T00:00:30Z', rssBytes: 1, goroutines: 1 }] }), now)
    expect(f?.live).toBe(true)
    expect(f?.ageMs).toBe(30_000)
  })
  test('stale once the last sample is older than the threshold', () => {
    const now = Date.parse('2026-01-01T00:05:00Z')
    const f = freshnessOf(agent({ samples: [{ t: '2026-01-01T00:00:00Z', rssBytes: 1, goroutines: 1 }] }), now)
    expect(f?.live).toBe(false)
    expect(f?.ageMs).toBe(300_000)
  })
})

describe('fieldEverReported / latestDefined', () => {
  const e = agent({
    samples: [
      { t: '2026-01-01T00:00:00Z', rssBytes: 1, goroutines: 1 },
      { t: '2026-01-01T00:00:30Z', rssBytes: 2, goroutines: 2, watts: 12.5 },
      { t: '2026-01-01T00:01:00Z', rssBytes: 3, goroutines: 3 },
    ],
  })
  test('fieldEverReported is true as soon as ANY sample carried it, not just the latest one', () => {
    expect(fieldEverReported(e, 'watts')).toBe(true)
  })
  test('fieldEverReported is false when no sample ever carried it', () => {
    expect(fieldEverReported(e, 'bandwidthSharePct')).toBe(false)
    expect(fieldEverReported(e, 'continuumWattsEstimate')).toBe(false)
  })
  test('latestDefined skips back past a trailing sample where the field is momentarily missing', () => {
    expect(latestDefined(e, 'watts')).toBe(12.5)
  })
  test('latestDefined is undefined when no sample ever carried the field', () => {
    expect(latestDefined(e, 'bandwidthSharePct')).toBeUndefined()
  })
  test('latestDefined on an always-present field just reads the last sample', () => {
    expect(latestDefined(e, 'rssBytes')).toBe(3)
  })
})

describe('toPoints', () => {
  test('keeps every sample, including ones missing the requested field, as an explicit undefined point', () => {
    const e = agent({
      samples: [
        { t: '2026-01-01T00:00:00Z', rssBytes: 1, goroutines: 1, cpuPct: 5 },
        { t: '2026-01-01T00:00:30Z', rssBytes: 2, goroutines: 2 },
      ],
    })
    const pts = toPoints(e, 'cpuPct')
    expect(pts).toHaveLength(2)
    expect(pts[0].v).toBe(5)
    expect(pts[1].v).toBeUndefined()
    expect(pts[0].t).toBe(Date.parse('2026-01-01T00:00:00Z'))
  })
})

describe('niceDomain', () => {
  test('rounds a tight, awkward range outward to a clean step', () => {
    const d = niceDomain(5, 97, 4)
    expect(d.min).toBe(0)
    expect(d.ticks[d.ticks.length - 1]).toBeGreaterThanOrEqual(97)
    // every tick is a clean multiple of the step, and the step divides evenly
    const step = d.ticks[1] - d.ticks[0]
    for (const t of d.ticks) expect(Math.round(t / step)).toBeCloseTo(t / step, 6)
  })
  test('never returns a negative minimum for a series whose real data is non-negative', () => {
    const d = niceDomain(0, 0, 4) // a flat-zero series, e.g. a quiet goroutine count
    expect(d.min).toBeGreaterThanOrEqual(0)
    expect(d.max).toBeGreaterThan(d.min)
  })
  test('a degenerate (min === max, non-zero) range still produces two distinct ticks', () => {
    const d = niceDomain(50, 50, 4)
    expect(d.ticks.length).toBeGreaterThanOrEqual(2)
    expect(d.max).toBeGreaterThan(d.min)
  })
})
