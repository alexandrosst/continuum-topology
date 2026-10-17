import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { test } from 'node:test'
import {
  cleanTags, emptyExportTarget, emptyScopeOverride, emptyTelemetry, keepAsInstalled, telemetryProblems, withFlowObserver, withMeasurements, withNodeProbe, withScope,
  withTelemetry, emptyScope, scopeProblems, type ScopeInput, type TelemetryInput,
} from '../src/lib/install'
import { helmUpgradeCommand, telemetryUpgradeCommand, telemetrySecretCommand, PICKABLE_SIGNALS, TELEMETRY_INTENT_PRESETS, applyIntentPreset } from '../src/lib/consent'
import { EXPORT_PRESETS } from '../src/lib/exportPresets'
import { newProcessorEntry } from '../src/lib/processorCatalog'
import { buildOperatorInstallCommand } from '../src/lib/operatorInstall'
import { processorSpec, telemetrySpec } from '../src/lib/telemetrySpec'
import { operatorCommandBlock } from '../src/lib/operatorIntent'

// The install commands the UI prints are built here, in TypeScript, as strings. The chart that runs them is checked in Go
// (backend/internal/chart/install_commands_test.go): that test takes every command pinned below, reads its --set flags the way a
// shell and Helm would, and renders the chart with them. This is the first half. It writes, for a matrix of settings, the exact
// command each one prints, in tests/install_commands.golden.json, so a change to what a person is told to paste shows in review
// and the Go test then proves the new text still renders.
//
// A case is a name, the chart it is for and the command. A case whose settings have a problem would print the bare install
// unchanged, which would pin nothing: such a case fails here instead.
//
// To accept a change on purpose:  UPDATE_GOLDEN=1 npm run test:unit   (or run this file alone with the variable set)
//
// What this does not cover: the commands the server builds in Go (they are covered by the Go tests next to them), and what the
// collector does with the rendered configuration (the chart tests that run the real collector).

type Chart = 'agent' | 'regional-operator'

/** What the Go renderer (backend/internal/install) is given to write the same command: the second half of this file's job. A case carries
 *  it beside the command it pinned, and the Go test (install/parity_test.go) renders it and demands the same bytes. The commands the web UI
 *  prints move to being written there, so this is the contract: whatever a case says here, the server must be able to say. */
interface Spec {
  /** The command the rest is appended to, as given (the server prints it), or ... */
  head?: string
  /** ... an upgrade of an installed agent, whose head the renderer writes. */
  upgrade?: { chartRef?: string; chartVersion?: string; chartFile?: string; namespace?: string; release?: string; tier?: number }
  collectors?: { nodeProbe?: boolean; flowObserver?: boolean; measurements?: boolean }
  scope?: { namespaces: string[]; exclude: string[]; selector: string }
  /** What telemetry was asked for: lib/telemetrySpec.ts, which is also what the server will be sent. */
  telemetry?: ReturnType<typeof telemetrySpec>
  /** The regional operator chart's extra processors. */
  operatorProcessors?: ReturnType<typeof processorSpec>[]
}
interface Case { name: string; chart: Chart; command: string; spec?: Spec }

const agentBase = 'helm install continuum-agent oci://registry.example.com/continuum-agent --namespace continuum-system --create-namespace'
const operatorBase = 'helm install continuum-regional-operator oci://registry.example.com/continuum-regional-operator --namespace continuum-system --create-namespace'

const cases: Case[] = []
const names = new Set<string>()
function add(name: string, chart: Chart, command: string, spec?: Spec) {
  assert.ok(!names.has(name), `two cases are called ${name}`)
  names.add(name)
  cases.push(spec ? { name, chart, command, spec } : { name, chart, command })
}

/** A telemetry case: the settings must be valid and must change the command, or the case would pin nothing. */
function telemetry(name: string, t: TelemetryInput, from = agentBase) {
  assert.deepEqual(telemetryProblems(t), [], `${name}: these settings have a problem, so no command would be printed`)
  const cmd = withTelemetry(from, t)
  assert.notEqual(cmd, from, `${name}: the command is the bare install`)
  add(name, 'agent', cmd, { head: from, telemetry: telemetrySpec(t) })
}

const to = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({ ...emptyTelemetry, exportEndpoint: 'otel.example.com:4317', ...over })

// ---------- the install, and the opt-in parts ----------
add('install', 'agent', agentBase, { head: agentBase })
add('node probe', 'agent', withNodeProbe(agentBase, true), { head: agentBase, collectors: { nodeProbe: true } })
add('flow observer', 'agent', withFlowObserver(agentBase, true), { head: agentBase, collectors: { flowObserver: true } })
add('measurements', 'agent', withMeasurements(agentBase, true), { head: agentBase, collectors: { measurements: true } })
add('node probe, flow observer and measurements', 'agent', withMeasurements(withFlowObserver(withNodeProbe(agentBase, true), true), true), { head: agentBase, collectors: { nodeProbe: true, flowObserver: true, measurements: true } })

