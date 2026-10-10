import { describe, expect, test } from 'vitest'
import type { PlatformEntity, PlatformModel, PlatformStatus } from '@/lib/platformLayer'
import { curve, hopNeedsLabel, LANE, layoutLanes, nodeLabel } from '@/lib/telemetryLanes'

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
const BAND = LANE.caption + LANE.nodeH + LANE.bandBelow
const PITCH = BAND + LANE.bandGap

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
    // The boxes are the same width everywhere and the grid is as wide as the columns and the gutters between them, with a little air at each end.
    expect(l.width).toBe(xs[4] + LANE.nodeW + LANE.padX)
    expect(xs[0]).toBe(LANE.padX)
  })

  test('the gutters take the room there is, up to a limit, and the boxes never stretch', () => {
    const tight = layoutLanes(model())
    const roomy = layoutLanes(model(), { width: 1600 })
    const huge = layoutLanes(model(), { width: 4000 })
    expect(roomy.width).toBeGreaterThan(tight.width)
    expect(roomy.width).toBeLessThanOrEqual(1600)
    expect(huge.width).toBe(2 * LANE.padX + 5 * LANE.nodeW + 4 * LANE.gutterMax)
    expect(roomy.nodeW).toBe(LANE.nodeW)
  })

  test('a lane is a band with its name on top; bands are on a pitch and every box is centred on the row under the name', () => {
    const l = layoutLanes(model())
    expect(l.lanes.map((x) => x.top)).toEqual([0, 1, 2].map((i) => LANE.head + i * PITCH))
    expect(l.lanes.every((x) => x.height === BAND)).toBe(true)
    expect(l.lanes.map((x) => x.y)).toEqual(l.lanes.map((x) => x.top + LANE.caption + LANE.nodeH / 2))
    for (const n of l.nodes.filter((x) => x.entity.kind === 'agent')) expect(l.lanes.map((x) => x.y)).toContain(n.y + LANE.nodeH / 2)
    expect(l.height).toBeGreaterThanOrEqual(l.lanes[2].top + BAND)
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
    expect((Math.max(eu, ap) - Math.min(eu, ap)) % PITCH).toBe(0)
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

  test('a line that changes row is a smooth curve, level where it leaves and where it arrives, with its pill in the gutter between the two columns', () => {
    const l = layoutLanes(model())
    const hop = l.hops.find((x) => x.id === 'local:a>regional:eu')!
    expect(hop.d).toMatch(/^M[\d.]+ [\d.]+C/)
    const [local, regional] = [at(l, 'local:a'), at(l, 'regional:eu')]
    const [c1, c2] = hop.d.match(/C([\d.]+) ([\d.]+) ([\d.]+) ([\d.]+)/)!.slice(1).map(Number).reduce<number[][]>((acc, n, i) => (i % 2 ? (acc[acc.length - 1].push(n), acc) : [...acc, [n]]), [])
    const [sy, ey] = [hop.d.match(/^M[\d.]+ ([\d.]+)/)![1], hop.tip.match(/L[\d.]+ ([\d.]+)/)![1]].map(Number)
    expect(c1[1]).toBe(sy)
    expect(c2[1]).toBe(ey)
    expect(hop.mid.x).toBeGreaterThan(local.x + LANE.nodeW)
    expect(hop.mid.x).toBeLessThan(regional.x)
  })

  test('a line starts on the right edge of its sender and its arrow is on the left edge of its receiver, on the box, never inside it', () => {
    const l = layoutLanes(model())
    for (const h of l.hops) {
      const [a, b] = [at(l, h.from), at(l, h.to)]
      expect(h.d.startsWith(`M${a.x + LANE.nodeW} `)).toBe(true)
      expect(h.tip).toContain(`L${b.x} `)
      const tipY = Number(h.tip.match(/L[\d.]+ ([\d.]+)/)![1])
      expect(tipY).toBeGreaterThan(b.y)
      expect(tipY).toBeLessThan(b.y + LANE.nodeH)
    }
  })

  test('a line that skips a column runs straight through the gap between that column\'s boxes, and curves in and out beside it', () => {
    const m = model()
    // cherry sends straight to the central operator, past the regional column.
    const skip: PlatformModel = { ...m, edges: m.edges.map((e) => (e.id === 'local:c>regional:ap' ? { ...e, id: 'local:c>central', to: 'central' } : e)) }
    const l = layoutLanes(skip)
    const hop = l.hops.find((x) => x.id === 'local:c>central')!
    const run = hop.d.match(/([\d.]+) ([\d.]+)H([\d.]+)/)!
    const y = Number(run[2])
    for (const n of l.nodes.filter((x) => x.entity.kind === 'regional')) expect(y < n.y || y > n.y + LANE.nodeH).toBe(true)
    expect(Number(run[3])).toBe(at(l, 'regional:eu').x + LANE.nodeW)
    expect(hop.mid.x).toBeGreaterThan(at(l, 'local:c').x + LANE.nodeW)
    expect(hop.mid.x).toBeLessThan(at(l, 'regional:eu').x)
  })

  test('a line between two boxes on one row is straight; otherwise it is a curve', () => {
    expect(curve(10, 20, 100, 20)).toBe('M10 20H100')
    expect(curve(0, 0, 100, 50)).toBe('M0 0C50 0 50 50 100 50')
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

  test('every line into a receiver has its own place on its edge, in the order of the boxes they come from, so no two share an arrowhead', () => {
    const l = layoutLanes(model())
    const into = l.hops.filter((h) => h.to === 'regional:eu')
    expect(into).toHaveLength(2)
    const tipY = (h: (typeof into)[number]) => Number(h.tip.match(/L[\d.]+ ([\d.]+)/)![1])
    expect(Math.abs(tipY(into[0]) - tipY(into[1]))).toBeGreaterThanOrEqual(LANE.edgeStep)
    // The one from the higher row arrives above the one from the lower row, so they never cross at the box.
    expect(tipY(l.hops.find((h) => h.id === 'local:a>regional:eu')!)).toBeLessThan(tipY(l.hops.find((h) => h.id === 'local:b>regional:eu')!))
    // A single line to a box on its own row is level, and in the middle.
    expect(tipY(l.hops.find((h) => h.id === 'agent:a>local:a')!)).toBe(centre(l, 'local:a'))
  })

  test('a line floats along the edge towards the box at its other end, never past the edge\'s margin', () => {
    const l = layoutLanes(model())
    const out = Number(l.hops.find((h) => h.id === 'local:a>regional:eu')!.d.match(/^M[\d.]+ ([\d.]+)/)![1])
    // apple's row is above eu's: its line leaves a little below the middle of its box, towards eu.
    expect(out).toBeGreaterThan(centre(l, 'local:a'))
    for (const h of l.hops) {
      const y = Number(h.d.match(/^M[\d.]+ ([\d.]+)/)![1])
      const n = at(l, h.from)
      expect(y).toBeGreaterThanOrEqual(n.y + LANE.edgeMargin - 0.1)
      expect(y).toBeLessThanOrEqual(n.y + LANE.nodeH - LANE.edgeMargin + 0.1)
    }
  })
})
