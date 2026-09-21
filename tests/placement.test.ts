import assert from 'node:assert/strict'
import { buildDecisionInput, BUILTIN_DECIDERS, compare, baselineDecider, externalDecider, parseDecisionOutput, runDecider, vet } from '../src/lib/placement/deciders'
import { evacuate, evaluate, recommend, totals, whatIf } from '../src/lib/placement/engine'
import { DEFAULT_POLICY, type Policy } from '../src/lib/placement/types'
import { buildWorld, freeCapacity, rtt, withMoves, type World } from '../src/lib/placement/world'
import { DEFAULT_ORG, type Cluster, type Dependency, type Device, type MachineNode, type Path, type Service, type Site, type SiteLink } from '../src/lib/types'

let failed = 0
const queue: { name: string; fn: () => void | Promise<void> }[] = []
const test = (name: string, fn: () => void | Promise<void>) => void queue.push({ name, fn })

const base = { orgId: DEFAULT_ORG, source: 'manual' as const }
const site = (id: string, name: string, lat: number, lng: number, extra: Partial<Site> = {}): Site => ({ id, orgId: DEFAULT_ORG, name, kind: 'data-center', lat, lng, country: 'DE', ...extra })
const cluster = (id: string, siteId: string, tier: Cluster['tier'], extra: Partial<Cluster> = {}): Cluster => ({
  ...base, id, siteId, name: id, tier, distribution: 'k3s', version: 'v1.30', provider: '', region: '', status: 'healthy', labels: {}, ...extra,
})
const node = (id: string, clusterId: string, cpu: number, requested: number, extra: Partial<MachineNode> = {}): MachineNode => ({
  ...base, id, name: id, clusterId, role: 'worker', kind: 'vm', ip: '', os: '', cpu, memoryGb: 16, status: 'healthy', labels: {},
  allocatable: { cpu, memoryGb: 16 }, requested: { cpu: requested, memoryGb: 2 }, ...extra,
})
const service = (id: string, clusterId: string, extra: Partial<Service> = {}): Service => ({
  ...base, id, name: id, namespace: 'app', clusterId, kind: 'Deployment', image: '', replicas: 2, nodeIds: [], status: 'healthy', labels: {}, cpuRequestM: 250, memRequestMi: 256, ...extra,
})
const dep = (id: string, from: string, to: string, bps?: number, extra: Partial<Dependency> = {}): Dependency => ({
  id, orgId: DEFAULT_ORG, from, fromKind: 'service', to, toKind: 'service', protocol: 'TCP', sources: bps === undefined ? ['declared'] : ['observed'], confidence: 'high',
  ...(bps === undefined ? {} : { stats: { bytesPerSec: bps }, via: 'ebpf' as const }), ...extra,
})

const SITES = [site('s-cloud', 'Frankfurt', 50.11, 8.68, { dataResidency: 'EU' }), site('s-edge', 'Athens', 37.98, 23.72, { dataResidency: 'EU' }), site('s-us', 'Virginia', 38.9, -77.4, { country: 'US', dataResidency: 'US' })]
const LINKS: SiteLink[] = [{ id: 'l1', orgId: DEFAULT_ORG, a: 's-edge', b: 's-cloud', rttMs: 40, source: 'declared' }]

function world(over: Partial<Parameters<typeof buildWorld>[0]> = {}): World {
  return buildWorld({
    clusters: [cluster('cl-cloud', 's-cloud', 'cloud'), cluster('cl-edge', 's-edge', 'edge'), cluster('cl-us', 's-us', 'cloud')],
    nodes: [node('n-cloud', 'cl-cloud', 32, 8), node('n-edge', 'cl-edge', 8, 2), node('n-us', 'cl-us', 16, 2)],
    services: [
      service('api', 'cl-edge'),
      service('db', 'cl-cloud', { kind: 'StatefulSet', volumes: [{ name: 'data', sizeGb: 20 }] }),
      service('cache', 'cl-edge'),
      service('worker', 'cl-cloud'),
    ],
    devices: [],
    dependencies: [dep('d-api-db', 'api', 'db', 4 * 1024 * 1024), dep('d-api-cache', 'api', 'cache', 200 * 1024), dep('d-worker-db', 'worker', 'db', 300 * 1024)],
    sites: SITES,
    siteLinks: LINKS,
    externalEndpoints: [],
    agents: [],
    paths: [],
    ...over,
  })
}

const P: Policy = DEFAULT_POLICY

/* ---------- round trips: the best evidence wins ---------- */

