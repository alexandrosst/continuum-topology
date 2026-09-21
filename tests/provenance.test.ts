// Observation state, evidence and what may be a placement target: the pure rules behind the chips and the exclusion
// reasons. The server computes the states; these pin how the browser reads and words them.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { moveTargets } from '../src/lib/movability'
import { buildDecisionInput } from '../src/lib/placement/deciders'
import { recommend } from '../src/lib/placement/engine'
import { DEFAULT_POLICY } from '../src/lib/placement/types'
import { buildWorld, clusterStatus, excludedClusters, unverifiableClusters } from '../src/lib/placement/world'
import { ageLabel, evidenceLevel, evidenceRows, formatAttr, goneInfo, indexModel, needsEvidenceChip, observation, sourceLabel, targetStatus, weakAttributes, type ModelEntity } from '../src/lib/provenance'
import { DEFAULT_ORG, type Cluster, type Dependency, type MachineNode, type Service, type Site } from '../src/lib/types'

const NOW = Date.parse('2026-09-21T12:00:00Z')
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString()

test('ageLabel words an age the way the server does', () => {
  assert.equal(ageLabel(iso(45_000), NOW), '45 s')
  assert.equal(ageLabel(iso(12 * 60_000), NOW), '12 min')
  assert.equal(ageLabel(iso(2 * 3600_000), NOW), '2 h')
  assert.equal(ageLabel(iso(3 * 86400_000), NOW), '3 d')
  assert.equal(ageLabel(undefined, NOW), '')
  assert.equal(ageLabel('not a date', NOW), '')
})

test('observation: hand-typed records have no state; discovered ones do, and only live is actionable', () => {
  assert.equal(observation({ source: 'manual' }), undefined)
  assert.equal(observation(undefined), undefined)
  const live = observation({ source: 'discovered', state: 'live' })!
  assert.deepEqual([live.kind, live.label, live.actionable], ['live', 'live', true])
  const stale = observation({ source: 'discovered', state: 'stale', stateReason: 'stale for 2 h' })!
  assert.deepEqual([stale.kind, stale.label, stale.actionable, stale.reason], ['stale', 'stale 2 h', false, 'stale for 2 h'])
  const off = observation({ source: 'discovered', state: 'disconnected', stateReason: 'agent not connected' })!
  assert.deepEqual([off.label, off.actionable], ['disconnected', false])
  const revoked = observation({ source: 'discovered', state: 'revoked', stateReason: 'agent revoked 3 h ago' })!
  assert.deepEqual([revoked.label, revoked.actionable], ['revoked', false])
  // an older server that only sends the stale flag is still understood
  assert.equal(observation({ source: 'discovered', stale: true })!.kind, 'stale')
  assert.equal(observation({ source: 'discovered' })!.kind, 'live')
})

test('a record with deletedAt, or a tombstone, reads as gone', () => {
  const g = observation({ source: 'discovered', state: 'live', deletedAt: iso(1000) })!
  assert.deepEqual([g.kind, g.label, g.actionable], ['gone', 'gone', false])
  const t = goneInfo({ goneAt: iso(3 * 3600_000), reason: 'no longer reported by its agent' }, NOW)
  assert.equal(t.label, 'gone')
  assert.match(t.reason ?? '', /3 h ago/)
})

test('targetStatus: says why a cluster is not a target, and unknown capacity is not room', () => {
  assert.deepEqual(targetStatus({ source: 'manual' }, false), { eligible: true, state: 'declared' })
  assert.equal(targetStatus({ source: 'discovered', state: 'live' }, true).eligible, true)
  const s = targetStatus({ source: 'discovered', state: 'stale', stateReason: 'stale for 2 h' }, true)
  assert.deepEqual([s.eligible, s.reason], [false, 'stale for 2 h'])
  const r = targetStatus({ source: 'discovered', state: 'revoked', stateReason: 'agent revoked' }, true)
  assert.deepEqual([r.eligible, r.reason], [false, 'agent revoked'])
  const c = targetStatus({ source: 'discovered', state: 'live' }, false)
  assert.equal(c.eligible, false)
  assert.match(c.reason ?? '', /capacity unknown/)
})

test('evidence chips appear for guesses and unknowns, not for the normal case', () => {
  assert.equal(needsEvidenceChip('guess'), true)
  assert.equal(needsEvidenceChip('unknown'), true)
  assert.equal(needsEvidenceChip('reported'), false)
  assert.equal(needsEvidenceChip('measured'), false)
  assert.equal(evidenceLevel({ confidence: 'low' }), 'guess')
  assert.equal(evidenceLevel({ confidence: 'high' }, true), 'measured')
  assert.equal(evidenceLevel({ confidence: 'high' }), 'inferred')
})

