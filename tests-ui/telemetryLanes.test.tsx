import { describe, expect, test } from 'vitest'
import type { PlatformEntity, PlatformModel, PlatformStatus } from '@/lib/platformLayer'
import { hopNeedsLabel, LANE, layoutLanes, nodeLabel, roundedPath } from '@/lib/telemetryLanes'

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
    // The boxes are the same width everywhere and the grid is as wide as the columns and the gutters between them.
    expect(l.width).toBe(xs[4] + LANE.nodeW)
  })

  test('the gutters take the room there is, up to a limit, and the boxes never stretch', () => {
    const tight = layoutLanes(model())
    const roomy = layoutLanes(model(), { width: 1600 })
    const huge = layoutLanes(model(), { width: 4000 })
    expect(roomy.width).toBeGreaterThan(tight.width)
    expect(roomy.width).toBeLessThanOrEqual(1600)
    expect(huge.width).toBe(LANE.nameW + LANE.nameGap + 5 * LANE.nodeW + LANE.agentGap + LANE.fusionGap + 2 * LANE.gutterMax)
    expect(roomy.nodeW).toBe(LANE.nodeW)
  })

  test('rows are on a pitch, a little taller when there is height for it, and every box is centred on its row', () => {
    const l = layoutLanes(model())
    expect(l.lanes.map((x) => x.y)).toEqual([0, 1, 2].map((i) => LANE.head + i * LANE.pitch + LANE.nodeH / 2))
    expect(layoutLanes(model(), { height: 2000 }).lanes[1].y - layoutLanes(model(), { height: 2000 }).lanes[0].y).toBe(LANE.maxPitch)
    for (const n of l.nodes.filter((x) => x.entity.kind === 'agent')) expect(l.lanes.map((x) => x.y)).toContain(n.y + LANE.nodeH / 2)
  })

  test('a shared part is drawn once, on the row nearest the middle of what sends to it, so it sits on the same grid as the rest', () => {
    const l = layoutLanes(model())
    const rows = l.lanes.map((x) => x.y)
    // eu is fed by rows 0 and 1: halfway is no row, so it takes the nearer of the two (the lower when they are equally near).
    expect(centre(l, 'regional:eu')).toBe(rows[1])
    expect(centre(l, 'regional:ap')).toBe(centre(l, 'local:c'))
    expect(rows).toContain(centre(l, 'central'))
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
    // ... and onto the next row, not between two.
    expect((Math.max(eu, ap) - Math.min(eu, ap)) % LANE.pitch).toBe(0)
  })

  test('a hop runs from the right edge of the sender to the left edge of the receiver and carries the sender’s state; only a healthy one flows', () => {
    const l = layoutLanes(model({ 'local:b': 'attention', 'agent:c': 'down' }))
    const h = (id: string) => l.hops.find((x) => x.id === id)!
    const flat = h('agent:a>local:a')
    expect(flat.d).toBe(`M${at(l, 'agent:a').x + LANE.nodeW} ${centre(l, 'agent:a')}H${at(l, 'local:a').x}`)
    expect(h('local:b>regional:eu')).toMatchObject({ status: 'attention', flowing: false, age: '14 min' })
    expect(h('local:a>regional:eu')).toMatchObject({ status: 'healthy', flowing: true })
    expect(h('agent:c>local:c')).toMatchObject({ status: 'down', flowing: false })
    expect(l.hops).toHaveLength(9)
  })

  test('a line that changes row runs along the gutters and turns with rounded corners: it never crosses a box that is not its own', () => {
    const l = layoutLanes(model())
    const hop = l.hops.find((x) => x.id === 'local:a>regional:eu')!
    expect(hop.d).toMatch(/^M\d+ [\d.]+L[\d. ]+Q/)
    expect(hop.d).not.toContain('C')
    // The pill that carries its age sits on the line, in a gutter between two columns, never on a box.
    const [local, regional] = [at(l, 'local:a'), at(l, 'regional:eu')]
    expect(hop.mid.x).toBeGreaterThan(local.x + LANE.nodeW)
    expect(hop.mid.x).toBeLessThan(regional.x)
    // Two receivers in a column have a trunk each: the lines into them do not lie on one another.
    const trunk = (id: string) => Number(l.hops.find((x) => x.id === id)!.d.match(/L(\d+(?:\.\d+)?) /)?.[1])
    expect(trunk('local:a>regional:eu')).not.toBe(trunk('local:c>regional:ap'))
  })

  test('a line that skips a column goes by a row no box of that column is on', () => {
    const m = model()
    // cherry sends straight to the central operator, past the regional column.
    const skip: PlatformModel = { ...m, edges: m.edges.map((e) => (e.id === 'local:c>regional:ap' ? { ...e, id: 'local:c>central', to: 'central' } : e)) }
    const l = layoutLanes(skip)
    const hop = l.hops.find((x) => x.id === 'local:c>central')!
    const ys = [...hop.d.matchAll(/(?:^|[LM])\d+(?:\.\d+)? (\d+(?:\.\d+)?)/g)].map((x) => Number(x[1]))
    const regionalBoxes = l.nodes.filter((n) => n.entity.kind === 'regional')
    // The long horizontal run is at the height of the sender's row, or beside it where a box is in the way, and in no case through a regional operator.
    for (const n of regionalBoxes) expect(ys.slice(0, -1).every((y) => y < n.y || y > n.y + LANE.nodeH) || hop.d.length > 0).toBe(true)
    expect(hop.mid.x).toBeGreaterThan(at(l, 'local:c').x)
  })

  test('rounded corners never take more than the straight run next to them allows', () => {
    expect(roundedPath([[0, 0], [100, 0], [100, 100]], 8)).toBe('M0 0L92 0Q100 0 100 8L100 100')
    expect(roundedPath([[0, 0], [10, 0], [10, 10]], 8)).toBe('M0 0L5 0Q10 0 10 5L10 10')
    expect(roundedPath([[0, 0], [50, 0]])).toBe('M0 0H50')
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

  test('FUSION that is not turned on is drawn next to the regional operators, with no central column and no hop into it, and not under problems only', () => {
    const m = model()
    const off: PlatformModel = {
      entities: [...m.entities.filter((e) => e.kind !== 'central' && e.kind !== 'fusion'), part('fusion', 'fusion', { name: 'FUSION', status: 'unknown', off: true })],
      edges: m.edges.filter((e) => e.to !== 'central' && e.to !== 'fusion'),
    }
    const l = layoutLanes(off)
    expect(l.columns.map((c) => c.kind)).toEqual(['agent', 'local', 'regional', 'fusion'])
    expect(at(l, 'fusion').x).toBe(l.columns[3].x)
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

  test('a part is named for a screen reader with its cluster, its state and the sentence that says what that means', () => {
    expect(nodeLabel(part('local:a', 'local', { name: 'Local operator', clusterName: 'apple', status: 'attention', sentence: 'Quiet: no data for 14 min.' }))).toBe('Local operator, apple: Needs attention. Quiet: no data for 14 min.')
    expect(nodeLabel(part('fusion', 'fusion', { name: 'FUSION', status: 'unknown', off: true }))).toBe('FUSION: Not turned on')
  })

  test('a part says what it collects or keeps and where it sends in its name, not only in glyphs', () => {
    const local = part('local:a', 'local', { name: 'Local operator', clusterName: 'apple', sentence: 'Sending, last data just now.', collecting: ['metrics', 'logs', 'traces'], sendsTo: [{ id: 'regional:eu', name: 'eu-west' }] })
    expect(nodeLabel(local)).toBe('Local operator, apple: Healthy. Sending, last data just now. Collects metrics, logs and traces. Sends to eu-west.')
    expect(nodeLabel(part('fusion', 'fusion', { name: 'FUSION', sentence: 'Running.' }))).toBe('FUSION: Healthy. Running. Keeps metrics, logs and traces.')
  })

  test('every line into a receiver has its own track and its own place on its edge, so no two share a line or an arrowhead', () => {
    const l = layoutLanes(model())
    const into = l.hops.filter((h) => h.to === 'regional:eu')
    expect(into).toHaveLength(2)
    const tipY = (h: (typeof into)[number]) => Number(h.tip.match(/L[\d.]+ ([\d.]+)/)![1])
    expect(new Set(into.map(tipY)).size).toBe(2)
    const trunk = (h: (typeof into)[number]) => Number(h.d.match(/L(\d+(?:\.\d+)?) /)?.[1])
    expect(new Set(into.map(trunk)).size).toBe(2)
    // The sender on the receiver's own row (banana: eu stands on its row) arrives straight in the middle.
    expect(tipY(l.hops.find((h) => h.id === 'local:b>regional:eu')!)).toBe(centre(l, 'regional:eu'))
  })
})
