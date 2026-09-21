import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { completeness } from '../src/lib/completeness'
import { mergeDiscovered, normalizeServerState, type ServerAgent, type ServerState } from '../src/lib/discovered'
import { applyEdit, applyEffective, effective } from '../src/lib/effective'
import { normalize, upgrade } from '../src/lib/migrate'
import { accelSummary, ageLabel, autoscalerRange, countryName, disruptionLabel, ipScope, podsLabel, podsPercent, distroKey, formatCpu, formatMemory, loadBand, placeLabel, providerKey, requestedPercent, shortVersion } from '../src/lib/present'
import { buildMapSites, clampPan, dominantTier, exitIps, groupByProximity, groupLabel, siteConnections, unplacedClusters, worstStatus } from '../src/lib/geo'
import { buildIndex, countryAt, countryShapes, derivePlacementSuggestions, distanceKm, findCities, fold, nearestCity, parseCities, placementCandidates, siteFromCandidate, siteLocationIssue } from '../src/lib/places'
import { EXONYMS } from '../src/data/exonyms'
import { moveTargets, movability, type MoveModel } from '../src/lib/movability'
import { buildSearchIndex, parseSel, searchItems } from '../src/lib/search'
import { activeView, describeView, sameView, viewParams } from '../src/lib/views'
import { emptyScope, scopeProblems, splitNames, withFlowObserver, withMeasurements, withNodeProbe, withScope } from '../src/lib/install'
import { anyMesh, connectionVerdict } from '../src/lib/mesh'
import { ago, bytesPerSec, bytesTotal, isObserved, trafficSummary, withObserved } from '../src/lib/observed'
import { buildGraph } from '../src/lib/graph'
import { seedTopology } from '../src/lib/seed'
import { applySuggestion } from '../src/lib/suggestions'
import { DEFAULT_ORG, SCHEMA_VERSION, type Cluster, type ClusterMesh, type Dependency, type Device, type ExternalEndpoint, type Model, type Service, type Suggestion } from '../src/lib/types'

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

const SEEN = '2026-09-19T08:00:00.000Z'
const seed = seedTopology()
const discovered = seed.clusters.find((c) => c.id === 'cl-edge-a')!
const manual = seed.clusters.find((c) => c.id === 'cl-cloud')!

test('seed is internally consistent and normalizes to itself', () => {
  const n = normalize(seed)
  assert.equal(n.clusters.length, seed.clusters.length)
  assert.equal(n.services.length, seed.services.length)
  assert.equal(n.dependencies.length, seed.dependencies.length)
  assert.equal(n.sites.length, 4)
  assert.equal(n.applications.length, 3)
  assert.equal(n.devices.length, seed.devices.length)
  assert.equal(n.agents.length, 3)
  assert.equal(n.siteLinks.length, 3)
  assert.ok(n.namespaces.length > 0)
})

test('seed: schema version is 4 and dependency endpoints point at real things', () => {
  assert.equal(SCHEMA_VERSION, 4)
  const ids = { service: new Set(seed.services.map((x) => x.id)), device: new Set(seed.devices.map((x) => x.id)), external: new Set(seed.externalEndpoints.map((x) => x.id)) }
  for (const d of seed.dependencies) {
    assert.ok(ids[d.fromKind].has(d.from), `${d.id} from`)
    assert.ok(ids[d.toKind].has(d.to), `${d.id} to`)
  }
  assert.ok(seed.dependencies.some((d) => d.fromKind === 'device') && seed.dependencies.some((d) => d.toKind === 'device'))
})

test('effective(): overrides win over detected values, base is untouched', () => {
  const c = seed.clusters.find((x) => x.id === 'cl-edge-b')!
  assert.equal(c.region, 'Thessaloniki')
  assert.equal(effective(c).region, 'Thessaloniki lab')
  assert.equal(effective(manual), manual) // no overrides: same object, no copy
})

test('applyEdit(): editing a discovered entity stores only changed fields as overrides', () => {
  const saved = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ', version: 'v9' })
  assert.deepEqual(saved.overrides, { region: 'Patras HQ', version: 'v9' })
  assert.equal(saved.region, discovered.region) // detected value preserved
  assert.equal(saved.version, discovered.version)
})

test('applyEdit(): editing a field back to the detected value drops its override', () => {
  const once = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ', version: 'v9' })
  const back = applyEdit<Cluster>(once, { ...effective(once), region: discovered.region })
  assert.deepEqual(back.overrides, { version: 'v9' })
  const none = applyEdit<Cluster>(back, { ...effective(back), version: discovered.version })
  assert.equal(none.overrides, undefined)
})

test('applyEdit(): rediscovery updating base values keeps human overrides on top', () => {
  const edited = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ' })
  const rediscovered: Cluster = { ...edited, region: 'Patras (agent says)', version: 'v1.31.0+k3s1' }
  const shown = effective(rediscovered)
  assert.equal(shown.region, 'Patras HQ') // human edit survives
  assert.equal(shown.version, 'v1.31.0+k3s1') // new detected value flows through
})

test('applyEdit(): manual entities just take the edit (no overrides)', () => {
  const saved = applyEdit<Cluster>(manual, { ...manual, name: 'renamed' })
  assert.equal(saved.name, 'renamed')
  assert.equal(saved.overrides, undefined)
})

test('applyEdit(): bookkeeping fields can never be overridden', () => {
  const saved = applyEdit<Cluster>(discovered, { ...discovered, source: 'manual', orgId: 'evil', id: 'x' })
  assert.equal(saved.overrides, undefined)
  assert.equal(saved.source, 'discovered')
  assert.equal(saved.id, discovered.id)
})

test('upgrade(): v1 file (workloads, no org ids, plain dependencies) becomes v3', () => {
  const v1 = {
    clusters: [{ id: 'c1', name: 'a', tier: 'cloud' }],
    nodes: [{ id: 'n1', name: 'n', clusterId: 'c1' }],
    workloads: [{ id: 's1', name: 'svc', clusterId: 'c1', nodeIds: ['n1'] }],
    dependencies: [{ id: 'd1', from: 's1', to: 's1', protocol: 'HTTP' }],
  }
  const t = upgrade(v1)
  assert.equal(t.services.length, 1)
  assert.equal(t.services[0].orgId, DEFAULT_ORG)
  assert.equal(t.services[0].source, 'manual')
  assert.deepEqual(t.dependencies[0].sources, ['manual'])
  assert.equal(t.dependencies[0].confidence, 'high')
  assert.deepEqual([t.applications, t.sites, t.externalEndpoints], [[], [], []])
})

test('upgrade(): v2 file becomes v3 (new collections empty, dependencies get endpoint kinds)', () => {
  const v2 = {
    schemaVersion: 2,
    clusters: [{ id: 'c1', orgId: 'default', source: 'manual', name: 'a', tier: 'cloud', labels: {} }],
    nodes: [],
    services: [{ id: 's1', orgId: 'default', source: 'manual', name: 'svc', clusterId: 'c1', nodeIds: [], labels: {} }],
    dependencies: [{ id: 'd1', orgId: 'default', from: 's1', to: 's1', protocol: 'HTTP', sources: ['declared'], confidence: 'medium' }],
    applications: [],
    sites: [],
    externalEndpoints: [],
  }
  const t = upgrade(v2)
  assert.deepEqual([t.namespaces, t.devices, t.siteLinks, t.agents, t.suggestions, t.auditLog], [[], [], [], [], [], []])
  assert.equal(t.dependencies[0].fromKind, 'service')
  assert.equal(t.dependencies[0].toKind, 'service')
  assert.deepEqual(t.dependencies[0].sources, ['declared']) // existing values are kept
  assert.equal(normalize(v2).dependencies.length, 1)
})

test('normalize(): devices lose dangling site/application/node links, keep valid ones', () => {
  const dv: Device = { ...seed.devices[0], siteId: 'ghost', applicationId: 'ghost', gatewayNodeId: 'ghost' }
  const good = seed.devices[1]
  const t = normalize({ ...seed, devices: [dv, good] })
  assert.equal(t.devices[0].siteId, undefined)
  assert.equal(t.devices[0].applicationId, undefined)
  assert.equal(t.devices[0].gatewayNodeId, undefined)
  assert.equal(t.devices[1].siteId, good.siteId)
  assert.equal(t.devices[1].gatewayNodeId, good.gatewayNodeId)
})

test('normalize(): dependencies must match the kind of their ends', () => {
  const t = normalize({
    ...seed,
    dependencies: [
      { ...seed.dependencies.find((d) => d.fromKind === 'device')! }, // valid
      { ...seed.dependencies.find((d) => d.fromKind === 'device')!, id: 'x1', fromKind: 'service' }, // device id claimed to be a service
      { ...seed.dependencies.find((d) => d.fromKind === 'device')!, id: 'x2', from: 'ghost-device' },
    ],
  })
  assert.deepEqual(t.dependencies.map((d) => d.id), [seed.dependencies.find((d) => d.fromKind === 'device')!.id])
})

test('normalize(): site links need two different, existing sites', () => {
  const l = seed.siteLinks[0]
  const t = normalize({ ...seed, siteLinks: [l, { ...l, id: 'g1', b: 'ghost' }, { ...l, id: 'g2', b: l.a }] })
  assert.deepEqual(t.siteLinks.map((x) => x.id), [l.id])
})

