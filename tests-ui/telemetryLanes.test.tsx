import { describe, expect, test } from 'vitest'
import type { PlatformEntity, PlatformModel, PlatformStatus } from '@/lib/platformLayer'
import { hopNeedsLabel, LANE, layoutLanes, nodeLabel } from '@/lib/telemetryLanes'

const part = (id: string, kind: PlatformEntity['kind'], over: Partial<PlatformEntity> = {}): PlatformEntity => ({ id, kind, name: kind, detail: '', status: 'healthy', sentence: '', sendsTo: [], ...over })
const hop = (from: string, to: string, status: PlatformStatus = 'healthy', age?: string) => ({ id: `${from}>${to}`, from, to, status, age })

/** Three clusters: a and b send to the regional operator eu, c to ap; both regional operators send to the central operator, then FUSION. */
const model = (status: Partial<Record<string, PlatformStatus>> = {}): PlatformModel => {
  const lane = (key: string, name: string) => [
    part(`agent:${key}`, 'agent', { clusterId: key, clusterName: name, agentId: key, status: status[`agent:${key}`] ?? 'healthy' }),
    part(`local:${key}`, 'local', { clusterId: key, clusterName: name, agentId: key, status: status[`local:${key}`] ?? 'healthy' }),
  ]
  const s = (id: string) => status[id] ?? 'healthy'
  return {
    entities: [
      ...lane('c', 'cherry'), ...lane('a', 'apple'), ...lane('b', 'banana'),
      part('regional:eu', 'regional', { name: 'eu', status: s('regional:eu') }), part('regional:ap', 'regional', { name: 'ap', status: s('regional:ap') }),
      part('central', 'central', { status: s('central') }), part('fusion', 'fusion', { name: 'FUSION', status: s('fusion') }),
    ],
    edges: [
      hop('agent:a', 'local:a', s('agent:a')), hop('agent:b', 'local:b', s('agent:b')), hop('agent:c', 'local:c', s('agent:c')),
      hop('local:a', 'regional:eu', s('local:a'), '2 s'), hop('local:b', 'regional:eu', s('local:b'), '14 min'), hop('local:c', 'regional:ap', s('local:c')),
      hop('regional:eu', 'central', s('regional:eu')), hop('regional:ap', 'central', s('regional:ap')), hop('central', 'fusion', s('central'), '8 s'),
    ],
  }
}
const at = (l: ReturnType<typeof layoutLanes>, id: string) => l.nodes.find((n) => n.entity.id === id)!
const centre = (l: ReturnType<typeof layoutLanes>, id: string) => at(l, id).y + LANE.nodeH / 2