// ---------- scope ----------
/** A scope case: the settings must be valid, or the command would be the bare install and pin nothing. */
function scoped(name: string, s: ScopeInput) {
  assert.deepEqual(scopeProblems(s), [], `${name}: these settings have a problem`)
  add(name, 'agent', withScope(agentBase, s), { head: agentBase, scope: s })
}
scoped('scope: two namespaces', { ...emptyScope, namespaces: ['checkout', 'payments'] })
scoped('scope: excluded namespaces', { ...emptyScope, exclude: ['kube-public', 'dev-sandbox'] })
scoped('scope: a label selector', { ...emptyScope, selector: 'continuum.io/scope=yes' })
scoped('scope: all three', { namespaces: ['checkout'], exclude: ['dev-sandbox'], selector: 'app=core' })

// ---------- upgrades (what the access and telemetry panels print) ----------
for (const tier of [0, 1, 2]) add(`upgrade: tier ${tier}`, 'agent', helmUpgradeCommand(undefined, tier), { upgrade: { tier } })
add('upgrade: tier 2 from a registry chart at a version', 'agent', helmUpgradeCommand({ chartRef: 'oci://registry.example.com/continuum-agent', chartVersion: '0.4.0' }, 2, { namespace: 'ikhnos', release: 'edge-agent' }), { upgrade: { chartRef: 'oci://registry.example.com/continuum-agent', chartVersion: '0.4.0', namespace: 'ikhnos', release: 'edge-agent', tier: 2 } })
add('upgrade: tier 1 from a local file', 'agent', helmUpgradeCommand({ chartFile: 'continuum-agent-0.4.0.tgz' }, 1), { upgrade: { chartFile: 'continuum-agent-0.4.0.tgz', tier: 1 } })

// ---------- telemetry: each signal alone, then all of them ----------
for (const s of PICKABLE_SIGNALS) {
  const t = to({ [s.id]: true } as Partial<TelemetryInput>)
  // Signals that need something beside the switch carry it here, so the case stands for the signal and not for a missing field.
  if (s.id === 'clusterLinks') t.clusterLinksProbe = true
  telemetry(`signal: ${s.id}`, t)
}
telemetry('signals: every one', to({ ...Object.fromEntries(PICKABLE_SIGNALS.map((s) => [s.id, true])) as Partial<TelemetryInput> }))
for (const p of TELEMETRY_INTENT_PRESETS) telemetry(`intent preset: ${p.id}`, applyIntentPreset(to(), p))

// ---------- telemetry: where it goes ----------
telemetry('destination: OTLP/HTTP', to({ resourceUsage: true, exportProtocol: 'http', exportEndpoint: 'https://otel.example.com:4318' }))
telemetry('destination: plaintext (TLS verification off)', to({ resourceUsage: true, exportInsecure: true }))
telemetry('destination: a credential from a Secret', to({ resourceUsage: true, exportAuthHeaderName: 'Authorization', exportAuthSecretName: 'otel-token', exportAuthSecretKey: 'token' }))
telemetry('destination: Zipkin for traces', to({ traces: true, exportProtocol: 'zipkin', exportEndpoint: 'http://zipkin.monitoring:9411/api/v2/spans' }))
for (const p of EXPORT_PRESETS) {
  const modalities = p.modalities ?? ['metrics', 'logs', 'traces']
  const t = to({
    resourceUsage: modalities.includes('metrics'), systemLogs: modalities.includes('logs') && !modalities.includes('metrics'), traces: modalities.includes('traces') && !modalities.includes('metrics'),
    exportEndpoint: p.endpointPattern.replace(/<[^<>]*>/g, 'x'), exportProtocol: p.protocol, exportInsecure: !!p.plain,
    exportAuthHeaderName: p.headerName, exportAuthSecretName: p.headerName ? 'dest-token' : '', exportAuthSecretKey: p.headerName ? 'token' : '',
  })
  telemetry(`destination preset: ${p.id}`, t)
}
const lane = (endpoint: string, over: Partial<TelemetryInput> = {}) => ({ exportEndpoint: endpoint, exportProtocol: 'grpc' as const, exportInsecure: false, exportAuthHeaderName: '', exportAuthSecretName: '', exportAuthSecretKey: '', exportOperatorId: '', exportOperatorAddress: '', ...over })
telemetry('destination: one per signal type', {
  ...emptyTelemetry, resourceUsage: true, systemLogs: true, traces: true, exportSplit: true,
  exportLanes: { metrics: lane('metrics.example.com:4317'), logs: lane('https://logs.example.com:4318', { exportProtocol: 'http' }), traces: lane('http://traces.example.com:9411/api/v2/spans', { exportProtocol: 'zipkin', exportAuthHeaderName: 'X-Api-Key', exportAuthSecretName: 'trace-key', exportAuthSecretKey: 'key' }) },
})
telemetry('destination: a regional operator', to({ resourceUsage: true, exportOperatorId: 'op-1', exportOperatorAddress: 'operator.example.com:4317', resourceOrgId: 'org-1', resourceClusterId: 'cluster-1' }))

