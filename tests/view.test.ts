import assert from 'node:assert/strict'
import { applyFilter, knownOnly, NO_APP, parseFilter, encodeList } from '../src/lib/filter'
import { clusterLoad, lossBand, pathQuality } from '../src/lib/metrics'
import { siteConnections } from '../src/lib/geo'
import { describeView, viewParams } from '../src/lib/views'
import { seedTopology } from '../src/lib/seed'
import { DEFAULT_ORG, type Dependency, type Path } from '../src/lib/types'

let failed = 0
const test = (name: string, fn: () => void) => {
  try {
    fn()
    console.log('PASS', name)
  } catch (e) {
    failed++
    console.log('FAIL', name, '\n  ', (e as Error).message)
  }
}

const seed = seedTopology()
const live = <T extends { deletedAt?: string }>(xs: T[]) => xs.filter((x) => !x.deletedAt)

test('a filter that chooses nothing changes nothing', () => {
  assert.equal(applyFilter(seed, { clusters: [], apps: [] }), seed)
})

test('one cluster: only its services, nodes and the dependencies whose both ends remain', () => {
  const c = live(seed.clusters)[0]
  const out = applyFilter(seed, { clusters: [c.id], apps: [] })
  assert.deepEqual(out.clusters.map((x) => x.id), [c.id])
  assert.ok(out.services.length > 0)
  assert.ok(out.services.every((s) => s.clusterId === c.id))
  assert.ok(out.nodes.every((n) => n.clusterId === c.id))
  const ids = new Set([...out.services.map((s) => s.id), ...out.devices.map((d) => d.id), ...out.externalEndpoints.map((e) => e.id)])
  for (const d of out.dependencies) assert.ok(ids.has(d.from) && ids.has(d.to), `dependency ${d.id} draws a line to something that is not shown`)
})

test('one application: every kept service belongs to it, and no other cluster keeps only unrelated services', () => {
  const app = live(seed.applications).find((a) => seed.services.some((s) => s.applicationId === a.id))!
  const out = applyFilter(seed, { clusters: [], apps: [app.id] })
  assert.ok(out.services.length > 0)
  assert.ok(out.services.every((s) => s.applicationId === app.id))
  for (const c of out.clusters) assert.ok(out.services.some((s) => s.clusterId === c.id), `cluster ${c.name} has nothing of the application`)
})

test('the "no application" choice keeps exactly the unassigned services', () => {
  const out = applyFilter(seed, { clusters: [], apps: [NO_APP] })
  assert.ok(out.services.every((s) => !s.applicationId))
  assert.equal(out.services.length, seed.services.filter((s) => !s.applicationId).length)
})

test('cluster and application together narrow (AND), never widen', () => {
  const c = live(seed.clusters)[0]
  const app = live(seed.applications)[0]
  const both = applyFilter(seed, { clusters: [c.id], apps: [app.id] })
  const onlyC = applyFilter(seed, { clusters: [c.id], apps: [] })
  const onlyA = applyFilter(seed, { clusters: [], apps: [app.id] })
  const ids = (xs: { id: string }[]) => new Set(xs.map((x) => x.id))
  for (const s of both.services) assert.ok(ids(onlyC.services).has(s.id) && ids(onlyA.services).has(s.id))
})

test('sites follow the clusters that are kept', () => {
  const c = live(seed.clusters).find((x) => x.siteId)!
  const out = applyFilter(seed, { clusters: [c.id], apps: [] })
  assert.ok(out.sites.some((s) => s.id === c.siteId))
  assert.ok(out.sites.length <= seed.sites.length)
  assert.ok(out.siteLinks.every((l) => out.sites.some((s) => s.id === l.a) && out.sites.some((s) => s.id === l.b)))
})

test('knownOnly drops ids that no longer exist so a stale link cannot hide everything', () => {
  const f = knownOnly({ clusters: ['gone', live(seed.clusters)[0].id], apps: ['gone', NO_APP] }, seed)
  assert.equal(f.clusters.length, 1)
  assert.deepEqual(f.apps, [NO_APP])
})

test('filter round-trips through the URL, commas and all', () => {
  const ids = ['a,b', 'c d', 'é']
  const sp = new URLSearchParams()
  sp.set('clusters', encodeList(ids)!)
  assert.deepEqual(parseFilter(new URLSearchParams(sp.toString())).clusters, ids)
  assert.equal(encodeList([]), null)
})

test('saved views keep the filter and describe it', () => {
  assert.equal(viewParams('view=map&clusters=a,b'), 'clusters=a%2Cb&view=map')
  assert.match(describeView('view=infrastructure&clusters=a,b&apps=x'), /2 clusters only.*1 application only/)
  assert.equal(viewParams('view=application&clusters='), '')
})

