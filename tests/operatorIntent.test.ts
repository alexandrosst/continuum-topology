import assert from 'node:assert/strict'
import { test } from 'node:test'
import { emptyExportTarget, emptyTelemetry, type TelemetryInput } from '../src/lib/install'
import { operatorReceiverEndpoint } from '../src/lib/operatorReceiver'
import { exportOperatorId, intentDestinations, intentScope, intentSignals, laneOperatorId, operatorCommandDraft, operatorTargets } from '../src/features/pipeline/lib/operatorIntent'

const operatorDraft = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'op-eu.ikhnos-system.svc:4317', exportOperatorId: 'op-eu', ...over })

test('exportOperatorId is only honoured while the endpoint still is that operator\'s receiver', () => {
  assert.equal(exportOperatorId(operatorDraft()), 'op-eu')
  assert.equal(exportOperatorId(operatorDraft({ exportEndpoint: ' op-eu.ikhnos-system.svc:4317 ' })), 'op-eu')
  assert.equal(exportOperatorId(operatorDraft({ exportEndpoint: 'otel.example.com:4317' })), '', 'a stale id must not trigger a certificate')
  assert.equal(exportOperatorId(operatorDraft({ exportOperatorId: '' })), '')
  assert.equal(operatorReceiverEndpoint({ id: 'op-eu' }), 'op-eu.ikhnos-system.svc:4317')
})

test('intentSignals: one grant per signal that is on, source from the draft\'s own source choices', () => {
  assert.deepEqual(intentSignals(emptyTelemetry), [])
  const t = { ...emptyTelemetry, resourceUsage: true, traces: true, energy: true, accelerators: true }
  assert.deepEqual(intentSignals(t), [
    { id: 'resourceUsage', source: 'builtin' },
    { id: 'energy', source: 'bundle-kepler' },
    { id: 'traces', source: 'builtin' },
    { id: 'accelerators', source: 'bundle-dcgm' },
  ])
  assert.deepEqual(
    intentSignals({ ...t, energySource: 'existing', acceleratorsSource: 'existing' }).filter((g) => g.id === 'energy' || g.id === 'accelerators'),
    [{ id: 'energy', source: 'existing' }, { id: 'accelerators', source: 'existing' }],
  )
})

test('intentScope never records a scope narrower than what is collected', () => {
  // A cluster-wide signal ignores namespaces: everything, nothing excluded - whatever the scoped ones say.
  const scoped = { applicationLogs: true, applicationLogsScope: { namespaces: ['shop'], exclude: ['shop-test'], workloads: [] } }
  assert.deepEqual(intentScope({ ...emptyTelemetry, resourceUsage: true, ...scoped }), { namespaces: [], exclude: [] })
  // Only scoped signals, all with included namespaces: their union; excluded only where every one excludes it.
  assert.deepEqual(
    intentScope({
      ...emptyTelemetry,
      ...scoped,
      traces: true,
      tracesScope: { namespaces: ['payments', 'shop'], exclude: ['shop-test', 'x'], workloads: [] },
    }),
    { namespaces: ['payments', 'shop'], exclude: ['shop-test'] },
  )
  // One scoped signal with an empty include list collects every namespace.
  assert.deepEqual(intentScope({ ...emptyTelemetry, ...scoped, traces: true }), { namespaces: [], exclude: [] })
  // A single scoped signal records exactly its own scope.
  assert.deepEqual(intentScope({ ...emptyTelemetry, ...scoped }), { namespaces: ['shop'], exclude: ['shop-test'] })
  // Accelerators has no namespace scope of its own, even with applyScope on.
  assert.deepEqual(intentScope({ ...emptyTelemetry, ...scoped, accelerators: true, acceleratorsApplyScope: true }), { namespaces: [], exclude: [] })
  assert.deepEqual(intentScope(emptyTelemetry), { namespaces: [], exclude: [] })
})

/* ---------- signal types going to different destinations ---------- */

const opLane = (id: string, over: Partial<typeof emptyExportTarget> = {}) => ({ ...emptyExportTarget, exportEndpoint: `${id}.ikhnos-system.svc:4317`, exportOperatorId: id, ...over })
const splitDraft = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({
  ...emptyTelemetry,
  resourceUsage: true,
  systemLogs: true,
  traces: true,
  exportSplit: true,
  exportLanes: { metrics: opLane('op-eu'), logs: opLane('op-eu'), traces: { ...emptyExportTarget, exportEndpoint: 'z:9411/api/v2/spans', exportProtocol: 'zipkin', exportInsecure: true } },
  ...over,
})