// ---------- telemetry: the choices inside a signal ----------
telemetry('energy: an existing Kepler', to({ energy: true, energySource: 'existing', energyExistingEndpoint: 'kepler.energy.svc:9102' }))
telemetry('accelerators: an existing DCGM', to({ accelerators: true, acceleratorsSource: 'existing', acceleratorsExistingEndpoint: 'dcgm.gpu.svc:9400' }))
telemetry('accelerators: bundled, following the scope', to({ accelerators: true, acceleratorsApplyScope: true, applicationMetrics: true, applicationMetricsScope: { ...emptyScopeOverride, namespaces: ['ml'] } }))
telemetry('cluster links: probe off', to({ clusterLinks: true, clusterLinksProbe: false }))
telemetry('cluster links: throughput tests with the operator own Secret', to({ clusterLinks: true, clusterLinksProbe: true, clusterLinksThroughput: true, clusterLinksAuthSecret: 'mesh-iperf' }))
telemetry('scope: application metrics from two namespaces', to({ applicationMetrics: true, applicationMetricsScope: { namespaces: ['checkout', 'payments'], exclude: [], workloads: [] } }))
telemetry('scope: application logs without one, one workload only', to({ applicationLogs: true, applicationLogsScope: { namespaces: ['checkout'], exclude: ['checkout-batch'], workloads: [{ namespace: 'checkout', names: ['api', 'worker'] }] } }))
telemetry('scope: traces from one namespace, infrastructure not following', to({ traces: true, resourceUsage: true, scopeInfrastructure: false, tracesScope: { namespaces: ['checkout'], exclude: [], workloads: [] } }))

// ---------- telemetry: processing and labelling ----------
telemetry('process: environment enrichment on, masking off', to({ resourceUsage: true, resourceDetection: true, redaction: false }))
telemetry('process: a quarter of the traces', to({ traces: true, tracesSamplingPercent: 25 }))
telemetry('process: tags', to({ resourceUsage: true, tags: cleanTags([{ key: 'team', value: 'core' }, { key: 'env', value: 'prod; test' }]) }))
telemetry('process: debug exporter, counts only', to({ resourceUsage: true, debugVerbosity: 'basic' }))
telemetry('process: debug exporter, every record', to({ resourceUsage: true, debugVerbosity: 'detailed' }))
const filter = { ...newProcessorEntry('filter'), name: 'drop_debug', config: { signal: 'log' as const, conditions: [{ field: 'level', op: 'eq' as const, value: "debug" }, { field: 'user', op: 'eq' as const, value: "O'Brien" }] } }
const tail = { ...newProcessorEntry('tailSampling'), name: 'errors_only', config: { decisionWaitSeconds: 10, policies: [{ name: 'errors', type: 'statusCode' as const, probabilisticPercent: 10, statusCodes: 'ERROR', latencyThresholdMs: 500 }] } }
const transform = { ...newProcessorEntry('transform'), name: 'tag_env', config: { signal: 'trace' as const, statements: [{ action: 'set' as const, key: 'env', value: 'prod' }] } }
telemetry('process: a filter on logs', to({ applicationLogs: true, extraProcessors: [filter] }))
telemetry('process: filter, tail sampling and a transform', to({ applicationLogs: true, traces: true, extraProcessors: [filter, tail, transform] }))

// ---------- telemetry on an install that already has it (reset-then-reuse upgrades) ----------
const had = (over: Partial<TelemetryInput> = {}) => to({ hadTelemetry: true, ...over })
{
  const t = to({ resourceUsage: true, traces: true })
  add('upgrade telemetry: a new configuration', 'agent', telemetryUpgradeCommand({ chartFile: 'continuum-agent-0.4.0.tgz' }, t, true), { upgrade: { chartFile: 'continuum-agent-0.4.0.tgz' }, telemetry: telemetrySpec(t, true) })
}
add('upgrade telemetry: every signal unchecked', 'agent', telemetryUpgradeCommand(undefined, had(), false), { upgrade: {}, telemetry: telemetrySpec(had(), false) })
telemetry('upgrade telemetry: single destination clears routes', had({ resourceUsage: true, exportRoutesInstalled: true }), 'helm upgrade continuum-agent ./continuum-agent.tgz --namespace continuum-system --reset-then-reuse-values')
telemetry('upgrade telemetry: processors emptied', had({ applicationLogs: true }), 'helm upgrade continuum-agent ./continuum-agent.tgz --namespace continuum-system --reset-then-reuse-values')
{
  const seeded = keepAsInstalled(had({ resourceUsage: true, traces: true, tracesScope: { ...emptyScopeOverride, namespaces: ['checkout'] }, tags: cleanTags([{ key: 'team', value: 'core' }]), debugVerbosity: 'basic' }),
    ['destination', 'scopeShared', 'scope:applicationMetrics', 'scope:applicationLogs', 'scope:traces', 'tags', 'debug', 'processors', 'extraProcessors', 'energySource', 'acceleratorsSource', 'clusterLinksOptions'])
  add('upgrade telemetry: every group kept as installed', 'agent', telemetryUpgradeCommand(undefined, seeded, false), { upgrade: {}, telemetry: telemetrySpec(seeded, false) })
}

// ---------- the regional operator's chart ----------
add('operator: install', 'regional-operator', operatorBase, { head: operatorBase })
{
  const cmd = buildOperatorInstallCommand(operatorBase, [filter, tail])
  assert.notEqual(cmd, operatorBase, 'operator processors: the command is the bare install')
  add('operator: a filter and tail sampling', 'regional-operator', cmd, { head: operatorBase, operatorProcessors: [filter, tail].map(processorSpec) })
}