test('rtt: measured beats declared beats distance, and each is labelled', () => {
  const w = world()
  assert.deepEqual(rtt(w, 'cl-edge', { kind: 'cluster', id: 'cl-edge' }), { ms: 0.3, basis: 'same-cluster' })
  assert.deepEqual(rtt(w, 'cl-edge', { kind: 'cluster', id: 'cl-cloud' }), { ms: 40, basis: 'declared' })
  const us = rtt(w, 'cl-edge', { kind: 'cluster', id: 'cl-us' })
  assert.equal(us.basis, 'estimated')
  assert.ok(us.ms > 80 && us.ms < 250, `Athens to Virginia estimated at ${us.ms}`)
  const measured: Path = { id: 'p', fromCluster: 'cl-cloud', fromName: '', host: '10.0.0.1', port: 6443, toCluster: 'cl-edge', source: 'observed', rttMinMs: 10, rttP50Ms: 12, rttP95Ms: 20, lossPct: 0, samples: 5, at: '' }
  const wm = world({ paths: [measured] })
  assert.deepEqual(rtt(wm, 'cl-edge', { kind: 'cluster', id: 'cl-cloud' }), { ms: 12, basis: 'measured' }) // either direction
  // stale, empty and fully failing measurements are not trusted
  for (const bad of [{ ...measured, stale: true }, { ...measured, samples: 0 }, { ...measured, lossPct: 100 }]) {
    assert.equal(rtt(world({ paths: [bad] }), 'cl-edge', { kind: 'cluster', id: 'cl-cloud' }).basis, 'declared')
  }
  assert.equal(rtt(w, 'cl-edge', { kind: 'unknown' }).basis, 'unknown')
})

test('rtt: an outside address is timed from a cluster, or reached through one that timed it', () => {
  const p: Path = { id: 'p', fromCluster: 'cl-cloud', fromName: '', host: '198.51.100.9', port: 443, source: 'observed', rttMinMs: 1, rttP50Ms: 5, rttP95Ms: 9, lossPct: 0, samples: 5, at: '' }
  const w = world({ paths: [p] })
  assert.deepEqual(rtt(w, 'cl-cloud', { kind: 'external', host: '198.51.100.9', port: 443 }), { ms: 5, basis: 'measured' })
  const via = rtt(w, 'cl-edge', { kind: 'external', host: '198.51.100.9', port: 443 })
  assert.equal(via.basis, 'estimated')
  assert.equal(via.ms, 45) // 40 declared to the cloud + 5 measured from there
  assert.equal(rtt(w, 'cl-edge', { kind: 'external', host: '203.0.113.1' }).basis, 'unknown')
})

/* ---------- the recommendation ---------- */

test('recommend: a busy service moves next to what it talks to, with evidence, and the data-heavy peer stays', () => {
  const plan = recommend(world(), P)
  const api = plan.recommendations.find((r) => r.serviceId === 'api')!
  assert.ok(api, 'api should be recommended to move')
  assert.equal(api.to, 'cl-cloud')
  assert.ok(api.benefit > 30, `benefit ${api.benefit}`)
  assert.ok(api.reasons.some((r) => /crosses between sites now stays local/.test(r) && /db/.test(r)), api.reasons.join(' | '))
  assert.ok(api.reasons.some((r) => /round trip/.test(r)))
  assert.equal(api.target.crossSiteBps, 200 * 1024) // the cache stays behind at the edge
  assert.equal(api.current.crossSiteBps, 4 * 1024 * 1024 + 200 * 1024 * 0) // the cache is local now, the db is not
  // the database is stateful with a volume: moving it is careful, and copying 20 GB is a cost
  assert.ok(!plan.recommendations.some((r) => r.serviceId === 'db'))
  // the recommendation never claims to have applied anything
  assert.equal(world().placement.get('api'), 'cl-edge')
})

test('recommend: nothing to gain, or nothing known, means stay', () => {
  const w = world({ dependencies: [dep('d1', 'api', 'cache', 200 * 1024)] }) // api and cache already together
  assert.equal(recommend(w, P).recommendations.length, 0)
  const none = world({ dependencies: [] })
  assert.equal(recommend(none, P).recommendations.length, 0)
})

test('recommend: hard constraints are never traded for a better score', () => {
  // confidential EU data may not go to the US, even though the US cluster is "closest" to nothing here
  const w = world({
    services: [service('api', 'cl-edge', { sensitivity: 'confidential' }), service('db', 'cl-us', { volumes: undefined })],
    dependencies: [dep('d1', 'api', 'db', 8 * 1024 * 1024)],
  })
  const plan = recommend(w, P)
  assert.ok(!plan.recommendations.some((r) => r.serviceId === 'api' && r.to === 'cl-us'), 'moved EU confidential data to the US')
  const ev = evaluate(w, w.byService.get('api')!, 'cl-us', P)
  assert.equal(ev.fits, false)
  assert.ok(ev.blockers.some((b) => /data must stay in EU/.test(b)), ev.blockers.join('; '))
})

