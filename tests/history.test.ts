import assert from 'node:assert/strict'
import { ageOf, atSnapshot, normalizeSettings, normalizeSnapshot, parseEventRetention, pointAt, kindLabel, DEFAULT_SETTINGS } from '../src/lib/history'
import { DEFAULT_ORG, type Cluster, type Model, type Service } from '../src/lib/types'
import { seedTopology } from '../src/lib/seed'

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

const SEEN = '2026-09-19T09:00:00.000Z'
const prov = { orgId: DEFAULT_ORG, source: 'discovered' as const, lastSeen: SEEN }
const cl = (id: string, extra: Partial<Cluster> = {}): Cluster => ({ ...prov, id, name: id, tier: 'edge', distribution: 'k3s', version: 'v1', provider: '', region: '', status: 'healthy', labels: {}, ...extra })
const sv = (id: string, clusterId: string, extra: Partial<Service> = {}): Service => ({
  ...prov, id, name: id, namespace: 'app', clusterId, kind: 'Deployment', image: 'i:1', replicas: 1, nodeIds: [], status: 'healthy', labels: {}, ...extra,
})
const EMPTY: Model = { ...seedTopology(), clusters: [], nodes: [], namespaces: [], services: [], devices: [], dependencies: [], applications: [], sites: [], siteLinks: [], externalEndpoints: [], agents: [], suggestions: [], auditLog: [] }

test('normalizeSnapshot: a snapshot with missing lists still has every list', () => {
  const s = normalizeSnapshot({ at: SEEN, topology: { clusters: [cl('a')] } })
  assert.equal(s.topology.clusters.length, 1)
  for (const k of ['nodes', 'namespaces', 'services', 'dependencies', 'externalEndpoints', 'paths'] as const) assert.deepEqual(s.topology[k], [])
  assert.equal(normalizeSnapshot({ at: SEEN, topology: null }).topology.services.length, 0)
})

test('normalizeSettings: unknown or missing values fall back to defaults; targets are always a list', () => {
  assert.deepEqual(normalizeSettings(null), DEFAULT_SETTINGS)
  const s = normalizeSettings({ snapshotMinutes: 10 })
  assert.equal(s.snapshotMinutes, 10)
  assert.equal(s.consistencyMinutes, DEFAULT_SETTINGS.consistencyMinutes)
  assert.deepEqual(s.probeTargets, [])
  assert.equal(s.tombstoneRetentionDays, DEFAULT_SETTINGS.tombstoneRetentionDays)
  assert.equal(s.eventRetentionDays, DEFAULT_SETTINGS.eventRetentionDays)
})

test('parseEventRetention: off is always 0 and always valid, whatever is left in the box', () => {
  assert.deepEqual(parseEventRetention(false, ''), { days: 0, ok: true })
  assert.deepEqual(parseEventRetention(false, '30'), { days: 0, ok: true })
  assert.deepEqual(parseEventRetention(false, 'not a number'), { days: 0, ok: true })
})

test('parseEventRetention: on requires an integer within [7, 3650]', () => {
  assert.deepEqual(parseEventRetention(true, ''), { days: 0, ok: false })
  assert.deepEqual(parseEventRetention(true, '  '), { days: 0, ok: false })
  assert.equal(parseEventRetention(true, '6').ok, false)
  assert.equal(parseEventRetention(true, '3651').ok, false)
  assert.equal(parseEventRetention(true, '1.5').ok, false)
  assert.equal(parseEventRetention(true, 'abc').ok, false)
  assert.deepEqual(parseEventRetention(true, '7'), { days: 7, ok: true })
  assert.deepEqual(parseEventRetention(true, '3650'), { days: 3650, ok: true })
  assert.deepEqual(parseEventRetention(true, '90'), { days: 90, ok: true })
})