// ---------- the Secrets the credential flags point at ----------
// The commands that create them are pinned in tests/install_secrets.golden.json, beside the spec each was written from. An empty command is
// "nothing to create", which is also pinned.
interface SecretCase { name: string; command: string; spec: { telemetry: ReturnType<typeof telemetrySpec>; namespace?: string } }
const secretCases: SecretCase[] = []
function secrets(name: string, t: TelemetryInput, namespace?: string) {
  assert.ok(!secretCases.some((c) => c.name === name), `two secret cases are called ${name}`)
  const command = telemetrySecretCommand(t, undefined, namespace === undefined ? undefined : { namespace }) ?? ''
  secretCases.push({ name, command, spec: { telemetry: telemetrySpec(t), ...(namespace === undefined ? {} : { namespace }) } })
}
{
  const routed = (lanes: Record<string, Partial<TelemetryInput>>, over: Partial<TelemetryInput> = {}): TelemetryInput => ({
    ...emptyTelemetry, resourceUsage: true, systemLogs: true, traces: true, exportSplit: true,
    exportLanes: { metrics: lane('m.example.com:4317', lanes.metrics), logs: lane('l.example.com:4317', lanes.logs), traces: lane('t.example.com:4317', lanes.traces) }, ...over,
  })
  secrets('none named', to({ resourceUsage: true }))
  secrets('one, the default key', to({ resourceUsage: true, exportAuthSecretName: 'otel-token' }))
  secrets('one, its own key', to({ resourceUsage: true, exportAuthSecretName: 'otel-token', exportAuthSecretKey: 'api-key' }))
  secrets('one, in the namespace of the release', to({ resourceUsage: true, exportAuthSecretName: 'otel-token' }), 'ikhnos')
  secrets('one, a namespace that is not a name is dropped', to({ resourceUsage: true, exportAuthSecretName: 'otel-token' }), 'x; rm -rf /')
  secrets('an operator is the same', to({ resourceUsage: true, exportAuthSecretName: 'otel-token', exportOperatorId: 'op-1' }))
  secrets('telemetry off', { ...emptyTelemetry, exportAuthSecretName: 'otel-token' })
  secrets('a draft with a problem', to({ resourceUsage: true, exportEndpoint: '', exportAuthSecretName: 'otel-token' }))
  secrets('a Secret name that is not a name', to({ resourceUsage: true, exportAuthSecretName: 'x; rm -rf /' }))
  secrets('routes sharing a Secret', routed({ metrics: { exportAuthSecretName: 'grafana-token' }, logs: { exportAuthSecretName: 'grafana-token' }, traces: { exportAuthSecretName: 'tempo-token' } }))
  secrets('routes, one Secret under two keys', routed({ metrics: { exportAuthSecretName: 'shared', exportAuthSecretKey: 'a' }, logs: { exportAuthSecretName: 'shared', exportAuthSecretKey: 'b' } }))
  secrets('routes, one with none', routed({ traces: { exportAuthSecretName: 'tempo-token' } }))
  secrets('routes, a lane with no signal on is not read', routed({ logs: { exportAuthSecretName: 'unused' } }, { systemLogs: false }))
}