test('applyEffective(): tombstoned records disappear from the view and take their dependencies with them', () => {
  const gone = { ...seed, devices: seed.devices.map((d) => (d.id === 'dev-cam-a' ? { ...d, deletedAt: SEEN } : d)) }
  const v = applyEffective(gone)
  assert.ok(!v.devices.some((d) => d.id === 'dev-cam-a'))
  assert.ok(!v.dependencies.some((d) => d.from === 'dev-cam-a'))
  assert.equal(v.dependencies.length, seed.dependencies.length - 1)
  // the stored record is untouched: history is kept
  assert.equal(gone.devices.length, seed.devices.length)
})

test('applyEdit(): discovery bookkeeping (evidence, agent, key, stale…) can never be overridden', () => {
  const node = seed.nodes.find((n) => n.id === 'n-a2')!
  assert.ok(node.evidence?.kind && node.key)
  const saved = applyEdit(node, { ...node, evidence: {}, agentId: 'evil', key: 'evil', stale: true, deletedAt: SEEN, revision: 999 })
  assert.equal(saved.overrides, undefined)
})

test('applySuggestion(): add-device and add-external change the model; informational ones do not', () => {
  const dev = seed.suggestions.find((x) => x.id === 'sg-door')!
  const ext = seed.suggestions.find((x) => x.id === 'sg-weather')!
  const info = seed.suggestions.find((x) => x.id === 'sg-volos')!
  const a = applySuggestion(seed, dev)
  assert.equal(a.devices!.length, seed.devices.length + 1)
  const b = applySuggestion(seed, ext)
  assert.equal(b.externalEndpoints!.length, 1)
  assert.equal(b.dependencies!.length, seed.dependencies.length + 1)
  assert.deepEqual(applySuggestion(seed, info), {})
  // applying twice does not duplicate
  assert.equal(applySuggestion({ ...seed, ...a }, dev).devices!.length, seed.devices.length + 1)
  // the result is a consistent model
  assert.equal(normalize({ ...seed, ...a, ...b }).dependencies.length, seed.dependencies.length + 1)
})

test('applySuggestion(): setting a discovered cluster\'s site is stored as an override', () => {
  const s = { ...seed.suggestions[0], apply: { type: 'set-cluster-site' as const, clusterId: 'cl-edge-a', siteId: 'site-ath' } }
  const c = applySuggestion(seed, s).clusters!.find((x) => x.id === 'cl-edge-a')!
  assert.equal(c.siteId, 'site-pat') // detected value kept
  assert.deepEqual(c.overrides, { siteId: 'site-ath' })
  assert.equal(effective(c).siteId, 'site-ath')
  const app = { ...seed.suggestions[0], apply: { type: 'set-service-application' as const, serviceIds: ['w-gw'], applicationId: 'app-ml' } }
  assert.equal(applySuggestion(seed, app).services!.find((x) => x.id === 'w-gw')!.applicationId, 'app-ml') // manual entity: base value
})

test('upgrade(): rejects non-objects, missing arrays and future schema versions', () => {
  assert.throws(() => upgrade(null), /not a JSON object/)
  assert.throws(() => upgrade({ clusters: 1 }), /Missing or invalid "clusters"/)
  assert.throws(() => upgrade({ ...seed, schemaVersion: SCHEMA_VERSION + 1 }), /newer than this app/)
})

test('normalize(): clears dangling siteId/applicationId and drops orphans', () => {
  const t = normalize({
    ...seed,
    clusters: [{ ...manual, siteId: 'ghost-site' }, ...seed.clusters.slice(1)],
    services: [
      { ...seed.services[0], applicationId: 'ghost-app', nodeIds: [...seed.services[0].nodeIds, 'ghost-node'] },
      { ...seed.services[1], clusterId: 'ghost-cluster' },
    ],
    dependencies: [{ ...seed.dependencies[0], to: 'ghost-service' }],
  })
  assert.equal(t.clusters[0].siteId, undefined)
  assert.equal(t.services.length, 1)
  assert.equal(t.services[0].applicationId, undefined)
  assert.ok(!t.services[0].nodeIds.includes('ghost-node'))
  assert.equal(t.dependencies.length, 0)
})

test('completeness(): reports which layers are known, never errors', () => {
  const empty: Cluster = { ...manual, id: 'empty' }
  assert.equal(completeness(empty, seed.nodes, seed.services, seed.dependencies).label, 'Nothing discovered yet')
  const infraOnly = completeness(manual, seed.nodes, [], [])
  assert.deepEqual([infraOnly.infra, infraOnly.services, infraOnly.dependencies], [true, false, false])
  assert.equal(infraOnly.label, 'Infrastructure, no services or dependencies')
  const full = completeness(manual, seed.nodes, seed.services, seed.dependencies)
  assert.equal(full.label, 'Infrastructure + services + dependencies')
})


/* ---------- server sync ---------- */

const EMPTY_MODEL: Model = { ...seedTopology(), clusters: [], nodes: [], namespaces: [], services: [], devices: [], dependencies: [], applications: [], sites: [], siteLinks: [], externalEndpoints: [], agents: [], suggestions: [], auditLog: [] }
const prov = { orgId: 'default', source: 'discovered' as const, agentId: 'ag-real', lastSeen: SEEN }
const srvAgent = (over: Partial<ServerAgent> = {}): ServerAgent => ({
  id: 'ag-real', orgId: 'default', name: 'real', clusterId: 'cl-real', version: '0.1.0', accessTier: 2, installedTier: 2, tierCap: 2, status: 'approved',
  fingerprint: 'd4e914e4-bc6c', requestedAt: SEEN, connected: true, synced: true, modules: [{ name: 'infrastructure', status: 'ok' }], ...over,
})
const realService = (id: string, over: Partial<Service> = {}): Service => ({
  ...prov, id, name: id, namespace: 'shop', clusterId: 'cl-real', kind: 'Deployment', image: 'img:1', replicas: 1, nodeIds: [], status: 'healthy', labels: {}, applicationHint: 'app-shop', ...over,
})
const realCluster: Cluster = { ...prov, id: 'cl-real', name: 'real', tier: 'edge', distribution: 'k3s', version: 'v1.30.5+k3s1', provider: 'On-prem', region: '', status: 'healthy', labels: {} }
const appSug = (services: string[]): Suggestion => ({
  id: 'sg-app-app-shop-cl-real', orgId: 'default', kind: 'application', title: 'Group shop', detail: 'from helm', agentId: 'ag-real', createdAt: SEEN, status: 'open',
  apply: { type: 'create-application', application: { ...prov, id: 'app-shop', name: 'shop', description: '', origin: 'helm', confidence: 'high' }, serviceIds: services },
})
const doc = (over: Partial<ServerState['topology']> = {}, agents: ServerAgent[] = [srvAgent()], auditLog: ServerState['auditLog'] = []): ServerState => ({
  generatedAt: '2026-09-19T09:00:00.000Z', agents,
  topology: { clusters: [realCluster], nodes: [], namespaces: [], services: [realService('sv-a'), realService('sv-b')], suggestions: [appSug(['sv-a', 'sv-b'])], ...over }, auditLog,
})
const apply = (m: Model, d: ServerState): Model => ({ ...m, ...mergeDiscovered(m, d) })

test('server sync: new records and agents arrive, sample agents that the server does not know stay', () => {
  const m = apply({ ...EMPTY_MODEL, agents: seed.agents }, doc())
  assert.equal(m.clusters.length, 1)
  assert.equal(m.services.length, 2)
  assert.equal(m.agents.length, seed.agents.length + 1)
  const a = m.agents.find((x) => x.id === 'ag-real')!
  assert.equal(a.status, 'approved')
  assert.equal(a.installedTier, 2)
  assert.equal(a.connected, true)
})

test('server sync: rediscovery updates detected values and never erases a human override', () => {
  let m = apply(EMPTY_MODEL, doc())
  const edited = applyEdit(m.services.find((s) => s.id === 'sv-a'), { ...m.services.find((s) => s.id === 'sv-a')!, sensitivity: 'confidential', replicas: 5 })
  m = { ...m, services: m.services.map((s) => (s.id === 'sv-a' ? edited : s)) }
  m = apply(m, doc({ services: [realService('sv-a', { replicas: 2, image: 'img:2' }), realService('sv-b')] }))
  const sv = m.services.find((s) => s.id === 'sv-a')!
  assert.equal(sv.image, 'img:2') // detected value follows the cluster
  assert.equal(sv.replicas, 2) // base is updated…
  const eff = effective(sv)
  assert.equal(eff.replicas, 5) // …but the person's value is what is shown
  assert.equal(eff.sensitivity, 'confidential')
})

test('server sync: a record the agent stops reporting is tombstoned, and comes back if it returns', () => {
  let m = apply(EMPTY_MODEL, doc())
  m = apply(m, doc({ services: [realService('sv-a')] }))
  assert.ok(m.services.find((s) => s.id === 'sv-b')!.deletedAt)
  assert.equal(applyEffective(m).services.length, 1)
  m = apply(m, doc())
  assert.equal(m.services.find((s) => s.id === 'sv-b')!.deletedAt, undefined)
})