describe('layoutLanes', () => {
  test('one lane per cluster, those that send to the same regional operator together; the pipeline reads as columns', () => {
    const l = layoutLanes(model())
    expect(l.lanes.map((x) => x.name)).toEqual(['apple', 'banana', 'cherry'])
    expect(l.columns.map((c) => c.label)).toEqual(['Discovery agent', 'Local operator', 'Regional operator', 'Central operator', 'FUSION'])
    for (const kind of ['agent', 'local', 'regional', 'central', 'fusion'] as const) {
      expect(new Set(l.nodes.filter((n) => n.entity.kind === kind).map((n) => n.x)).size).toBe(1)
    }
    const xs = ['agent:a', 'local:a', 'regional:eu', 'central', 'fusion'].map((id) => at(l, id).x)
    expect(xs).toEqual([...xs].sort((p, q) => p - q))
    expect(l.nodes.filter((n) => n.entity.kind === 'regional')).toHaveLength(2)
    expect(centre(l, 'agent:a')).toBe(centre(l, 'local:a'))
    expect(l.width).toBe(LANE.padX * 2 + 5 * LANE.nodeW + 4 * LANE.colGap)
  })

  test('a shared part is drawn once, level with the middle of what sends to it', () => {
    const l = layoutLanes(model())
    expect(centre(l, 'regional:eu')).toBe((centre(l, 'local:a') + centre(l, 'local:b')) / 2)
    expect(centre(l, 'regional:ap')).toBe(centre(l, 'local:c'))
    expect(centre(l, 'central')).toBe((centre(l, 'regional:eu') + centre(l, 'regional:ap')) / 2)
    expect(centre(l, 'fusion')).toBe(centre(l, 'central'))
  })

  test('two shared parts that would touch are pushed apart', () => {
    const m = model()
    // Both clusters of one lane pair send to eu, so eu and ap are one lane apart or more; squeeze them by sending c to eu too and back.
    const crowded = { ...m, edges: m.edges.map((e) => (e.from === 'local:c' ? { ...e, to: 'regional:eu' } : e)) }
    const l = layoutLanes(crowded)
    const [eu, ap] = ['regional:eu', 'regional:ap'].map((id) => centre(l, id))
    expect(Math.abs(eu - ap)).toBeGreaterThanOrEqual(LANE.nodeH + LANE.minGap)
    expect(l.height).toBeGreaterThanOrEqual(Math.max(eu, ap) + LANE.nodeH / 2)
  })

  test('a hop runs from the right edge of the sender to the left edge of the receiver and carries the sender’s state; only a healthy one flows', () => {
    const l = layoutLanes(model({ 'local:b': 'attention', 'agent:c': 'down' }))
    const h = (id: string) => l.hops.find((x) => x.id === id)!
    const flat = h('agent:a>local:a')
    expect(flat.d).toBe(`M${at(l, 'agent:a').x + LANE.nodeW} ${centre(l, 'agent:a')}H${at(l, 'local:a').x}`)
    expect(h('local:b>regional:eu')).toMatchObject({ status: 'attention', flowing: false, age: '14 min' })
    expect(h('local:a>regional:eu')).toMatchObject({ status: 'healthy', flowing: true })
    expect(h('agent:c>local:c')).toMatchObject({ status: 'down', flowing: false })
    expect(h('local:a>regional:eu').d).toMatch(/^M\d+ \d+C/)
    expect(l.hops).toHaveLength(9)
  })

  test('problems only keeps the lanes where something on the way to FUSION is wrong, and the shared parts they reach', () => {
    expect(layoutLanes(model(), { problemsOnly: true }).lanes).toEqual([])
    const l = layoutLanes(model({ 'local:c': 'attention' }), { problemsOnly: true })
    expect(l.lanes.map((x) => x.name)).toEqual(['cherry'])
    expect(l.nodes.map((n) => n.entity.id).sort()).toEqual(['agent:c', 'central', 'fusion', 'local:c', 'regional:ap'])
    expect(l.hops.map((x) => x.id).sort()).toEqual(['agent:c>local:c', 'central>fusion', 'local:c>regional:ap', 'regional:ap>central'])
    // A shared part that is wrong puts every lane that goes through it in.
    expect(layoutLanes(model({ 'regional:eu': 'down' }), { problemsOnly: true }).lanes.map((x) => x.name)).toEqual(['apple', 'banana'])
    expect(layoutLanes(model({ fusion: 'attention' }), { problemsOnly: true }).lanes).toHaveLength(3)
    // Not known is not a problem.
    expect(layoutLanes(model({ 'local:a': 'unknown' }), { problemsOnly: true }).lanes).toEqual([])
  })

  test('FUSION that is not turned on is drawn in its column with no hop into it, and not under problems only', () => {
    const m = model()
    const off: PlatformModel = {
      entities: [...m.entities.filter((e) => e.kind !== 'central' && e.kind !== 'fusion'), part('fusion', 'fusion', { name: 'FUSION', status: 'unknown', off: true })],
      edges: m.edges.filter((e) => e.to !== 'central' && e.to !== 'fusion'),
    }
    const l = layoutLanes(off)
    expect(at(l, 'fusion').x).toBe(l.columns[4].x)
    expect(l.hops.some((x) => x.to === 'fusion')).toBe(false)
    expect(layoutLanes({ ...off, entities: off.entities.map((e) => (e.id === 'local:a' ? { ...e, status: 'down' as const } : e)) }, { problemsOnly: true }).nodes.some((n) => n.entity.kind === 'fusion')).toBe(false)
  })

  test('a cluster whose agent has no local operator is a lane of one part; no agent at all is no lane', () => {
    const only: PlatformModel = { entities: [part('agent:a', 'agent', { clusterId: 'a', clusterName: 'apple', agentId: 'a' })], edges: [] }
    expect(layoutLanes(only).nodes.map((n) => n.entity.id)).toEqual(['agent:a'])
    expect(layoutLanes({ entities: [part('fusion', 'fusion', { off: true, status: 'unknown' })], edges: [] }).lanes).toEqual([])
  })
})

describe('labels', () => {
  test('a hop says its age only when something is wrong with it', () => {
    expect(hopNeedsLabel({ status: 'healthy', age: '2 s' })).toBe(false)
    expect(hopNeedsLabel({ status: 'attention', age: '14 min' })).toBe(true)
    expect(hopNeedsLabel({ status: 'down', age: undefined })).toBe(false)
  })

  test('a part is named for a screen reader with its cluster, its state and when its data last arrived', () => {
    expect(nodeLabel(part('local:a', 'local', { name: 'Local operator', clusterName: 'apple', status: 'attention' }), '14 min')).toBe('Local operator, apple: Needs attention, last data 14 min')
    expect(nodeLabel(part('fusion', 'fusion', { name: 'FUSION', status: 'unknown', off: true }))).toBe('FUSION: Not turned on')
  })
})