// ---------- what is wrong with a draft ----------
// The page checks a draft as it is edited (telemetryProblems) and the server checks the one it is sent (Telemetry.Problems in Go). They must
// say the same, and these pin what the page says of each draft, beside the spec the server is sent; the Go test holds the server to it.
// The page knows more than the server about one thing - which backends carry which signals (Jaeger only traces) - and says it too; the Go test
// names that advice and sets it aside. Everything else is the same words in the same order.
interface ProblemCase { name: string; problems: string[]; spec: { telemetry: ReturnType<typeof telemetrySpec> } }
const problemCases: ProblemCase[] = []
function problem(name: string, t: TelemetryInput, measurementsOn?: boolean) {
  assert.ok(!problemCases.some((c) => c.name === name), `two problem cases are called ${name}`)
  problemCases.push({ name, problems: telemetryProblems(t, measurementsOn), spec: { telemetry: telemetrySpec(t, measurementsOn) } })
}
{
  const routed = (lanes: Record<string, Partial<TelemetryInput>>, over: Partial<TelemetryInput> = {}): TelemetryInput => ({
    ...emptyTelemetry, resourceUsage: true, systemLogs: true, traces: true, exportSplit: true,
    exportLanes: { metrics: lane('m.example.com:4317', lanes.metrics), logs: lane('l.example.com:4317', lanes.logs), traces: lane('t.example.com:4317', lanes.traces) }, ...over,
  })
  const namedFilter = { ...newProcessorEntry('filter'), name: 'drop_debug', config: { signal: 'log' as const, conditions: [{ field: 'level', op: 'eq' as const, value: 'debug' }] } }
  // fine
  problem('nothing on', emptyTelemetry)
  problem('one signal, an endpoint', to({ resourceUsage: true }))
  problem('every signal, an endpoint', to({ ...Object.fromEntries(PICKABLE_SIGNALS.map((s) => [s.id, true])) as Partial<TelemetryInput>, clusterLinksProbe: true }), true)
  // where it goes
  problem('no endpoint', to({ resourceUsage: true, exportEndpoint: '' }))
  problem('an endpoint of spaces', to({ resourceUsage: true, exportEndpoint: '   ' }))
  problem('an endpoint with a space inside', to({ resourceUsage: true, exportEndpoint: 'otel example.com:4317' }))
  problem('an endpoint with a line break', to({ resourceUsage: true, exportEndpoint: 'otel.example.com:4317\nrm -rf /' }))
  problem('an endpoint that is a shell command', to({ resourceUsage: true, exportEndpoint: '$(id).example.com:4317' }))
  problem('a Secret that is not a name', to({ resourceUsage: true, exportAuthSecretName: 'Not A Name' }))
  problem('a Secret key that is not a name', to({ resourceUsage: true, exportAuthSecretName: 'ok', exportAuthSecretKey: 'bad key' }))
  problem('a header that is not a header', to({ resourceUsage: true, exportAuthHeaderName: 'bad header', exportAuthSecretName: 'ok' }))
  problem('a destination as installed, with no endpoint', keepAsInstalled(to({ resourceUsage: true, exportEndpoint: '' }), ['destination']))
  problem('a destination as installed, with a bad Secret', keepAsInstalled(to({ resourceUsage: true, exportAuthSecretName: 'Bad Name' }), ['destination']))
  // the sources of the signals
  problem('energy from an existing source, none named', to({ energy: true, energySource: 'existing' }))
  problem('energy from an existing source, a bad address', to({ energy: true, energySource: 'existing', energyExistingEndpoint: 'not an address' }))
  problem('accelerators from an existing source, none named', to({ accelerators: true, acceleratorsSource: 'existing' }))
  problem('accelerators from an existing source, a bad address', to({ accelerators: true, acceleratorsSource: 'existing', acceleratorsExistingEndpoint: 'a;b' }))
  problem('energy from an existing source, the signal off', to({ resourceUsage: true, energySource: 'existing' }))
  problem('cluster links, a Secret that is not a name', to({ clusterLinks: true, clusterLinksProbe: true, clusterLinksThroughput: true, clusterLinksAuthSecret: 'Bad_Name' }))
  problem('cluster links, a Secret that is not a name, without throughput', to({ clusterLinks: true, clusterLinksProbe: true, clusterLinksThroughput: false, clusterLinksAuthSecret: 'Bad_Name' }))
  problem('cluster links, a Secret that is not a name, kept', keepAsInstalled(to({ clusterLinks: true, clusterLinksProbe: true, clusterLinksThroughput: true, clusterLinksAuthSecret: 'Bad_Name' }), ['clusterLinksOptions']))
  // path measurements
  problem('network latency with measurements off', to({ networkLatency: true }), false)
  problem('network latency with measurements on', to({ networkLatency: true }), true)
  problem('network latency, measurements unknown', to({ networkLatency: true }))
  // processing
  problem('traces sampling over 100', to({ traces: true, tracesSamplingPercent: 101 }))
  problem('traces sampling below 0', to({ traces: true, tracesSamplingPercent: -1 }))
  problem('traces sampling emptied', to({ traces: true, tracesSamplingPercent: NaN }))
  problem('traces sampling 0', to({ traces: true, tracesSamplingPercent: 0 }))
  problem('an unnamed filter', to({ applicationLogs: true, extraProcessors: [{ ...newProcessorEntry('filter'), id: 'p1' }] }))
  problem('a named filter with a condition', to({ applicationLogs: true, extraProcessors: [namedFilter] }))
  problem('two processors of one name', to({ applicationLogs: true, extraProcessors: [namedFilter, { ...namedFilter, id: 'other' }] }))
  problem('a reserved tag', to({ resourceUsage: true, tags: [{ key: 'continuum.x', value: 'y' }] }))
  problem('a tag with a space in its name', to({ resourceUsage: true, tags: [{ key: 'a b', value: 'y' }] }))
  problem('a tag listed twice', to({ resourceUsage: true, tags: [{ key: 'a', value: 'y' }, { key: 'a', value: 'z' }] }))
  problem('a tag with no name', to({ resourceUsage: true, tags: [{ key: '', value: 'y' }] }))
  problem('a tag row nobody typed into', to({ resourceUsage: true, tags: [{ key: '', value: '' }] }))
  // scope
  problem('application metrics from a namespace that is not a name', to({ applicationMetrics: true, applicationMetricsScope: { namespaces: ['Bad Name'], exclude: [], workloads: [] } }))
  problem('application logs excluding a namespace that is not a name', to({ applicationLogs: true, applicationLogsScope: { namespaces: [], exclude: ['x;y'], workloads: [] } }))
  problem('traces with a workload that is not a name', to({ traces: true, tracesScope: { namespaces: ['ok'], exclude: [], workloads: [{ namespace: 'ok', names: ['Bad'] }] } }))
  problem('traces with workloads for a namespace listed twice', to({ traces: true, tracesScope: { namespaces: ['ok'], exclude: [], workloads: [{ namespace: 'ok', names: ['a'] }, { namespace: 'ok', names: ['b'] }] } }))
  problem('traces with workloads of a namespace that is not a name', to({ traces: true, tracesScope: { namespaces: [], exclude: [], workloads: [{ namespace: 'Bad', names: ['a'] }] } }))
  problem('a bad namespace in the scope of a signal that is off', to({ resourceUsage: true, applicationMetricsScope: { namespaces: ['Bad Name'], exclude: [], workloads: [] } }))
  // one destination per signal type
  problem('routes: all three with an endpoint', routed({}))
  problem('routes: no destination for metrics', routed({ metrics: { exportEndpoint: '' } }))
  problem('routes: no destination for logs and traces', routed({ logs: { exportEndpoint: ' ' }, traces: { exportEndpoint: '' } }))
  problem('routes: a lane with a bad endpoint', routed({ logs: { exportEndpoint: 'bad endpoint' } }))
  problem('routes: a lane with a bad Secret', routed({ traces: { exportAuthSecretName: 'Bad Name' } }))
  problem('routes: Zipkin for metrics', routed({ metrics: { exportProtocol: 'zipkin' } }))
  problem('routes: Zipkin for traces', routed({ traces: { exportProtocol: 'zipkin' } }))
  problem('routes: a lane as installed, with a bad Secret', routed({ logs: { exportAuthSecretName: 'Bad Name' } }, { exportLanesKept: ['logs'] }))
  problem('routes: a lane that is off is not read', routed({ logs: { exportEndpoint: '' } }, { systemLogs: false }))
  problem('routes: Jaeger for metrics (the page knows what Jaeger carries)', routed({ metrics: { exportEndpoint: 'jaeger-collector.observability:4317' } }))
  // one destination, other kinds of backend
  problem('Zipkin for metrics', to({ resourceUsage: true, exportProtocol: 'zipkin' }))
  problem('Zipkin for metrics and logs', to({ resourceUsage: true, systemLogs: true, traces: true, exportProtocol: 'zipkin' }))
  problem('Zipkin for traces', to({ traces: true, exportProtocol: 'zipkin' }))
  problem('Zipkin with only energy on', to({ energy: true, exportProtocol: 'zipkin' }))
  problem('Jaeger for everything (the page knows what Jaeger carries)', to({ resourceUsage: true, systemLogs: true, traces: true, exportEndpoint: 'jaeger-collector.observability:4317' }))
  for (const p of EXPORT_PRESETS) problem(`preset ${p.id} with every signal`, to({ resourceUsage: true, systemLogs: true, traces: true, exportEndpoint: p.endpointPattern.replace(/<[^<>]*>/g, 'x'), exportProtocol: p.protocol }))
}