test('recommend: pinned services and non-movable kinds are skipped with the reason', () => {
  const w = world({
    services: [
      service('api', 'cl-edge', { nodeSelector: { 'kubernetes.io/hostname': 'n-edge' } }),
      service('db', 'cl-cloud'),
      service('agent', 'cl-edge', { kind: 'DaemonSet' }),
      service('migrate', 'cl-edge', { kind: 'Job' }),
    ],
    dependencies: [dep('d1', 'api', 'db', 8 * 1024 * 1024), dep('d2', 'agent', 'db', 8 * 1024 * 1024), dep('d3', 'migrate', 'db', 8 * 1024 * 1024)],
  })
  const plan = recommend(w, P)
  assert.deepEqual(plan.recommendations.filter((r) => r.serviceId !== 'db').map((r) => r.serviceId), [])
  const why = Object.fromEntries(plan.skipped.map((s) => [s.serviceId, s.why]))
  assert.match(why.api, /Pinned to the machine/)
  assert.match(why.agent, /DaemonSet/)
  assert.match(why.migrate, /run it again/)
})

test('recommend: capacity is a hard limit and a nearly full cluster costs more', () => {
  const tight = world({ nodes: [node('n-cloud', 'cl-cloud', 1, 0.9), node('n-edge', 'cl-edge', 8, 2), node('n-us', 'cl-us', 16, 2)] })
  const ev = evaluate(tight, tight.byService.get('api')!, 'cl-cloud', P)
  assert.equal(ev.fits, false)
  assert.ok(ev.blockers.some((b) => /not enough free CPU/.test(b)))
  // fits, but leaves the cluster at 90 %: the headroom cost shows up
  const snug = world({ nodes: [node('n-cloud', 'cl-cloud', 10, 8.5), node('n-edge', 'cl-edge', 8, 2), node('n-us', 'cl-us', 16, 2)] })
  const e2 = evaluate(snug, snug.byService.get('api')!, 'cl-cloud', P)
  assert.equal(e2.fits, true)
  assert.ok(e2.headroomCost > 0 && e2.utilAfter! > 0.85, `headroom ${e2.headroomCost}, util ${e2.utilAfter}`)
})

test('recommend: the policy changes the answer, and unmeasured traffic lowers confidence', () => {
  const w = world()
  // caring only about traffic volume makes the light edge to the cache irrelevant but the db call decisive
  const lat0: Policy = { ...P, latency: 0 }
  assert.equal(recommend(w, lat0).recommendations.find((r) => r.serviceId === 'api')?.to, 'cl-cloud')
  // copying data is expensive enough to keep the worker/db pair as it is
  const heavy: Policy = { ...P, migration: 1000 }
  assert.ok(!recommend(world({ services: [service('api', 'cl-cloud'), service('db', 'cl-edge', { kind: 'StatefulSet', volumes: [{ name: 'd', sizeGb: 50 }] })], dependencies: [dep('d', 'api', 'db', 4 * 1024 * 1024)] }), heavy).recommendations.some((r) => r.serviceId === 'db'))
  // declared-only dependencies: still a recommendation, but flagged as a guess
  const declared = world({ dependencies: [dep('d1', 'api', 'db')] })
  const r = recommend(declared, { ...P, minAbsolute: 1 }).recommendations.find((x) => x.serviceId === 'api')
  assert.ok(r, 'a declared dependency should still count')
  // (changed: an unmeasured busy-ness is a guess, and the weakest deciding input sets the level, so this is low, not medium)
  assert.equal(r!.confidence, 'low')
  assert.ok(r!.caveats.some((c) => /No traffic was measured/.test(c)))
})

/* ---------- what-if ---------- */