test('server sync: an empty or unsynced server never wipes records', () => {
  const m0 = apply(EMPTY_MODEL, doc())
  const unsynced = apply(m0, doc({ clusters: [], nodes: [], services: [], suggestions: [] }, [srvAgent({ synced: false })]))
  assert.equal(unsynced.services.filter((s) => s.deletedAt).length, 0)
  assert.equal(unsynced.clusters.filter((s) => s.deletedAt).length, 0)
  const noAgents = apply(m0, doc({ clusters: [], services: [], suggestions: [] }, []))
  assert.equal(noAgents.services.filter((s) => s.deletedAt).length, 0)
})

test('server sync: hand-made records and other agents\' records are untouched', () => {
  const m = apply({ ...seed }, doc())
  for (const c of seed.clusters) assert.deepEqual(m.clusters.find((x) => x.id === c.id), c)
  assert.equal(m.services.filter((s) => s.deletedAt).length, 0)
})

test('server sync: suggestions arrive open, a decision is never reopened, and no-longer-made ones vanish', () => {
  let m = apply(EMPTY_MODEL, doc())
  assert.equal(m.suggestions.filter((s) => s.status === 'open').length, 1)
  m = { ...m, suggestions: m.suggestions.map((s) => ({ ...s, status: 'dismissed' as const })) }
  m = apply(m, doc())
  assert.equal(m.suggestions.length, 1)
  assert.equal(m.suggestions[0].status, 'dismissed')
  let n = apply(EMPTY_MODEL, doc())
  n = apply(n, doc({ suggestions: [] }))
  assert.equal(n.suggestions.length, 0)
})

test('accepting an application suggestion creates it and groups the services as overrides', () => {
  let m = apply(EMPTY_MODEL, doc())
  const s = m.suggestions[0]
  m = { ...m, ...applySuggestion(m, s) }
  assert.equal(m.applications.length, 1)
  assert.equal(m.applications[0].name, 'shop')
  assert.equal(m.applications[0].source, 'discovered')
  const eff = applyEffective(m)
  assert.deepEqual(eff.services.map((x) => x.applicationId), ['app-shop', 'app-shop'])
  assert.equal(m.services[0].applicationId, undefined, 'the detected base stays clean; the grouping is the person\'s decision')
})

test('after accepting, later merges keep the grouping, auto-place new members and do not re-suggest', () => {
  let m = apply(EMPTY_MODEL, doc())
  m = { ...m, ...applySuggestion(m, m.suggestions[0]), suggestions: m.suggestions.map((x) => ({ ...x, status: 'accepted' as const })) }
  // a third service shows up, hinted at the same application
  m = apply(m, doc({ services: [realService('sv-a'), realService('sv-b'), realService('sv-c')], suggestions: [appSug(['sv-a', 'sv-b', 'sv-c'])] }))
  const eff = applyEffective(m)
  assert.equal(eff.services.find((x) => x.id === 'sv-c')!.applicationId, 'app-shop')
  assert.equal(eff.services.find((x) => x.id === 'sv-a')!.applicationId, 'app-shop')
  assert.equal(m.suggestions.length, 1)
  // …and a brand new suggestion for an already-grouped application is not added at all
  const again = apply({ ...m, suggestions: [] }, doc({ services: [realService('sv-a'), realService('sv-b')], suggestions: [appSug(['sv-a', 'sv-b'])] }))
  assert.equal(again.suggestions.length, 0)
})

test('server sync: audit events are appended once', () => {
  const ev = { id: 'au-1', orgId: 'default', at: SEEN, actor: 'admin', action: 'agent-approved', targetKind: 'agent', targetId: 'ag-real' }
  let m = apply({ ...EMPTY_MODEL, auditLog: [{ ...ev, id: 'ev-local', actor: 'you' }] }, doc({}, [srvAgent()], [ev]))
  m = apply(m, doc({}, [srvAgent()], [ev]))
  assert.deepEqual(m.auditLog.map((e) => e.id), ['ev-local', 'au-1'])
})

test('server sync: statuses and reasons map onto agents, including rejected', () => {
  const m = apply(EMPTY_MODEL, doc({}, [srvAgent({ status: 'rejected', reason: 'not ours', synced: false, connected: false })]))
  const a = m.agents.find((x) => x.id === 'ag-real')!
  assert.equal(a.status, 'rejected')
  assert.equal(a.reason, 'not ours')
})

test('places read as "City, Country" and degrade gracefully', () => {
  assert.equal(countryName('gr'), 'Greece')
  assert.equal(countryName('ZZ'), 'ZZ', 'an unknown code is shown as it is, never dropped')
  assert.equal(countryName(''), '')
  assert.equal(placeLabel({ city: 'Athens', country: 'GR' }), 'Athens, Greece')
  assert.equal(placeLabel({ city: ' Patras ', country: 'GR' }), 'Patras, Greece')
  assert.equal(placeLabel({ country: 'DE' }), 'Germany')
  assert.equal(placeLabel({ city: 'Volos', country: '' }), 'Volos')
  assert.equal(placeLabel(undefined), '')
  const fra = seed.sites.find((x) => x.id === 'site-fra')!
  assert.equal(placeLabel(fra), 'Frankfurt, Germany')
})

test('distribution and provider text map to a known logo, and unknown text falls back safely', () => {
  const d: [string, string][] = [['EKS', 'eks'], ['Amazon EKS', 'eks'], ['GKE', 'gke'], ['AKS', 'aks'], ['k3s', 'k3s'], ['K3S v1.29', 'k3s'], ['RKE2', 'rke2'], ['Rancher', 'rke2'],
    ['OpenShift', 'openshift'], ['MicroK8s', 'microk8s'], ['Talos', 'talos'], ['k0s', 'k0s'], ['kind', 'kind'], ['kubeadm', 'kubeadm'], ['something homegrown', 'kubernetes'], ['', 'kubernetes']]
  for (const [text, key] of d) assert.equal(distroKey(text), key, text)
  const p: [string, string][] = [['AWS', 'aws'], ['Amazon Web Services', 'aws'], ['Azure', 'azure'], ['GCP', 'gcp'], ['Google Cloud', 'gcp'], ['Hetzner', 'hetzner'], ['On-prem', 'on-prem'],
    ['bare metal', 'on-prem'], ['Edge', 'edge'], ['Contoso Cloud', 'unknown'], ['', 'unknown']]
  for (const [text, key] of p) assert.equal(providerKey(text), key, text)
})

test('resources are written for people: units, TB, MB, and how much is already requested', () => {
  assert.equal(formatMemory(16), '16 GB')
  assert.equal(formatMemory(7.6), '7.6 GB')
  assert.equal(formatMemory(0.5), '512 MB')
  assert.equal(formatMemory(1536), '1.5 TB')
  assert.equal(formatMemory(0), '0 GB')
  assert.equal(formatCpu(4), '4')
  assert.equal(formatCpu(0.5), '0.5')
  assert.deepEqual(requestedPercent({ cpu: 3, memoryGb: 6 }, { cpu: 6, memoryGb: 8 }), { cpu: 50, memory: 75 })
  assert.deepEqual(requestedPercent(undefined, { cpu: 6, memoryGb: 8 }), { cpu: undefined, memory: undefined }, 'unknown stays unknown, not 0%')
  assert.deepEqual(requestedPercent({ cpu: 1, memoryGb: 1 }, { cpu: 0, memoryGb: 0 }), { cpu: undefined, memory: undefined })
  assert.equal(requestedPercent({ cpu: 9, memoryGb: 1 }, { cpu: 6, memoryGb: 8 }).cpu, 100, 'over-committed nodes cap at 100')
  assert.equal(loadBand(50), 'ok')
  assert.equal(loadBand(70), 'warn')
  assert.equal(loadBand(94), 'hot')
  assert.equal(shortVersion('v1.29.6+k3s1'), 'v1.29.6')
  assert.equal(shortVersion(undefined), '')
  assert.equal(accelSummary([{ vendor: 'NVIDIA', model: 'A100', count: 2 }]), '2× NVIDIA A100')
  assert.equal(accelSummary(undefined), '')
})

test('a site typed with a lowercase country code is normalized when loaded', () => {
  const n = normalize({ ...seed, sites: [{ ...seed.sites[0], country: ' gr ' }] })
  assert.equal(n.sites[0].country, 'GR')
})

test('IP scope: private, public, shared, loopback, link-local, IPv6 and non-addresses', () => {
  const cases: Record<string, string> = {
    '10.0.0.5': 'private', '172.16.0.1': 'private', '172.31.255.1': 'private', '172.32.0.1': 'public', '192.168.1.1': 'private',
    '100.64.0.1': 'shared', '100.127.255.1': 'shared', '100.128.0.1': 'public', '127.0.0.1': 'loopback', '169.254.1.1': 'link-local',
    '203.0.113.24': 'public', '8.8.8.8': 'public', '224.0.0.1': 'reserved', '0.0.0.0': 'reserved', '10.0.0.5:6443': 'private', '8.8.8.8:443': 'public',
    '::1': 'loopback', 'fd12:3456::1': 'private', 'fe80::1%eth0': 'link-local', '2001:db8::1': 'public', '[2606:4700::1]:443': 'public', '::ffff:10.1.2.3': 'private', 'ff02::1': 'reserved',
    '999.1.1.1': 'unknown', 'example.org': 'unknown', 'abc.def.eks.amazonaws.com': 'unknown', '': 'unknown',
  }
  for (const [ip, want] of Object.entries(cases)) assert.equal(ipScope(ip), want, ip)
  assert.equal(ipScope(undefined), 'unknown')
})