/** The cases as the file holds them: a command per entry, and its spec on one line, so that a change of what is written reads as a change of
 *  a line and not as thousands. */
function serialize(all: { spec?: unknown }[]): string {
  const entries = all.map(({ spec, ...rest }) => {
    const text = JSON.stringify(rest, null, 2)
    return (spec ? `${text.replace(/\n}$/, '')},\n  "spec": ${JSON.stringify(spec)}\n}` : text).split('\n').map((l) => `  ${l}`).join('\n')
  })
  return `[\n${entries.join(',\n')}\n]\n`
}

test('every case is named once and carries a command', () => {
  assert.ok(cases.length >= 60, `only ${cases.length} cases: a loop above no longer produces its cases`)
  for (const c of cases) assert.ok(c.command.startsWith('helm '), c.name)
})

test('the commands that create the credential Secrets are the ones pinned in tests/install_secrets.golden.json', () => {
  const file = 'tests/install_secrets.golden.json'
  const got = serialize(secretCases)
  if (process.env.UPDATE_GOLDEN) {
    writeFileSync(file, got)
    return
  }
  assert.equal(got, readFileSync(file, 'utf8'), 'a Secret command the UI prints changed. If this is intended: UPDATE_GOLDEN=1 npm run test:unit')
})

test('what the page says is wrong with a draft is what is pinned in tests/telemetry_problems.golden.json', () => {
  assert.ok(problemCases.filter((c) => c.problems.length > 0).length >= 30, 'too few drafts with a problem: the cases above no longer produce theirs')
  assert.ok(problemCases.filter((c) => c.problems.length === 0).length >= 5, 'too few drafts without one: the server could be wrong the other way unseen')
  const file = 'tests/telemetry_problems.golden.json'
  const got = serialize(problemCases)
  if (process.env.UPDATE_GOLDEN) {
    writeFileSync(file, got)
    return
  }
  assert.equal(got, readFileSync(file, 'utf8'), 'what the page says of a draft changed. If this is intended: UPDATE_GOLDEN=1 npm run test:unit  (then run the Go tests, which hold the server to it)')
})

test('the commands the UI prints are the ones pinned in tests/install_commands.golden.json', () => {
  const file = 'tests/install_commands.golden.json'
  const got = serialize(cases)
  if (process.env.UPDATE_GOLDEN) {
    writeFileSync(file, got)
    return
  }
  const want = readFileSync(file, 'utf8')
  if (want === got) return
  const w = new Map((JSON.parse(want) as Case[]).map((c) => [c.name, c]))
  const lines: string[] = []
  for (const c of cases) {
    const old = w.get(c.name)
    if (!old) lines.push(`+ new case: ${c.name}`)
    else if (old.command !== c.command || old.chart !== c.chart) lines.push(`~ changed: ${c.name}\n    was: ${old.command.replace(/\\\n\s*/g, '')}\n    now: ${c.command.replace(/\\\n\s*/g, '')}`)
    w.delete(c.name)
  }
  for (const n of w.keys()) lines.push(`- gone: ${n}`)
  assert.fail(`a command the UI prints changed:\n${lines.join('\n')}\nIf this is intended: UPDATE_GOLDEN=1 npm run test:unit  (then run the Go chart test, which renders every command pinned here)`)
})

