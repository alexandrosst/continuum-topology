import assert from 'node:assert/strict'
import { test } from 'node:test'
import { addressCell, certChip, intentDestination, joinLocal } from '../src/lib/operatorsView'
import type { TelemetryIntent } from '../src/lib/types'

const NOW = Date.parse('2026-10-06T12:00:00Z')

test('certChip: nothing while the certificates are fine, amber with the days left of the soonest one, red once one has run out', () => {
  assert.equal(certChip({ certState: 'ok' }, NOW), undefined)
  assert.equal(certChip({}, NOW), undefined)
  assert.deepEqual(certChip({ certState: 'expired' }, NOW), { tone: 'bad', text: 'Certificate expired' })
  assert.deepEqual(
    certChip({ certState: 'expiring', certs: { receiverNotAfter: '2026-12-01T00:00:00Z', clientNotAfter: '2026-10-26T12:00:00Z' } }, NOW),
    { tone: 'warn', text: 'Certificate expires in 20 days' },
  )
  assert.equal(certChip({ certState: 'expiring', certs: { caNotAfter: '2026-10-07T00:00:00Z' } }, NOW)?.text, 'Certificate expires in 1 day')
  assert.equal(certChip({ certState: 'expiring' }, NOW)?.text, 'Certificate expires soon')
})

test('addressCell: a recorded address, "Needs an address" while an exposed one has none, otherwise its own cluster only', () => {
  assert.deepEqual(addressCell({ addressState: 'set', address: 'otlp.eu.example.com:4317' }), { kind: 'set', address: 'otlp.eu.example.com:4317', text: 'Reachable at otlp.eu.example.com:4317' })
  assert.equal(addressCell({ addressState: 'pending' }).kind, 'pending')
  assert.equal(addressCell({ addressState: 'none' }).text, 'This cluster only')
  // A server that does not send addressState yet: read from the fields it does send.
  assert.equal(addressCell({ reachableFromOtherClusters: true, address: 'a:1' }).kind, 'set')
  assert.equal(addressCell({ exposure: 'loadbalancer' }).kind, 'pending')
  assert.equal(addressCell({ exposure: 'cluster' }).kind, 'cluster')
})

const intent = (over: Partial<TelemetryIntent>): TelemetryIntent => ({
  id: 'i1',
  agentId: 'a1',
  name: 'n',
  status: 'active',
  namespaces: [],
  exclude: [],
  signals: [{ id: 'resourceUsage' }],
  destination: { kind: 'external', endpoint: 'gw.example.com:4317' },
  createdAt: '2026-10-01T00:00:00Z',
  createdBy: 'alice',
  ...over,
}) as TelemetryIntent

const raw = (id: string, installed: string[]) => ({ id, diagnostics: { reportedAt: '2026-10-06T11:59:00Z', installedTelemetry: installed } })
const agent = (id: string, over: Record<string, unknown> = {}) => ({ id, name: id, status: 'approved', clusterId: `c-${id}`, ...over })

test('joinLocal: running telemetry, or asked-for and not yet reported (pending); never an unapproved agent or an idle one', () => {
  const rows = joinLocal({
    agents: [agent('a1'), agent('a2'), agent('a3'), agent('a4', { status: 'pending' })],
    clusters: [{ id: 'c-a1', name: 'Prod' }],
    rawAgents: [raw('a1', ['resourceUsage']), raw('a2', []), raw('a3', []), raw('a4', ['resourceUsage'])],
    intents: [intent({ agentId: 'a3' })],
  })
  assert.deepEqual(rows.map((r) => [r.agent.id, r.pending]), [['a1', false], ['a3', true]])
  assert.equal(rows[0].cluster?.name, 'Prod')
})

test('joinLocal: drifted only when something is running and differs from the newest active intent', () => {
  const rows = (installed: string[], intents: TelemetryIntent[]) => joinLocal({ agents: [agent('a1')], clusters: [], rawAgents: [raw('a1', installed)], intents })
  assert.equal(rows(['resourceUsage'], [intent({})])[0].drifted, false)
  assert.equal(rows(['resourceUsage', 'traces'], [intent({})])[0].drifted, true)
  assert.equal(rows(['resourceUsage'], [])[0].drifted, false)
  // An older intent was replaced, and a revoked one is not asked for any more.
  const newer = intent({ id: 'i2', createdAt: '2026-10-05T00:00:00Z', signals: [{ id: 'traces' }] as never })
  assert.equal(rows(['traces'], [intent({}), newer])[0].drifted, false)
  assert.equal(rows(['traces'], [intent({ status: 'revoked' as never })])[0].drifted, false)
})

test('intentDestination: the operator it names, one line for split routes, otherwise the endpoint', () => {
  assert.equal(intentDestination(undefined), undefined)
  assert.deepEqual(intentDestination(intent({ destination: { kind: 'operator', endpoint: 'x:4317', targetOperatorId: 'op-eu' } as never })), { operatorId: 'op-eu', label: 'op-eu' })
  assert.deepEqual(intentDestination(intent({})), { label: 'gw.example.com:4317' })
  assert.deepEqual(intentDestination(intent({ routes: { logs: { kind: 'external', endpoint: 'l:1' } } as never })), { label: 'One per signal type' })
})