test('operatorTargets lists each operator once, with the signal types that go to it', () => {
  assert.deepEqual(operatorTargets(splitDraft()), [{ id: 'op-eu', lanes: ['metrics', 'logs'] }])
  const two = splitDraft({ exportLanes: { ...splitDraft().exportLanes, logs: opLane('op-us') } })
  assert.deepEqual(operatorTargets(two), [{ id: 'op-eu', lanes: ['metrics'] }, { id: 'op-us', lanes: ['logs'] }])
  assert.deepEqual(operatorTargets(operatorDraft()), [{ id: 'op-eu', lanes: [] }])
  assert.deepEqual(operatorTargets({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317' }), [])
  // Only the lanes that have a signal on count, and an id that no longer matches its endpoint is not an operator.
  assert.deepEqual(operatorTargets(splitDraft({ traces: false, systemLogs: false })), [{ id: 'op-eu', lanes: ['metrics'] }])
  const stale = splitDraft({ exportLanes: { ...splitDraft().exportLanes, metrics: { ...opLane('op-eu'), exportEndpoint: 'edited:4317' } } })
  assert.equal(laneOperatorId(stale, 'metrics'), '')
  // One destination's operator id is not read while sending each signal type separately.
  assert.equal(exportOperatorId(splitDraft({ exportOperatorId: 'op-eu', exportEndpoint: 'op-eu.ikhnos-system.svc:4317' })), '')
})

test('the intent records every signal type\'s destination, the first as the default, and one destination as no routes', () => {
  const d = intentDestinations(splitDraft(), 'op-eu')
  assert.deepEqual(d.routes?.metrics, { kind: 'operator', endpoint: 'op-eu.ikhnos-system.svc:4317', targetOperatorId: 'op-eu' })
  assert.deepEqual(d.routes?.logs, d.routes?.metrics)
  assert.deepEqual(d.routes?.traces, { kind: 'external', endpoint: 'z:9411/api/v2/spans', insecure: true })
  assert.deepEqual(d.destination, d.routes?.metrics)
  const single = intentDestinations(operatorDraft(), 'op-eu')
  assert.equal(single.routes, undefined)
  assert.deepEqual(single.destination, { kind: 'operator', endpoint: 'op-eu.ikhnos-system.svc:4317', targetOperatorId: 'op-eu' })
  // A credential is recorded by the names of its Secret and header, never by a value.
  const named = splitDraft({ exportLanes: { ...splitDraft().exportLanes, traces: { ...emptyExportTarget, exportEndpoint: 't:4317', exportAuthSecretName: 'tempo-token' } } })
  assert.deepEqual(intentDestinations(named, 'op-eu').routes?.traces, { kind: 'external', endpoint: 't:4317', authHeaderName: 'Authorization', authSecretName: 'tempo-token', authSecretKey: 'token' })
})

test('each operator lane drops a credential its operator does not take; the others keep theirs', () => {
  const d = splitDraft({
    exportLanes: {
      metrics: opLane('op-eu', { exportAuthSecretName: 'eu-token' }),
      logs: opLane('op-us', { exportAuthSecretName: 'us-token' }),
      traces: { ...emptyExportTarget, exportEndpoint: 't:4317', exportAuthSecretName: 'tempo-token' },
    },
  })
  const out = operatorCommandDraft(d, undefined, (id) => (id === 'op-eu' ? 'mtls' : 'bearer'))
  assert.equal(out.exportLanes.metrics.exportAuthSecretName, '')
  assert.equal(out.exportLanes.logs.exportAuthSecretName, 'us-token')
  assert.equal(out.exportLanes.traces.exportAuthSecretName, 'tempo-token')
  // Never HTTP or skip-verify for an operator lane.
  assert.equal(operatorCommandDraft(splitDraft({ exportLanes: { ...splitDraft().exportLanes, metrics: opLane('op-eu', { exportInsecure: true, exportProtocol: 'http' }) } })).exportLanes.metrics.exportProtocol, 'grpc')
})

test('the address this sender chose is recorded with the operator destination, one or per signal type, and left out when none was chosen', () => {
  const chosen = intentDestinations(operatorDraft({ exportOperatorAddress: ' 10.20.0.14:31317 ' }), 'op-eu')
  assert.deepEqual(chosen.destination, { kind: 'operator', endpoint: 'op-eu.ikhnos-system.svc:4317', targetOperatorId: 'op-eu', address: '10.20.0.14:31317' })
  assert.deepEqual(intentDestinations(operatorDraft(), 'op-eu').destination, { kind: 'operator', endpoint: 'op-eu.ikhnos-system.svc:4317', targetOperatorId: 'op-eu' })
  // Each signal type keeps the address of its own lane.
  const lane = (address: string) => ({ ...emptyExportTarget, exportEndpoint: 'op-eu.ikhnos-system.svc:4317', exportOperatorId: 'op-eu', exportOperatorAddress: address })
  const split = { ...emptyTelemetry, resourceUsage: true, systemLogs: true, exportSplit: true, exportLanes: { metrics: lane('10.20.0.14:31317'), logs: lane(''), traces: emptyExportTarget } } as TelemetryInput
  const d = intentDestinations(split, 'op-eu')
  assert.equal(d.routes?.metrics?.address, '10.20.0.14:31317')
  assert.equal(d.routes?.logs?.address, undefined)
})

// What the command states of an operator destination is written by the server (backend/internal/install: operator_block_test.go pins it against
// the bytes this page used to paste). What stays here is what the page itself decides: which destination counts as an operator's.
test('operatorCommandDraft makes the draft fit an operator\'s receiver: gRPC, verified', () => {
  const draft = operatorDraft({ exportProtocol: 'http', exportInsecure: true })
  assert.deepEqual(operatorCommandDraft(draft), { ...draft, exportProtocol: 'grpc', exportInsecure: false })
})