test('age, pod and scaling labels', () => {
  const now = Date.parse('2026-09-19T12:00:00Z')
  assert.equal(ageLabel('2026-09-19T11:59:30Z', now), 'just now')
  assert.equal(ageLabel('2026-09-19T09:00:00Z', now), '3 hours')
  assert.equal(ageLabel('2026-09-18T11:00:00Z', now), '1 day')
  assert.equal(ageLabel('2026-06-19T12:00:00Z', now), '3 months')
  assert.equal(ageLabel('2024-03-01T12:00:00Z', now), '2 years')
  assert.equal(ageLabel('2027-01-01T00:00:00Z', now), '')
  assert.equal(ageLabel(undefined, now), '')
  assert.equal(ageLabel('garbage', now), '')
  assert.equal(podsLabel(12, 110), '12 / 110')
  assert.equal(podsLabel(0, 110), '0 / 110')
  assert.equal(podsLabel(5, undefined), '5')
  assert.equal(podsLabel(undefined, 110), '')
  assert.equal(podsPercent(28, 30), 93)
  assert.equal(podsPercent(3, 0), undefined)
  assert.equal(podsPercent(undefined, 110), undefined)
  assert.equal(autoscalerRange({ min: 2, max: 6 }), '2–6 replicas')
  assert.equal(autoscalerRange({ min: 3, max: 3 }), '3 replicas')
  assert.equal(disruptionLabel({ minAvailable: '50%' }), 'at least 50% up')
  assert.equal(disruptionLabel({ maxUnavailable: '1' }), 'at most 1 down')
})

test('sample data shows pods, pinned storage and scaling rules', () => {
  const n0 = seed.nodes.find((n) => n.id === 'n-a2')!
  assert.equal(n0.podCount, 9)
  const svc = seed.services.find((s) => s.id === 'w-kafka')!
  assert.equal(svc.volumes?.length, 2)
  assert.ok(svc.volumes!.every((v) => v.pinnedNodeIds?.length === 1))
})

test('map: sites carry what lives there and the worst status', () => {
  const ms = buildMapSites(seed.sites, seed.clusters, seed.devices, seed.nodes, seed.services)
  assert.equal(ms.length, seed.sites.length)
  const pat = ms.find((m) => m.site.id === 'site-pat')!
  assert.ok(pat.clusters.length >= 1 && pat.nodes >= 1 && pat.devices.length >= 1)
  assert.equal(worstStatus(['healthy', 'degraded', 'unknown']), 'degraded')
  assert.equal(worstStatus(['healthy', 'offline', 'degraded']), 'offline')
  assert.equal(worstStatus([]), 'unknown')
})

test('map: a site with impossible coordinates is not drawn and its clusters count as unplaced', () => {
  const bad = { ...seed.sites[0], id: 'bad', lat: 123, lng: 0 }
  const c = { ...seed.clusters[0], id: 'cx', siteId: 'bad' }
  assert.equal(buildMapSites([bad], [c], [], [], []).length, 0)
  assert.deepEqual(unplacedClusters([bad], [c]).map((x) => x.id), ['cx'])
  assert.deepEqual(unplacedClusters(seed.sites, [{ ...c, siteId: undefined }]).map((x) => x.id), ['cx'])
  assert.deepEqual(unplacedClusters(seed.sites, [{ ...c, siteId: undefined, deletedAt: '2026-01-01T00:00:00Z' }]), [])
})

test('map: dots merge when close on screen and separate when zoomed in', () => {
  const pts = [{ id: 'a', x: 500, y: 200 }, { id: 'b', x: 508, y: 204 }, { id: 'c', x: 700, y: 300 }]
  assert.deepEqual(groupByProximity(pts, 1, 30).map((g) => g.map((p) => p.id)), [['a', 'b'], ['c']])
  assert.deepEqual(groupByProximity(pts, 8, 30).map((g) => g.map((p) => p.id)), [['a'], ['b'], ['c']])
  assert.deepEqual(groupByProximity([], 1, 30), [])
})

test('map: labels and connections', () => {
  const ms = buildMapSites(seed.sites, seed.clusters, seed.devices, seed.nodes, seed.services)
  const greek = ms.filter((m) => m.site.country === 'GR')
  assert.ok(greek.length >= 2)
  assert.equal(groupLabel(greek), 'Greece')
  assert.equal(groupLabel([greek[0]]), greek[0].site.city || greek[0].site.name)
  assert.equal(groupLabel([...greek, ms.find((m) => m.site.country !== 'GR')!]), `${greek.length + 1} sites`)
  const conns = siteConnections(seed.sites, seed.clusters, seed.services, seed.devices, seed.dependencies, seed.siteLinks)
  assert.ok(conns.length > 0 && conns.every((c) => c.a < c.b && c.a !== c.b))
  assert.equal(new Set(conns.map((c) => c.a + '|' + c.b)).size, conns.length)
  assert.equal(clampPan(50, 2, 1000), 0)
  assert.equal(clampPan(-5000, 2, 1000), -1000)
  assert.equal(clampPan(-300, 2, 1000), -300)
})

test('a server state with null or missing lists is filled in, never crashes', () => {
  const s = normalizeServerState({ agents: null, topology: { clusters: null, nodes: undefined } } as never)
  assert.deepEqual([s.agents, s.auditLog, s.topology.clusters, s.topology.nodes, s.topology.services, s.topology.suggestions], [[], [], [], [], [], []])
  assert.deepEqual(normalizeServerState(undefined).agents, [])
  const a = normalizeServerState({ agents: [{ id: 'x', modules: null }] } as never).agents[0]
  assert.deepEqual(a.modules, [])
})

/* ---------- places: tables instead of labels ---------- */

const places = buildIndex(
  parseCities(readFileSync('src/data/cities.tsv', 'utf8')),
  countryShapes(JSON.parse(readFileSync('node_modules/world-atlas/countries-110m.json', 'utf8')), JSON.parse(readFileSync('src/data/country-numeric.json', 'utf8'))),
)

test('places: the city table is loaded, folded and searchable', () => {
  assert.ok(places.cities.length > 20000)
  assert.equal(fold('São Paulo'), 'sao paulo')
  assert.equal(fold('Zürich'), 'zurich')
  assert.equal(findCities(places, 'athe')[0].name, 'Athens')
  assert.equal(findCities(places, 'Zurich')[0].cc, 'CH')
  assert.equal(findCities(places, 'a').length, 0) // too short to mean anything
  assert.ok(findCities(places, 'patras', 5, 'GR').every((c) => c.cc === 'GR'))
  assert.equal(findCities(places, 'patras', 5, 'DE').length, 0)
})

test('places: distance and nearest city', () => {
  assert.ok(Math.abs(distanceKm(37.98, 23.73, 40.64, 22.94) - 303) < 10) // Athens - Thessaloniki
  assert.equal(nearestCity(places, 37.97, 23.72)?.city.name, 'Athens')
  assert.equal(nearestCity(places, 0, -30), undefined) // mid-Atlantic
})

test('places: country from coordinates, tolerant at coasts', () => {
  assert.equal(countryAt(places, 37.98, 23.73), 'GR')
  assert.equal(countryAt(places, 48.86, 2.35), 'FR')
  assert.equal(countryAt(places, 40.71, -74.0), 'US')
  assert.equal(countryAt(places, 0, -30), undefined)
  assert.equal(siteLocationIssue(places, { lat: 37.98, lng: 23.73, country: 'GR' }), undefined)
  assert.equal(siteLocationIssue(places, { lat: 37.94, lng: 23.64, country: 'GR' }), undefined) // Piraeus harbour
  assert.match(siteLocationIssue(places, { lat: 37.98, lng: 23.73, country: 'DE' }) ?? '', /Greece, not Germany/)
})

test('places: cloud region codes win over guessing', () => {
  const c = placementCandidates(places, { region: 'eu-central-1', provider: 'AWS' })[0]
  assert.equal(c.city, 'Frankfurt')
  assert.equal(c.country, 'DE')
  assert.equal(c.confidence, 'high')
  assert.equal(placementCandidates(places, { region: 'eu-central-1a', provider: 'aws' })[0].city, 'Frankfurt') // a zone
  assert.equal(placementCandidates(places, { region: 'europe-west3', provider: 'GCP' })[0].city, 'Frankfurt')
  // an AWS code on a cluster that says it is GCP is not trusted as a region
  assert.equal(placementCandidates(places, { region: 'eu-central-1', provider: 'GCP' }).length, 0)
  // no provider given: still found, but not "high"
  assert.equal(placementCandidates(places, { region: 'eu-central-1', provider: '' })[0].confidence, 'medium')
})

test('places: a city named in a free-text label is matched, words that are not places are not', () => {
  const p = placementCandidates(places, { region: 'Patras HQ', provider: 'on-prem' })
  assert.equal(p[0].city, 'Patras')
  assert.equal(p[0].country, 'GR')
  assert.equal(p[0].confidence, 'medium')
  assert.equal(fold(placementCandidates(places, { region: 'thessaloniki-lab', provider: 'on-prem' })[0].city), 'thessaloniki')
  assert.equal(placementCandidates(places, { region: 'Zürich DC', provider: '' })[0].city, 'Zürich')
  assert.equal(placementCandidates(places, { region: 'lab', provider: '' }).length, 0)
  assert.equal(placementCandidates(places, { region: 'edge site 3', provider: '' }).length, 0)
  assert.equal(placementCandidates(places, { region: '', provider: '' }).length, 0)
})

