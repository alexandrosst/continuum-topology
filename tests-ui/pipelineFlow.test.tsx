import { readFileSync } from 'node:fs'
import { describe, expect, test } from 'vitest'
import type { Hop } from '@/lib/operatorsView'
import { EXPECTED_MS, freshness, hopChip, linkState } from '@/lib/pipelineFlow'

const NOW = Date.parse('2026-10-09T12:00:00Z')
const ago = (s: number) => new Date(NOW - s * 1000).toISOString()

describe('freshness', () => {
  test('1 for data that just arrived, running out at five expected intervals, with fresh, late and stale between', () => {
    expect(freshness(ago(0), NOW, 60_000)).toMatchObject({ level: 'fresh', fraction: 1 })
    expect(freshness(ago(150), NOW, 60_000).level).toBe('fresh')
    expect(freshness(ago(151), NOW, 60_000).level).toBe('late')
    expect(freshness(ago(301), NOW, 60_000)).toMatchObject({ level: 'stale', fraction: 0 })
    expect(freshness(ago(30), NOW, 60_000).fraction).toBeCloseTo(0.9)
  })
  test('an agent is late at the same 75 s its heartbeat check uses', () => {
    expect(freshness(ago(75), NOW, EXPECTED_MS.agent).level).toBe('fresh')
    expect(freshness(ago(76), NOW, EXPECTED_MS.agent).level).toBe('late')
  })
  test('no data, or a date that cannot be read, is "none" with an empty bar; a clock a little ahead is not negative', () => {
    expect(freshness(undefined, NOW, 60_000)).toEqual({ level: 'none', fraction: 0 })
    expect(freshness('nope', NOW, 60_000).level).toBe('none')
    expect(freshness(ago(-5), NOW, 60_000)).toMatchObject({ level: 'fresh', fraction: 1, ageMs: 0 })
  })
})

describe('linkState', () => {
  const fresh = freshness(ago(10), NOW, 60_000)
  const late = freshness(ago(200), NOW, 60_000)
  const none = freshness(undefined, NOW, 60_000)
  test('only healthy and fresh moves; attention is stalled, not working is broken, unknown is unknown', () => {
    expect(linkState('healthy', 3, fresh)).toBe('flowing')
    expect(linkState('attention', 3, fresh)).toBe('stalled')
    expect(linkState('down', 3, fresh)).toBe('broken')
    expect(linkState('unknown', 3, fresh)).toBe('unknown')
  })
  test('a healthy sender whose data has gone late is stalled, and one that never sent is unknown; nothing there is unknown', () => {
    expect(linkState('healthy', 3, late)).toBe('stalled')
    expect(linkState('healthy', 3, none)).toBe('unknown')
    expect(linkState('healthy', 0, fresh)).toBe('unknown')
    expect(linkState(undefined, 0, none)).toBe('unknown')
  })
})

describe('hopChip', () => {
  const hop = (over: Partial<Hop>): Hop => ({ key: 'local', label: 'Local operators', total: 6, healthy: 4, state: 'attention', worst: 2, ...over })
  test('Healthy by itself; otherwise how many are in the worst state; nothing for an empty hop', () => {
    expect(hopChip(hop({ state: 'healthy', worst: 6, healthy: 6 }))).toEqual({ state: 'healthy' })
    expect(hopChip(hop({}))).toEqual({ state: 'attention', count: 2 })
    expect(hopChip(hop({ total: 0, state: undefined, worst: 0 }))).toBeUndefined()
  })
})

describe('flow animation', () => {
  const css = readFileSync('src/index.css', 'utf8')
  test('the flow lines move only for people who have not asked for less motion', () => {
    const reduced = css.slice(css.lastIndexOf('@media (prefers-reduced-motion: reduce)'))
    expect(css).toMatch(/\.flow-dash \{[^}]*stroke-dasharray: 5 4;[^}]*animation: flow-dash 1\.6s linear infinite/)
    for (const c of ['.flow-dash', '.flow-dash-bg', '.flow-dash-bg-v']) expect(reduced).toContain(c)
    expect(reduced).toMatch(/animation: none/)
  })
})
