import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { completeness } from '../src/lib/completeness'
import { mergeDiscovered, normalizeServerState, type ServerAgent, type ServerState } from '../src/lib/discovered'
import { applyEdit, applyEffective, confirmOverride, effective } from '../src/lib/effective'
import { normalize, upgrade } from '../src/lib/migrate'
import { accelSummary, ageLabel, autoscalerRange, callerIfaceSpeedMbps, count, countryName, middleTruncate, disruptionLabel, ipScope, linkUtilizationPct, podsLabel, podsPercent, distroKey, formatCpu, formatMemory, loadBand, placeLabel, providerKey, requestedPercent, shortVersion } from '../src/lib/present'
import { buildMapSites, clampPan, dominantTier, exitIps, groupByProximity, groupLabel, siteConnections, unplacedClusters, worstStatus } from '../src/lib/geo'
import { buildIndex, countryAt, countryShapes, derivePlacementSuggestions, distanceKm, findCities, fold, nearestCity, parseCities, placementCandidates, siteFromCandidate, siteLocationIssue } from '../src/lib/places'
import { EXONYMS } from '../src/data/exonyms'
import { moveTargets, movability, type MoveModel } from '../src/lib/movability'
import { buildSearchIndex, parseSel, searchItems } from '../src/lib/search'
import { filterSummary } from '../src/lib/filter'
import { narrowFitZoom } from '../src/lib/fit'
import { activeView, describeView, sameView, viewParams } from '../src/lib/views'
import { emptyScope, scopeProblems, splitNames, withFlowObserver, withMeasurements, withNodeProbe, withScope } from '../src/lib/install'
import { anyMesh, connectionVerdict } from '../src/lib/mesh'
import { ago, bytesPerSec, bytesTotal, isObserved, trafficSummary, withObserved } from '../src/lib/observed'
import { alertOfLoad, alertOfPods, alertOfStatus, worstAlert, parseDetail } from '../src/lib/detail'
import { applyGraphUpdate, APP_CARD, CALM_CARD_H, CALM_HEADER, CALM_NOTE, sameLayout, buildGraph, cardId, groupId, HEADER, MACHINE_CARD, MIN_GROUP_HEADER_WIDTH, NS_HEADER, NS_PAD, PAD, pickSides, resyncNodes, selectedServiceIds, syncPickEligibility, syncSelected } from '../src/lib/graph'
import { seedTopology } from '../src/lib/seed'
import { buildNetworks, networkSentence } from '../src/lib/networks'
import { applySuggestion, groupingAlternativesFor } from '../src/lib/suggestions'
import { DEFAULT_ORG, SCHEMA_VERSION, type Cluster, type ClusterLink, type ClusterMesh, type Dependency, type Device, type ExternalEndpoint, type Model, type Service, type Suggestion } from '../src/lib/types'

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

test('applyEdit(): a genuinely changed field is attributed to whoever saved it', () => {
  const saved = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ' }, 'alexandros')
  assert.equal(saved.overrideMeta?.region?.by, 'alexandros')
  assert.ok(saved.overrideMeta?.region?.at)
})

test('applyEdit(): re-saving an unrelated field keeps a field\'s existing attribution untouched', () => {
  const once = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ' }, 'alexandros', '2026-01-01T00:00:00.000Z')
  // A second save by someone else that leaves `region` exactly as shown, but changes `version` too.
  const twice = applyEdit<Cluster>(once, { ...effective(once), version: 'v9' }, 'someone-else', '2026-06-01T00:00:00.000Z')
  assert.deepEqual(twice.overrideMeta?.region, { by: 'alexandros', at: '2026-01-01T00:00:00.000Z' })
  assert.deepEqual(twice.overrideMeta?.version, { by: 'someone-else', at: '2026-06-01T00:00:00.000Z' })
})

test('applyEdit(): a field edited back to the detected value drops its attribution along with its override', () => {
  const once = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ' }, 'alexandros')
  const back = applyEdit<Cluster>(once, { ...effective(once), region: discovered.region }, 'alexandros')
  assert.equal(back.overrides, undefined)
  assert.equal(back.overrideMeta, undefined)
})

test('applyEdit(): a field changed again to a different value is re-attributed to the new editor', () => {
  const once = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ' }, 'alexandros', '2026-01-01T00:00:00.000Z')
  const changed = applyEdit<Cluster>(once, { ...effective(once), region: 'Athens HQ' }, 'someone-else', '2026-06-01T00:00:00.000Z')
  assert.deepEqual(changed.overrideMeta?.region, { by: 'someone-else', at: '2026-06-01T00:00:00.000Z' })
})

test('applyEdit(): no `by` given leaves the override in place with no attribution (backward compatible)', () => {
  const saved = applyEdit<Cluster>(discovered, { ...discovered, region: 'Patras HQ' })
  assert.deepEqual(saved.overrides, { region: 'Patras HQ' })
  assert.equal(saved.overrideMeta, undefined)
})

test('confirmOverride(): freezes a guessed value as-is, with who confirmed it and when', () => {
  assert.equal(discovered.overrides?.provider, undefined)
  const confirmed = confirmOverride(discovered, 'provider', 'alexandros', '2026-01-01T00:00:00.000Z')
  assert.equal(confirmed.overrides?.provider, discovered.provider) // no value change
  assert.deepEqual(confirmed.overrideMeta?.provider, { by: 'alexandros', at: '2026-01-01T00:00:00.000Z' })
})

