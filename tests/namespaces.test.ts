import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildNamespaceRows, excludedWords, namespaceMesh, rowMatches, scopeOf } from '../src/lib/namespaces'
import type { Agent, Cluster, Namespace, Service } from '../src/lib/types'

const cl = (over: Partial<Cluster> = {}) => ({ id: 'c1', name: 'edge', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Cluster
const ns = (name: string, over: Partial<Namespace> = {}) => ({ id: `ns-${name}`, clusterId: 'c1', name, source: 'discovered', state: 'live', labels: {}, orgId: 'o', ...over }) as Namespace
const svc = (name: string, namespace: string, over: Partial<Service> = {}) => ({ id: `s-${namespace}-${name}`, clusterId: 'c1', name, namespace, kind: 'Deployment', source: 'discovered', state: 'live', orgId: 'o', ...over }) as Service
const ag = (over: Partial<Agent> = {}) => ({ id: 'a1', clusterId: 'c1', status: 'approved', kubernetesVersion: 'v1.30', ...over }) as Agent

test('one row per namespace, alphabetical, with workload counts and how many are reachable from outside', () => {
  const rows = buildNamespaceRows({
    clusters: [cl()],
    namespaces: [ns('shop'), ns('data')],
    services: [svc('cart', 'shop', { exposure: 'ingress' }), svc('pay', 'shop', { exposure: 'internal' }), svc('db', 'data', { kind: 'StatefulSet' })],
    agents: [ag()],
  })
  assert.deepEqual(rows.map((r) => (r.kind === 'namespace' ? r.name : 'x')), ['data', 'shop'])
  const shop = rows[1]
  assert.equal(shop.kind === 'namespace' && shop.workloads, 2)
  assert.equal(shop.kind === 'namespace' && shop.exposed, 1)
  assert.equal(shop.kind === 'namespace' && shop.kinds, '2 Deployments')
  assert.equal(rows[0].kind === 'namespace' && rows[0].kinds, '1 StatefulSet')
  assert.equal(rows[0].kind === 'namespace' && rows[0].observation?.kind, 'live')
})

test('a namespace only its workloads mention is still listed; a deleted one is not', () => {
  const rows = buildNamespaceRows({ clusters: [cl()], namespaces: [ns('gone', { deletedAt: '2026-01-01T00:00:00Z' })], services: [svc('x', 'orphan')], agents: [] })
  assert.deepEqual(rows.map((r) => (r.kind === 'namespace' ? r.name : '')), ['orphan'])
})

test('a narrowed scope adds one row per cluster that counts what was left out, and never names anything', () => {
  const rows = buildNamespaceRows({
    clusters: [cl()],
    namespaces: [ns('shop')],
    services: [svc('cart', 'shop')],
    agents: [ag({ scope: { description: 'namespaces shop', namespaces: 12, inScope: 3 } })],
  })
  const last = rows[rows.length - 1]
  assert.equal(last.kind, 'excluded')
  assert.equal(last.kind === 'excluded' && last.count, 9)
  assert.equal(rows.filter((r) => r.kind === 'excluded').length, 1)
  assert.match(excludedWords(9), /9 namespaces are left out by this agent’s scope/)
  assert.match(excludedWords(1), /1 namespace is left out/)
})

test('an agent that reads everything, or reports no scope, leaves no excluded row', () => {
  const base = { clusters: [cl()], namespaces: [ns('shop')], services: [] as Service[] }
  assert.equal(buildNamespaceRows({ ...base, agents: [ag({ scope: { description: '', namespaces: 5, inScope: 5 } })] }).some((r) => r.kind === 'excluded'), false)
  assert.equal(buildNamespaceRows({ ...base, agents: [ag()] }).some((r) => r.kind === 'excluded'), false)
  assert.equal(buildNamespaceRows({ ...base, agents: [ag({ status: 'revoked', scope: { description: 'x', namespaces: 5, inScope: 1 } })] }).some((r) => r.kind === 'excluded'), false, 'a revoked agent reads nothing and its scope is not a statement about now')
})

test('scope wording: none, all, narrowed', () => {
  assert.deepEqual(scopeOf(undefined), { kind: 'none' })
  assert.deepEqual(scopeOf(ag({ kubernetesVersion: undefined })), { kind: 'none' }, 'a hand-made agent has no scope to speak of')
  assert.equal(scopeOf(ag()).kind, 'all')
  const n = scopeOf(ag({ scope: { description: 'label team=a', namespaces: 8, inScope: 2 } }))
  assert.deepEqual(n, { kind: 'narrowed', namespaces: 8, inScope: 2, excluded: 6, description: 'label team=a' })
})

test('rows of a declared cluster say declared, observed ones say in scope', () => {
  const rows = buildNamespaceRows({ clusters: [cl({ id: 'c2', name: 'mine', source: 'manual' }), cl()], namespaces: [ns('shop')], services: [svc('m', 'app', { clusterId: 'c2', source: 'manual' })], agents: [] })
  const mine = rows.find((r) => r.kind === 'namespace' && r.clusterName === 'mine')
  assert.equal(mine?.kind === 'namespace' && mine.scope, 'declared')
  assert.equal(mine?.kind === 'namespace' && mine.observation, undefined, 'nobody observes it, so there is nothing to be stale about')
  const seen = rows.find((r) => r.kind === 'namespace' && r.clusterName === 'edge')
  assert.equal(seen?.kind === 'namespace' && seen.scope, 'in')
})

test('a stale namespace shows its state, not "live"', () => {
  const rows = buildNamespaceRows({ clusters: [cl({ state: 'stale' })], namespaces: [ns('shop', { state: 'stale', stateReason: 'stale for 3 h', stale: true })], services: [], agents: [] })
  const r = rows[0]
  assert.equal(r.kind === 'namespace' && r.observation?.label, 'stale 3 h')
})

test('mesh: covered workloads, strictness, opt-out, and silence where the mesh does nothing', () => {
  const mesh = { kind: 'istio', mode: 'sidecar', mtls: 'strict', controlPlane: [], policyRead: true } as Cluster['mesh']
  const c = cl({ mesh })
  const inM = (n: string) => svc(n, 'shop', { mesh: { mesh: 'istio', proxy: 'sidecar' } })
  const m = namespaceMesh(c, ns('shop'), [inM('a'), inM('b'), svc('c', 'shop')])
  assert.equal(m?.label, 'Istio · mTLS strict')
  assert.equal(m?.covered, 2)
  assert.equal(m?.total, 3)
  assert.equal(m?.tone, 'ok')
  assert.equal(namespaceMesh(c, ns('plain'), [svc('x', 'plain')]), undefined)
  assert.equal(namespaceMesh(c, ns('off', { meshOff: true }), [svc('x', 'off')])?.label, 'Istio · opted out')
  assert.match(namespaceMesh(c, ns('istio-system'), [svc('istiod', 'istio-system', { mesh: { mesh: 'istio', controlPlane: true } })])?.label ?? '', /control plane/)
  assert.equal(namespaceMesh(undefined, ns('x'), [svc('x', 'x')]), undefined, 'no mesh anywhere')
  const unread = namespaceMesh(cl({ mesh: { ...mesh!, mtls: 'permissive', policyRead: false } }), ns('shop'), [inM('a')])
  assert.equal(unread?.label, 'Istio · mTLS not read')
})

test('search matches the namespace or its cluster; an excluded row follows its cluster', () => {
  const rows = buildNamespaceRows({ clusters: [cl()], namespaces: [ns('shop')], services: [], agents: [ag({ scope: { description: 'namespaces shop', namespaces: 4, inScope: 1 } })] })
  assert.equal(rows.filter((r) => rowMatches(r, 'sho')).length, 1)
  assert.equal(rows.filter((r) => rowMatches(r, 'edge')).length, 2)
  assert.equal(rows.filter((r) => rowMatches(r, '')).length, 2)
})