test('clusterLoad: percentages from what nodes report, nothing for what they do not', () => {
  const cid = 'k'
  const node = (id: string, extra: object) => ({ ...seed.nodes[0], id, clusterId: cid, status: 'healthy', ...extra }) as (typeof seed.nodes)[number]
  const l = clusterLoad({ id: cid }, [
    node('n1', { allocatable: { cpu: 4, memoryGb: 8 }, requested: { cpu: 3, memoryGb: 2 }, podCount: 10, podCapacity: 100 }),
    node('n2', { allocatable: { cpu: 4, memoryGb: 8 }, requested: { cpu: 1, memoryGb: 2 }, podCount: 30, podCapacity: 100, status: 'offline' }),
  ], [])
  assert.equal(l.cpuPct, 50)
  assert.equal(l.memPct, 25)
  assert.equal(l.podPct, 20)
  assert.equal(l.nodes, 2)
  assert.equal(l.ready, 1)
  const none = clusterLoad({ id: cid }, [node('n3', { allocatable: undefined, requested: undefined, podCount: undefined, podCapacity: undefined })], [])
  assert.equal(none.cpuPct, undefined)
  assert.equal(none.podPct, undefined)
})

test('pathQuality prefers the direct measurement, falls back to the reverse one, ignores stale', () => {
  const p = (from: string, to: string, extra: Partial<Path> = {}): Path => ({ id: from + to, fromCluster: from, fromName: from, host: 'h', port: 1, toCluster: to, source: 'observed', rttMinMs: 1, rttP50Ms: 20, rttP95Ms: 30, lossPct: 0, samples: 5, at: '2026-09-19T00:00:00Z', ...extra })
  assert.equal(pathQuality([p('a', 'b', { rttP50Ms: 12 }), p('b', 'a', { rttP50Ms: 40 })], 'a', 'b')!.rttMs, 12)
  assert.equal(pathQuality([p('b', 'a', { rttP50Ms: 40 })], 'a', 'b')!.reversed, true)
  assert.equal(pathQuality([p('a', 'b', { stale: true })], 'a', 'b'), undefined)
  assert.equal(pathQuality([p('a', 'b', { samples: 0 })], 'a', 'b'), undefined)
  assert.equal(lossBand(0.2), 'ok')
  assert.equal(lossBand(1), 'warn')
  assert.equal(lossBand(5), 'hot')
})

test('siteConnections: traffic per direction, measured round trip and loss beat declared', () => {
  const site = (id: string) => ({ id, orgId: DEFAULT_ORG, name: id, kind: 'datacenter', lat: 0, lng: 0, country: 'GR' }) as (typeof seed.sites)[number]
  const cl = (id: string, siteId: string) => ({ ...seed.clusters[0], id, siteId }) as (typeof seed.clusters)[number]
  const sv = (id: string, clusterId: string) => ({ ...seed.services[0], id, clusterId }) as (typeof seed.services)[number]
  const dep = (id: string, from: string, to: string, bps: number): Dependency => ({ id, orgId: DEFAULT_ORG, from, fromKind: 'service', to, toKind: 'service', sources: ['observed'], confidence: 'high', protocol: 'HTTP', stats: { bytesPerSec: bps, errorRate: 0, p95Ms: 1, windowSec: 60 } })
  const path: Path = { id: 'p', fromCluster: 'c1', fromName: 'c1', host: 'h', port: 1, toCluster: 'c2', source: 'observed', rttMinMs: 5, rttP50Ms: 9, rttP95Ms: 11, lossPct: 2, samples: 4, at: '2026-09-19T00:00:00Z' }
  const out = siteConnections(
    [site('s1'), site('s2')], [cl('c1', 's1'), cl('c2', 's2')], [sv('w1', 'c1'), sv('w2', 'c2')], [],
    [dep('d1', 'w1', 'w2', 1000), dep('d2', 'w2', 'w1', 300)],
    [{ a: 's1', b: 's2', rttMs: 80, source: 'declared' } as never], [path],
  )
  assert.equal(out.length, 1)
  const c = out[0]
  assert.equal(c.dependencies, 2)
  assert.equal(c.bpsAB, 1000)
  assert.equal(c.bpsBA, 300)
  assert.equal(c.rttMs, 9)
  assert.equal(c.measured, true)
  assert.equal(c.lossPct, 2)
  assert.equal(siteConnections([site('s1'), site('s2')], [cl('c1', 's1'), cl('c2', 's2')], [], [], [], [{ a: 's1', b: 's2', rttMs: 80, source: 'declared' } as never])[0].measured, false)
})

if (failed) {
  console.log(`${failed} failed`)
  process.exit(1)
}