test('confirmOverride(): a confirmation survives an unrelated later applyEdit() save', () => {
  const confirmed = confirmOverride(discovered, 'provider', 'alexandros', '2026-01-01T00:00:00.000Z')
  const saved = applyEdit<Cluster>(confirmed, { ...effective(confirmed), region: 'Patras HQ' }, 'someone-else', '2026-06-01T00:00:00.000Z')
  assert.equal(saved.overrides?.provider, discovered.provider)
  assert.deepEqual(saved.overrideMeta?.provider, { by: 'alexandros', at: '2026-01-01T00:00:00.000Z' }) // untouched
  assert.deepEqual(saved.overrideMeta?.region, { by: 'someone-else', at: '2026-06-01T00:00:00.000Z' }) // the actual edit
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

test('applySuggestion(): create-application groups the listed services under a fresh application', () => {
  const newApp = { id: 'app-fresh', orgId: DEFAULT_ORG, name: 'checkout', description: '', origin: 'explicit', confidence: 'high' as const, source: 'discovered' as const }
  const s = { ...seed.suggestions[0], apply: { type: 'create-application' as const, application: newApp, serviceIds: ['w-gw', 'w-orch'] } }
  const out = applySuggestion(seed, s)
  assert.ok(out.applications!.some((a) => a.id === 'app-fresh' && a.name === 'checkout'))
  assert.equal(out.services!.find((x) => x.id === 'w-gw')!.applicationId, 'app-fresh')
  assert.equal(out.services!.find((x) => x.id === 'w-orch')!.applicationId, 'app-fresh')
})

test('applySuggestion(): regrouping onto an alternative label creates its own application, not the original one', () => {
  // What the Discovery page's "use a different label instead" picker does: it swaps the suggestion's
  // application for a fresh one named after the runner-up label before accepting, rather than the
  // one discovery originally proposed.
  const original = { id: 'app-orig', orgId: DEFAULT_ORG, name: 'my-release', description: '', origin: 'helm', confidence: 'high' as const, source: 'discovered' as const }
  const reworked = { ...original, id: 'app-alt', name: 'shop-suite', origin: 'part-of', confidence: 'medium' as const }
  const s = { ...seed.suggestions[0], apply: { type: 'create-application' as const, application: reworked, serviceIds: ['w-gw'] } }
  const out = applySuggestion(seed, s)
  assert.ok(!out.applications!.some((a) => a.id === 'app-orig'), 'the label that was passed over should never be created')
  assert.ok(out.applications!.some((a) => a.id === 'app-alt' && a.name === 'shop-suite'))
  assert.equal(out.services!.find((x) => x.id === 'w-gw')!.applicationId, 'app-alt')
})

test('groupingAlternativesFor(): gathers alternatives from the suggestion(s) that produced this application', () => {
  const app = { id: 'app-x', name: 'my-release' }
  const withAlts = (id: string, alts: { name: string; origin: string; confidence: 'high' | 'medium' | 'low'; signal: string }[]): Suggestion => ({
    ...seed.suggestions[0],
    id,
    apply: { type: 'create-application', application: { id: 'app-x', orgId: DEFAULT_ORG, name: 'my-release', description: '', origin: 'helm', confidence: 'high', source: 'discovered' }, serviceIds: [], alternatives: alts },
  })
  const a = withAlts('sg-1', [{ name: 'shop-suite', origin: 'part-of', confidence: 'medium', signal: 'label app.kubernetes.io/part-of=shop-suite' }])
  const b = withAlts('sg-2', [
    { name: 'shop-suite', origin: 'part-of', confidence: 'medium', signal: 'label app.kubernetes.io/part-of=shop-suite' }, // same name as `a`'s: not repeated
    { name: 'checkout', origin: 'namespace', confidence: 'low', signal: 'namespace checkout' },
  ])
  const unrelated: Suggestion = { ...seed.suggestions[0], id: 'sg-other', apply: { type: 'create-application', application: { id: 'app-other', orgId: DEFAULT_ORG, name: 'other', description: '', origin: 'helm', confidence: 'high', source: 'discovered' }, serviceIds: [], alternatives: [{ name: 'not-this', origin: 'helm', confidence: 'high', signal: 'x' }] } }
  const alts = groupingAlternativesFor(app, [a, b, unrelated])
  assert.deepEqual(alts.map((x) => x.name), ['shop-suite', 'checkout'])
})

test('groupingAlternativesFor(): never offers the application\'s own current name, and is empty when nothing suggested it', () => {
  const app = { id: 'app-x', name: 'shop-suite' }
  const s: Suggestion = {
    ...seed.suggestions[0],
    apply: { type: 'create-application', application: { id: 'app-x', orgId: DEFAULT_ORG, name: 'shop-suite', description: '', origin: 'part-of', confidence: 'medium', source: 'discovered' }, serviceIds: [], alternatives: [{ name: 'Shop-Suite', origin: 'helm', confidence: 'high', signal: 'x' }] },
  }
  assert.deepEqual(groupingAlternativesFor(app, [s]), []) // case-insensitive match against its own name
  assert.deepEqual(groupingAlternativesFor({ id: 'app-manual', name: 'hand-made' }, seed.suggestions), [])
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

test('linkUtilizationPct: % of a rated link speed actually in use, undefined when there is nothing to compare against', () => {
  // 1000 Mbps (decimal megabits, the real-world network-speed convention) -> 125,000,000 bytes/sec capacity.
  assert.equal(linkUtilizationPct(62_500_000, 1000), 50, 'half of a 1 Gbps link')
  assert.equal(linkUtilizationPct(125_000_000, 1000), 100, 'exactly saturated')
  // Regression guard against a binary-prefix mixup (1000 * 1024 * 1024 / 8, a classic Mbps-vs-MiB slip):
  // that would put 100% at ~131,072,000 B/s instead of the correct 125,000,000, silently shifting every
  // reported percentage by about 4.9%.
  assert.notEqual(linkUtilizationPct(131_072_000, 1000), 100)
  assert.equal(linkUtilizationPct(1000, 0), undefined, 'no rated speed to compare against')
  assert.equal(linkUtilizationPct(1000, -1), undefined, 'a negative rated speed is not a real one either')
  // A bad upstream counter (a reset/delta gone negative, or NaN) must not render as a nonsensical
  // percentage - "no data" beats a negative or NaN number on screen.
  assert.equal(linkUtilizationPct(-5, 1000), undefined)
  assert.equal(linkUtilizationPct(NaN, 1000), undefined)
  // Deliberately uncapped above 100%: real oversubscription/measurement noise is worth showing as-is.
  assert.equal(linkUtilizationPct(250_000_000, 1000), 200)
})

test("callerIfaceSpeedMbps: a dependency's own interface capacity, resolved from the calling service's node(s), never guessed when ambiguous", () => {
  const nodeById = new Map([
    ['n-1', { networkInterfaces: [{ name: 'eth0', kind: 'ethernet', speedMbps: 1000 }] }],
    ['n-2', { networkInterfaces: [{ name: 'eth0', kind: 'ethernet', speedMbps: 1000 }] }],
    ['n-3', { networkInterfaces: [{ name: 'eth0', kind: 'ethernet', speedMbps: 100 }] }],
    ['n-4', { networkInterfaces: [{ name: 'wlan0', kind: 'wifi', speedMbps: 300 }] }],
    ['n-5', { networkInterfaces: [] }],
  ])
  assert.equal(callerIfaceSpeedMbps('eth0', ['n-1'], nodeById), 1000, 'the one node this service runs on')
  assert.equal(callerIfaceSpeedMbps('eth0', ['n-1', 'n-2'], nodeById), 1000, 'every node agrees - still unambiguous')
  assert.equal(callerIfaceSpeedMbps('eth0', ['n-1', 'n-3'], nodeById), undefined, 'different hardware behind two replicas disagree on eth0 - never guessed')
  assert.equal(callerIfaceSpeedMbps('eth0', ['n-4'], nodeById), undefined, "that node has no eth0 at all")
  assert.equal(callerIfaceSpeedMbps('eth0', ['n-5'], nodeById), undefined, 'a node with no interfaces reported')
  assert.equal(callerIfaceSpeedMbps(undefined, ['n-1'], nodeById), undefined, 'no interface named at all')
  assert.equal(callerIfaceSpeedMbps('eth0', undefined, nodeById), undefined, 'no nodes to resolve against (an external/device caller)')
  assert.equal(callerIfaceSpeedMbps('eth0', [], nodeById), undefined)
  assert.equal(callerIfaceSpeedMbps('eth0', ['does-not-exist'], nodeById), undefined)
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

test('places: an estimated GeoIP result (the server\'s own public IP, standing in for a private address) never beats low confidence and says so', () => {
  const geoBase = { country: 'GR', countryName: 'Greece', city: 'Athens', lat: 37.98, lng: 23.73, accuracyKm: 20, level: 'city' as const, database: 'DB-IP' }
  const estimated = { ...geoBase, estimated: true }
  // on-prem, matching the region label too - would normally reach "high" via agreement, but an
  // estimated address is never trusted that far since it isn't really the cluster's own address.
  const c = placementCandidates(places, { region: 'Athens', provider: 'on-prem', geo: estimated })
  assert.equal(c[0].confidence, 'low')
  assert.ok(c.find((x) => x.city === 'Athens')!.evidence.some((e) => /this server's own internet connection/.test(e.detail ?? '')))
  // a normal (non-estimated) GeoIP result under the same inputs does reach high via agreement -
  // confirms the low ceiling above is specifically about `estimated`, not the region label losing effect.
  const notEstimated = placementCandidates(places, { region: 'Athens', provider: 'on-prem', geo: geoBase })
  assert.equal(notEstimated.length, 1)
  assert.equal(notEstimated[0].confidence, 'high')
})

test('places: a placement suggestion carries every signal that went into its confidence, not just the flattened sentence', () => {
  const cl = (over: Partial<Cluster>): Cluster => ({ ...manual, id: 'cl-new', siteId: undefined, region: 'Athens', provider: 'On-prem', tier: 'edge', source: 'discovered', ...over })
  const geo = { country: 'GR', countryName: 'Greece', city: 'Athens', lat: 37.98, lng: 23.73, level: 'city' as const, database: 'DB-IP' }
  const model = { clusters: [cl({})], sites: [], nodes: [], agents: [{ clusterId: 'cl-new', connectingGeo: geo }], suggestions: [] as Suggestion[] }
  const [s] = derivePlacementSuggestions(places, model)
  // The region label and the agent's GeoIP address agree on Athens, which is exactly the two-source
  // agreement placementCandidates rewards with 'high' - so there must be two evidence entries here,
  // each with its own confidence, not one string that has already thrown that breakdown away.
  assert.equal(s.confidence, 'high')
  assert.equal(s.evidence.length, 2)
  assert.ok(s.evidence.every((e) => e.signal && e.confidence))
  assert.ok(s.evidence.some((e) => /names/.test(e.signal))) // the region label match
  assert.ok(s.evidence.some((e) => /GeoIP/.test(e.signal))) // the agent's connecting address
  // detail (the flattened sentence other UI still reads, e.g. the Inbox list) keeps working alongside it
  assert.match(s.detail, /Confidence: high/)
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

test('movability: a device dependency with measured loss or jitter names it in the caution, unmeasured ones do not', () => {
  // w-infer-a talks to dev-act-a (dependency d20) without being plugged into its gateway node - this is
  // the 'devices' caution's own existing case, re-verified against the current seed before extending it
  // below. Unmeasured (the seed's own default): the caution text is unchanged from before this session.
  const plain = movability(svc('w-infer-a'), mm()).reasons.find((r) => r.code === 'devices')!
  assert.match(plain.text, /Moving far away adds latency to every message\.$/)
  assert.ok(!/already measures/.test(plain.text), plain.text)

  // Now with d20 actually eBPF-measuring real loss and jitter on that exact link.
  const degraded = seed.dependencies.map((d) => (d.id === 'd20' ? { ...d, stats: { ...d.stats, lossPct: 3.2 }, jitterMs: 40 } : d))
  const withEvidence = movability(svc('w-infer-a'), mm({ dependencies: degraded })).reasons.find((r) => r.code === 'devices')!
  assert.match(withEvidence.text, /already measures 3\.2% packet loss and 40 ms of jitter/, withEvidence.text)
  assert.equal(withEvidence.severity, plain.severity) // enriches the existing caution, never changes its severity

  // Below the noteworthy thresholds (see movability.ts's own worstLoss/worstJitter comment): nothing is
  // added, so a merely-present-but-tiny measurement does not clutter the sentence with noise.
  const tiny = seed.dependencies.map((d) => (d.id === 'd20' ? { ...d, stats: { ...d.stats, lossPct: 0.1 }, jitterMs: 2 } : d))
  const withTiny = movability(svc('w-infer-a'), mm({ dependencies: tiny })).reasons.find((r) => r.code === 'devices')!
  assert.ok(!/already measures/.test(withTiny.text), withTiny.text)
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

test('movability: a kubernetes.io/os selector resolves from MachineNode.os, not just the raw label', () => {
  // None of the seeded nodes carry a literal kubernetes.io/os label (seed.ts's `n` helper defaults labels to {}),
  // so this is exactly the stock-Helm-chart nodeSelector that used to come back "does not fit" everywhere even
  // though every node's (required, always-known) os field answers it.
  const linux = moveTargets({ ...svc('w-orch'), nodeSelector: { 'kubernetes.io/os': 'linux' } }, mm())
  assert.ok(linux.some((t) => t.fits), 'a linux selector should fit somewhere: every seeded node is Linux')
  assert.ok(!linux.some((t) => t.blockers.some((b) => /no node carries/.test(b))))

  // os is required, so unlike arch it resolves definitively: a selector nothing can satisfy is a hard "does not
  // fit", not a "can't tell" waiting on an agent to report it.
  const windows = moveTargets({ ...svc('w-orch'), nodeSelector: { 'kubernetes.io/os': 'windows' } }, mm())
  assert.ok(windows.every((t) => !t.fits))
  assert.ok(windows.some((t) => t.blockers.some((b) => /no node carries/.test(b))))
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

test('observed traffic: a seen edge to a real device is kept, not just services and external endpoints', () => {
  const [a] = seed.services
  const base = { services: seed.services, devices: seed.devices, dependencies: [] as Dependency[], externalEndpoints: [] as ExternalEndpoint[] }
  const r = withObserved(base, {
    dependencies: [
      seenDep({ from: a.id, to: 'dev-cam-a', toKind: 'device' }),
      seenDep({ id: 'x', from: a.id, to: 'dev-deleted', toKind: 'device' }),
    ],
    externalEndpoints: [],
  })
  assert.equal(r.dependencies.length, 1)
  assert.equal(r.dependencies[0].to, 'dev-cam-a')
})

test('observed traffic: an observed edge with a real port merges into the matching port, not a portless placeholder it reaches first', () => {
  const [a, b] = seed.services
  const portless: Dependency = { id: 'dep-portless', orgId: DEFAULT_ORG, from: a.id, fromKind: 'service', to: b.id, toKind: 'service', sources: ['declared'], confidence: 'low', protocol: 'TCP' }
  const specific: Dependency = { id: 'dep-5432', orgId: DEFAULT_ORG, from: a.id, fromKind: 'service', to: b.id, toKind: 'service', sources: ['declared'], confidence: 'low', protocol: 'TCP', port: 5432 }
  const base = { services: seed.services, devices: [] as Device[], dependencies: [portless, specific], externalEndpoints: [] as ExternalEndpoint[] }
  const r = withObserved(base, { dependencies: [seenDep({ from: a.id, to: b.id, port: 5432 })], externalEndpoints: [] })
  assert.equal(r.dependencies.length, 2, 'no new edge should have been created')
  const merged = r.dependencies.find((d) => d.id === 'dep-5432')!
  const untouched = r.dependencies.find((d) => d.id === 'dep-portless')!
  assert.ok(isObserved(merged), 'the port-matching edge should have absorbed the observed traffic')
  assert.ok(!isObserved(untouched), 'the portless placeholder must be left alone when a specific-port match exists')
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

test('lines between the same two boxes are spread apart, and a lone line at a quiet end is left alone', () => {
  const [a, b, c] = inCluster
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'dep-2', from: a.id, to: b.id, port: 9090 }), seenDep({ id: 'dep-3', from: b.id, to: a.id, port: 80 }), seenDep({ id: 'dep-4', from: a.id, to: c.id })] }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const offs = (id: string) => {
    const e = g.edges.find((e) => e.id === id)!
    return `${e.data!.sourceOffset ?? 0},${e.data!.targetOffset ?? 0}`
  }
  assert.equal(new Set(['dep-obs-1', 'dep-2', 'dep-3'].map(offs)).size, 3, 'three lines between a and b, three positions')
  // c only ever hears from a, so the far end of dep-4 has nothing to fan out from and stays put.
  assert.equal(g.edges.find((e) => e.id === 'dep-4')!.data!.targetOffset ?? 0, 0, 'a lone edge at a quiet node is left alone there')
})

test('edges to different destinations landing on the same side of a busy node fan out, ordered by where they are headed', () => {
  const [a, b, c] = inCluster
  // b and c both hang off a's right side; a lone edge elsewhere on a (to a fourth node) would not be touched.
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'dep-4', from: a.id, to: c.id })] }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const toB = g.edges.find((e) => e.id === 'dep-obs-1')!
  const toC = g.edges.find((e) => e.id === 'dep-4')!
  assert.equal(toB.sourceHandle, toC.sourceHandle, 'both leave a from the same side')
  assert.notEqual(toB.data!.sourceOffset ?? 0, toC.data!.sourceOffset ?? 0, 'fanned apart at the shared end')
  // Neither b nor c hears from anyone else, so the arriving end of each line is left alone.
  assert.equal(toB.data!.targetOffset ?? 0, 0)
  assert.equal(toC.data!.targetOffset ?? 0, 0)
})

test('pickSides also works on two bare points (zero-size boxes) - used only to pick an initial sourceHandle/targetHandle id at build time; OffsetEdge computes its own live anchor points independently, see OffsetEdge.test.tsx', () => {
  assert.deepEqual(pickSides({ x: 0, y: 0, w: 0, h: 0 }, { x: 100, y: 0, w: 0, h: 0 }), ['right', 'left'])
  assert.deepEqual(pickSides({ x: 100, y: 0, w: 0, h: 0 }, { x: 0, y: 0, w: 0, h: 0 }), ['left', 'right'])
  assert.deepEqual(pickSides({ x: 0, y: 0, w: 0, h: 0 }, { x: 0, y: 100, w: 0, h: 0 }), ['bottom', 'top'])
  assert.deepEqual(pickSides({ x: 0, y: 100, w: 0, h: 0 }, { x: 0, y: 0, w: 0, h: 0 }), ['top', 'bottom'])
})

test('selectedServiceIds: resolves a canvas selection to service ids, expanding group/namespace boxes, dropping anything not selected', () => {
  const [a, b] = inCluster
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id })] }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const clusterBoxId = g.nodes.find((n) => n.data.kind === 'group')!.id
  const clusterServiceIds = new Set(g.nodes.filter((n) => n.data.kind === 'service' && n.parentId === clusterBoxId).map((n) => n.data.entityId))

  const cardsOnly = [cardId(a.id), cardId(b.id), 'not-a-real-node-id']
  assert.deepEqual(new Set(selectedServiceIds(g.nodes, cardsOnly)), new Set([a.id, b.id]), 'the two selected service cards, not the unknown id')
  assert.deepEqual(selectedServiceIds(g.nodes, []), [], 'an empty selection resolves to nothing')
  // Selecting the cluster box itself (rather than each service inside it one at a time) resolves to every
  // service nested under it - this is what lets a shift/ctrl-click or box-select on a whole cluster build a
  // scope out of it directly, instead of only ever working service-by-service. Selecting a card that's
  // already inside the selected box too changes nothing (a Set either way).
  assert.deepEqual(new Set(selectedServiceIds(g.nodes, [clusterBoxId])), clusterServiceIds, 'selecting a group box resolves to every service nested under it')
  assert.deepEqual(new Set(selectedServiceIds(g.nodes, [clusterBoxId, cardId(a.id)])), clusterServiceIds, 'a card already covered by a selected ancestor box adds nothing new')
})

test('chain layout: services rank strictly by dependency depth, across clusters, with no cluster/tier boxes', () => {
  const [a, b] = inCluster
  const other = seed.services.find((w) => w.clusterId !== meshCluster.id)!
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'dep-2', from: b.id, to: other.id })] }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false, chain: true })
  assert.ok(!g.nodes.some((n) => n.data.kind === 'group'), 'no cluster/tier boxes in chain layout')
  assert.ok(g.nodes.every((n) => n.parentId === undefined), 'every card sits directly on the canvas')
  const xOf = (id: string) => g.nodes.find((n) => n.id === cardId(id))!.position.x
  assert.ok(xOf(a.id) < xOf(b.id), 'a leads the service it calls')
  assert.ok(xOf(b.id) < xOf(other.id), 'b leads its own callee, even one in a different cluster')
})