test('places: every alias in the exonym table resolves to a real city', () => {
  for (const [alias, [name, cc]] of Object.entries(EXONYMS)) {
    const hit = places.byName.get(alias)?.find((c) => c.cc === cc && (c.name === name || c.ascii === name))
    assert.ok(hit, `${alias} -> ${name}, ${cc}`)
  }
  assert.equal(placementCandidates(places, { region: 'Frankfurt lab', provider: 'on-prem' })[0].country, 'DE')
  assert.equal(placementCandidates(places, { region: 'Hanover', provider: '' })[0].country, 'DE')
})

test('places: GeoIP is a low-confidence hint, and agreement raises confidence', () => {
  const geo = { country: 'GR', countryName: 'Greece', city: 'Athens', lat: 37.98, lng: 23.73, accuracyKm: 20, level: 'city' as const, database: 'DB-IP' }
  assert.equal(placementCandidates(places, { region: '', provider: 'on-prem', geo })[0].confidence, 'medium')
  // behind a cloud provider's network the address says little about the cluster
  assert.equal(placementCandidates(places, { region: '', provider: 'AWS', geo })[0].confidence, 'low')
  const both = placementCandidates(places, { region: 'Athens', provider: 'on-prem', geo })
  assert.equal(both.length, 1)
  assert.equal(both[0].confidence, 'high')
  assert.equal(both[0].evidence.length, 2)
  // a label and an address that disagree stay separate, label first
  const clash = placementCandidates(places, { region: 'Patras', provider: 'on-prem', geo })
  assert.deepEqual(clash.map((c) => c.city), ['Patras', 'Athens'])
  // country-only record: start from its largest city, say so, and keep it low
  const co = placementCandidates(places, { region: '', provider: '', geo: { country: 'GR', countryName: 'Greece', level: 'country' as const } })
  assert.equal(co[0].city, 'Athens')
  assert.equal(co[0].confidence, 'low')
  assert.match(co[0].evidence[0].detail ?? '', /only knows the country/)
})

test('places: an existing site nearby is reused, not duplicated', () => {
  const sites = [{ id: 'site-x', orgId: DEFAULT_ORG, name: 'Athens DC', kind: 'data-center' as const, lat: 37.99, lng: 23.75, country: 'GR' }]
  const c = placementCandidates(places, { region: 'Athens', provider: '' }, sites)[0]
  assert.equal(c.siteId, 'site-x')
  assert.equal(siteFromCandidate(c, { provider: '', tier: 'edge', orgId: DEFAULT_ORG }).id, 'site-x')
})

test('places: placement suggestions are derived, deterministic, and decided once', () => {
  const cl = (over: Partial<Cluster>): Cluster => ({ ...manual, id: 'cl-new', siteId: undefined, region: 'Patras HQ', provider: 'On-prem', tier: 'edge', source: 'discovered', ...over })
  const model = { clusters: [cl({})], sites: [], nodes: [], agents: [], suggestions: [] as Suggestion[] }
  const a = derivePlacementSuggestions(places, model)
  const b = derivePlacementSuggestions(places, model)
  assert.equal(a.length, 1)
  assert.deepEqual(a, b) // same in every browser
  assert.equal(a[0].id, 'sg-place-cl-new')
  assert.equal(a[0].apply?.type, 'place-cluster')
  // accepting creates the site once and puts the cluster there (as an override: rediscovery keeps it)
  const m0 = { ...seedTopology(), clusters: [cl({})], sites: [], suggestions: [] } as Model
  const changes = applySuggestion(m0, a[0])
  assert.equal(changes.sites?.length, 1)
  assert.equal(changes.sites?.[0].id, 'site-patras-gr')
  assert.equal(changes.sites?.[0].kind, 'edge-site')
  assert.equal(effective(changes.clusters![0]).siteId, 'site-patras-gr')
  // a cluster already on the map, a deleted one, and a decided suggestion produce nothing
  assert.equal(derivePlacementSuggestions(places, { ...model, sites: changes.sites!, clusters: changes.clusters! }).length, 0)
  assert.equal(derivePlacementSuggestions(places, { ...model, clusters: [cl({ deletedAt: SEEN })] }).length, 0)
  assert.equal(derivePlacementSuggestions(places, { ...model, suggestions: [{ ...a[0], status: 'dismissed' }] }).length, 0)
  // no evidence, no suggestion
  assert.equal(derivePlacementSuggestions(places, { ...model, clusters: [cl({ region: '' })] }).length, 0)
  // node zones and the agent's address count as evidence too
  const viaZone = derivePlacementSuggestions(places, { ...model, clusters: [cl({ region: '', provider: 'AWS' })], nodes: [{ clusterId: 'cl-new', zone: 'eu-north-1a' }] })
  assert.match(viaZone[0].title, /Stockholm/)
})

test('merge: an agent\'s public address becomes the cluster exit address; private ones do not', () => {
  const base = { ...seedTopology(), clusters: [], agents: [] } as Model
  const cluster = { ...manual, id: 'cl-ip', source: 'discovered' as const, agentId: 'ag-ip', egressIp: undefined }
  const mk = (ip: string): ServerState => ({
    generatedAt: SEEN,
    agents: [{ id: 'ag-ip', orgId: DEFAULT_ORG, name: 'a', clusterId: 'cl-ip', version: '1', accessTier: 2, installedTier: 2, tierCap: 2, status: 'approved', fingerprint: 'f', connectingIp: ip, connectingGeo: { country: 'GR', level: 'country' }, requestedAt: SEEN, connected: true, synced: true, modules: [] }],
    topology: { clusters: [cluster], nodes: [], namespaces: [], services: [], suggestions: [] },
    auditLog: [],
  })
  const pub = mergeDiscovered(base, mk('203.0.113.9'))
  assert.equal(pub.clusters![0].egressIp, '203.0.113.9')
  assert.equal(pub.agents![0].connectingGeo?.country, 'GR')
  assert.equal(mergeDiscovered(base, mk('10.0.0.4')).clusters![0].egressIp, undefined)
})

/* ---------- map: tier fill, exit addresses ---------- */

test('map: a place\'s tier is the one most of its clusters are in; devices alone mean the far edge', () => {
  assert.equal(dominantTier(['edge', 'edge', 'cloud']), 'edge')
  assert.equal(dominantTier(['edge', 'cloud']), 'cloud') // tie: the more central one
  assert.equal(dominantTier([], true), 'far-edge')
  assert.equal(dominantTier([], false), undefined)
  const ms = buildMapSites(seed.sites, seed.clusters, seed.devices, seed.nodes, seed.services)
  assert.equal(ms.find((m) => m.site.id === 'site-pat')?.tier, seed.clusters.find((c) => c.siteId === 'site-pat')!.tier)
  assert.ok(ms.every((m) => m.tier !== undefined || (m.clusters.length === 0 && m.devices.length === 0)))
})

test('map: only public addresses are shown as a site\'s exit IP', () => {
  assert.deepEqual(exitIps([{ egressIp: '203.0.113.24' }, { egressIp: '203.0.113.24' }, { egressIp: '10.0.0.5' }, { egressIp: undefined }, { egressIp: '100.64.1.1' }]), ['203.0.113.24'])
  const ms = buildMapSites(seed.sites, seed.clusters, seed.devices, seed.nodes, seed.services)
  assert.deepEqual(ms.find((m) => m.site.id === 'site-pat')?.exitIps, ['203.0.113.24'])
})

/* ---------- can it move? ---------- */

const mm = (over: Partial<MoveModel> = {}): MoveModel => ({ clusters: seed.clusters, nodes: seed.nodes, services: seed.services, devices: seed.devices, dependencies: seed.dependencies, sites: seed.sites, ...over })
const svc = (id: string) => seed.services.find((x) => x.id === id)!
const codes = (id: string, over?: Partial<Service>) => movability({ ...svc(id), ...over }, mm()).reasons.map((r) => r.code)

test('movability: local volumes and machine-bound devices pin a service, with the reason', () => {
  const kafka = movability(svc('w-kafka'), mm())
  assert.equal(kafka.verdict, 'pinned')
  assert.equal(kafka.reasons[0].code, 'local-volume') // worst first
  assert.match(kafka.reasons[0].text, /local volume/)
  const mqtt = movability(svc('w-mqtt-a'), mm())
  assert.equal(mqtt.verdict, 'pinned')
  assert.ok(codes('w-mqtt-a').includes('attached-device')) // the sensors' gateway is the node it runs on
})

test('movability: a cloud volume is a caution, not a blocker; policy is spelled out', () => {
  const reg = movability(svc('w-registry'), mm())
  assert.equal(reg.verdict, 'careful')
  assert.ok(reg.reasons.some((r) => r.code === 'volume' && /200 GB/.test(r.text)))
  assert.ok(reg.reasons.some((r) => r.code === 'residency' && /EU/.test(r.text)))
})