test('pointAt: the recording at or before a moment, else the first', () => {
  const pts = ['2026-09-19T09:00:00Z', '2026-09-19T09:05:00Z', '2026-09-19T09:10:00Z'].map((at) => ({ at, bytes: 1 }))
  assert.equal(pointAt(pts, '2026-09-19T09:07:00Z')?.at, pts[1].at)
  assert.equal(pointAt(pts, '2026-09-19T09:05:00Z')?.at, pts[1].at)
  assert.equal(pointAt(pts, '2026-09-19T08:00:00Z')?.at, pts[0].at)
  assert.equal(pointAt(pts, '2026-09-19T10:00:00Z')?.at, pts[2].at)
  assert.equal(pointAt([], SEEN), undefined)
})

test('ageOf: says it the way people do', () => {
  const now = Date.parse(SEEN)
  assert.equal(ageOf(SEEN, now + 30_000), 'a moment ago')
  assert.equal(ageOf(SEEN, now + 10 * 60_000), '10 minutes ago')
  assert.equal(ageOf(SEEN, now + 5 * 3600_000), '5 hours ago')
  assert.equal(ageOf(SEEN, now + 3 * 86400_000), '3 days ago')
})

test('kindLabel: known kinds are named in plain words, unknown ones are made readable', () => {
  assert.equal(kindLabel('service-migrated'), 'Service moved')
  assert.equal(kindLabel('drift'), 'Missed change found')
  assert.equal(kindLabel('some-new-kind'), 'some new kind')
})

test('atSnapshot: the past shows what agents discovered then, and what people own as it is now', () => {
  const now: Model = {
    ...EMPTY,
    clusters: [cl('a', { siteId: 'site-1', overrides: { name: 'Athens edge' } }), cl('b')],
    services: [sv('x', 'a', { replicas: 5, applicationId: 'app-1', overrides: { sensitivity: 'confidential' } }), sv('new', 'b')],
    dependencies: [
      { id: 'd1', orgId: DEFAULT_ORG, from: 'x', fromKind: 'service', to: 'new', toKind: 'service', protocol: 'TCP', sources: ['declared'], confidence: 'high' },
      { id: 'd2', orgId: DEFAULT_ORG, from: 'x', fromKind: 'service', to: 'gone', toKind: 'service', protocol: 'TCP', sources: ['declared'], confidence: 'high' },
    ],
  }
  const snap = normalizeSnapshot({ at: SEEN, topology: { clusters: [cl('a')], services: [sv('x', 'a', { replicas: 2 }), sv('gone', 'a')] } }).topology
  const past = atSnapshot(now, snap)
  // discovered facts come from the recording
  assert.equal(past.services.find((s) => s.id === 'x')!.replicas, 2)
  assert.ok(past.services.some((s) => s.id === 'gone'))
  assert.ok(!past.services.some((s) => s.id === 'new'), 'a service that did not exist yet is not shown')
  assert.ok(!past.clusters.some((c) => c.id === 'b'))
  // what people own is as it is now
  assert.deepEqual(past.services.find((s) => s.id === 'x')!.overrides, { sensitivity: 'confidential' })
  assert.equal(past.services.find((s) => s.id === 'x')!.applicationId, 'app-1')
  assert.equal(past.clusters.find((c) => c.id === 'a')!.siteId, 'site-1')
  assert.deepEqual(past.clusters.find((c) => c.id === 'a')!.overrides, { name: 'Athens edge' })
  // declared dependencies only between things that existed then
  assert.deepEqual(past.dependencies.map((d) => d.id), ['d2'])
  // the input is untouched
  assert.equal(now.services.length, 2)
})

test('atSnapshot: hand-made records are kept as they are, since a recording of agents cannot hold them', () => {
  const now: Model = { ...EMPTY, clusters: [cl('m', { source: 'manual' })] }
  const past = atSnapshot(now, normalizeSnapshot({ at: SEEN }).topology)
  assert.deepEqual(past.clusters.map((c) => c.id), ['m'])
})

console.log(failed ? `\n${failed} FAILED` : '\nall passed')
process.exit(failed ? 1 : 0)