test('chain layout: no card, service or leaf, is drawn over another, and a repeated name says its cluster', () => {
  const [a, b] = inCluster
  const other = seed.services.find((w) => w.clusterId !== a.clusterId)!
  const twin = { ...other, id: 'w-twin', name: a.name }
  const withPods = { ...b, pods: [{ name: `${b.name}-1`, nodeId: 'n-c2', phase: 'Running', ready: true }] }
  const t = {
    ...seed,
    services: [...seed.services.map((w) => (w.id === b.id ? withPods : w)), twin],
    dependencies: [...seed.dependencies, seenDep({ id: 'dep-twin', from: a.id, to: 'w-twin' })],
  }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: true, chain: true })
  const boxes = g.nodes.map((n) => ({ id: n.id, x: n.position.x, y: n.position.y, w: Number(n.style?.width), h: Number(n.style?.height) }))
  assert.ok(boxes.some((x) => x.h > APP_CARD.h), 'sanity: a taller card (its pod row) is among them')
  for (const [i, p] of boxes.entries()) {
    for (const q of boxes.slice(i + 1)) {
      assert.ok(p.x + p.w <= q.x || q.x + q.w <= p.x || p.y + p.h <= q.y || q.y + q.h <= p.y, `${p.id} and ${q.id} overlap`)
    }
  }
  const tag = (id: string) => g.nodes.find((n) => n.data.entityId === id)!.data.clusterTag
  const clusterName = (id: string) => seed.clusters.find((c) => c.id === id)!.name
  assert.equal(tag(a.id), clusterName(a.clusterId), 'a repeated name carries its cluster')
  assert.equal(tag('w-twin'), clusterName(other.clusterId))
  assert.equal(tag(b.id), undefined, 'a unique name carries nothing extra')
})

test('chain layout: a dependency cycle is broken for ranking, but both directions are still drawn', () => {
  const [a, b] = inCluster
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'dep-back', from: b.id, to: a.id, port: 80 })] }
  const g = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false, chain: true })
  assert.equal(g.edges.length, 2, 'the cycle is drawn in full even though only one direction sets rank')
  assert.ok(g.edges.some((e) => e.id === 'dep-obs-1'))
  assert.ok(g.edges.some((e) => e.id === 'dep-back'))
})

test('chain layout does not blow the call stack on a very long, strictly linear dependency chain', () => {
  // layoutChain's feedback-arc DFS used to be a plain recursive function, one JS call-stack frame per node
  // on the current path - fine for a wide, shallow graph, but a long straight chain (exactly what this
  // layout exists to draw) puts every node on the SAME path, one frame deep per node. Empirically, that
  // recursive shape overflowed Node's stack well under 6000 services (confirmed separately - it survived
  // 4000, crashed by 6000), a size this tool could plausibly reach on a large deployment; the point of this
  // test is that 6000 no longer throws at all; the exact stack limit is a V8 implementation detail, not
  // something to assert on.
  const N = 6000
  const template = seed.services[0]
  const services: Service[] = Array.from({ length: N }, (_, i) => ({ ...template, id: `chain-${i}`, name: `chain-${i}`, key: undefined }))
  const dependencies: Dependency[] = Array.from({ length: N - 1 }, (_, i) => seenDep({ id: `chain-dep-${i}`, from: `chain-${i}`, to: `chain-${i + 1}` }))
  const t = { ...seed, services, dependencies, devices: [] as Device[], externalEndpoints: [] as ExternalEndpoint[] }
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false, chain: true }

  const graph = buildGraph(t, opts) // throws "Maximum call stack size exceeded" with the old recursive DFS

  const xById = new Map(graph.nodes.filter((n) => n.id.startsWith('c:chain-')).map((n) => [n.id, n.position.x]))
  assert.equal(xById.size, N, 'every service in the chain is drawn')
  // A pure chain ranks strictly by depth, one column per service - if the DFS above silently mis-ranked
  // anything (not just crashed), this is what would catch it.
  for (let i = 0; i < N - 1; i++) {
    assert.ok(xById.get(`c:chain-${i}`)! < xById.get(`c:chain-${i + 1}`)!, `chain-${i} should sit left of chain-${i + 1}`)
  }
})

test('a service calling itself does not produce a degenerate zero-length edge, in either the grouped or the chain view', () => {
  const [a, b] = inCluster
  // A self-dependency (e.g. a sidecar proxy hairpin) has no distinct "other end" - OffsetEdge's anchor math
  // would place both ends at the same point (the card's own center), rendering an invisible line that just
  // clutters the edge list for no visible benefit. A real dependency to a different service is included
  // alongside it so the fix is proven to drop only the self-loop, not dependencies in general.
  const t = { ...seed, dependencies: [seenDep({ from: a.id, to: b.id }), seenDep({ id: 'dep-self', from: a.id, to: a.id })] }
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const grouped = buildGraph(t, opts)
  assert.ok(!grouped.edges.some((e) => e.id === 'dep-self'), 'grouped view drops the self-loop')
  assert.ok(grouped.edges.some((e) => e.id === 'dep-obs-1'), 'a real dependency is still drawn')
  const chain = buildGraph(t, { ...opts, chain: true })
  assert.ok(!chain.edges.some((e) => e.id === 'dep-self'), 'chain view drops the self-loop too')
  assert.ok(chain.edges.some((e) => e.id === 'dep-obs-1'))
})