test('movability: selectors, disruption budgets, single replicas and daemon sets', () => {
  assert.ok(codes('w-infer-b').includes('arch'))
  assert.ok(codes('w-train').includes('accelerator'))
  assert.ok(codes('w-gw', { disruption: { minAvailable: '2', allowed: 0 } }).includes('pdb'))
  assert.ok(codes('w-orch', { replicas: 1 }).includes('single'))
  assert.ok(!codes('w-orch', { replicas: 1, autoscaler: { min: 1, max: 3, current: 1 } }).includes('single'))
  assert.equal(movability({ ...svc('w-orch'), kind: 'DaemonSet' }, mm()).verdict, 'pinned')
  assert.ok(codes('w-orch', { nodeSelector: { 'kubernetes.io/hostname': 'n-c2' } }).includes('hostname'))
  // an agent that does not read storage means "no volumes" proves nothing
  assert.ok(movability({ ...svc('w-orch'), volumes: undefined }, mm({ storageKnown: false })).reasons.some((r) => r.code === 'storage-unknown'))
})

test('movability: a plain stateless public service is free', () => {
  const plain: Service = { ...svc('w-orch'), replicas: 3, sensitivity: 'public', applicationId: undefined, exposure: 'internal', hosts: undefined, volumes: undefined, nodeSelector: undefined, tolerations: undefined, disruption: undefined }
  const r = movability(plain, mm({ clusters: seed.clusters.map((c) => ({ ...c, trustZone: undefined, dataResidency: undefined })), sites: seed.sites.map((s) => ({ ...s, trustZone: undefined, dataResidency: undefined })) }))
  assert.equal(r.verdict, 'free')
  assert.deepEqual(r.reasons, [])
})

test('moveTargets: residency, trust zone, selectors and capacity rule clusters out, with the reason', () => {
  const reg = svc('w-registry') // confidential, EU, in the cloud cluster
  const us = seed.clusters.map((c) => (c.id === 'cl-region' ? { ...c, dataResidency: 'US' } : c))
  const t = moveTargets(reg, mm({ clusters: us }))
  const region = t.find((x) => x.cluster.id === 'cl-region')!
  assert.equal(region.fits, false)
  assert.match(region.blockers.join(' '), /must stay in EU/)
  assert.ok(!t.some((x) => x.cluster.id === reg.clusterId))
  assert.ok(t.findIndex((x) => x.fits) < t.findIndex((x) => !x.fits) || t.every((x) => x.fits)) // fits first

  // restricted edge to a merely private cluster: less trusted
  const fromEdge = moveTargets(svc('w-mqtt-a'), mm())
  assert.match(fromEdge.find((x) => x.cluster.id === 'cl-cloud')!.blockers.join(' '), /less trusted/)

  // a selector nobody in the target satisfies
  const jet = moveTargets(svc('w-infer-a'), mm()).find((x) => x.cluster.id === 'cl-cloud')!
  assert.match(jet.blockers.join(' '), /jetson-orin/)

  // capacity: ask for far more than any node has free
  const huge = moveTargets({ ...svc('w-orch'), cpuRequestM: 900000 }, mm())
  assert.ok(huge.every((x) => x.unknown.length > 0 || x.blockers.some((b) => /free CPU/.test(b))))
  // a daemon set never moves between clusters
  assert.ok(moveTargets({ ...svc('w-orch'), kind: 'DaemonSet' }, mm()).every((x) => !x.fits))
})

/* ---------- saved views and search ---------- */

test('views: options are canonical, defaults are dropped, unknown options are ignored', () => {
  assert.equal(viewParams('view=application&group=cluster&links=1'), '')
  assert.equal(viewParams('group=tier&view=infrastructure'), 'group=tier&view=infrastructure')
  assert.equal(viewParams('view=infrastructure&sel=cluster:x&junk=1'), 'view=infrastructure')
  assert.ok(sameView('view=map', 'view=map&sel=site:a'))
  assert.ok(sameView('', 'devices=1&view=application'))
  assert.ok(!sameView('view=map', ''))
  assert.equal(describeView('view=infrastructure&group=tier&services=1'), 'Infrastructure, grouped by tier, services on nodes')
  assert.equal(describeView('view=map&group=tier'), 'Map')
  assert.equal(describeView(''), 'Application')
})

test('views: the active saved view is the one matching the URL', () => {
  const views = seed.savedViews
  assert.equal(activeView(views, new URLSearchParams('view=map'))?.id, 'view-where')
  assert.equal(activeView(views, new URLSearchParams('group=tier&view=infrastructure&sel=node:x'))?.id, 'view-tiers')
  assert.equal(activeView(views, new URLSearchParams('view=infrastructure')), undefined)
})

test('views: save replaces a view of the same name, delete removes it, and they survive normalize', () => {
  const n = normalize({ ...seed, schemaVersion: SCHEMA_VERSION })
  assert.equal(n.savedViews.length, 2)
  const old = normalize({ ...seed, savedViews: undefined, schemaVersion: SCHEMA_VERSION })
  assert.deepEqual(old.savedViews, []) // a file from before views existed
  const junk = normalize({ ...seed, savedViews: [{ id: 'x' }, { id: 'ok', name: 'A', params: 'view=map' }], schemaVersion: SCHEMA_VERSION })
  assert.deepEqual(junk.savedViews.map((v) => v.id), ['ok'])
})

const idx = buildSearchIndex({ ...seed, savedViews: seed.savedViews })

test('search: finds things by name, word start and small print, best match first', () => {
  const r = searchItems(idx, 'kafka')
  assert.equal(r[0].kind, 'service')
  assert.equal(r[0].title, 'kafka')
  assert.ok(r[0].to.includes('sel=service%3Aw-kafka'))
  assert.ok(searchItems(idx, 'patras').slice(0, 4).some((i) => i.kind === 'site')) // named for it: near the top
  assert.ok(searchItems(idx, 'jetson').some((i) => i.kind === 'node' || i.kind === 'service'))
  assert.ok(searchItems(idx, 'eks').some((i) => i.kind === 'cluster')) // distribution is small print
  assert.equal(searchItems(idx, 'zzzzqqq').length, 0)
})

test('search: every word must match; nothing typed offers pages and views', () => {
  assert.ok(searchItems(idx, 'kafka ingest').every((i) => /kafka|ingest/i.test(`${i.title} ${i.subtitle} ${i.keywords}`)))
  assert.equal(searchItems(idx, 'kafka zzzz').length, 0)
  const start = searchItems(idx, '')
  assert.ok(start.length > 0 && start.every((i) => i.kind === 'page' || i.kind === 'view'))
  assert.equal(searchItems(idx, 'ZÜRICH'.toLowerCase()).length, 0)
  assert.ok(searchItems(idx, 'topolo')[0].to === '/topology')
})

test('search: deleted things are not offered, and nodes open the infrastructure view', () => {
  const gone = buildSearchIndex({ ...seed, services: seed.services.map((w) => (w.id === 'w-kafka' ? { ...w, deletedAt: SEEN } : w)) })
  assert.equal(searchItems(gone, 'kafka').filter((i) => i.kind === 'service').length, 0)
  const node = idx.find((i) => i.kind === 'node')!
  assert.ok(node.to.includes('view=infrastructure'))
  assert.deepEqual(parseSel('service:w-gw'), { kind: 'service', id: 'w-gw' })
  assert.deepEqual(parseSel('site:site:odd'), { kind: 'site', id: 'site:odd' })
  assert.equal(parseSel('nonsense'), undefined)
  assert.equal(parseSel('wat:1'), undefined)
  assert.equal(parseSel(null), undefined)
})

test('node probe: the install command gains one flag, once, and only when asked', () => {
  const cmd = 'helm install continuum-agent oci://x \\\n  --set server.caPin=ab \\\n  --set enrollment.token=cnt_1'
  assert.equal(withNodeProbe(cmd, false), cmd)
  const on = withNodeProbe(cmd, true)
  assert.ok(on.endsWith(' \\\n  --set nodeProbe.enabled=true'))
  assert.equal(withNodeProbe(on, true), on)
  assert.equal(on.split('nodeProbe.enabled').length, 2)
})

test('traffic observer: its install flag is added once, only when asked, and stacks with the probe', () => {
  const cmd = 'helm install continuum-agent oci://x \\\n  --set enrollment.token=cnt_1'
  assert.equal(withFlowObserver(cmd, false), cmd)
  const both = withFlowObserver(withNodeProbe(cmd, true), true)
  assert.ok(both.includes('nodeProbe.enabled=true') && both.endsWith(' \\\n  --set flowObserver.enabled=true'))
  assert.equal(withFlowObserver(both, true), both)
})

test('node probe: what the machine reported survives normalize and merge, and a person still has the last word', () => {
  const probed = { ...seed.nodes.find((n) => n.source === 'discovered')!, kind: 'bare-metal' as const, probed: true, virtualization: undefined, hasBattery: true, evidence: { kind: { signal: 'node probe: no hypervisor bit on the CPU', confidence: 'high' as const } } }
  const n = normalize({ ...seed, nodes: seed.nodes.map((x) => (x.id === probed.id ? probed : x)) }).nodes.find((x) => x.id === probed.id)!
  assert.equal(n.probed, true)
  assert.equal(n.hasBattery, true)
  assert.equal(n.evidence?.kind?.confidence, 'high')
  const vm = { ...probed, kind: 'vm' as const, virtualization: 'KVM/QEMU' }
  const doc = { generatedAt: SEEN, agents: [], auditLog: [], topology: { clusters: [], nodes: [vm], namespaces: [], services: [], suggestions: [] } } as unknown as ServerState
  const merged = mergeDiscovered({ ...seed, nodes: [{ ...probed, overrides: { kind: 'edge-device' } }] }, doc).nodes!.find((x) => x.id === probed.id)!
  assert.equal(merged.virtualization, 'KVM/QEMU')
  assert.equal(merged.kind, 'vm') // the stored base value follows discovery...
  assert.equal(effective(merged).kind, 'edge-device') // ...but the person's override still wins
})

