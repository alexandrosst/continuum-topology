import assert from 'node:assert/strict'
import { test } from 'node:test'
import { emptyTelemetry, withTelemetry, type TelemetryInput } from '../src/lib/install'
import { telemetrySecretCommand, telemetryUpgradeCommand } from '../src/lib/consent'
import { operatorReceiverEndpoint } from '../src/lib/destinationCatalog'
import { exportOperatorId, fragmentEndpoint, intentScope, intentSignals, operatorCommandBlock, operatorCommandDraft } from '../src/lib/operatorIntent'

const base = 'helm install continuum-agent oci://registry.example.com/continuum-agent --namespace continuum-system --create-namespace'
const operatorDraft = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'op-eu.continuum-system.svc:4317', exportOperatorId: 'op-eu', ...over })

// Every command is built exactly as before while exportOperatorId is '' - and the field never changes what
// withTelemetry / telemetryUpgradeCommand emit even when it is set: it only routes the panel's flow.
test('exportOperatorId never changes any command string', () => {
  const drafts: TelemetryInput[] = [
    { ...emptyTelemetry },
    { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'otel.example.com:4317' },
    { ...emptyTelemetry, traces: true, tracesScope: { namespaces: ['shop'], exclude: ['kube-system'], workloads: [] }, applicationLogs: true, exportEndpoint: 'x.example.com:4318', exportProtocol: 'http', exportInsecure: true },
    { ...emptyTelemetry, energy: true, energySource: 'existing', energyExistingEndpoint: 'kepler:9102/metrics', accelerators: true, exportEndpoint: 'o:4317', exportAuthHeaderName: 'x-api-key', exportAuthSecretName: 'tok', exportAuthSecretKey: 'k' },
  ]
  for (const d of drafts) {
    const withoutField = { ...d } as Partial<TelemetryInput>
    delete withoutField.exportOperatorId
    assert.equal(d.exportOperatorId, '')
    assert.equal(withTelemetry(base, d), withTelemetry(base, withoutField as TelemetryInput))
    assert.equal(withTelemetry(base, d), withTelemetry(base, { ...d, exportOperatorId: 'op-eu' }))
    assert.equal(telemetryUpgradeCommand(undefined, d), telemetryUpgradeCommand(undefined, { ...d, exportOperatorId: 'op-eu' }))
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
  assert.ok(!cmd.includes('otlp.protocol') && !cmd.includes('tls.insecure'))
  assert.equal(fragmentEndpoint(fragment), draft.exportEndpoint, 'the server\'s endpoint is the receiver the draft names')
  assert.match(cmd, /--set-string telemetry\.export\.otlp\.endpoint=op-eu\.continuum-system\.svc:4317/)
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
