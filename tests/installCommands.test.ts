import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { test } from 'node:test'
import {
  cleanTags, emptyScopeOverride, emptyTelemetry, keepAsInstalled, telemetryProblems, withFlowObserver, withMeasurements, withNodeProbe, withScope,
  withTelemetry, emptyScope, scopeProblems, type ScopeInput, type TelemetryInput,
} from '../src/lib/install'
import { helmUpgradeCommand, telemetryUpgradeCommand, telemetrySecretCommand, PICKABLE_SIGNALS, TELEMETRY_INTENT_PRESETS, applyIntentPreset } from '../src/lib/consent'
import { EXPORT_PRESETS } from '../src/lib/exportPresets'
import { newProcessorEntry } from '../src/lib/processorCatalog'
import { buildOperatorInstallCommand } from '../src/lib/operatorInstall'
import { processorSpec, telemetrySpec } from '../src/lib/telemetrySpec'

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