// ---------- the block a person pastes to point an agent at a regional operator ----------
// The server writes the operator's side (the certificate Secrets and the destination values, which it alone can state) and the page wrote
// the rest and merged the two by pattern. A case here is the page's draft, what the server says of its operators, and the block that came
// out of merging them; the Go test (install/operator_block_test.go) renders the same from the draft and the server's part, and demands the
// same bytes.

interface BlockFragmentValue { key: string; value: string }
interface BlockCase {
  name: string
  chartFile: string
  spec: ReturnType<typeof telemetrySpec>
  /** What the server says of the release and of how each operator's receiver authenticates (the page's draft says nothing of either). */
  namespace: string
  release: string
  operators: Record<string, string>
  /** What the server states of the destination(s) and of whom the telemetry belongs to, and the Secrets it needs run first. */
  fragment: BlockFragmentValue[]
  certificates: string[]
  block: string
}
const blockCases: BlockCase[] = []

const opTarget = (id: string, over: Partial<typeof emptyExportTarget> = {}) => ({ ...emptyExportTarget, exportEndpoint: `${id}.continuum-system.svc:4317`, exportOperatorId: id, ...over })
const mtlsFragment = (base: string, id: string, real?: string): BlockFragmentValue[] => [
  { key: `${base}.endpoint`, value: real ?? `${id}.continuum-system.svc:4317` },
  { key: `${base}.tls.mtls.enabled`, value: 'true' },
  { key: `${base}.tls.mtls.secretName`, value: `${id}-export-mtls` },
]
const provenance: BlockFragmentValue[] = [
  { key: 'telemetry.resource.orgId', value: 'org-1' }, { key: 'telemetry.resource.clusterId', value: 'cl-1' }, { key: 'telemetry.resource.intentId', value: 'ti-1' },
]
const certSecret = (id: string, ns = 'continuum-system') => `kubectl create secret generic ${id}-export-mtls --namespace ${ns} \\\n  --from-literal=tls.crt="C"`

function block(name: string, draft: TelemetryInput, o: { operators: Record<string, string>; fragment: BlockFragmentValue[]; certificates?: string[]; namespace?: string; release?: string; measurementsOn?: boolean; chartFile?: string }) {
  assert.ok(!blockCases.some((c) => c.name === name), `two block cases are called ${name}`)
  const certificates = o.certificates ?? []
  const namespace = o.namespace ?? 'continuum-system'
  const release = o.release ?? 'continuum-agent'
  const chartFile = o.chartFile ?? 'continuum-agent-0.4.0.tgz'
  const text = operatorCommandBlock({
    install: { chartFile }, draft, measurementsOn: o.measurementsOn,
    result: {
      installFragment: o.fragment.map((v) => `--set ${v.key}=${v.value}`).join(' '), secretCommands: certificates, namespace, release,
      operators: o.operators as never,
      ...(Object.keys(o.operators).length === 1 && !draft.exportSplit ? { receiverAuth: Object.values(o.operators)[0] as never } : {}),
    },
  })
  blockCases.push({ name, chartFile, spec: telemetrySpec(draft, o.measurementsOn), namespace, release, operators: o.operators, fragment: o.fragment, certificates, block: text })
}