const seenDep = (over: Partial<Dependency>): Dependency => ({
  id: 'dep-obs-1', orgId: DEFAULT_ORG, from: 'a', fromKind: 'service', to: 'b', toKind: 'service', sources: ['observed'], confidence: 'high',
  protocol: 'TCP', port: 5432, via: 'ebpf', connections: 10, stats: { connectionsPerMin: 12, bytesPerSec: 3500, windowSec: 60 }, lastSeen: SEEN, ...over,
})
const ext = (over: Partial<ExternalEndpoint> = {}): ExternalEndpoint => ({ id: 'ext-1', orgId: DEFAULT_ORG, source: 'discovered', host: '93.184.216.34', port: 443, kind: 'unknown', ...over })

test('observed traffic: seen-only edges are added, but only between things that exist', () => {
  const [a, b] = seed.services
  const base = { services: seed.services, devices: seed.devices, dependencies: [] as Dependency[], externalEndpoints: [] as ExternalEndpoint[] }
  const r = withObserved(base, { dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'x', from: a.id, to: 'w-deleted' })], externalEndpoints: [] })
  assert.equal(r.dependencies.length, 1)
  assert.equal(r.dependencies[0].to, b.id)
  assert.ok(isObserved(r.dependencies[0]))
})

test('observed traffic: a declared dependency that was also seen becomes one edge with both sources and the numbers', () => {
  const [a, b] = seed.services
  const declared: Dependency = { id: 'dep-1', orgId: DEFAULT_ORG, from: a.id, fromKind: 'service', to: b.id, toKind: 'service', sources: ['declared'], confidence: 'medium', protocol: 'HTTP' }
  const r = withObserved({ services: seed.services, devices: [], dependencies: [declared], externalEndpoints: [] }, { dependencies: [seenDep({ from: a.id, to: b.id })], externalEndpoints: [] })
  assert.equal(r.dependencies.length, 1)
  const d = r.dependencies[0]
  assert.equal(d.id, 'dep-1') // the stored record keeps its identity
  assert.deepEqual(d.sources, ['declared', 'observed'])
  assert.equal(d.confidence, 'high')
  assert.equal(d.port, 5432)
  assert.equal(d.stats?.connectionsPerMin, 12)
  assert.equal(declared.sources.length, 1, 'the stored dependency must not be mutated')
})

test('observed traffic: an outside address a person already named is that endpoint, not a second one', () => {
  const [a] = seed.services
  const named = ext({ id: 'ext-mine', source: 'manual', name: 'Payments API', kind: 'saas' })
  const dep = seenDep({ from: a.id, to: 'ext-1', toKind: 'external', port: 443 })
  const r = withObserved({ services: seed.services, devices: [], dependencies: [], externalEndpoints: [named] }, { dependencies: [dep], externalEndpoints: [ext()] })
  assert.equal(r.externalEndpoints.length, 1)
  assert.equal(r.dependencies[0].to, 'ext-mine')
  const fresh = withObserved({ services: seed.services, devices: [], dependencies: [], externalEndpoints: [] }, { dependencies: [dep], externalEndpoints: [ext()] })
  assert.equal(fresh.externalEndpoints.length, 1)
  assert.equal(fresh.dependencies[0].to, 'ext-1')
})

test('observed traffic: nothing observed leaves the model exactly as it was', () => {
  const base = { services: seed.services, devices: seed.devices, dependencies: seed.dependencies, externalEndpoints: seed.externalEndpoints }
  const r = withObserved(base, { dependencies: [], externalEndpoints: [] })
  assert.equal(r.dependencies, base.dependencies)
  assert.equal(r.externalEndpoints, base.externalEndpoints)
})

test('observed traffic: DNS and system traffic is hidden until asked for, and does not pull external endpoints onto the canvas', () => {
  const [a, b, c] = seed.services
  const t = {
    ...seed,
    dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'n1', from: a.id, to: c.id, noise: 'dns', port: 53 }), seenDep({ id: 'n2', from: a.id, to: 'ext-dns', toKind: 'external', noise: 'dns', port: 53 })],
    externalEndpoints: [ext({ id: 'ext-dns', host: '8.8.8.8', port: 53 })],
  }
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: true }
  const quiet = buildGraph(t, opts)
  assert.ok(quiet.edges.some((e) => e.id === 'dep-obs-1'))
  assert.ok(!quiet.edges.some((e) => e.id === 'n1' || e.id === 'n2'))
  assert.ok(!quiet.nodes.some((n) => n.id.includes('ext-dns')), 'an address only DNS went to is not worth a card')
  const loud = buildGraph(t, { ...opts, noise: true })
  assert.ok(loud.edges.some((e) => e.id === 'n1'))
  assert.ok(loud.nodes.some((n) => n.id.includes('ext-dns')))
})

test('observed traffic: an edge that was seen carries its numbers, its weight and its staleness to the canvas', () => {
  const [a, b] = seed.services
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true }
  const g = buildGraph({ ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'old', from: b.id, to: a.id, stale: true, stats: undefined })] }, opts)
  const live = g.edges.find((e) => e.id === 'dep-obs-1')!
  assert.ok(live.data?.observed && !live.data.stale)
  assert.equal(live.label, "TCP:5432") // the numbers live in the inspector, not on the line
  assert.ok((live.data?.weight ?? 0) > 0 && (live.data?.weight ?? 2) <= 1)
  const old = g.edges.find((e) => e.id === 'old')!
  assert.ok(old.data?.stale)
  assert.equal(old.label, "TCP:5432") // staleness is drawn (faded) and explained in the inspector, not written on the line
  const declared = buildGraph({ ...seed, dependencies: [{ ...seenDep({ from: a.id, to: b.id }), sources: ['declared'], stats: undefined, via: undefined }] }, opts).edges[0]
  assert.equal(declared.label, "TCP:5432")
  assert.ok(!declared.data?.observed)
})

test('observed traffic: rates and sizes read the way a person would say them, and never claim bytes that were not measured', () => {
  assert.equal(bytesPerSec(0), '0 B/s')
  assert.equal(bytesPerSec(3500), '3.4 KB/s')
  assert.equal(bytesPerSec(5 * 1024 * 1024), '5.0 MB/s')
  assert.equal(bytesTotal(1536), '1.5 KB')
  assert.equal(bytesTotal(12), '12 B')
  assert.equal(trafficSummary(seenDep({})), '12 conn/min · 3.4 KB/s')
  assert.equal(trafficSummary(seenDep({ via: 'conntrack', stats: { connectionsPerMin: 0.4 } })), '<1 conn/min')
  assert.equal(ago(new Date(Date.now() - 3 * 3600_000).toISOString()), '3 h ago')
  assert.equal(ago(undefined), 'never')
})

test('observed traffic: the server state always has the lists and the observer health the UI reads', () => {
  const s = normalizeServerState({ topology: { clusters: [], nodes: [], namespaces: [], services: [], suggestions: [] }, agents: [{ id: 'a', observer: { lastReport: SEEN, lost: 0 } }] } as unknown as ServerState)
  assert.deepEqual(s.topology.dependencies, [])
  assert.deepEqual(s.topology.externalEndpoints, [])
  assert.deepEqual(s.agents[0].observer?.collectors, [])
  assert.equal(normalizeServerState(null).topology.dependencies.length, 0)
})

test('server sync: an agent carries its consistency check and measuring count; a stale suspicion goes away once a cluster is onboarded', () => {
  const chk = { lastCheck: SEEN, checks: 3, differences: 1, summary: '1 service had been missed' }
  const m = apply(EMPTY_MODEL, doc({}, [srvAgent({ consistency: chk, measuring: 4, link: { connects: 1, bytes: 3550, syncs: 1, flows: 0, measurements: 0, heartbeats: 2 } })]))
  const a = m.agents.find((x) => x.id === 'ag-real')!
  assert.deepEqual(a.consistency, chk)
  assert.equal(a.measuring, 4)
  assert.equal(a.link?.bytes, 3550) // the Agents page reads what the agent has sent
  const sus: Suggestion = {
    id: 'sg-unk-198.51.100', orgId: 'default', kind: 'infrastructure', title: 'Something that looks like Kubernetes', detail: 'ports 6443 and 10250 at 198.51.100.7', createdAt: SEEN, status: 'open',
    apply: { type: 'connect-cluster', addresses: ['198.51.100.7'] },
  }
  // shown while the server still raises it…
  const withIt = apply(EMPTY_MODEL, doc({ suggestions: [sus] }))
  assert.ok(withIt.suggestions.some((x) => x.id === sus.id))
  // …applying it changes nothing (the Connect wizard opens instead)…
  assert.deepEqual(applySuggestion(withIt, sus), {})
  // …and it disappears once the server no longer raises it, unless a person already decided about it
  assert.ok(!apply(withIt, doc({ suggestions: [] })).suggestions.some((x) => x.id === sus.id))
  const dismissed = { ...withIt, suggestions: withIt.suggestions.map((x) => (x.id === sus.id ? { ...x, status: 'dismissed' as const } : x)) }
  assert.ok(apply(dismissed, doc({ suggestions: [] })).suggestions.some((x) => x.id === sus.id && x.status === 'dismissed'))
})