test('what-if: moves are judged in order, capacity follows them, and nothing is applied', () => {
  const w = world({ nodes: [node('n-cloud', 'cl-cloud', 2, 0.6), node('n-edge', 'cl-edge', 8, 2), node('n-us', 'cl-us', 16, 2)] })
  // 1.4 cores free in the cloud cluster: api (2 x 250m = 0.5) fits; then cache (0.5) fits, even allowing the reported free figure
  // to be 15 % too high (it is only certified when it fits at the pessimistic end: 0.9 x 0.85 = 0.77 >= 0.5)
  const r = whatIf(w, P, [{ serviceId: 'api', to: 'cl-cloud' }, { serviceId: 'cache', to: 'cl-cloud' }, { serviceId: 'worker', to: 'cl-edge' }])
  assert.equal(r.moves[0].fits, true)
  assert.equal(r.moves[1].fits, true)
  assert.ok(r.after.crossSiteBps < r.before.crossSiteBps)
  assert.ok(r.after.cost < r.before.cost)
  const cloud = r.loads.find((l) => l.clusterId === 'cl-cloud')!
  assert.ok(cloud.after! > cloud.before!, 'the cloud cluster should be fuller after taking two services')
  assert.equal(w.placement.get('api'), 'cl-edge')
  const full = whatIf(world({ nodes: [node('n-cloud', 'cl-cloud', 1, 0.4), node('n-edge', 'cl-edge', 8, 2), node('n-us', 'cl-us', 16, 2)] }), P, [{ serviceId: 'api', to: 'cl-cloud' }, { serviceId: 'cache', to: 'cl-cloud' }])
  assert.equal(full.moves[0].fits, true)
  assert.equal(full.moves[1].fits, false, 'the second move must see the capacity the first one used')
  assert.ok(full.warnings.some((x) => /cache cannot run at cl-cloud/.test(x)))
})

test('what-if: moving a service onto a bad cluster raises the cost, and a pinned one is warned about', () => {
  const w = world()
  const bad = whatIf(w, P, [{ serviceId: 'db', to: 'cl-us' }])
  assert.ok(bad.after.cost > bad.before.cost)
  const pinned = world({ services: [service('api', 'cl-edge', { nodeSelector: { 'kubernetes.io/hostname': 'n-edge' } }), service('db', 'cl-cloud'), service('cache', 'cl-edge'), service('worker', 'cl-cloud')] })
  assert.ok(whatIf(pinned, P, [{ serviceId: 'api', to: 'cl-cloud' }]).warnings.some((x) => /pinned/.test(x)))
  assert.equal(totals(withMoves(w, []), P).cost, totals(w, P).cost)
})

test('evacuate: services find new homes biggest first; what cannot move is reported as lost', () => {
  const w = world({ services: [...world().services, service('agent', 'cl-cloud', { kind: 'DaemonSet' }), service('pinned', 'cl-cloud', { volumes: [{ name: 'v', sizeGb: 1, pinnedNodeIds: ['n-cloud'] }] })] })
  const e = evacuate(w, P, 'cl-cloud')
  assert.ok(e.moves.length > 0 && e.moves.every((m) => m.to !== 'cl-cloud'))
  assert.ok(e.lost.some((l) => l.serviceId === 'agent'))
  assert.ok(e.lost.some((l) => l.serviceId === 'pinned' && /local volume/.test(l.why)))
  assert.ok(!e.moves.some((m) => m.serviceId === 'pinned' || m.serviceId === 'agent'))
  // capacity was counted: nothing is over-committed after the move
  assert.ok(e.result.loads.every((l) => l.after === undefined || l.after <= 1))
  assert.ok(freeCapacity(withMoves(w, e.moves), 'cl-cloud').cpu >= freeCapacity(w, 'cl-cloud').cpu)
})

test('devices count as places: a service is drawn towards the site of the device it talks to', () => {
  const cam: Device = { ...base, id: 'cam', name: 'Camera', kind: 'camera', count: 4, siteId: 's-edge', protocol: 'RTSP', connectivity: 'ethernet', status: 'healthy', labels: {} }
  const w = world({
    services: [service('vision', 'cl-cloud')],
    devices: [cam],
    dependencies: [{ id: 'd', orgId: DEFAULT_ORG, from: 'vision', fromKind: 'service', to: 'cam', toKind: 'device', protocol: 'RTSP', sources: ['observed'], confidence: 'high', stats: { bytesPerSec: 2 * 1024 * 1024 } }],
  })
  const r = recommend(w, P).recommendations[0]
  assert.equal(r.serviceId, 'vision')
  assert.equal(r.to, 'cl-edge')
})

/* ---------- deciders ---------- */

test('decision input: schema 1, ids only from the model, candidates already satisfy the constraints', () => {
  const input = buildDecisionInput(world(), P, new Date('2026-09-19T10:00:00Z'))
  assert.equal(input.schema, 1)
  assert.equal(input.question, 'placement')
  assert.equal(input.services.length, 4)
  const api = input.services.find((s) => s.id === 'api')!
  assert.deepEqual(api.candidates.sort(), ['cl-cloud', 'cl-us'])
  assert.equal(input.services.find((s) => s.id === 'db')!.volumesGb, 20)
  assert.ok(input.links.some((l) => l.from === 'cl-cloud' && l.to === 'cl-edge' && l.rttMs === 40 && l.basis === 'declared'))
  assert.equal(input.flows.length, 3)
  assert.doesNotThrow(() => JSON.stringify(input))
})