const base = { orgId: DEFAULT_ORG, source: 'discovered' as const }
test('weakAttributes: guessed and unknown values of a discovered record; a person\'s override is not weak', () => {
  const rec = { ...base, id: 'n1', region: '', version: 'v1', evidence: { provider: { signal: 'node names look like EKS', confidence: 'low' } }, provider: 'aws' } as never
  const w = weakAttributes('cluster', rec)
  assert.deepEqual(w.map((x) => [x.field, x.level]), [['provider', 'guess'], ['region', 'unknown']])
  assert.match(w[0].why, /EKS/)
  const overridden = { ...(rec as object), overrides: { region: 'eu-west-1', provider: 'gcp' } } as never
  assert.deepEqual(weakAttributes('cluster', overridden), [])
  assert.deepEqual(weakAttributes('cluster', { ...(rec as object), source: 'manual' } as never), [])
  const svc = weakAttributes('service', { ...base, id: 's', cpuRequestM: undefined, memRequestMi: 128 } as never)
  assert.deepEqual(svc.map((x) => x.field), ['cpuRequestM'])
})

test('formatAttr: units are explicit and unknown is never a zero', () => {
  assert.equal(formatAttr({ value: 4, unit: 'cores', source: 'agent', confidence: 'reported', state: 'live' }), '4 cores')
  assert.equal(formatAttr({ value: 3 * 1024 ** 3, unit: 'bytes', source: 'agent', confidence: 'reported', state: 'live' }), '3 GiB')
  assert.equal(formatAttr({ value: 3, unit: 'count', source: 'agent', confidence: 'reported', state: 'live' }), '3')
  assert.equal(formatAttr({ value: null, source: 'agent', confidence: 'unknown', state: 'live' }), 'unknown')
  assert.equal(formatAttr({ value: 0, source: 'agent', confidence: 'unknown', state: 'live' }), 'unknown')
  assert.equal(formatAttr({ value: 0, unit: 'cores', source: 'agent', confidence: 'reported', state: 'live' }), '0 cores')
  assert.equal(formatAttr({ value: true, source: 'declared', confidence: 'reported', state: 'live' }), 'yes')
})

test('evidenceRows: weakest first, with source and time, shadowed observed values kept', () => {
  const e: ModelEntity = {
    kind: 'node', id: 'nd-1', name: 'w1', origin: 'observed+declared', state: 'live',
    attributes: {
      cpuAllocatable: { value: 8, unit: 'cores', source: 'agent', agentId: 'ag-1', confidence: 'reported', observedAt: '2026-09-21T11:59:00Z', state: 'live' },
      region: { value: null, source: 'agent', confidence: 'unknown', state: 'live', evidence: 'no node carries a region label' },
      provider: { value: 'aws', source: 'inferred', confidence: 'guess', state: 'live', evidence: 'node names' },
      zone: { value: 'a', source: 'declared', confidence: 'reported', state: 'live', shadowed: { value: 'b', source: 'agent', confidence: 'reported', state: 'live' } },
    },
  }
  const rows = evidenceRows(e, (id) => (id === 'ag-1' ? 'edge-agent' : undefined))
  assert.deepEqual(rows.map((r) => r.attribute), ['region', 'provider', 'cpuAllocatable', 'zone'])
  assert.equal(rows[0].value, 'unknown')
  assert.equal(rows[2].source, 'agent (edge-agent)')
  assert.equal(rows[2].unit, 'cores')
  assert.equal(rows[3].shadowed, 'b')
  assert.equal(sourceLabel({ source: 'declared' }), 'declared by a person')
  assert.deepEqual([...indexModel({ entities: [e] } as never).keys()], ['node|nd-1'])
  assert.deepEqual(evidenceRows(undefined), [])
})

/* ---------- a stale, disconnected or revoked cluster is never a target ---------- */

const site = (id: string): Site => ({ id, orgId: DEFAULT_ORG, name: id, kind: 'data-center', lat: 50, lng: 8, country: 'DE' })
const cluster = (id: string, over: Partial<Cluster> = {}): Cluster => ({
  orgId: DEFAULT_ORG, source: 'discovered', id, siteId: 's1', name: id, tier: 'cloud', distribution: 'k3s', version: 'v1.30', provider: '', region: '', status: 'healthy', labels: {}, state: 'live', ...over,
})
const node = (id: string, clusterId: string, over: Partial<MachineNode> = {}): MachineNode => ({
  orgId: DEFAULT_ORG, source: 'discovered', id, name: id, clusterId, role: 'worker', kind: 'vm', ip: '', os: '', cpu: 16, memoryGb: 32, status: 'healthy', labels: {},
  allocatable: { cpu: 16, memoryGb: 32 }, requested: { cpu: 2, memoryGb: 2 }, ...over,
})
const service = (id: string, clusterId: string): Service => ({
  orgId: DEFAULT_ORG, source: 'manual', id, name: id, namespace: 'app', clusterId, kind: 'Deployment', image: '', replicas: 1, nodeIds: [], status: 'healthy', labels: {}, cpuRequestM: 250, memRequestMi: 256,
})
const dep = (from: string, to: string): Dependency => ({ id: `${from}-${to}`, orgId: DEFAULT_ORG, from, fromKind: 'service', to, toKind: 'service', protocol: 'TCP', sources: ['observed'], confidence: 'high', stats: { bytesPerSec: 5e6 }, via: 'ebpf' })

