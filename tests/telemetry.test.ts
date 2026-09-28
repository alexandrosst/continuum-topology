import assert from 'node:assert/strict'
import { test } from 'node:test'
import { emptyTelemetry, telemetryActive, telemetryProblems, withTelemetry, type TelemetryInput } from '../src/lib/install'
import { TELEMETRY_SIGNALS, telemetryUpgradeCommand } from '../src/lib/consent'

const base = 'helm install continuum-agent oci://registry.example.com/continuum-agent --namespace continuum-system --create-namespace'

test('telemetryActive is false until a signal is turned on', () => {
  assert.equal(telemetryActive(emptyTelemetry), false)
  assert.equal(telemetryActive({ ...emptyTelemetry, resourceUsage: true }), true)
  assert.equal(telemetryActive({ ...emptyTelemetry, traces: true }), true)
})

test('every signal in the catalog is a real TelemetryInput boolean field', () => {
  // TELEMETRY_SIGNALS.id must match install.ts's own field names exactly - this is what keeps
  // TelemetryFields, the self-report and the chart's own telemetrySignalNames helper all one vocabulary.
  const t: TelemetryInput = { ...emptyTelemetry }
  for (const s of TELEMETRY_SIGNALS) {
    assert.equal(typeof (t as unknown as Record<string, unknown>)[s.id], 'boolean', `${s.id} is not a boolean field of TelemetryInput`)
  }
  assert.deepEqual(TELEMETRY_SIGNALS.map((s) => s.id).sort(), [
    'applicationLogs', 'applicationMetrics', 'energy', 'kubernetesEvents', 'kubernetesState',
    'networkLatency', 'nodeRuntime', 'resourceUsage', 'systemLogs', 'traces',
  ].sort())
})

test('with nothing on, the command is untouched', () => {
  assert.equal(withTelemetry(base, emptyTelemetry), base)
})

test('an export endpoint is required once anything is on', () => {
  const t = { ...emptyTelemetry, resourceUsage: true }
  assert.deepEqual(telemetryProblems(t), ['An export endpoint is required once any telemetry signal is on'])
  assert.equal(withTelemetry(base, t), base, 'a command with a real problem is left unchanged, like withScope')
})

test('a full signal turns into one --set per signal, every one stated explicitly', () => {
  const t: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, traces: true, exportEndpoint: 'otel.example.com:4317' }
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /--set-string telemetry\.export\.otlp\.endpoint=otel\.example\.com:4317/)
  assert.match(cmd, /--set telemetry\.resourceUsage\.metrics\.enabled=true/)
  assert.match(cmd, /--set telemetry\.traces\.traces\.enabled=true/)
  // every OTHER signal is stated explicitly false, not just omitted - this is what makes the same
  // function usable for the post-install "change telemetry" upgrade command (see below): --reuse-values
  // only changes what is actually named.
  assert.match(cmd, /--set telemetry\.energy\.metrics\.enabled=false/)
  assert.match(cmd, /--set telemetry\.kubernetesState\.metrics\.enabled=false/)
  assert.match(cmd, /--set telemetry\.nodeRuntime\.metrics\.enabled=false/)
  assert.match(cmd, /--set telemetry\.networkLatency\.metrics\.enabled=false/)
  assert.match(cmd, /--set telemetry\.applicationMetrics\.metrics\.enabled=false/)
  assert.match(cmd, /--set telemetry\.systemLogs\.logs\.enabled=false/)
  assert.match(cmd, /--set telemetry\.kubernetesEvents\.logs\.enabled=false/)
  assert.match(cmd, /--set telemetry\.applicationLogs\.logs\.enabled=false/)
  // protocol defaults to grpc and is not restated; insecure defaults to false and is not restated either
  assert.ok(!cmd.includes('telemetry.export.otlp.protocol'))
  assert.ok(!cmd.includes('telemetry.export.otlp.tls.insecure'))
})

test('non-default export protocol and insecure are carried', () => {
  const t: TelemetryInput = { ...emptyTelemetry, applicationMetrics: true, exportEndpoint: 'x:4318', exportProtocol: 'http', exportInsecure: true }
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /--set telemetry\.export\.otlp\.protocol=http/)
  assert.match(cmd, /--set telemetry\.export\.otlp\.tls\.insecure=true/)
})

test('energy pointed at an existing source needs its endpoint, and carries the source + endpoint when valid', () => {
  const missing: TelemetryInput = { ...emptyTelemetry, energy: true, energySource: 'existing', exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(missing), ['The existing Prometheus endpoint is required when energy points at an existing source'])
  assert.equal(withTelemetry(base, missing), base)

  const ok: TelemetryInput = { ...missing, energyExistingEndpoint: 'kepler.monitoring:9102/metrics' }
  const cmd = withTelemetry(base, ok)
  assert.match(cmd, /--set telemetry\.energy\.metrics\.enabled=true/)
  assert.match(cmd, /--set telemetry\.energy\.metrics\.source=existing/)
  assert.match(cmd, /--set-string telemetry\.energy\.metrics\.existing\.prometheusEndpoint=kepler\.monitoring:9102\/metrics/)

  // the bundled-Kepler default never sets `source` or the existing endpoint at all
  const bundled: TelemetryInput = { ...emptyTelemetry, energy: true, exportEndpoint: 'x:4317' }
  const bundledCmd = withTelemetry(base, bundled)
  assert.ok(!bundledCmd.includes('telemetry.energy.metrics.source'))
  assert.ok(!bundledCmd.includes('prometheusEndpoint'))
})

test('network latency without path measurements is flagged, but only when the caller says measurements are off', () => {
  const t: TelemetryInput = { ...emptyTelemetry, networkLatency: true, exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(t, false), ['Network latency re-emits the path measurements extra, so turn that on too, or it will report nothing'])
  assert.equal(telemetryProblems(t, true).length, 0)
  assert.equal(telemetryProblems(t).length, 0, 'unknown (undefined) is not treated as off')
})

test('telemetryUpgradeCommand mirrors helmUpgradeCommand\'s shape exactly', () => {
  const t: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'otel.example.com:4317' }
  const cmd = telemetryUpgradeCommand({ chartFile: 'continuum-agent-0.4.0.tgz', chartRef: '', chartVersion: '0.4.0' }, t)
  assert.ok(cmd.startsWith('helm upgrade continuum-agent ./continuum-agent-0.4.0.tgz --namespace continuum-system --reuse-values'))
  assert.match(cmd, /--set telemetry\.traces\.traces\.enabled=true/)
  // nothing turned on: the command is the bare upgrade line, matching withTelemetry's own "untouched" case
  assert.equal(telemetryUpgradeCommand(undefined, emptyTelemetry), 'helm upgrade continuum-agent ./continuum-agent.tgz --namespace continuum-system --reuse-values')
})