test('edge throughput/quality data reaches EdgeData - a single dependency keeps its own stats, an aggregated group link sums them', () => {
  // w-gw (cl-cloud) -> w-kafka (cl-region): a real cross-cluster dependency, so it both draws as its own
  // line in the application view AND rolls into the one aggregated group<->group line the infrastructure
  // view draws when "Cross-cluster links" is on (o.links) - the same underlying Dependency.stats needs to
  // reach EdgeData correctly on both paths, one passed straight through and one summed across a bundle.
  const dep = seenDep({ id: 'dep-cross', from: 'w-gw', to: 'w-kafka' })
  const t = { ...seed, dependencies: [dep] }

  const app = buildGraph(t, { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const single = app.edges.find((e) => e.id === 'dep-cross')!
  assert.equal(single.data?.stats?.bytesPerSec, 3500, 'the dependency\'s own stats ride straight through')
  assert.equal(single.data?.via, 'ebpf')

  const infra = buildGraph(t, { view: 'infrastructure', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const agg = infra.edges.find((e) => e.id === 'agg:cl-cloud|cl-region')!
  assert.ok(agg, 'the two clusters get one bundled line')
  assert.equal(agg.data?.aggregated, true)
  assert.equal(agg.data?.stats?.bytesPerSec, 3500, 'a single bundled dependency sums to its own figure')
  assert.equal(agg.data?.activeCount, 1, 'seen in traffic (sources includes observed, not stale)')
  assert.equal(agg.data?.protocols, undefined, 'a single-dependency bundle has nothing to break down')
  // Regression guard: an aggregated group<->group edge used to animate purely because it crossed a cluster
  // boundary (the old `d.cross`-driven trigger); makeEdge's animation now means "real, live traffic"
  // (d.observed && !d.stale, see its own comment), so a bundle must set `observed` itself from its own
  // active count or it would have silently stopped animating even while genuinely carrying live traffic.
  assert.equal(agg.data?.observed, true, 'a bundle with at least one active dependency reports itself observed')
  assert.equal(agg.className, 'edge-animated', 'so it keeps animating for real traffic, not just for crossing clusters')

  // A second, undeclared-throughput dependency between the same two clusters adds to the count but not the
  // (unmeasured) total - conntrack-only traffic contributes 0 rather than silently understating as "measured".
  const t2 = { ...seed, dependencies: [dep, seenDep({ id: 'dep-cross-2', from: 'w-orch', to: 'w-kafka', via: 'conntrack', stats: { connectionsPerMin: 4 } })] }
  const infra2 = buildGraph(t2, { view: 'infrastructure', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const agg2 = infra2.edges.find((e) => e.id === 'agg:cl-cloud|cl-region')!
  assert.equal(agg2.data?.activeCount, 2, 'both dependencies were seen in traffic')
  assert.equal(agg2.data?.stats?.bytesPerSec, 3500, 'only the one with a measured bytesPerSec contributes to the total')
  assert.equal(agg2.data?.protocols, undefined, 'still only TCP in the bundle, so still nothing to break down')

  // A bundle with NO active dependency (declared only, never observed) must not animate at all - the fix
  // above must not regress back to animating every cross-cluster bundle unconditionally.
  const t3 = { ...seed, dependencies: [seenDep({ id: 'dep-cross-3', from: 'w-gw', to: 'w-kafka', sources: ['declared'], stats: undefined, via: undefined })] }
  const infra3 = buildGraph(t3, { view: 'infrastructure', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const agg3 = infra3.edges.find((e) => e.id === 'agg:cl-cloud|cl-region')!
  assert.equal(agg3.data?.activeCount, 0, 'nothing in the bundle was ever observed')
  assert.equal(agg3.data?.observed, false)
  assert.equal(agg3.className, undefined, 'a purely-declared bundle does not animate as if it were live traffic')
})

test('an aggregated group link breaks down its bundle by protocol once it actually mixes more than one', () => {
  // Three cross-cluster dependencies between the same two clusters, two different protocols: the UI's
  // "N dependencies" label alone would hide that this is really two HTTP calls and one Kafka one.
  const t = {
    ...seed,
    dependencies: [
      seenDep({ id: 'dep-http-1', from: 'w-gw', to: 'w-kafka', protocol: 'HTTP' }),
      seenDep({ id: 'dep-http-2', from: 'w-orch', to: 'w-kafka', protocol: 'HTTP' }),
      seenDep({ id: 'dep-kafka-1', from: 'w-train', to: 'w-kafka', protocol: 'Kafka' }),
    ],
  }
  const infra = buildGraph(t, { view: 'infrastructure', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const agg = infra.edges.find((e) => e.id === 'agg:cl-cloud|cl-region')!
  assert.ok(agg, 'the two clusters still get one bundled line')
  assert.deepEqual(agg.data?.protocols, { HTTP: 2, Kafka: 1 }, 'counted per protocol, not just a total')
})

test("infrastructure view: a machine card reserves height for its own hardware badge row (Battery/NIC speed), so the badge doesn't sit flush against the card's bottom border", () => {
  const infra = buildGraph(seed, { view: 'infrastructure', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })

  // n-a1 (patras-gw-1) declares a 1000Mbps NIC in the seed data, so its card renders the extra hint/notReady/
  // mesh/hardware badge row (see Card in nodes.tsx) that n-c3 (eks-worker-2, no networkInterfaces/hasBattery
  // in the seed) never renders.
  const withNic = infra.nodes.find((n) => n.id === cardId('n-a1'))!
  const withoutHardware = infra.nodes.find((n) => n.id === cardId('n-c3'))!
  assert.equal(Number(withoutHardware.style?.height), MACHINE_CARD.h, 'no hardware badge, no chips: just the base card height')
  assert.equal(Number(withNic.style?.height), MACHINE_CARD.h + 24, 'the hardware badge row adds its own reserved height, exactly like a service card\'s hint/notReady/mesh row already does')
})

test('infrastructure view: a single-node cluster\'s box is never narrower than its own header needs, so LoadRow\'s meters/warnings overlap the card below (task: node card missing top spacing)', () => {
  // A cluster with exactly one node is the exact case this was found on: PAD*2 + MACHINE_CARD.w = 328px,
  // which is inside LoadRow's flex-wrap danger zone. An earlier version of this fix only accounted for
  // LoadRow showing CPU+Mem (2 items) and set the floor to 352px - which turned out to still be too narrow:
  // reported live by a user after that fix had already shipped. LoadRow can carry up to FIVE items at once
  // (CPU/Mem/Pods mini-bars, plus "N/M nodes ready" and "N services not fully up" warning text once a
  // cluster is unhealthy), and a real single-node cluster reporting all five wrapped to three lines even at
  // 352px, overflowing HEADER's 96px budget by over 10px. Direct measurement (forcing this exact worst-case
  // content through a live, real-browser render and bisecting box width) found the two-line breakpoint at
  // ~430px, so the floor now sits well past that. n-a2 (not n-a1) is used below because it's the node in
  // the seed data that actually has allocatable/requested/podCount data - cl-edge-a's `load` is *computed*
  // from real node metrics (see clusterLoad in metrics.ts), never read back off a `load` field placed
  // directly on the seed Cluster object, so a test needs a node with real numbers to exercise this at all.
  const t = {
    ...seed,
    nodes: seed.nodes
      .filter((n) => n.clusterId !== 'cl-edge-a' || n.id === 'n-a2')
      .map((n) => (n.id === 'n-a2' ? { ...n, status: 'degraded' as const } : n)),
    services: seed.services.map((s) => (s.id === 'w-sensor-a' ? { ...s, readyReplicas: (s.readyReplicas ?? s.replicas ?? 1) - 1 } : s)),
  }
  const infra = buildGraph(t, { view: 'infrastructure', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  const group = infra.nodes.find((n) => n.id === groupId('cl-edge-a'))!
  assert.equal(group.data.load?.nodes, 1, 'sanity: this is genuinely the single-node case')
  assert.ok(group.data.load?.cpuPct !== undefined && group.data.load?.podPct !== undefined, 'sanity: real per-node metrics reached clusterLoad, so LoadRow actually renders all three mini-bars')
  assert.ok((group.data.load?.ready ?? 1) < (group.data.load?.nodes ?? 0), 'sanity: the degraded node makes LoadRow also render the "nodes ready" warning text')
  assert.ok((group.data.load?.unready ?? 0) > 0, 'sanity: the not-fully-up service makes LoadRow also render the "services not fully up" warning text')
  assert.ok(
    Number(group.style?.width) >= MIN_GROUP_HEADER_WIDTH,
    `a single-node cluster's box (${group.style?.width}px) must be at least MIN_GROUP_HEADER_WIDTH (${MIN_GROUP_HEADER_WIDTH}px) - the width LoadRow needs to stay within two wrapped lines even at its fullest`,
  )
})

test("route: 'direct' vs 'gateway' on a cross-cluster dependency, undefined everywhere else", () => {
  const withExposure = (exposure: Service['exposure']) => ({ ...seed, services: seed.services.map((sv) => (sv.id === 'w-kafka' ? { ...sv, exposure } : sv)) })

  // No declared exposure at all: treated the same as 'internal' (consistent with how namespaces.ts already
  // treats an unset exposure as "not exposed outside"), not as "unknown, so assume the worst".
  const undeclared = buildGraph({ ...withExposure(undefined), dependencies: [seenDep({ id: 'd-route-1', from: 'w-gw', to: 'w-kafka', crossCluster: true })] },
    { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  assert.equal(undeclared.edges.find((e) => e.id === 'd-route-1')!.data?.route, 'direct')

  const internal = buildGraph({ ...withExposure('internal'), dependencies: [seenDep({ id: 'd-route-2', from: 'w-gw', to: 'w-kafka', crossCluster: true })] },
    { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  assert.equal(internal.edges.find((e) => e.id === 'd-route-2')!.data?.route, 'direct')

  const exposed = buildGraph({ ...withExposure('load-balancer'), dependencies: [seenDep({ id: 'd-route-3', from: 'w-gw', to: 'w-kafka', crossCluster: true })] },
    { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  assert.equal(exposed.edges.find((e) => e.id === 'd-route-3')!.data?.route, 'gateway')

  // Same-cluster call: always its target's ClusterIP directly, regardless of `crossCluster` or exposure -
  // `crossCluster` itself is what gates this, not merely the two services living in different clusters.
  const notCross = buildGraph({ ...withExposure('load-balancer'), dependencies: [seenDep({ id: 'd-route-4', from: 'w-gw', to: 'w-kafka' })] },
    { view: 'application', groupBy: 'cluster', servicesOnNodes: false, links: true, devices: false })
  assert.equal(notCross.edges.find((e) => e.id === 'd-route-4')!.data?.route, undefined)
})

test('namespace sub-boxes nest cards under one box per namespace, only when asked for and only grouped by cluster', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const off = buildGraph(seed, opts)
  assert.ok(!off.nodes.some((n) => n.data.kind === 'namespace'), 'off by default')
  assert.equal(off.nodes.find((n) => n.id === 'c:w-gw')!.parentId, 'g:cl-cloud', 'cards parent straight to the cluster box when the option is off')

  const on = buildGraph(seed, { ...opts, namespaces: true })
  const platform = on.nodes.find((n) => n.id === 'ns:cl-cloud:platform')
  const ml = on.nodes.find((n) => n.id === 'ns:cl-cloud:ml')
  assert.ok(platform && ml, 'one sub-box per namespace used in that cluster')
  assert.equal(platform!.parentId, 'g:cl-cloud')
  assert.equal(platform!.type, 'namespace')
  assert.equal((platform!.data as { count: number }).count, 2, 'w-gw and w-orch')
  assert.equal(on.nodes.find((n) => n.id === 'c:w-gw')!.parentId, 'ns:cl-cloud:platform')
  assert.equal(on.nodes.find((n) => n.id === 'c:w-orch')!.parentId, 'ns:cl-cloud:platform')
  assert.equal(on.nodes.find((n) => n.id === 'c:w-train')!.parentId, 'ns:cl-cloud:ml')
  // A namespace box sits fully inside its cluster box: it never runs past it.
  const cluster = on.nodes.find((n) => n.id === 'g:cl-cloud')!
  for (const ns of [platform!, ml!]) {
    assert.ok(ns.position.x >= 0 && ns.position.x + Number(ns.style?.width) <= Number(cluster.style?.width))
    assert.ok(ns.position.y >= 0 && ns.position.y + Number(ns.style?.height) <= Number(cluster.style?.height))
  }

  // Grouped by tier, several clusters would share one box: namespace nesting is skipped rather than mixing them.
  const byTier = buildGraph(seed, { ...opts, groupBy: 'tier', namespaces: true })
  assert.ok(!byTier.nodes.some((n) => n.data.kind === 'namespace'))
})

test('a card\'s own drag extent keeps it inside its parent box\'s PAD/HEADER margins, not flush against the bare edges', () => {
  // A plain React Flow extent:'parent' only keeps a card within its parent's full [0,w]x[0,h] rectangle -
  // dragging it could still park it flush against the box's own left/right/bottom border, or up under the
  // header text at the top. buildGraph gives every card its own [[left,top],[right,bottom]] extent instead,
  // reusing the same PAD/HEADER margins the initial layout already placed it with.
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }

  const g = buildGraph(seed, opts)
  const cluster = g.nodes.find((n) => n.id === 'g:cl-cloud')!
  const card = g.nodes.find((n) => n.id === 'c:w-gw')!
  const w = Number(cluster.style?.width)
  const h = Number(cluster.style?.height)
  assert.deepEqual(card.extent, [[PAD, HEADER], [w - PAD, h - PAD]])
  assert.notEqual(card.extent, 'parent')

  const withNs = buildGraph(seed, { ...opts, namespaces: true })
  const ns = withNs.nodes.find((n) => n.id === 'ns:cl-cloud:platform')!
  const nsCard = withNs.nodes.find((n) => n.id === 'c:w-gw')!
  const nw = Number(ns.style?.width)
  const nh = Number(ns.style?.height)
  assert.deepEqual(nsCard.extent, [[NS_PAD, NS_HEADER], [nw - NS_PAD, nh - NS_PAD]])

  // Sanity: the extent's own bottom-right corner is still comfortably past its top-left, i.e. a real usable
  // box, not an inverted or degenerate one - would only fail if a cluster/namespace box ever shrank so far
  // that PAD/HEADER margins on opposite sides overlapped each other.
  for (const [ext, node] of [[card.extent, cluster] as const, [nsCard.extent, ns] as const]) {
    const [[left, top], [right, bottom]] = ext as [[number, number], [number, number]]
    assert.ok(right > left && bottom > top, `${node.id}'s card extent is a real box, not inverted`)
  }
})

test('service card: cards sharing a packed row are all as tall as the row\'s tallest', () => {
  // w-registry and w-train (both in cl-cloud, namespace 'ml') sort ahead of w-gw/w-orch (namespace
  // 'platform') and together already fill the group's first row of two, so w-gw and w-orch land in the
  // same row together - exactly the layout packItems rows cards into.
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const now = Date.now()
  const old = new Date(now - 20 * 60 * 1000).toISOString()
  const steady = {
    ...seed,
    services: seed.services.map((s) => (s.id === 'w-gw' ? { ...s, pods: [
      { name: 'gw-1', nodeId: 'n-c2', phase: 'Running', ready: true, createdAt: old },
      { name: 'gw-2', nodeId: 'n-c3', phase: 'Running', ready: true, createdAt: old },
    ] } : s)),
  }
  const g = buildGraph(steady, opts)
  const gw = g.nodes.find((n) => n.id === cardId('w-gw'))!
  const orch = g.nodes.find((n) => n.id === cardId('w-orch'))!
  assert.equal(Number(gw.style?.height), APP_CARD.h + 24, 'the taller card (its own pods row) keeps that height')
  assert.equal(Number(orch.style?.height), Number(gw.style?.height), 'its shorter row-mate is stretched to match, so the row reads as one band')
  assert.equal(orch.position.y, gw.position.y, 'both start at the same top')
})

test('calm detail: a card that is fine is small, a card with a problem adds one line saying what, and no card is stretched to its row-mate', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const old = new Date(Date.now() - 20 * 60 * 1000).toISOString()
  const fine = { ...seed, services: seed.services.map((s) => ({ ...s, status: 'healthy' as const, pods: undefined, readyReplicas: undefined })) }
  const quiet = buildGraph(fine, { ...opts, detail: 'calm' })
  const cards = quiet.nodes.filter((n) => n.type === 'card')
  assert.ok(cards.length > 3)
  assert.ok(cards.every((n) => Number(n.style?.height) === CALM_CARD_H && !n.data.alert), 'nothing is wrong, so every card is the small one')
  const full = buildGraph(fine, { ...opts, detail: 'full' })
  assert.ok(full.nodes.filter((n) => n.type === 'card').every((n) => Number(n.style?.height) === APP_CARD.h), 'Full keeps the card it always was')
  assert.equal(Number(buildGraph(fine, opts).nodes.find((n) => n.type === 'card')!.style?.height), APP_CARD.h, 'no choice made is the old canvas')
  // One workload goes bad: its card adds the line that says so; its row-mate keeps its own small height, top-aligned with it.
  const bad = { ...fine, services: fine.services.map((s) => (s.id === 'w-gw' ? { ...s, status: 'degraded' as const } : s)) }
  const g = buildGraph(bad, { ...opts, detail: 'calm' })
  const gw = g.nodes.find((n) => n.id === cardId('w-gw'))!
  assert.equal(gw.data.alert, 'warn')
  assert.equal(gw.data.note, 'Degraded')
  assert.equal(Number(gw.style?.height), CALM_CARD_H + CALM_NOTE)
  const orch = g.nodes.find((n) => n.id === cardId('w-orch'))!
  assert.equal(orch.data.alert, undefined)
  assert.equal(Number(orch.style?.height), CALM_CARD_H, 'a fine card next to a problem one is not stretched into an empty box')
  assert.equal(orch.position.y, gw.position.y, 'but both start at the same top')
  // Pods that are not all ready are a warning on their own, a crash loop is worse, and the line names them.
  const pods = (ready: boolean, phase = 'Running', restarts = 0) => [{ name: 'gw-1', nodeId: 'n-c2', phase, ready, restarts, createdAt: old }]
  const withPods = (p: ReturnType<typeof pods>) => buildGraph({ ...fine, services: fine.services.map((s) => (s.id === 'w-gw' ? { ...s, replicas: 1, readyReplicas: p[0].ready ? 1 : 0, pods: p } : s)) }, { ...opts, detail: 'calm' }).nodes.find((n) => n.id === cardId('w-gw'))!
  assert.equal(withPods(pods(true)).data.alert, undefined)
  assert.equal(withPods(pods(false, 'Pending')).data.alert, 'warn')
  assert.equal(withPods(pods(false, 'Pending')).data.note, '1 not ready')
  assert.equal(withPods(pods(true, 'Running', 5)).data.note, '1 crash-looping')
})

test('calm detail: a cluster box says only what is wrong with itself, in words, and keeps a shorter header than Full', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const fine = { ...seed, clusters: seed.clusters.map((c) => ({ ...c, status: 'healthy' as const })), services: seed.services.map((s) => ({ ...s, status: 'healthy' as const, readyReplicas: undefined })), nodes: seed.nodes.map((n) => ({ ...n, status: 'healthy' as const, requested: undefined, podCount: undefined })) }
  const quiet = buildGraph(fine, { ...opts, detail: 'calm' })
  const boxes = quiet.nodes.filter((n) => n.type === 'boundary')
  assert.ok(boxes.length > 1)
  assert.ok(boxes.every((n) => !n.data.alert && !(n.data as { note?: string }).note), 'nothing wrong with any cluster itself')
  // A card that is down is the card's problem: its cluster box does not repeat it (the box keeps rolling the status up into its dot).
  const down = { ...fine, services: fine.services.map((s, i) => (i === 0 ? { ...s, status: 'offline' as const } : s)) }
  const g = buildGraph(down, { ...opts, detail: 'calm' })
  const home = g.nodes.find((n) => n.id === groupId(down.services[0].clusterId))!
  assert.equal(home.data.alert, undefined)
  assert.equal((home.data as { status?: string }).status, 'offline')
  // A cluster that is down itself says so, and its header grows by that line.
  const offline = { ...fine, clusters: fine.clusters.map((c, i) => (i === 0 ? { ...c, status: 'offline' as const } : c)) }
  const og = buildGraph(offline, { ...opts, detail: 'calm' })
  const box = og.nodes.find((n) => n.id === groupId(offline.clusters[0].id))!
  assert.equal(box.data.alert, 'bad')
  assert.equal((box.data as { note?: string }).note, 'Offline')
  const firstCard = og.nodes.find((n) => n.type === 'card' && n.parentId === box.id)!
  assert.equal(firstCard.position.y, CALM_HEADER + CALM_NOTE)
  assert.ok(CALM_HEADER < HEADER, 'Calm needs less header than Full')
})

test('calm detail: the alert helpers rank status, pods and load the way the rest of the app does', () => {
  assert.equal(alertOfStatus('offline'), 'bad')
  assert.equal(alertOfStatus('degraded'), 'warn')
  assert.equal(alertOfStatus('healthy'), undefined)
  assert.equal(alertOfStatus('unknown'), undefined, 'not knowing is not a problem')
  assert.equal(alertOfLoad(69), undefined)
  assert.equal(alertOfLoad(70), 'warn')
  assert.equal(alertOfLoad(90), 'bad')
  assert.equal(alertOfLoad(undefined), undefined)
  assert.equal(alertOfPods(undefined), undefined)
  assert.equal(worstAlert('warn', undefined, 'bad'), 'bad')
  assert.equal(worstAlert(undefined, 'warn'), 'warn')
  assert.equal(worstAlert(), undefined)
  assert.equal(parseDetail('full'), 'full')
  assert.equal(parseDetail(null), 'calm')
  assert.equal(parseDetail('anything'), 'calm')
})

test('calm detail: a machine under pressure says which resource in one line; one that lists its services keeps the room for the list', () => {
  const opts = { view: 'infrastructure' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false, detail: 'calm' as const }
  const calmed = { ...seed, nodes: seed.nodes.map((n) => ({ ...n, status: 'healthy' as const })) }
  const g = buildGraph(calmed, opts)
  const machines = g.nodes.filter((n) => n.data.kind === 'machine')
  assert.ok(machines.length > 2)
  assert.ok(machines.some((n) => !n.data.alert && Number(n.style?.height) === CALM_CARD_H), 'a machine that is fine is the small card')
  assert.ok(machines.filter((n) => n.data.alert).every((n) => Number(n.style?.height) === CALM_CARD_H + CALM_NOTE && !!(n.data as { note?: string }).note), 'one under pressure adds its line')
  const down = { ...calmed, nodes: calmed.nodes.map((n, i) => (i === 0 ? { ...n, status: 'offline' as const } : n)) }
  const loud = buildGraph(down, opts).nodes.find((n) => n.id === cardId(down.nodes[0].id))!
  assert.equal(loud.data.alert, 'bad')
  assert.equal((loud.data as { note?: string }).note, 'Offline')
  assert.equal(Number(loud.style?.height), CALM_CARD_H + CALM_NOTE)
  const pressed = { ...calmed, nodes: calmed.nodes.map((n, i) => (i === 0 ? { ...n, allocatable: { cpu: 10, memoryGb: 10 }, requested: { cpu: 3, memoryGb: 9.2 } } : n)) }
  const hot = buildGraph(pressed, opts).nodes.find((n) => n.id === cardId(pressed.nodes[0].id))!
  assert.equal((hot.data as { note?: string }).note, 'Memory 92% requested')
  const listed = buildGraph(calmed, { ...opts, servicesOnNodes: true }).nodes.filter((n) => n.data.kind === 'machine')
  assert.ok(listed.every((n) => Number(n.style?.height) > CALM_CARD_H), 'the list is something the person asked for')
})

test('networks: pair links that name the same evidence are one network, and the sentence names who else is on it', () => {
  const pair = (a: string, b: string, kind: 'overlay' | 'subnet', via: string): ClusterLink => ({ fromCluster: a, fromName: a.toUpperCase(), toCluster: b, toName: b.toUpperCase(), kind, via, redundancy: 1 })
  const nets = buildNetworks([pair('a', 'b', 'subnet', '10.0.0.0/16'), pair('b', 'c', 'subnet', '10.0.0.0/16'), pair('a', 'b', 'overlay', 'wg0 (wireguard)')])
  assert.equal(nets.length, 2, 'the same prefix is one network however many pairs were confirmed')
  const subnet = nets.find((n) => n.kind === 'subnet')!
  assert.deepEqual(subnet.members.map((m) => m.id), ['a', 'b', 'c'])
  assert.equal(networkSentence(subnet, 'a'), 'Shares subnet 10.0.0.0/16 with B, C')
  assert.equal(networkSentence(nets.find((n) => n.kind === 'overlay')!, 'b'), 'On overlay wg0 (wireguard) with A')
  assert.deepEqual(buildNetworks(undefined), [])
})

test('calm detail: network membership is a fact on the boxes (chips, peers), never a line; Full still draws the pair line', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const links: ClusterLink[] = [
    { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'cl-cloud', toName: 'Cloud', kind: 'subnet', via: '10.30.0.0/16', redundancy: 1 },
    { fromCluster: 'cl-edge-b', fromName: 'Edge B', toCluster: 'cl-cloud', toName: 'Cloud', kind: 'subnet', via: '10.30.0.0/16', redundancy: 1 },
  ]
  const calm = buildGraph(seed, { ...opts, detail: 'calm', clusterLinks: links })
  assert.ok(!calm.edges.some((e) => e.data?.clusterLink), 'no line for a network')
  const box = (id: string) => calm.nodes.find((n) => n.id === groupId(id))!.data as { networks?: { id: string; text: string }[]; peers?: string[] }
  assert.equal(box('cl-cloud').networks?.length, 1, 'one network, however many pairs')
  assert.match(box('cl-cloud').networks![0].text, /^Shares subnet 10\.30\.0\.0\/16 with /)
  assert.deepEqual([...box('cl-edge-a').peers!].sort(), [groupId('cl-cloud'), groupId('cl-edge-b')].sort(), 'every other member rings with it')
  assert.equal(box('cl-region').networks, undefined, 'a cluster on no network says nothing')
  const full = buildGraph(seed, { ...opts, detail: 'full', clusterLinks: links })
  assert.ok(full.edges.some((e) => e.data?.clusterLink), 'Full is as it was')
  assert.equal((full.nodes.find((n) => n.id === groupId('cl-cloud'))!.data as { networks?: unknown }).networks, undefined)
  const header = (g: typeof calm, id: string) => Number(g.nodes.find((n) => n.id === groupId(id))!.style?.height)
  assert.ok(header(calm, 'cl-cloud') > header(buildGraph(seed, { ...opts, detail: 'calm' }), 'cl-cloud'), 'a box with a network chip has room for the chip row')
})

test('cluster links: a confirmed overlay/subnet edge is drawn directly between the two clusters, with no arrowhead', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const overlay: ClusterLink = { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'cl-cloud', toName: 'Cloud', kind: 'overlay', via: 'wg0 (wireguard)' }
  const g = buildGraph(seed, { ...opts, clusterLinks: [overlay] })
  const edge = g.edges.find((e) => e.source === groupId('cl-edge-a') && e.target === groupId('cl-cloud'))
  assert.ok(edge, 'a real edge is drawn straight between the two cluster boxes - no third box involved')
  assert.equal(edge!.data?.clusterLink?.kind, 'overlay')
  assert.equal(edge!.data?.clusterLink?.via, 'wg0 (wireguard)')
  assert.ok(!edge!.markerEnd, 'undirected in reality (two clusters either are or are not joined this way), so no arrowhead')
  assert.equal(edge!.data?.clusterLink?.flowsObserved, undefined, 'no rollup data on this link: nothing to show, not a fabricated 0')
  assert.equal(edge!.data?.clusterLink?.avgRttMs, undefined)
  assert.equal(edge!.data?.clusterLink?.avgLossPct, undefined)

  const overlayWithRollup: ClusterLink = {
    ...overlay,
    flowsObserved: 3,
    avgRttMs: 42,
    avgLossPct: 0.2,
  }
  const gRollup = buildGraph(seed, { ...opts, clusterLinks: [overlayWithRollup] })
  const edgeRollup = gRollup.edges.find((e) => e.source === groupId('cl-edge-a') && e.target === groupId('cl-cloud'))
  assert.equal(edgeRollup?.data?.clusterLink?.flowsObserved, 3, 'the flow-rollup fields pass straight through onto the edge, same as via/redundancy do')
  assert.equal(edgeRollup?.data?.clusterLink?.avgRttMs, 42)
  assert.equal(edgeRollup?.data?.clusterLink?.avgLossPct, 0.2)

  assert.equal(edge!.data?.clusterLink?.encryption, undefined, 'no encryption classification on this fixture link')

  const overlayEncrypted: ClusterLink = { ...overlay, encryption: 'encrypted' }
  const gEnc = buildGraph(seed, { ...opts, clusterLinks: [overlayEncrypted] })
  const edgeEnc = gEnc.edges.find((e) => e.source === groupId('cl-edge-a') && e.target === groupId('cl-cloud'))
  assert.equal(edgeEnc?.data?.clusterLink?.encryption, 'encrypted', 'the encryption classification passes straight through onto the edge, same as the flow-rollup fields do')

  const subnet: ClusterLink = { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'cl-cloud', toName: 'Cloud', kind: 'subnet', via: '10.20.30.0/24' }
  const g2 = buildGraph(seed, { ...opts, clusterLinks: [subnet] })
  const edge2 = g2.edges.find((e) => e.source === groupId('cl-edge-a') && e.target === groupId('cl-cloud'))
  assert.equal(edge2?.data?.clusterLink?.kind, 'subnet')
  assert.equal(edge2?.data?.clusterLink?.via, '10.20.30.0/24')

  // Grouped by tier, cl-edge-a and cl-edge-b collapse into the one far-edge box - no distinct "other end"
  // to draw a line to, so the link is simply not drawn rather than becoming a nonsensical self-loop.
  const sameTier: ClusterLink = { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'cl-edge-b', toName: 'Edge B', kind: 'subnet', via: '10.20.30.0/24' }
  const byTier = buildGraph(seed, { ...opts, groupBy: 'tier' as const, clusterLinks: [sameTier] })
  assert.ok(!byTier.edges.some((e) => e.data?.clusterLink), 'same-tier ends collapse to one box; no self-loop is drawn')

  // A cluster that doesn't exist (or was filtered off the canvas) draws nothing either.
  const orphan: ClusterLink = { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'does-not-exist', toName: 'Ghost', kind: 'overlay', via: 'wg0 (wireguard)' }
  const g3 = buildGraph(seed, { ...opts, clusterLinks: [orphan] })
  assert.ok(!g3.edges.some((e) => e.data?.clusterLink), 'an end not on the canvas draws nothing')
})

test('cluster links: a dependency edge whose own tunnelLink matches the overlay link suppresses the standalone line, but only when it is the sole match', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const overlay: ClusterLink = { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'cl-cloud', toName: 'Cloud', kind: 'overlay', via: 'wg0 (wireguard)' }
  const tunnelLink = { fromCluster: 'cl-edge-a', toCluster: 'cl-cloud', via: 'wg0 (wireguard)', redundancy: 1, encryption: 'encrypted' as const }

  // Zero dependencies cross the tunnel: nothing to suppress it with, so the standalone overlay line still shows.
  const g0 = buildGraph({ ...seed, dependencies: [] }, { ...opts, clusterLinks: [overlay] })
  assert.ok(g0.edges.some((e) => e.data?.clusterLink), 'no annotated dependency edge exists yet - the overlay line is the only evidence, so it must stay')

  // Exactly one dependency crosses it: that edge already shows everything the standalone line would, so
  // the standalone line is redundant and gets suppressed.
  const oneDep = seenDep({ id: 'dep-tun-1', from: 'w-orch', to: 'w-infer-a', iface: 'wg0', tunnelLink })
  const g1 = buildGraph({ ...seed, dependencies: [oneDep] }, { ...opts, clusterLinks: [overlay] })
  assert.ok(!g1.edges.some((e) => e.data?.clusterLink), 'the sole crossing dependency already carries the tunnel info - no need for a second, redundant line')
  const depEdge1 = g1.edges.find((e) => e.id === 'dep-tun-1')
  assert.equal(depEdge1?.data?.tunnelLink?.fromCluster, 'cl-edge-a')
  assert.equal(depEdge1?.data?.tunnelLink?.toCluster, 'cl-cloud')
  assert.equal(depEdge1?.data?.tunnelLink?.via, 'wg0 (wireguard)')
  assert.equal(depEdge1?.data?.tunnelLink?.encryption, 'encrypted')

  // Two distinct dependencies cross it: neither one alone is the full picture, so the standalone line
  // stays alongside both annotated dependency edges.
  const twoDeps = [oneDep, seenDep({ id: 'dep-tun-2', from: 'w-sensor-a', to: 'w-registry', iface: 'wg0', tunnelLink })]
  const g2 = buildGraph({ ...seed, dependencies: twoDeps }, { ...opts, clusterLinks: [overlay] })
  assert.ok(g2.edges.some((e) => e.data?.clusterLink), 'two distinct dependencies share the tunnel - no single edge fully represents it, so the standalone line stays')
  assert.equal(g2.edges.find((e) => e.id === 'dep-tun-1')?.data?.tunnelLink?.via, 'wg0 (wireguard)')
  assert.equal(g2.edges.find((e) => e.id === 'dep-tun-2')?.data?.tunnelLink?.via, 'wg0 (wireguard)')

  // A subnet link never produces a tunnelLink match (only overlay tunnels do), so a dependency carrying
  // one never suppresses a subnet-kind standalone line even if the clusters happen to coincide.
  const subnet: ClusterLink = { fromCluster: 'cl-edge-a', fromName: 'Edge A', toCluster: 'cl-cloud', toName: 'Cloud', kind: 'subnet', via: '10.20.30.0/24' }
  const g3 = buildGraph({ ...seed, dependencies: [oneDep] }, { ...opts, clusterLinks: [subnet] })
  assert.ok(g3.edges.some((e) => e.data?.clusterLink?.kind === 'subnet'), 'a subnet link is never fully represented by a tunnelLink match, so it is never suppressed')
})

test("edge interface capacity reaches EdgeData alongside its throughput, never guessed when the caller's own nodes disagree", () => {
  // w-gw runs on n-c2 and n-c3 (both cl-cloud) - give both the same eth0 speed first.
  const dep = seenDep({ id: 'dep-iface', from: 'w-gw', to: 'w-orch', iface: 'eth0' })
  const withSpeed = (mbps: number) => (n: (typeof seed.nodes)[number]) =>
    n.id === 'n-c2' || n.id === 'n-c3' ? { ...n, networkInterfaces: [{ name: 'eth0', kind: 'ethernet' as const, speedMbps: mbps }] } : n
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }

  const agree = { ...seed, dependencies: [dep], nodes: seed.nodes.map(withSpeed(1000)) }
  const g = buildGraph(agree, opts)
  assert.equal(g.edges.find((e) => e.id === 'dep-iface')?.data?.ifaceSpeedMbps, 1000, "both of w-gw's nodes agree on eth0's speed")

  // One of the two disagrees (different hardware behind two replicas of the same service) - ambiguous, so
  // this must come back undefined rather than guessing either node's number.
  const disagree = {
    ...seed,
    dependencies: [dep],
    nodes: seed.nodes.map((n) => (n.id === 'n-c2' ? withSpeed(1000)(n) : n.id === 'n-c3' ? withSpeed(100)(n) : n)),
  }
  const g2 = buildGraph(disagree, opts)
  assert.equal(g2.edges.find((e) => e.id === 'dep-iface')?.data?.ifaceSpeedMbps, undefined)

  // A declared-only (never-observed) dependency has no iface at all, so there is nothing to resolve.
  const declaredOnly = { ...seed, dependencies: [{ ...dep, iface: undefined, via: undefined, sources: ['declared'] }], nodes: seed.nodes.map(withSpeed(1000)) }
  const g3 = buildGraph(declaredOnly, opts)
  assert.equal(g3.edges.find((e) => e.id === 'dep-iface')?.data?.ifaceSpeedMbps, undefined)
})

test('service card: the pod rail reserves one row, and its summary replaces the "x/y ready" chip', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const withPods = (pods?: { name: string; phase: string; ready: boolean; nodeId?: string }[]) => ({
    ...seed,
    services: seed.services.map((s) => (s.id === 'w-gw' ? { ...s, replicas: 2, readyReplicas: 1, pods } : s)),
  })
  const card = (g: ReturnType<typeof buildGraph>) => g.nodes.find((n) => n.id === cardId('w-gw'))!

  // No per-pod facts (older agent tier, or none up): no rail and no row for it; the chip says what is not ready.
  const none = card(buildGraph(withPods(), opts))
  assert.equal(none.data.pods, undefined)
  assert.equal(none.data.notReady, '1/2 ready')

  // With them: the rail carries the summary (so no chip) and takes the one row the chip would have.
  const pods = [
    { name: 'gw-1', nodeId: 'n-c2', phase: 'Running', ready: true },
    { name: 'gw-2', nodeId: 'n-c3', phase: 'Pending', ready: false },
  ]
  const some = card(buildGraph(withPods(pods), opts))
  assert.equal(some.data.pods?.ready, 1)
  assert.equal(some.data.pods?.total, 2)
  assert.equal(some.data.notReady, undefined)
  assert.equal(Number(some.style?.height), Number(none.style?.height))
})

test('resyncNodes: a node mid-drag is left untouched, others keep position until re-parented', () => {
  const node = (id: string, overrides: Record<string, unknown> = {}) =>
    ({ id, type: 'card', position: { x: 0, y: 0 }, parentId: 'g:cl-a', data: {}, ...overrides }) as unknown as ReturnType<typeof buildGraph>['nodes'][number]

  // A node React Flow is actively dragging keeps its exact object (not just its position) - simulated here
  // with a freshly-dragged position (10, 20) and a freshly computed one elsewhere (99, 99): if the guard
  // were merging instead of skipping, the assertions below on identity and on the untouched position would
  // both fail.
  const dragging = node('c:svc-a', { position: { x: 10, y: 20 }, dragging: true })
  const next = [node('c:svc-a', { position: { x: 99, y: 99 } })]
  const out = resyncNodes([dragging], next, new Set())
  assert.equal(out[0], dragging, 'the dragging node object itself is returned, not a copy')
  assert.deepEqual(out[0].position, { x: 10, y: 20 }, 'its in-progress drag position is not overwritten')

  // A node that isn't being dragged keeps its on-screen position across a poll as long as its parent is
  // unchanged - this is what lets a completed manual drag "stick" instead of snapping back on the next poll.
  const settled = node('c:svc-b', { position: { x: 5, y: 5 } })
  const samePlaceholder = resyncNodes([settled], [node('c:svc-b', { position: { x: 50, y: 50 } })], new Set())
  assert.deepEqual(samePlaceholder[0].position, { x: 5, y: 5 }, 'position sticks when the parent is unchanged')

  // A changed parent (e.g. toggling namespace sub-boxes) always takes the freshly computed position - an
  // old, differently-relative position would otherwise land the card in the wrong spot.
  const reparented = resyncNodes([settled], [node('c:svc-b', { position: { x: 50, y: 50 }, parentId: 'g:ns-x' })], new Set())
  assert.deepEqual(reparented[0].position, { x: 50, y: 50 }, 'a new parent always takes the fresh position')

  // The highlighted set is applied to the result regardless of which branch a node took above, and covers
  // more than one id at once - the multi-select case this now also has to serve, not just a single click.
  const sel = resyncNodes([dragging, settled], [node('c:svc-a', { position: { x: 99, y: 99 } }), node('c:svc-b', { position: { x: 50, y: 50 } })], new Set(['c:svc-b']))
  assert.equal(sel.find((n) => n.id === 'c:svc-b')!.selected, true)
  assert.equal(sel.find((n) => n.id === 'c:svc-a')!.selected, undefined, 'the dragging node is returned as-is, selected flag included')

  const multi = resyncNodes([], [node('c:svc-a', {}), node('c:svc-b', {}), node('c:svc-c', {})], new Set(['c:svc-a', 'c:svc-c']))
  assert.equal(multi.find((n) => n.id === 'c:svc-a')!.selected, true)
  assert.equal(multi.find((n) => n.id === 'c:svc-b')!.selected, false)
  assert.equal(multi.find((n) => n.id === 'c:svc-c')!.selected, true)
})

test('syncSelected: a node mid-drag is left untouched, and nothing is re-allocated when the selection already matches', () => {
  const node = (id: string, overrides: Record<string, unknown> = {}) =>
    ({ id, type: 'card', position: { x: 0, y: 0 }, parentId: 'g:cl-a', data: {}, ...overrides }) as unknown as ReturnType<typeof buildGraph>['nodes'][number]

  // A node React Flow is actively dragging keeps its exact object, selected flag included - same reason
  // resyncNodes does (graph.ts): a write from outside the gesture can fight React Flow's own drag tracking,
  // which is what produced a real "Maximum update depth exceeded" crash (React error #185) when a click or
  // drag caught a node mid-gesture.
  const dragging = node('c:svc-a', { dragging: true, selected: false })
  const other = node('c:svc-b', { selected: false })
  const out = syncSelected([dragging, other], new Set(['c:svc-a', 'c:svc-b']))
  assert.equal(out[0], dragging, 'the dragging node is returned completely unchanged, even though the wanted set includes it')
  assert.equal(out[0].selected, false, 'its selected flag is not touched while dragging')
  assert.equal(out[1].selected, true, 'a node that is not dragging is updated normally')

  // When every node's .selected already matches what's wanted, the exact same array (and every element in
  // it) comes back - not a fresh array with identical contents. This is the other half of the same fix:
  // handing a caller's setNodes a "new" array on every no-op change is what turned the drag/select fight
  // above into an unbounded loop instead of settling after one pass.
  const settled = [node('c:svc-a', { selected: true }), node('c:svc-b', { selected: false })]
  const noop = syncSelected(settled, new Set(['c:svc-a']))
  assert.equal(noop, settled, 'nothing changed, so the identical array reference is returned')

  // A real change still only re-allocates the node(s) that actually changed - c:svc-b here - not the whole
  // array from scratch.
  const partial = syncSelected(settled, new Set(['c:svc-a', 'c:svc-b']))
  assert.notEqual(partial, settled, 'the array itself is new, since something in it changed')
  assert.equal(partial[0], settled[0], 'a node whose selected flag was already correct keeps its own object')
  assert.notEqual(partial[1], settled[1], 'the node that actually changed gets a fresh object')
  assert.equal(partial[1].selected, true)
})

test('syncPickEligibility: dims ineligible nodes, leaves eligible ones alone, is a no-op once settled', () => {
  const node = (id: string, overrides: Record<string, unknown> = {}) =>
    ({ id, type: 'card', position: { x: 0, y: 0 }, parentId: 'g:cl-a', data: {}, ...overrides }) as unknown as ReturnType<typeof buildGraph>['nodes'][number]

  // null (pick mode off) clears the class from every node, same as syncSelected's "nothing highlighted" case.
  const withStaleClass = [node('c:svc-a', { className: 'pick-ineligible' })]
  assert.equal(syncPickEligibility(withStaleClass, null)[0].className, undefined)

  const a = node('c:svc-a')
  const b = node('c:svc-b')
  const out = syncPickEligibility([a, b], new Set(['c:svc-a']))
  assert.equal(out[0].className, undefined, 'eligible nodes are left alone')
  assert.equal(out[1].className, 'pick-ineligible', 'everything outside the eligible set gets dimmed')

  // A node mid-drag is left completely untouched, same reason syncSelected skips one - a write from outside
  // React Flow's own drag tracking here would fight the gesture the same way it did for `selected`.
  const dragging = node('c:svc-c', { dragging: true, className: undefined })
  const out2 = syncPickEligibility([dragging], new Set([]))
  assert.equal(out2[0], dragging, 'the dragging node is returned completely unchanged')
  assert.equal(out2[0].className, undefined, 'its class is not touched while dragging, even though it would otherwise become ineligible')

  // Once settled, calling this again with the same eligibility returns the exact same array - not a fresh
  // one with identical contents - so a steady pick-mode toggle never forces an extra render.
  const settled = syncPickEligibility([node('c:svc-a'), node('c:svc-b')], new Set(['c:svc-a']))
  const noop = syncPickEligibility(settled, new Set(['c:svc-a']))
  assert.equal(noop, settled, 'nothing changed, so the identical array reference is returned')
})

test('applyGraphUpdate: an explicit change (a filter, a view toggle) gets a clean fresh layout, not the stale poll-time merge', () => {
  const node = (id: string, overrides: Record<string, unknown> = {}) =>
    ({ id, type: 'card', position: { x: 0, y: 0 }, parentId: 'g:cl-a', data: {}, ...overrides }) as unknown as ReturnType<typeof buildGraph>['nodes'][number]

  // Same cluster box, three cards. The person drags svc-a somewhere else, then applies a filter that
  // removes svc-c: svc-a and svc-b both keep the SAME parent, so resyncNodes' own "parent changed" escape
  // hatch never fires for them - exactly the case that used to leave a gap where svc-c used to sit until
  // the person left the page and came back.
  const prev = [node('c:svc-a', { position: { x: 500, y: 500 } }), node('c:svc-b', { position: { x: 5, y: 5 } }), node('c:svc-c', { position: { x: 50, y: 5 } })]
  // buildGraph's own fresh repack for the filtered-down pair - deliberately different from prev's stale
  // coordinates, the way a real repack would be once svc-c is no longer taking up space.
  const freshlyPacked = [node('c:svc-a', { position: { x: 5, y: 5 } }), node('c:svc-b', { position: { x: 50, y: 5 } })]

  // An ordinary poll (explicit: false) is unchanged from plain resyncNodes - the manual drag sticks.
  const polled = applyGraphUpdate(prev, freshlyPacked, new Set(), false)
  assert.deepEqual(polled.find((n) => n.id === 'c:svc-a')!.position, { x: 500, y: 500 }, 'a background poll still preserves the manual drag')

  // An explicit change (the filter that produced this exact freshlyPacked output) takes buildGraph's fresh
  // positions outright, for every survivor - not just the ones that are brand new.
  const filtered = applyGraphUpdate(prev, freshlyPacked, new Set(), true)
  assert.deepEqual(filtered.find((n) => n.id === 'c:svc-a')!.position, { x: 5, y: 5 }, 'an explicit filter change re-lays out even a node that already existed and kept its parent')
  assert.deepEqual(filtered.find((n) => n.id === 'c:svc-b')!.position, { x: 50, y: 5 })
  assert.equal(filtered.length, 2, 'the filtered-out card is gone, same as a plain merge would give')

  // The highlighted set still applies on the explicit path, same as the merge path.
  const withHighlight = applyGraphUpdate(prev, freshlyPacked, new Set(['c:svc-b']), true)
  assert.equal(withHighlight.find((n) => n.id === 'c:svc-a')!.selected, false)
  assert.equal(withHighlight.find((n) => n.id === 'c:svc-b')!.selected, true)
})

test('applyGraphUpdate: an explicit change still leaves a node React Flow is actively dragging completely untouched', () => {
  const node = (id: string, overrides: Record<string, unknown> = {}) =>
    ({ id, type: 'card', position: { x: 0, y: 0 }, parentId: 'g:cl-a', data: {}, ...overrides }) as unknown as ReturnType<typeof buildGraph>['nodes'][number]

  // svc-a is mid-drag (React Flow's own `dragging` flag) exactly when an explicit change (a filter, a view
  // toggle) lands - the same race resyncNodes/syncSelected already guard against, but here on the explicit
  // path specifically, which used to hand every survivor a brand new object unconditionally (see graph.ts's
  // applyGraphUpdate doc comment - this is what could desync React Flow's own drag tracking and produce a
  // real "Maximum update depth exceeded" crash, React error #185). It must come back as the exact same
  // object, not buildGraph's freshly computed position, even though every other survivor does get relaid out.
  const dragging = { ...node('c:svc-a', { position: { x: 500, y: 500 } }), dragging: true }
  const prev = [dragging, node('c:svc-b', { position: { x: 5, y: 5 } })]
  const freshlyPacked = [node('c:svc-a', { position: { x: 5, y: 5 } }), node('c:svc-b', { position: { x: 50, y: 5 } })]

  const filtered = applyGraphUpdate(prev, freshlyPacked, new Set(), true)
  assert.equal(filtered.find((n) => n.id === 'c:svc-a'), dragging, 'the dragging node comes back as the exact same object, position and all')
  assert.deepEqual(filtered.find((n) => n.id === 'c:svc-b')!.position, { x: 50, y: 5 }, 'every other survivor still gets the fresh explicit layout')
})

test('applyGraphUpdate and resyncNodes keep the very same nodes when the fresh layout changes nothing, and sameLayout tells a lines-only toggle from a relayout', () => {
  const node = (id: string, overrides: Record<string, unknown> = {}) =>
    ({ id, type: 'card', position: { x: 5, y: 5 }, parentId: 'g:cl-a', style: { width: 100, height: 40 }, data: { label: id }, ...overrides }) as unknown as ReturnType<typeof buildGraph>['nodes'][number]
  // On the canvas React Flow has added its own measured size; the freshly built node never carries it.
  const onCanvas = [{ ...node('c:a'), selected: false, measured: { width: 100, height: 40 } }, { ...node('c:b', { position: { x: 60, y: 5 } }), selected: false, measured: { width: 100, height: 40 } }]
  const same = [node('c:a'), node('c:b', { position: { x: 60, y: 5 } })]
  assert.equal(applyGraphUpdate(onCanvas, same, new Set(), true), onCanvas, 'an explicit pass that changes nothing returns the canvas list itself')
  assert.equal(resyncNodes(onCanvas, same, new Set()), onCanvas, 'so does a poll')
  const moved = applyGraphUpdate(onCanvas, [node('c:a'), node('c:b', { position: { x: 70, y: 5 } })], new Set(), true)
  assert.equal(moved[0], onCanvas[0], 'a node that did not change keeps its object')
  assert.notEqual(moved[1], onCanvas[1], 'one that moved is replaced')
  assert.deepEqual(moved[1].position, { x: 70, y: 5 })
  const selected = resyncNodes(onCanvas, same, new Set(['c:a']))
  assert.equal(selected[0].selected, true, 'a changed highlight still lands')
  assert.equal(selected[1], onCanvas[1])
  assert.equal(sameLayout(onCanvas, same), true, 'the same boxes at the same size: only lines changed')
  assert.equal(sameLayout(onCanvas, [same[0], node('c:b', { position: { x: 70, y: 5 } })]), false, 'a box that moved')
  assert.equal(sameLayout(onCanvas, [same[0], node('c:b', { position: { x: 60, y: 5 }, style: { width: 120, height: 40 } })]), false, 'a box that grew')
  assert.equal(sameLayout(onCanvas, [same[0]]), false, 'a box that went')
  assert.equal(sameLayout(onCanvas, [same[0], node('c:b', { position: { x: 60, y: 5 }, parentId: 'g:cl-b' })]), false, 'a box under another parent')
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

test('cluster stats: singular and plural, and "not fully up" counts only the services that are drawn', () => {
  assert.equal(count(1, 'service'), '1 service')
  assert.equal(count(0, 'node'), '0 nodes')
  const one = inCluster[0]
  const hidden: Service = { ...inCluster[1], id: 'w-istiod', name: 'istiod', replicas: 2, readyReplicas: 0, mesh: { mesh: 'istio', controlPlane: true, source: 'workload' } }
  const t = {
    ...seed,
    clusters: seed.clusters.filter((c) => c.id === one.clusterId),
    nodes: seed.nodes.filter((n) => n.clusterId === one.clusterId),
    services: [{ ...one, replicas: 1, readyReplicas: 1 }, hidden],
    dependencies: [],
  }
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: false, devices: false }
  const box = buildGraph(t, opts).nodes.find((n) => n.id === groupId(one.clusterId))!.data as { stats: string; load?: { unready: number; services: number } }
  assert.equal(box.stats, '1 service', 'one card, not "1 services"')
  assert.equal(box.load?.unready, 0, 'the hidden mesh workload is not counted as "not fully up"')
  assert.equal(box.load?.services, 1)
  const infra = buildGraph(t, { ...opts, view: 'infrastructure' }).nodes.find((n) => n.id === groupId(one.clusterId))!.data as { load?: { services: number } }
  assert.equal(infra.load?.services, 2, 'the Infrastructure view draws no service cards, so it keeps the whole cluster')
})

test('middleTruncate: keeps the start and the end of a long name, leaves a short one whole', () => {
  assert.equal(middleTruncate('worker-1', 24), 'worker-1')
  const long = 'ip-10-0-12-34.eu-west-1.compute.internal'
  const cut = middleTruncate(long, 24)
  assert.equal(cut.length, 24)
  assert.ok(cut.startsWith('ip-10-0-12') && cut.endsWith(long.slice(-11)) && cut.includes('\u2026'))
  assert.equal(middleTruncate('abcdef', 3), 'abcdef', 'a budget too small to be useful leaves the name alone')
})

test('filterSummary: says what the filter is doing in a few words, nothing when it does nothing', () => {
  assert.equal(filterSummary({ clusters: [], apps: [], kinds: [] }), undefined)
  assert.equal(filterSummary({ clusters: [], apps: [], kinds: ['Deployment'] }), 'Deployments only')
  assert.equal(filterSummary({ clusters: ['a'], apps: ['x', 'y'], kinds: ['Job', 'DaemonSet'] }), '2 kinds \u00b7 1 cluster \u00b7 2 applications')
})

test('narrowFitZoom: on a phone the widest box sets the fit, elsewhere the whole-graph fit stands', () => {
  assert.equal(narrowFitZoom(1440, 0.5, 400), 0.5, 'a desktop-width canvas is untouched')
  assert.ok(Math.abs(narrowFitZoom(390, 0.5, 400) - (390 * 0.92) / 400) < 1e-9, 'the widest box fills the phone')
  assert.equal(narrowFitZoom(390, 0.5, 200), 1, 'never magnified past 1')
  assert.equal(narrowFitZoom(390, 0.9, 800), 0.9, 'a fit that is already larger stays')
  assert.equal(narrowFitZoom(390, 0.5, 0), 0.5, 'nothing measured yet')
})

console.log(failed ? `\n${failed} FAILED` : '\nall passed')
process.exit(failed ? 1 : 0)

test('calm detail: every dependency across two boxes is ONE bundle (with a count); the calls are quiet "detail" lines the focus brings forward', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false }
  const a = seed.services[0]
  const sameBox = seed.services.find((s) => s.id !== a.id && s.clusterId === a.clusterId)
  const others = seed.services.filter((s) => s.clusterId !== a.clusterId)
  assert.ok(others.length >= 2 && sameBox, 'the seed has two services across the same pair of boxes')
  const [b, c] = others
  const t = { ...seed, dependencies: [seenDep({ id: 'x1', from: a.id, to: b.id }), seenDep({ id: 'x2', from: c.id, to: a.id }), seenDep({ id: 'in', from: a.id, to: sameBox!.id })] }
  const calm = buildGraph(t, { ...opts, detail: 'calm' })
  const bundles = calm.edges.filter((e) => e.data?.role === 'bundle')
  const detail = calm.edges.filter((e) => e.data?.role === 'detail')
  assert.ok(bundles.length >= 1 && bundles.every((e) => e.id.startsWith('bundle:') && e.data?.aggregated), 'one aggregated line per pair of boxes')
  assert.equal(new Set(bundles.map((e) => [e.source, e.target].sort().join('|'))).size, bundles.length, 'never two lines for one pair of boxes')
  assert.deepEqual(detail.map((e) => e.id).sort(), ['x1', 'x2'].sort(), 'every call across boxes is kept, as a detail line')
  assert.ok(detail.every((e) => e.data?.focusIds?.includes(e.source) && e.data.focusIds.includes(e.target)), 'the hover of either end brings it forward')
  assert.equal(calm.edges.find((e) => e.id === 'in')!.data?.role, undefined, 'a call inside a box is drawn as before')
  const two = buildGraph({ ...t, dependencies: [...t.dependencies, seenDep({ id: 'x3', from: b.id, to: a.id })] }, { ...opts, detail: 'calm' })
  const ab = two.edges.find((e) => e.data?.role === 'bundle' && e.data.focusIds?.includes(a.id) && e.data.focusIds.includes(b.id))
  assert.ok(ab && ab.data?.count === 2 && ab.label === '2 dependencies', 'two calls across the same two boxes are one line that says 2')
  const full = buildGraph(t, { ...opts, detail: 'full' })
  assert.ok(!full.edges.some((e) => e.data?.role), 'Full is unchanged: one line per dependency')
})

test('calm detail: only a seen link losing connection attempts is a problem, on its line and on the bundle that holds it', () => {
  const opts = { view: 'application' as const, groupBy: 'cluster' as const, servicesOnNodes: false, links: true, devices: false, detail: 'calm' as const }
  const a = seed.services[0]
  const b = seed.services.find((s) => s.clusterId !== a.clusterId)!
  const lossy = [{ id: 'p', fromCluster: a.clusterId, fromName: 'A', host: '1.2.3.4', port: 443, toCluster: b.clusterId, toName: 'B', source: 'observed' as const, rttMinMs: 1, rttP50Ms: 2, rttP95Ms: 3, lossPct: 6, samples: 9, at: SEEN }]
  const seen = buildGraph({ ...seed, dependencies: [seenDep({ id: 'x1', from: a.id, to: b.id })] }, { ...opts, paths: lossy })
  assert.equal(seen.edges.find((e) => e.id === 'x1')!.data?.problem, true)
  assert.equal(seen.edges.find((e) => e.data?.role === 'bundle')!.data?.problem, true)
  const declared = buildGraph({ ...seed, dependencies: [{ ...seenDep({ id: 'x1', from: a.id, to: b.id }), sources: ['declared'], stats: undefined, via: undefined }] }, { ...opts, paths: lossy })
  assert.ok(declared.edges.every((e) => !e.data?.problem), 'nothing was measured on a declared link, so it is never a problem')
  const fine = buildGraph({ ...seed, dependencies: [seenDep({ id: 'x1', from: a.id, to: b.id })] }, { ...opts, paths: lossy.map((p) => ({ ...p, lossPct: 0.2 })) })
  assert.ok(fine.edges.every((e) => !e.data?.problem))
})