function world(over: Record<string, Partial<Cluster>> = {}, nodes: MachineNode[] = [node('n-a', 'a'), node('n-b', 'b'), node('n-c', 'c')]) {
  return buildWorld({
    clusters: [cluster('a', over.a), cluster('b', over.b), cluster('c', over.c)],
    nodes,
    services: [service('api', 'a'), service('db', 'b')],
    devices: [],
    dependencies: [dep('api', 'db')],
    sites: [site('s1')],
    siteLinks: [],
    externalEndpoints: [],
    agents: [],
    paths: [],
  })
}

test('placement: live clusters with known capacity are targets; the others are excluded with their reason', () => {
  const w = world({ b: { state: 'stale', stale: true, stateReason: 'stale for 2 h' }, c: { state: 'revoked', stateReason: 'agent revoked 3 h ago' } })
  const ex = excludedClusters(w)
  assert.deepEqual(ex.map((x) => [x.cluster.id, x.status.state, x.status.reason]), [['b', 'stale', 'stale for 2 h'], ['c', 'revoked', 'agent revoked 3 h ago']])
  assert.equal(clusterStatus(w, 'a').eligible, true)

  const targets = moveTargets(w.byService.get('api')!, { clusters: w.clusters, nodes: w.nodes, services: w.services, devices: [], dependencies: w.deps, sites: w.sites })
  const t = new Map(targets.map((x) => [x.cluster.id, x]))
  assert.equal(t.has('a'), false) // its own cluster is not a "target"
  assert.equal(t.get('b')!.fits, false)
  assert.match(t.get('b')!.blockers.join(' '), /stale for 2 h/)
  assert.deepEqual(t.get('c')!.excluded, { state: 'revoked', reason: 'agent revoked 3 h ago' })
  assert.deepEqual(targets.map((x) => x.cluster.id), ['b', 'c'].sort((x, y) => x.localeCompare(y)))
})

test('placement: a cluster whose capacity is unknown is not excluded (an unknown is not a no): it is listed as "can\'t tell", apart from clusters left out for not being live', () => {
  const w = world({}, [node('n-a', 'a'), node('n-b', 'b')]) // no node reported for c
  const ex = excludedClusters(w)
  assert.deepEqual(ex.map((x) => x.cluster.id), []) // live, so not "excluded"
  const unsure = unverifiableClusters(w)
  assert.deepEqual(unsure.map((x) => x.cluster.id), ['c'])
  assert.equal(unsure[0].nothing, true)
  assert.match(unsure[0].why, /no nodes are known/)
  // it is still a live target (an unknown is not a no); it just cannot be certified to have room
  assert.equal(clusterStatus(w, 'c').capacityUnknown, true)
  assert.equal(clusterStatus(w, 'c').eligible, true)
})

test('placement: nothing is recommended for a service whose own cluster is not live, and a stale cluster is never proposed', () => {
  const w = world({ a: { state: 'disconnected', stateReason: 'agent not connected' } })
  assert.equal(recommend(w, DEFAULT_POLICY).recommendations.length, 0)
  const w2 = world({ b: { state: 'stale', stale: true, stateReason: 'stale for 1 d' } })
  for (const r of recommend(w2, DEFAULT_POLICY).recommendations) assert.notEqual(r.to, 'b')
})

test('decider input: additive fields say which clusters were left out and why', () => {
  const w = world({ b: { state: 'stale', stale: true, stateReason: 'stale for 2 h' } })
  const input = buildDecisionInput(w, DEFAULT_POLICY, new Date(NOW))
  assert.deepEqual(input.excluded, [{ cluster: 'b', name: 'b', state: 'stale', reason: 'stale for 2 h', kind: 'not-live' }])
  const b = input.clusters.find((c) => c.id === 'b')!
  assert.equal(b.eligible, false)
  assert.equal(input.clusters.find((c) => c.id === 'a')!.eligible, true)
  assert.equal(input.services.find((s) => s.id === 'db')!.clusterState, 'stale')
  for (const s of input.services) assert.equal(s.candidates.includes('b'), false)
})