{
  const one = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'op-eu.continuum-system.svc:4317', exportOperatorId: 'op-eu', ...over })
  const frag = [...provenance, ...mtlsFragment('telemetry.export.otlp', 'op-eu')]
  const mtls = { 'op-eu': 'mtls' }
  block('operator: one destination, mutual TLS only', one(), { operators: mtls, fragment: frag, certificates: [certSecret('op-eu')] })
  block('operator: nothing left of an earlier destination (http, skip-verify)', one({ exportProtocol: 'http', exportInsecure: true }), { operators: mtls, fragment: frag, certificates: [certSecret('op-eu')] })
  block('operator: a credential the operator does not take is dropped', one({ exportAuthHeaderName: 'x-api-key', exportAuthSecretName: 'old-token', exportAuthSecretKey: 'k' }), { operators: mtls, fragment: frag, certificates: [certSecret('op-eu')] })
  block('operator: a receiver token as well as the certificate', one({ exportAuthSecretName: 'op-token' }), { operators: { 'op-eu': 'bearer' }, fragment: frag, certificates: [certSecret('op-eu')] })
  block('operator: the real address replaces the placeholder', one(), { operators: mtls, fragment: [...provenance, ...mtlsFragment('telemetry.export.otlp', 'op-eu', 'op-eu.example.net:443')], certificates: [certSecret('op-eu')] })
  block('operator: no certificate to create', one(), { operators: mtls, fragment: frag })
  block('operator: where the release runs is the server\'s word', one({ exportAuthSecretName: 'op-token' }), { operators: { 'op-eu': 'bearer' }, fragment: frag, certificates: [certSecret('op-eu', 'edge-agents')], namespace: 'edge-agents', release: 'cont-edge' })
  block('operator: a namespace or release that is not a plain name is not pasted', one({ exportAuthSecretName: 'op-token' }), { operators: { 'op-eu': 'bearer' }, fragment: frag, namespace: 'x; rm -rf /', release: '$(id)' })
  block('operator: a registry-less chart file is named as given', one(), { operators: mtls, fragment: frag, chartFile: 'continuum-agent.tgz' })
  block('operator: with the rest of a full configuration', one({
    traces: true, tracesScope: { namespaces: ['shop'], exclude: ['kube-system'], workloads: [] }, applicationLogs: true, redaction: true, tracesSamplingPercent: 20,
    tags: [{ key: 'team', value: 'edge' }], debugVerbosity: 'basic', hadTelemetry: true,
  }), { operators: mtls, fragment: frag, certificates: [certSecret('op-eu')], measurementsOn: true })
  block('operator: groups kept as installed', keepAsInstalled(one({ hadTelemetry: true }), ['scopeShared', 'tags', 'debug']), { operators: mtls, fragment: frag, certificates: [certSecret('op-eu')] })

  const split = (lanes: Partial<Record<'metrics' | 'logs' | 'traces', ReturnType<typeof opTarget>>>, over: Partial<TelemetryInput> = {}): TelemetryInput => ({
    ...emptyTelemetry, resourceUsage: true, systemLogs: true, exportSplit: true,
    exportLanes: {
      metrics: opTarget('op-eu'), logs: opTarget('op-eu'),
      traces: { ...emptyExportTarget, exportEndpoint: 'z.example.com:9411/api/v2/spans', exportProtocol: 'zipkin', exportInsecure: true },
      ...lanes,
    },
    ...over,
  })
  const routeFrag = [...provenance, ...mtlsFragment('telemetry.export.routes.logs', 'op-eu'), ...mtlsFragment('telemetry.export.routes.metrics', 'op-eu')]
  block('operator routes: two signal types to one operator, one certificate', split({}), { operators: mtls, fragment: routeFrag, certificates: [certSecret('op-eu')] })
  block('operator routes: the real address replaces the placeholder of each', split({}), {
    operators: mtls, certificates: [certSecret('op-eu')],
    fragment: [...provenance, ...mtlsFragment('telemetry.export.routes.logs', 'op-eu', 'op-eu.example.net:443'), ...mtlsFragment('telemetry.export.routes.metrics', 'op-eu', 'op-eu.example.net:443')],
  })
  block('operator routes: an external signal type keeps its own credential', split({ traces: { ...emptyExportTarget, exportEndpoint: 't.example.com:4317', exportAuthSecretName: 'trace-key' } }, { traces: true }), {
    operators: mtls, fragment: [...routeFrag], certificates: [certSecret('op-eu')],
  })
  block('operator routes: two operators, one that also wants a token', split({ logs: opTarget('op-us', { exportAuthSecretName: 'us-token' }), metrics: opTarget('op-eu', { exportAuthSecretName: 'eu-token' }) }), {
    operators: { 'op-eu': 'mtls', 'op-us': 'bearer' }, certificates: [certSecret('op-us'), certSecret('op-eu')],
    fragment: [...provenance, ...mtlsFragment('telemetry.export.routes.logs', 'op-us'), ...mtlsFragment('telemetry.export.routes.metrics', 'op-eu')],
  })
  const leftovers = { exportProtocol: 'http' as const, exportInsecure: true, exportAuthHeaderName: 'x-api-key', exportAuthSecretName: 'old-token', exportAuthSecretKey: 'k' }
  block('operator routes: every signal type to an operator, what an earlier destination left is dropped from each', split({ metrics: opTarget('op-eu', leftovers), logs: opTarget('op-us', leftovers), traces: opTarget('op-eu', leftovers) }, { traces: true }), {
    operators: { 'op-eu': 'mtls', 'op-us': 'bearer' }, certificates: [certSecret('op-eu'), certSecret('op-us')],
    fragment: [...provenance, ...mtlsFragment('telemetry.export.routes.logs', 'op-us'), ...mtlsFragment('telemetry.export.routes.metrics', 'op-eu'), ...mtlsFragment('telemetry.export.routes.traces', 'op-eu')],
  })
  block('operator routes: a signal type that is off has no route', split({}, { systemLogs: false }), { operators: mtls, fragment: [...provenance, ...mtlsFragment('telemetry.export.routes.metrics', 'op-eu')], certificates: [certSecret('op-eu')] })
}

test('the blocks the page pastes for an operator destination are the ones pinned in tests/operator_block.golden.json', () => {
  assert.ok(blockCases.length >= 15)
  const file = 'tests/operator_block.golden.json'
  const got = serialize(blockCases)
  if (process.env.UPDATE_GOLDEN) {
    writeFileSync(file, got)
    return
  }
  assert.equal(got, readFileSync(file, 'utf8'), 'a block the page pastes for an operator destination changed. If this is intended: UPDATE_GOLDEN=1 npm run test:unit  (then run the Go tests)')
})
