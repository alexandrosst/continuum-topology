import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { test } from 'node:test'
import { emptyTelemetry, keepAsInstalled, telemetryProblems, type TelemetryInput } from '../src/lib/install'
import { PICKABLE_SIGNALS } from '../src/lib/consent'
import { EXPORT_PRESETS } from '../src/lib/exportPresets'
import { newProcessorEntry } from '../src/lib/processorCatalog'
import { telemetrySpec } from '../src/lib/telemetrySpec'

// What the page says is wrong with a telemetry draft, pinned beside the spec the server is sent (tests/telemetry_problems.golden.json). The page
// no longer writes any command: the server does, from the same spec (backend/internal/install). What stays on the page is the checking of a
// draft as it is edited, and the Go test (install/problems_test.go) holds the server to saying the same.
//
// To accept a change on purpose:  UPDATE_GOLDEN=1 npm run test:unit   (then run the Go tests)

const to = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({ ...emptyTelemetry, exportEndpoint: 'otel.example.com:4317', ...over })
const lane = (endpoint: string, over: Partial<TelemetryInput> = {}) => ({ exportEndpoint: endpoint, exportProtocol: 'grpc' as const, exportInsecure: false, exportAuthHeaderName: '', exportAuthSecretName: '', exportAuthSecretKey: '', exportOperatorId: '', exportOperatorAddress: '', ...over })

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
  const namedFilter = { ...newProcessorEntry('filter'), id: 'p-named', name: 'drop_debug', config: { signal: 'log' as const, conditions: [{ field: 'level', op: 'eq' as const, value: 'debug' }] } }
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
  problem('a reserved tag', to({ resourceUsage: true, tags: [{ key: 'ikhnos.x', value: 'y' }] }))
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

function serialize(all: { spec?: unknown }[]): string {
  const entries = all.map(({ spec, ...rest }) => {
    const text = JSON.stringify(rest, null, 2)
    return (spec ? `${text.replace(/\n}$/, '')},\n  "spec": ${JSON.stringify(spec)}\n}` : text).split('\n').map((l) => `  ${l}`).join('\n')
  })
  return `[\n${entries.join(',\n')}\n]\n`
}

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