test('install: measurements are an explicit opt-in flag on the install command', () => {
  const base = 'helm upgrade --install continuum-agent oci://x --set a=b'
  assert.equal(withMeasurements(base, false), base)
  assert.match(withMeasurements(base, true), /--set measurements\.enabled=true/)
})

/* ---------- service mesh ---------- */
const meshCluster = seed.clusters[0]
const inCluster = seed.services.filter((w) => w.clusterId === meshCluster.id)
const meshOf = (over: Partial<ClusterMesh> = {}): ClusterMesh => ({ kind: 'istio', mode: 'sidecar', mtls: 'strict', controlPlane: [], policyRead: true, ...over })
const withMesh = (mesh?: ClusterMesh): Cluster => ({ ...meshCluster, mesh })
const svcMesh = (w: Service, m?: Service['mesh']): Service => ({ ...w, mesh: m })
const proxied = { mesh: 'istio', proxy: 'sidecar' as const, source: 'pods' }

test('mesh: two proxied services under a strict policy are called encrypted, and the words say it is inferred', () => {
  const [a, b] = inCluster
  const v = connectionVerdict({ port: 5432 }, svcMesh(a, proxied), svcMesh(b, proxied), withMesh(meshOf()), [])
  assert.equal(v?.state, 'encrypted')
  assert.match(v!.detail, /strict/)
})

test('mesh: a permissive policy is not called encrypted, and an unreadable policy says it is not known', () => {
  const [a, b] = inCluster
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(b, proxied), withMesh(meshOf({ mtls: 'permissive' })), [])?.state, 'permissive')
  const unknown = connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(b, proxied), withMesh(meshOf({ mtls: 'unknown', policyRead: false })), [])
  assert.equal(unknown?.state, 'permissive')
  assert.match(unknown!.short, /unknown/)
})

test('mesh: a namespace override beats the mesh-wide policy', () => {
  const [a, b] = inCluster
  const ns = [{ id: 'n', orgId: DEFAULT_ORG, clusterId: meshCluster.id, name: b.namespace, labels: {}, source: 'discovered' as const, mtls: 'permissive' as const }]
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(b, proxied), withMesh(meshOf()), ns as never)?.state, 'permissive')
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(b, proxied), withMesh(meshOf({ namespaceMtls: { [b.namespace]: 'disabled' } })), [])?.state, 'plaintext')
})

test('mesh: one end outside the mesh, a bypassed port and a control plane each get the right verdict', () => {
  const [a, b] = inCluster
  const c = withMesh(meshOf())
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(b, undefined), c, [])?.state, 'partial')
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, undefined), svcMesh(b, undefined), c, []), undefined)
  assert.equal(connectionVerdict({ port: 5432 }, svcMesh(a, { ...proxied, excludedPorts: ['out:5432'] }), svcMesh(b, proxied), c, [])?.state, 'bypassed')
  assert.equal(connectionVerdict({ port: 8005 }, svcMesh(a, proxied), svcMesh(b, { ...proxied, excludedPorts: ['in:8000-8100'] }), c, [])?.state, 'bypassed')
  assert.equal(connectionVerdict({ port: 5432 }, svcMesh(a, proxied), svcMesh(b, { ...proxied, excludedPorts: ['in:8000-8100'] }), c, [])?.state, 'encrypted')
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(b, { mesh: 'istio', controlPlane: true }), c, []), undefined)
  // A namespace that only asks for injection has no proxy yet: not in the mesh.
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, { mesh: 'istio', source: 'namespace' }), svcMesh(b, proxied), c, [])?.state, 'partial')
})

test('mesh: ambient workloads are secured by the node proxy, Linkerd is automatic, and neither is guessed at across clusters', () => {
  const [a, b] = inCluster
  const amb = { mesh: 'istio', proxy: 'ambient' as const, source: 'pods' }
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, amb), svcMesh(b, amb), withMesh(meshOf({ mode: 'ambient', mtls: 'permissive' })), [])?.state, 'encrypted')
  const lk = { mesh: 'linkerd', proxy: 'sidecar' as const, source: 'pods' }
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, lk), svcMesh(b, lk), withMesh(meshOf({ kind: 'linkerd', mtls: 'automatic' })), [])?.state, 'encrypted')
  const other = seed.services.find((w) => w.clusterId !== meshCluster.id)!
  assert.equal(connectionVerdict({ port: 1 }, svcMesh(a, proxied), svcMesh(other, proxied), withMesh(meshOf()), []), undefined)
})

test('mesh: the toggle brings the control plane and the chips; off, the mesh machinery is not drawn as an application', () => {
  const [a, b] = inCluster
  const cp: Service = { ...b, id: 'w-istiod', name: 'istiod', mesh: { mesh: 'istio', controlPlane: true, source: 'workload' } }
  const t = {
    ...seed,
    clusters: seed.clusters.map((c) => (c.id === meshCluster.id ? { ...c, mesh: meshOf({ controlPlane: [cp.id] }) } : c)),
    services: [...seed.services.filter((w) => w.id !== a.id && w.id !== b.id), svcMesh(a, proxied), svcMesh(b, proxied), cp],
    dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'to-cp', from: a.id, to: cp.id })],
  }
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const off = buildGraph(t, opts)
  assert.ok(!off.nodes.some((n) => n.id === `c:${cp.id}`), 'istiod is hidden')
  assert.ok(!off.nodes.some((n) => (n.data as { mesh?: unknown }).mesh), 'no chip')
  assert.ok(off.edges.every((e) => !e.data?.mesh))
  const on = buildGraph(t, { ...opts, mesh: true })
  assert.ok(on.nodes.some((n) => n.id === `c:${cp.id}`))
  const chip = on.nodes.find((n) => n.id === `c:${a.id}`)!.data as { mesh?: { tone: string } }
  assert.equal(chip.mesh?.tone, 'in')
  const group = on.nodes.find((n) => n.id === `g:${meshCluster.id}`)!.data as { mesh?: { label: string; tone: string } }
  assert.match(group.mesh!.label, /Istio/)
  assert.equal(group.mesh!.tone, 'good')
  const e = on.edges.find((x) => x.id === 'dep-obs-1')!
  assert.equal(e.data?.mesh?.state, 'encrypted')
  assert.equal(String(e.label), 'TCP:5432', 'a long label would run into the boxes it joins')
  // The chip needs a taller card only when it shares its row.
  const card = on.nodes.find((n) => n.id === `c:${a.id}`)!
  const plain = off.nodes.find((n) => n.id === `c:${a.id}`)!
  assert.ok(Number(card.style?.height) > Number(plain.style?.height))
})

test('lines between the same two boxes are spread apart, and a lone line is left alone', () => {
  const [a, b, c] = inCluster
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'dep-2', from: a.id, to: b.id, port: 9090 }), seenDep({ id: 'dep-3', from: b.id, to: a.id, port: 80 }), seenDep({ id: 'dep-4', from: a.id, to: c.id })] }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const offs = (id: string) => g.edges.find((e) => e.id === id)!.data!.offset
  assert.equal(new Set(['dep-obs-1', 'dep-2', 'dep-3'].map(offs)).size, 3, 'three lines, three positions')
  assert.equal(offs('dep-4'), undefined)
})

test('mesh: anyMesh looks at live clusters only, and the saved-view URL keeps the option', () => {
  assert.equal(anyMesh(seed.clusters), false)
  assert.equal(anyMesh([withMesh(meshOf())]), true)
  assert.equal(anyMesh([{ ...withMesh(meshOf()), deletedAt: SEEN }]), false)
  assert.equal(viewParams('mesh=1'), 'mesh=1')
  assert.equal(viewParams('mesh=0'), '')
  assert.match(describeView('mesh=1'), /service mesh/)
})

test('scope: names are split and checked, a selector must be on a label the agent reads, and the flags are quoted for helm', () => {
  assert.deepEqual(splitNames('shop, payments  ops\nshop'), ['shop', 'payments', 'ops'])
  assert.deepEqual(scopeProblems({ ...emptyScope, namespaces: ['shop', 'Bad_Name'] }), ['"Bad_Name" is not a valid namespace name'])
  assert.equal(scopeProblems({ ...emptyScope, selector: 'continuum.io/scope=yes' }).length, 0)
  assert.match(scopeProblems({ ...emptyScope, selector: 'team=a' })[0], /continuum\.io\//)
  assert.match(scopeProblems({ ...emptyScope, selector: 'continuum.io/x=a=b' })[0], /key=value/)
  const base = 'helm upgrade --install continuum-agent oci://x --set a=b'
  assert.equal(withScope(base, emptyScope), base)
  assert.equal(withScope(base, { ...emptyScope, namespaces: ['a', 'b'] }).includes("--set scope.namespaces='{a,b}'"), true)
  const all = withScope(base, { namespaces: ['a'], exclude: ['hr'], selector: 'continuum.io/scope=yes' })
  assert.match(all, /scope\.exclude='\{hr\}'/)
  assert.match(all, /--set-string scope\.selector='continuum\.io\/scope=yes'/)
  // An invalid scope never reaches the command: the wizard refuses it first, and this stays safe if it did not.
  assert.equal(withScope(base, { ...emptyScope, namespaces: ['Bad Name'] }), base)
})

console.log(failed ? `\n${failed} FAILED` : '\nall passed')
process.exit(failed ? 1 : 0)