test('vet: an external decider cannot propose what the constraints forbid', () => {
  const w = world({ services: [service('api', 'cl-edge', { sensitivity: 'confidential' }), service('db', 'cl-cloud'), service('agent', 'cl-edge', { kind: 'DaemonSet' })], dependencies: [dep('d', 'api', 'db', 1024)] })
  const { kept, rejected } = vet(w, P, [
    { serviceId: 'api', to: 'cl-cloud' },
    { serviceId: 'api', to: 'cl-us' }, // second proposal for the same service
    { serviceId: 'ghost', to: 'cl-cloud' },
    { serviceId: 'db', to: 'nowhere' },
    { serviceId: 'db', to: 'cl-cloud' }, // already there
    { serviceId: 'agent', to: 'cl-cloud' },
  ])
  assert.deepEqual(kept.map((k) => `${k.serviceId}>${k.to}`), ['api>cl-cloud'])
  assert.equal(rejected.length, 5)
  const usApi = vet(w, P, [{ serviceId: 'api', to: 'cl-us' }]).rejected[0]
  assert.match(usApi.why, /cannot run there/)
})

test('parseDecisionOutput: only well-formed recommendations survive', () => {
  assert.throws(() => parseDecisionOutput(null))
  assert.throws(() => parseDecisionOutput({ recommendations: 'x' }))
  assert.throws(() => parseDecisionOutput({ recommendations: new Array(5001).fill({ serviceId: 'a', to: 'b' }) }))
  const out = parseDecisionOutput({ recommendations: [{ serviceId: 'a', to: 'b', reason: 'because' }, { serviceId: 'c', toCluster: 'd' }, { serviceId: 5, to: 'x' }, null, { serviceId: 'e' }, 'junk'] })
  assert.deepEqual(out, [{ serviceId: 'a', to: 'b', reason: 'because' }, { serviceId: 'c', to: 'd', reason: undefined }])
})

test('runDecider + compare: built-in and external deciders are scored by the same function', async () => {
  const w = world()
  const ext = externalDecider('Research policy', async () => ({ decider: 'x', result: { recommendations: [{ serviceId: 'api', to: 'cl-us', reason: 'cheap' }, { serviceId: 'cache', to: 'cl-cloud' }, { serviceId: 'ghost', to: 'cl-cloud' }] } }))
  const broken = externalDecider('Broken', async () => {
    throw new Error('the external decider could not be reached')
  }, 'broken')
  const results = await Promise.all([...BUILTIN_DECIDERS, ext, broken].map((d) => runDecider(d, w, P)))
  const [base, talker, ex, bad] = results
  assert.ok(base.ok && base.moves.some((m) => m.serviceId === 'api' && m.to === 'cl-cloud'))
  assert.ok(base.outcome!.after.cost < base.outcome!.before.cost)
  assert.ok(talker.ok)
  assert.equal(ex.rejected.length, 1) // "ghost"
  assert.equal(ex.moves.find((m) => m.serviceId === 'api')!.to, 'cl-us')
  assert.ok(ex.moves.find((m) => m.serviceId === 'api')!.benefit < base.moves.find((m) => m.serviceId === 'api')!.benefit, 'the far-away pick must score worse than the baseline pick')
  assert.equal(bad.ok, false)
  assert.match(bad.error!, /could not be reached/)
  const c = compare(w, results)
  const apiRow = c.rows.find((r) => r.serviceId === 'api')!
  assert.equal(apiRow.agree, false)
  assert.equal(apiRow.picks.get('baseline')!.to, 'cl-cloud')
  assert.equal(apiRow.picks.get('external')!.to, 'cl-us')
  assert.equal(c.rows[0].agree, false, 'disagreements sort first')
  assert.equal(c.totals.find((t) => t.deciderId === 'baseline')!.moves, base.moves.length)
  assert.equal(c.totals.find((t) => !t.ok)!.moves, 0)
  assert.equal(baselineDecider.kind, 'builtin')
})

for (const { name, fn } of queue) {
  try {
    await fn()
    console.log('PASS', name)
  } catch (e) {
    failed++
    console.log('FAIL', name, '\n  ', (e as Error).message)
  }
}
console.log(failed ? `\n${failed} FAILED` : '\nall passed')
process.exit(failed ? 1 : 0)
