import assert from 'node:assert/strict'
import { test } from 'node:test'
import { emptyExportTarget, emptyTelemetry, withTelemetry, type TelemetryInput } from '../src/lib/install'
import { telemetrySecretCommand, telemetryUpgradeCommand } from '../src/lib/consent'
import { operatorReceiverEndpoint } from '../src/lib/destinationCatalog'
import { exportOperatorId, fragmentEndpoint, intentDestinations, intentScope, intentSignals, laneOperatorId, operatorCommandBlock, operatorCommandDraft, operatorTargets } from '../src/lib/operatorIntent'

const base = 'helm install continuum-agent oci://registry.example.com/continuum-agent --namespace continuum-system --create-namespace'
const operatorDraft = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'op-eu.continuum-system.svc:4317', exportOperatorId: 'op-eu', ...over })

// Every command is built exactly as before while exportOperatorId is '' - and the field never changes what
// withTelemetry / telemetryUpgradeCommand emit even when it is set: it only routes the panel's flow.
test('exportOperatorId only decides whether the client-certificate lines are cleared, never anything else in the command', () => {
  const drafts: TelemetryInput[] = [
    { ...emptyTelemetry },
    { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'otel.example.com:4317' },
    { ...emptyTelemetry, traces: true, tracesScope: { namespaces: ['shop'], exclude: ['kube-system'], workloads: [] }, applicationLogs: true, exportEndpoint: 'x.example.com:4318', exportProtocol: 'http', exportInsecure: true },
    { ...emptyTelemetry, energy: true, energySource: 'existing', energyExistingEndpoint: 'kepler:9102/metrics', accelerators: true, exportEndpoint: 'o:4317', exportAuthHeaderName: 'x-api-key', exportAuthSecretName: 'tok', exportAuthSecretKey: 'k' },
  ]
  // An ordinary destination states "no client certificate"; an operator's is stated by the server's own fragment, which these lines must
  // not contradict - so they are the only lines the id may remove.
  const mtlsLines = (c: string) => c.split('\n').filter((l) => /tls\.(mtls|serverName|caFile)/.test(l))
  const rest = (c: string) => c.split('\n').filter((l) => !/tls\.(mtls|serverName|caFile)/.test(l)).join('\n')
  for (const d of drafts) {
    const withoutField = { ...d } as Partial<TelemetryInput>
    delete withoutField.exportOperatorId
    assert.equal(d.exportOperatorId, '')
    assert.equal(withTelemetry(base, d), withTelemetry(base, withoutField as TelemetryInput))
    const forOperator = withTelemetry(base, { ...d, exportOperatorId: 'op-eu' })
    assert.equal(rest(withTelemetry(base, d)), rest(forOperator))
    assert.equal(mtlsLines(forOperator).length, 0)
    assert.equal(mtlsLines(withTelemetry(base, d)).length > 0, d.exportEndpoint !== '', 'an ordinary destination clears the client certificate it may have had')
    assert.equal(telemetrySecretCommand(d), telemetrySecretCommand({ ...d, exportOperatorId: 'op-eu' }))
  }
})

