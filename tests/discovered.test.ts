import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mergeDiscovered, normalizeServerState, type ServerState } from '../src/lib/discovered'
import type { Agent, AuditEvent, Cluster, Model, Suggestion } from '../src/lib/types'

// Minimal fixtures - only the fields mergeDiscovered/mergeList actually read.
const base = { orgId: 'o', source: 'discovered' as const, agentId: 'a1' }
const cluster = (id: string, over: Partial<Cluster> = {}) =>
  ({ ...base, id, name: id, siteId: '', tier: 'edge', distribution: 'k3s', version: 'v1.30', provider: '', region: '', status: 'healthy', labels: {}, ...over }) as Cluster

const serverAgent = (id: string, over: Record<string, unknown> = {}) => ({
  id, orgId: 'o', name: id, version: '1.0.0', accessTier: 'full', installedTier: 'full', tierCap: 'full', status: 'approved',
  fingerprint: 'fp', requestedAt: '2024-01-01T00:00:00Z', connected: true, synced: true, modules: [], ...over,
})

const doc = (over: Partial<ServerState> = {}): ServerState =>
  normalizeServerState({
    generatedAt: '2024-01-01T00:00:01Z',
    agents: [],
    topology: { clusters: [], nodes: [], namespaces: [], services: [], suggestions: [], dependencies: [], externalEndpoints: [], paths: [] },
    auditLog: [],
    tombstones: [],
    ...over,
  })

const model = (over: Partial<Model> = {}): Model =>
  ({
    clusters: [], nodes: [], namespaces: [], services: [], devices: [], dependencies: [], applications: [], sites: [], siteLinks: [],
    externalEndpoints: [], agents: [], suggestions: [], auditLog: [], savedViews: [], refs: {}, ...over,
  }) as unknown as Model

test('a cluster the server reports back unchanged keeps its old object identity', () => {
  const c = cluster('c1')
  const m = model({ clusters: [c] })
  const state = doc({ topology: { clusters: [{ ...c }], nodes: [], namespaces: [], services: [], suggestions: [], dependencies: [], externalEndpoints: [], paths: [] } })
  const { clusters } = mergeDiscovered(m, state)
  assert.equal(clusters, m.clusters, 'nothing changed, so the whole array keeps its identity')
  assert.equal(clusters![0], c, 'the unchanged cluster keeps its own identity too')
})

test('a cluster whose reported fields actually changed gets a fresh object; its siblings do not', () => {
  const c1 = cluster('c1')
  const c2 = cluster('c2')
  const m = model({ clusters: [c1, c2] })
  const state = doc({
    topology: { clusters: [{ ...c1 }, { ...c2, status: 'degraded' }], nodes: [], namespaces: [], services: [], suggestions: [], dependencies: [], externalEndpoints: [], paths: [] },
  })
  const { clusters } = mergeDiscovered(m, state)
  assert.notEqual(clusters, m.clusters, 'something changed, so the array is rebuilt')
  assert.equal(clusters![0], c1, 'the unaffected cluster keeps its identity')
  assert.notEqual(clusters![1], c2, 'the changed cluster is a new object')
  assert.equal((clusters![1] as Cluster).status, 'degraded')
})

test('a human override on a cluster is preserved and does not defeat the identity check', () => {
  const c = cluster('c1', { overrides: { name: { value: 'Prod', by: 'me', at: '2024-01-01T00:00:00Z' } } })
  const m = model({ clusters: [c] })
  const state = doc({ topology: { clusters: [{ ...c, overrides: undefined }], nodes: [], namespaces: [], services: [], suggestions: [], dependencies: [], externalEndpoints: [], paths: [] } })
  const { clusters } = mergeDiscovered(m, state)
  assert.equal(clusters, m.clusters)
  assert.equal((clusters![0] as Cluster).overrides, c.overrides)
})

test("an agent report that hasn't moved keeps its identity, and the agents array keeps its identity too", () => {
  const a: Agent = { id: 'a1', orgId: 'o', name: 'agent-1', version: '1.0.0', accessTier: 'full', status: 'approved', fingerprint: 'fp', modules: [], installedTier: 'full', tierCap: 'full', requestedAt: '2024-01-01T00:00:00Z', connected: true } as Agent
  const m = model({ agents: [a] })
  const state = doc({ agents: [serverAgent('a1', { name: 'agent-1' }) as never] })
  const { agents } = mergeDiscovered(m, state)
  assert.equal(agents, m.agents)
  assert.equal(agents![0], a)
})

test('an agent whose status changed gets a fresh object and the array is rebuilt', () => {
  const a: Agent = { id: 'a1', orgId: 'o', name: 'agent-1', version: '1.0.0', accessTier: 'full', status: 'approved', fingerprint: 'fp', modules: [], installedTier: 'full', tierCap: 'full', requestedAt: '2024-01-01T00:00:00Z', connected: true } as Agent
  const m = model({ agents: [a] })
  const state = doc({ agents: [serverAgent('a1', { name: 'agent-1', connected: false }) as never] })
  const { agents } = mergeDiscovered(m, state)
  assert.notEqual(agents, m.agents)
  assert.notEqual(agents![0], a)
  assert.equal((agents![0] as Agent).connected, false)
})

test('audit events already known keep the log at its old identity; a new one appends without touching old entries', () => {
  const events: AuditEvent[] = [{ id: 'e1', orgId: 'o', at: '2024-01-01T00:00:00Z', kind: 'note', message: 'hello' } as unknown as AuditEvent]
  const m = model({ auditLog: events })
  const seenAgain = doc({ auditLog: events as never })
  const nochange = mergeDiscovered(m, seenAgain)
  assert.equal(nochange.auditLog, m.auditLog, 'no new events, so the log keeps its identity')

  const withNew = doc({ auditLog: [...events, { id: 'e2', orgId: 'o', at: '2024-01-01T00:00:01Z', kind: 'note', message: 'world' }] as never })
  const changed = mergeDiscovered(m, withNew)
  assert.notEqual(changed.auditLog, m.auditLog)
  assert.equal(changed.auditLog![0], events[0], 'the old entry keeps its identity')
  assert.equal(changed.auditLog!.length, 2)
})

test('an open suggestion the server keeps repeating verbatim keeps its identity; the list keeps its identity too', () => {
  const s: Suggestion = { id: 'sg-1', orgId: 'o', status: 'open', title: 'Group these', detail: 'x' } as unknown as Suggestion
  const m = model({ suggestions: [s] })
  const state = doc({ topology: { clusters: [], nodes: [], namespaces: [], services: [], suggestions: [{ id: 'sg-1', title: 'Group these', detail: 'x' } as never], dependencies: [], externalEndpoints: [], paths: [] } })
  const { suggestions } = mergeDiscovered(m, state)
  assert.equal(suggestions, m.suggestions)
  assert.equal(suggestions![0], s)
})
