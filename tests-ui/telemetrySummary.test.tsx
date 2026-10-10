import { describe, expect, test } from 'vitest'
import type { PlatformEntity, PlatformModel, PlatformStatus } from '@/lib/platformLayer'
import { aside, headline, summarize } from '@/lib/telemetrySummary'

const part = (id: string, kind: PlatformEntity['kind'], status: PlatformStatus = 'healthy', over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id, kind, name: kind, detail: '', status, sentence: '', sendsTo: [], ...over })
const hop = (from: string, to: string) => ({ id: `${from}>${to}`, from, to, status: 'healthy' as const })

/** Clusters a, b, c, each with an agent and a local operator, sending to one regional operator, the central operator and FUSION. */
const model = (s: Record<string, PlatformStatus> = {}, clusters = ['a', 'b', 'c']): PlatformModel => ({
  entities: [
    ...clusters.flatMap((k) => [part(`agent:${k}`, 'agent', s[`agent:${k}`], { agentId: k, clusterId: k }), part(`local:${k}`, 'local', s[`local:${k}`], { agentId: k, clusterId: k })]),
    part('regional:eu', 'regional', s['regional:eu']), part('central', 'central', s.central), part('fusion', 'fusion', s.fusion),
  ],
  edges: [...clusters.flatMap((k) => [hop(`agent:${k}`, `local:${k}`), hop(`local:${k}`, 'regional:eu')]), hop('regional:eu', 'central'), hop('central', 'fusion')],
})

describe('summarize', () => {
  test('counts the clusters meant to send and those whose data is on its way', () => {
    const s = summarize(model({ 'local:b': 'attention' }))
    expect(s).toMatchObject({ clusters: 3, flowing: 2, waiting: 0, fusionOff: false })
    expect(headline(s)).toBe('Telemetry is flowing from 2 of 3 clusters')
  })

  test('a place is a cluster or a shared operator: an agent and its local operator that are both wrong count once, as the worse of the two', () => {
    const s = summarize(model({ 'agent:a': 'down', 'local:a': 'attention', 'regional:eu': 'attention' }))
    expect(s.places).toEqual([{ id: 'agent:a', alert: 'bad' }, { id: 'regional:eu', alert: 'warn' }])
    // As bad as each other: the agent, which is the cause.
    expect(summarize(model({ 'agent:a': 'attention', 'local:a': 'attention' })).places).toEqual([{ id: 'agent:a', alert: 'warn' }])
    expect(summarize(model({ 'agent:a': 'attention', 'local:a': 'down' })).places).toEqual([{ id: 'local:a', alert: 'bad' }])
  })

  test('worst first, then in the order the grid shows them; not known is not a problem', () => {
    const order = (id: string) => ({ 'local:a': 1, 'local:b': 2, 'local:c': 3 })[id] ?? 9
    const s = summarize(model({ 'local:a': 'attention', 'local:b': 'down', 'local:c': 'attention', 'agent:c': 'unknown' }), order)
    expect(s.places.map((p) => p.id)).toEqual(['local:b', 'local:a', 'local:c'])
    expect(summarize(model({ 'local:a': 'unknown' })).places).toEqual([])
  })

  test('data still flows past an operator that only needs attention, and stops at one that is not working', () => {
    expect(summarize(model({ 'regional:eu': 'attention' })).flowing).toBe(3)
    expect(summarize(model({ central: 'down' })).flowing).toBe(0)
  })

  test('a cluster not known yet is waiting, not a problem, and the headline never says zero', () => {
    const waiting = summarize(model({ 'local:a': 'unknown', 'local:b': 'unknown', 'local:c': 'unknown' }))
    expect(waiting).toMatchObject({ flowing: 0, waiting: 3, places: [] })
    expect(headline(waiting)).toBe('No data yet from any cluster')
    expect(headline(summarize(model({ central: 'down' })))).toBe('Telemetry is not flowing from any cluster')
    expect(aside(summarize(model({ 'local:a': 'unknown' })))).toBe('1 cluster has no data yet')
  })

  test('all of them, or the one', () => {
    expect(headline(summarize(model()))).toBe('Telemetry is flowing from all 3 clusters')
    expect(headline(summarize(model({}, ['a'])))).toBe('Telemetry is flowing from 1 cluster')
  })

  test('agents without a local operator are no sending cluster; FUSION that is off is said, not counted', () => {
    const only: PlatformModel = { entities: [part('agent:a', 'agent', 'healthy', { agentId: 'a', clusterId: 'a' }), part('fusion', 'fusion', 'unknown', { off: true })], edges: [] }
    const s = summarize(only)
    expect(s).toMatchObject({ clusters: 0, flowing: 0, fusionOff: true, places: [] })
    expect(headline(s)).toBe('No cluster sends telemetry yet')
    expect(aside(s)).toBe('FUSION is off')
  })
})