test('exportOperatorId is only honoured while the endpoint still is that operator\'s receiver', () => {
  assert.equal(exportOperatorId(operatorDraft()), 'op-eu')
  assert.equal(exportOperatorId(operatorDraft({ exportEndpoint: ' op-eu.continuum-system.svc:4317 ' })), 'op-eu')
  assert.equal(exportOperatorId(operatorDraft({ exportEndpoint: 'otel.example.com:4317' })), '', 'a stale id must not trigger a certificate')
  assert.equal(exportOperatorId(operatorDraft({ exportOperatorId: '' })), '')
  assert.equal(operatorReceiverEndpoint({ id: 'op-eu' }), 'op-eu.continuum-system.svc:4317')
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

test('operatorCommandBlock: server secrets first, then the upgrade with the server fragment last; export flags agree', () => {
  const fragment = '--set telemetry.resource.orgId=o --set telemetry.resource.clusterId=c --set telemetry.resource.intentId=ti-1 --set telemetry.export.otlp.endpoint=op-eu.continuum-system.svc:4317 --set telemetry.export.otlp.tls.mtls.enabled=true --set telemetry.export.otlp.tls.mtls.secretName=op-eu-export-mtls'
  const secret = 'kubectl create secret generic op-eu-export-mtls --namespace continuum-system \\\n  --from-literal=tls.crt="C"'
  // A previous destination's HTTP and skip-verify would conflict with mutual TLS over gRPC: dropped.
  const draft = operatorDraft({ exportProtocol: 'http', exportInsecure: true })
  const cmd = operatorCommandBlock({ install: undefined, draft, result: { installFragment: fragment, secretCommands: [secret] } })
  assert.ok(cmd.startsWith(secret + ' && \\\nhelm upgrade continuum-agent'))
  assert.ok(cmd.endsWith(' \\\n  ' + fragment))
  // Stated as gRPC and verified, not left out: a release that used http or skip-verify before would keep them under --reset-then-reuse-values.
  assert.ok(cmd.includes('otlp.protocol=grpc') && cmd.includes('tls.insecure=false'))
  assert.ok(!cmd.includes('otlp.protocol=http') && !cmd.includes('tls.insecure=true'))
  assert.ok(!cmd.includes('tls.mtls.enabled=false'), 'the server fragment owns the client-certificate lines of an operator destination')
  assert.equal(fragmentEndpoint(fragment), draft.exportEndpoint, 'the server\'s endpoint is the receiver the draft names')
  // The endpoint is the server's, stated once (the client's placeholder for it is dropped, not repeated).
  assert.equal(cmd.match(/telemetry\.export\.otlp\.endpoint=/g)?.length, 1)
  assert.match(cmd, /--set telemetry\.export\.otlp\.endpoint=op-eu\.continuum-system\.svc:4317/)
  assert.deepEqual(operatorCommandDraft(draft), { ...draft, exportProtocol: 'grpc', exportInsecure: false })
})

test('operatorCommandBlock puts the receiver-token Secret between the certificate Secret and the upgrade', () => {
  const draft = operatorDraft({ exportAuthSecretName: 'op-token' })
  const cmd = operatorCommandBlock({ install: undefined, draft, result: { installFragment: '--set a=b', secretCommands: ['kubectl create secret generic cert'] } })
  const parts = cmd.split(' && \\\n')
  assert.equal(parts.length, 3)
  assert.ok(parts[0].startsWith('kubectl create secret generic cert'))
  assert.ok(parts[1].startsWith('kubectl create secret generic op-token'))
  assert.ok(parts[2].startsWith('helm upgrade') && parts[2].includes('auth.secretName=op-token'))
})

test('operatorCommandBlock targets the namespace and release the server reports, for the Secrets and the upgrade alike', () => {
  const draft = operatorDraft({ exportAuthSecretName: 'op-token' })
  const cmd = operatorCommandBlock({
    install: undefined, draft,
    result: { installFragment: '--set a=b', secretCommands: ['kubectl create secret generic cert --namespace edge-agents'], namespace: 'edge-agents', release: 'cont-edge' },
  })
  const parts = cmd.split(' && \\\n')
  assert.ok(parts[1].includes('--namespace edge-agents '), 'the receiver-token Secret is created where the release runs')
  assert.ok(parts[2].startsWith('helm upgrade cont-edge ') && parts[2].includes('--namespace edge-agents '))
})

test('a namespace or release that is not a plain Kubernetes name is never pasted into a command', () => {
  const draft = operatorDraft({ exportAuthSecretName: 'op-token' })
  const cmd = operatorCommandBlock({ install: undefined, draft, result: { installFragment: '--set a=b', secretCommands: [], namespace: 'x; rm -rf /', release: '$(id)' } })
  assert.ok(!cmd.includes('rm -rf') && !cmd.includes('$(id)'))
  assert.ok(cmd.includes('helm upgrade continuum-agent ') && cmd.includes('--namespace continuum-system '))
})

/* ---------- signal types going to different destinations ---------- */

const opLane = (id: string, over: Partial<typeof emptyExportTarget> = {}) => ({ ...emptyExportTarget, exportEndpoint: `${id}.continuum-system.svc:4317`, exportOperatorId: id, ...over })
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
  assert.equal(exportOperatorId(splitDraft({ exportOperatorId: 'op-eu', exportEndpoint: 'op-eu.continuum-system.svc:4317' })), '')
})

test('the intent records every signal type\'s destination, the first as the default, and one destination as no routes', () => {
  const d = intentDestinations(splitDraft(), 'op-eu')
  assert.deepEqual(d.routes?.metrics, { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' })
  assert.deepEqual(d.routes?.logs, d.routes?.metrics)
  assert.deepEqual(d.routes?.traces, { kind: 'external', endpoint: 'z:9411/api/v2/spans', insecure: true })
  assert.deepEqual(d.destination, d.routes?.metrics)
  const single = intentDestinations(operatorDraft(), 'op-eu')
  assert.equal(single.routes, undefined)
  assert.deepEqual(single.destination, { kind: 'operator', endpoint: 'op-eu.continuum-system.svc:4317', targetOperatorId: 'op-eu' })
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

test('operatorCommandBlock for routed signal types: one certificate Secret, the routes, and the server fragment last', () => {
  const draft = splitDraft({ traces: false })
  const fragment = '--set telemetry.export.routes.metrics.tls.mtls.enabled=true --set telemetry.export.routes.logs.tls.mtls.enabled=true'
  const cmd = operatorCommandBlock({ install: undefined, draft, result: { installFragment: fragment, secretCommands: ['kubectl create secret generic op-eu-export-mtls'], operators: { 'op-eu': 'mtls' } } })
  assert.equal(cmd.match(/kubectl create secret/g)?.length, 1)
  assert.ok(cmd.trimEnd().endsWith(fragment))
  assert.match(cmd, /--set-string telemetry\.export\.routes\.metrics\.endpoint=op-eu\.continuum-system\.svc:4317/)
  assert.doesNotMatch(cmd, /telemetry\.export\.otlp\.endpoint/)
  // A client half never turns an operator route's mutual TLS off; the server's fragment turns it on.
  assert.doesNotMatch(cmd, /routes\.(metrics|logs)\.tls\.mtls\.enabled=false/)
})
