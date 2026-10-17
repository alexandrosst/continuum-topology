import assert from 'node:assert/strict'
import { test } from 'node:test'
import { activeLanes, cleanTags, isKept, keepAsInstalled, combinedScope, destinationReady, emptyExportTarget, startLanes, withLane, laneView, emptyScopeOverride, emptyTelemetry, scopeOverlap, scopeTag, tagProblems, TAG_LIMIT, telemetryActive, telemetryProblems, workloadProblems, type ScopeOverrideInput, type TelemetryInput } from '../src/lib/install'
import { newProcessorEntry, processorBody, processorProblems } from '../src/lib/processorCatalog'
import { PICKABLE_SIGNALS, applyIntentPreset, describeTelemetryChanges, parseRoutedDestination, seedTelemetryFromInstalled, telemetrySecrets, TELEMETRY_CREDENTIAL_VAR, TELEMETRY_INTENT_PRESETS, TELEMETRY_SIGNALS } from '../src/lib/consent'
import { EXPORT_PRESETS, unsupportedDestinationNote } from '../src/lib/exportPresets'

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
    'accelerators', 'applicationLogs', 'applicationMetrics', 'energy', 'kubernetesEvents', 'kubernetesState',
    'clusterLinks', 'networkLatency', 'nodeRuntime', 'resourceUsage', 'systemLogs', 'traces',
  ].sort())
})

test('an export endpoint is required once anything is on', () => {
  const t = { ...emptyTelemetry, resourceUsage: true }
  assert.deepEqual(telemetryProblems(t), ['An export endpoint is required once any telemetry signal is on'])
})

test('zipkin is a protocol of its own, carried like http, and only valid with traces alone', () => {
  const traces: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'zipkin.obs.svc:9411', exportProtocol: 'zipkin', exportInsecure: true }
  assert.deepEqual(telemetryProblems(traces), [])
  // Anything else on is refused (and so no command is printed): Zipkin cannot receive metrics or logs.
  const mixed: TelemetryInput = { ...traces, resourceUsage: true, systemLogs: true }
  assert.match(telemetryProblems(mixed).join(' '), /Zipkin only carries traces - turn off metrics and logs/)
  // Metrics alone with zipkin is also wrong, and says why rather than printing a command the chart rejects.
  const none: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'z:9411', exportProtocol: 'zipkin' }
  assert.match(telemetryProblems(none).join(' '), /Zipkin only carries traces/)
})

test('energy pointed at an existing source needs its endpoint', () => {
  const missing: TelemetryInput = { ...emptyTelemetry, energy: true, energySource: 'existing', exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(missing), ['The existing Prometheus endpoint is required when energy points at an existing source'])
  assert.deepEqual(telemetryProblems({ ...missing, energyExistingEndpoint: 'kepler.monitoring:9102' }), [])
})

test('energy defaults to the bundled Kepler wherever a draft is created, with no endpoint', () => {
  assert.equal(emptyTelemetry.energySource, 'bundle-kepler')
  assert.equal(emptyTelemetry.energyExistingEndpoint, '')
  assert.equal(seedTelemetryFromInstalled(['energy']).energySource, 'bundle-kepler')
  assert.equal(seedTelemetryFromInstalled(['energy'], { exportEndpoint: 'gw:4317', redactionEnabled: true, resourceDetectionEnabled: false }).energySource, 'bundle-kepler')
})

test('an existing Prometheus endpoint is a host and a port: a scheme or a path stops the collector, so it is refused here', () => {
  const t: TelemetryInput = { ...emptyTelemetry, energy: true, energySource: 'existing', exportEndpoint: 'x:4317' }
  for (const bad of ['kepler.monitoring:9102/metrics', 'http://kepler.monitoring:9102', 'kepler.monitoring', 'a b:9102', 'kepler:99999x']) {
    assert.match(telemetryProblems({ ...t, energyExistingEndpoint: bad }).join(), /host and port only/, bad)
  }
  for (const good of ['kepler.monitoring:9102', '10.0.0.5:9102', 'kepler.monitoring.svc.cluster.local:9102', '[fd00::1]:9102']) assert.deepEqual(telemetryProblems({ ...t, energyExistingEndpoint: good }), [], good)
  const g: TelemetryInput = { ...emptyTelemetry, accelerators: true, acceleratorsSource: 'existing', exportEndpoint: 'x:4317', acceleratorsExistingEndpoint: 'dcgm:9400/metrics' }
  assert.match(telemetryProblems(g).join(), /host and port only/)
})

test('network latency without path measurements is flagged, but only when the caller says measurements are off', () => {
  const t: TelemetryInput = { ...emptyTelemetry, networkLatency: true, exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(t, false), ['Network latency re-emits the path measurements extra, so turn that on too, or it will report nothing'])
  assert.equal(telemetryProblems(t, true).length, 0)
  assert.equal(telemetryProblems(t).length, 0, 'unknown (undefined) is not treated as off')
})

test('traces sampling out of range is a problem', () => {
  const t: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317', tracesSamplingPercent: 150 }
  assert.deepEqual(telemetryProblems(t), ['Traces sampling must be between 0 and 100'])
})

test('an unnamed or empty extra processor is a problem, and blocks the command the same way any other problem does', () => {
  // Captures its own entry rather than hard-coding an id string like "proc-1": newProcessorEntry()'s id
  // counter is module-level and shared across every test in this file, so its exact value depends on how
  // many other tests already called it - fragile to hard-code, trivial to read back off the entry itself.
  const entry = newProcessorEntry('filter')
  const t: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317', extraProcessors: [entry] }
  assert.deepEqual(telemetryProblems(t), ['Every processor needs a name.', `"${entry.id}" (filter) has no conditions - it would drop nothing.`])
})

test('accelerators pointed at an existing source needs its endpoint', () => {
  const missing: TelemetryInput = { ...emptyTelemetry, accelerators: true, acceleratorsSource: 'existing', exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(missing), ['The existing Prometheus endpoint is required when accelerators points at an existing source'])
  assert.deepEqual(telemetryProblems({ ...missing, acceleratorsExistingEndpoint: 'dcgm-exporter.monitoring:9400' }), [])
})

test('intent presets set every signal to exactly their combination, and leave everything else alone', () => {
  const minimal = TELEMETRY_INTENT_PRESETS.find((p) => p.id === 'minimal')!
  const t = applyIntentPreset({ ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317' }, minimal)
  assert.equal(t.resourceUsage, true)
  assert.equal(t.kubernetesState, true)
  assert.equal(t.traces, false, 'a signal not in the preset is turned off, not merely left alone')
  assert.equal(t.exportEndpoint, 'x:4317', 'export target is untouched by applying a preset')

  const debugAll = TELEMETRY_INTENT_PRESETS.find((p) => p.id === 'debug-everything')!
  const everything = applyIntentPreset(emptyTelemetry, debugAll)
  for (const s of PICKABLE_SIGNALS) assert.equal((everything as unknown as Record<string, boolean>)[s.id], true, `${s.id} should be on for debug-everything`)
  // The network-latency signal has no emitter behind it yet: no preset turns it on, and nothing offers it.
  assert.equal(everything.networkLatency, false)
  assert.ok(!PICKABLE_SIGNALS.some((s) => s.id === 'networkLatency'))
})

test('every export preset resolves to the existing generic export.otlp shape (no destination-specific export mode)', () => {
  for (const preset of EXPORT_PRESETS) {
    // A signal this preset can carry: a metrics-only or logs-only one is refused with traces turned on.
    const signal = { metrics: 'resourceUsage', logs: 'systemLogs', traces: 'traces' }[preset.modalities?.[0] ?? 'traces']
    // The placeholder in a pattern (`<region>`) is filled in first: left in, the command would send everything to a host that does not exist.
    const endpoint = preset.endpointPattern.replace(/<[^<>]*>/g, 'eu1')
    const t: TelemetryInput = { ...emptyTelemetry, [signal]: true, exportEndpoint: endpoint, exportProtocol: preset.protocol }
    if (endpoint !== preset.endpointPattern) {
      assert.match(telemetryProblems({ ...t, exportEndpoint: preset.endpointPattern }).join(), /replace <[^>]*> in the endpoint/, preset.id)
    }
  }
})

test('a known unsupported destination is flagged with why, not silently ignored', () => {
  assert.match(unsupportedDestinationNote('otlp.aws.example.com') ?? '', /AWS/)
  assert.equal(unsupportedDestinationNote('otel-gateway.example.com:4317'), undefined)
})

test('seeding from installedTelemetry turns on exactly the reported signals, nothing else', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces', 'accelerators'])
  assert.equal(seeded.resourceUsage, true)
  assert.equal(seeded.traces, true)
  assert.equal(seeded.accelerators, true)
  assert.equal(seeded.energy, false, 'a signal not reported as installed stays off')
  assert.equal(seeded.redaction, true, 'non-signal fields fall back to the same defaults as a fresh install')

  assert.deepEqual(seedTelemetryFromInstalled([]), emptyTelemetry, 'nothing installed seeds exactly the fresh-install defaults')
})

test('catalog metadata: every signal has a coherent layer/modality/scope, namespaceScopable only where it means something', () => {
  const known: Record<string, { layer: string; modality: string; scope: string }> = {
    resourceUsage: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
    energy: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
    kubernetesState: { layer: 'infrastructure', modality: 'metrics', scope: 'cluster' },
    nodeRuntime: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
    clusterLinks: { layer: 'infrastructure', modality: 'metrics', scope: 'cluster' },
    networkLatency: { layer: 'infrastructure', modality: 'metrics', scope: 'cluster' },
    applicationMetrics: { layer: 'application', modality: 'metrics', scope: 'application' },
    systemLogs: { layer: 'infrastructure', modality: 'logs', scope: 'node' },
    kubernetesEvents: { layer: 'infrastructure', modality: 'logs', scope: 'cluster' },
    applicationLogs: { layer: 'application', modality: 'logs', scope: 'application' },
    traces: { layer: 'application', modality: 'traces', scope: 'application' },
    accelerators: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
  }
  for (const s of TELEMETRY_SIGNALS) {
    const want = known[s.id]
    assert.ok(want, `${s.id} is not in the expected catalog - update this test alongside TELEMETRY_SIGNALS`)
    assert.equal(s.layer, want.layer, `${s.id}.layer`)
    assert.equal(s.modality, want.modality, `${s.id}.modality`)
    assert.equal(s.scope, want.scope, `${s.id}.scope`)
  }
  // namespaceScopable is the one honest exception to layer/scope being the whole story (accelerators is
  // node-scoped infrastructure, but its data can still carry namespace identity) - nothing else should claim it.
  const scopable = TELEMETRY_SIGNALS.filter((s) => s.namespaceScopable).map((s) => s.id)
  assert.deepEqual(scopable, ['accelerators'])
  // application-layer kinds are exactly the application-scoped ones, and vice versa - if this ever drifts,
  // the "Application scope overrides" panel (gated on layer === 'application' scope fields existing) and
  // the chart's own per-kind override plumbing would silently disagree about which kinds it applies to.
  const appLayer = TELEMETRY_SIGNALS.filter((s) => s.layer === 'application').map((s) => s.id).sort()
  const appScope = TELEMETRY_SIGNALS.filter((s) => s.scope === 'application').map((s) => s.id).sort()
  assert.deepEqual(appLayer, appScope)
  assert.deepEqual(appLayer, ['applicationLogs', 'applicationMetrics', 'traces'])
})

test('an invalid namespace in a per-kind scope override is only flagged while its own kind is on', () => {
  const off: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317', applicationMetricsScope: { namespaces: ['not valid!'], exclude: [], workloads: [] } }
  assert.deepEqual(telemetryProblems(off), [], 'applicationMetrics is off, so its override is not even looked at')

  const on: TelemetryInput = { ...off, applicationMetrics: true }
  assert.deepEqual(telemetryProblems(on), ['"not valid!" is not a valid namespace name'])
})


test('telemetryProblems blocks a destination that cannot carry every signal that is on, not just TelemetryFields.tsx\'s warning', () => {
  const jaeger = EXPORT_PRESETS.find((p) => p.id === 'jaeger')!
  // Traces only, pointed at Jaeger: fine, nothing to flag.
  const tracesOnly: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: jaeger.endpointPattern, exportProtocol: jaeger.protocol }
  assert.deepEqual(telemetryProblems(tracesOnly), [])

  // Metrics turned on too, destination still Jaeger (traces only): this must actually block, not just warn.
  const mismatched: TelemetryInput = { ...tracesOnly, resourceUsage: true }
  assert.deepEqual(telemetryProblems(mismatched), ['Jaeger (traces only) only carries traces - turn off the other signals, or send everything somewhere else'])

  // A generic (unrestricted) destination never trips this check, however many signals are on.
  const generic: TelemetryInput = { ...emptyTelemetry, traces: true, resourceUsage: true, systemLogs: true, exportEndpoint: 'otel-gateway.example.com:4317' }
  assert.deepEqual(telemetryProblems(generic), [])
})

test('scopeOverlap: the shared namespace names between two scopes, or none', () => {
  const a: ScopeOverrideInput = { namespaces: ['shop', 'payments'], exclude: [], workloads: [] }
  const b: ScopeOverrideInput = { namespaces: ['payments', 'ops'], exclude: [], workloads: [] }
  assert.deepEqual(scopeOverlap(a, b), ['payments'])
  assert.deepEqual(scopeOverlap(b, a), ['payments'], 'symmetric in content, whichever side is asked')
  assert.deepEqual(scopeOverlap(a, { namespaces: ['ops'], exclude: [], workloads: [] }), [])
  assert.deepEqual(scopeOverlap(a, { namespaces: [], exclude: ['shop'], workloads: [] }), [], 'a shared exclude is not a claim on the same namespace, so it is not an overlap')
  assert.deepEqual(scopeOverlap({ namespaces: [], exclude: [], workloads: [] }, a), [], 'an empty scope (falls back to global) overlaps nothing')
})

const on: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'otel.example.com:4317' }

test('tag problems: reserved prefix, limit, duplicates, missing parts, spaces', () => {
  assert.deepEqual(tagProblems([{ key: 'team', value: 'x' }]), [])
  assert.match(tagProblems([{ key: 'Continuum.org.id', value: 'x' }])[0], /reserved/)
  assert.match(tagProblems(Array.from({ length: TAG_LIMIT + 1 }, (_, i) => ({ key: `k${i}`, value: 'v' })))[0], /At most 8/)
  assert.match(tagProblems([{ key: 'a', value: '1' }, { key: 'a', value: '2' }])[0], /listed twice/)
  assert.match(tagProblems([{ key: 'a', value: ' ' }])[0], /needs a value/)
  assert.match(tagProblems([{ key: ' ', value: 'v' }])[0], /needs a name/)
  assert.deepEqual(tagProblems([{ key: '', value: '' }]), [], 'an untouched row is not a tag')
  assert.match(tagProblems([{ key: 'a b', value: 'v' }])[0], /spaces/)
  assert.deepEqual(cleanTags([{ key: ' a ', value: ' b ' }, { key: '', value: '' }]), [{ key: 'a', value: 'b' }])
})

test('the scope tag names the namespaces of the narrowed application signals, with no commas', () => {
  assert.equal(scopeTag(on), '')
  const t: TelemetryInput = { ...on, traces: true, tracesScope: { namespaces: ['shop', 'payments'], exclude: [], workloads: [] }, applicationLogs: true, applicationLogsScope: { namespaces: ['shop'], exclude: ['tmp', 'legacy'], workloads: [] } }
  // Traces collect both namespaces whole and logs take shop only, so every namespace is collected by some signal and nothing is "excluded"
  // overall: a union of each signal's own exclusions would claim legacy and tmp were left out of what traces still read.
  assert.equal(scopeTag(t), 'payments; shop')
  assert.doesNotMatch(scopeTag(t), /,/)
  // A scope on a signal that is off says nothing.
  assert.equal(scopeTag({ ...on, tracesScope: { namespaces: ['shop'], exclude: [], workloads: [] } }), '')
  // An exclusion is named only when EVERY signal leaves that namespace out; one that collects it everywhere makes the tag say nothing.
  const both: TelemetryInput = { ...on, traces: true, tracesScope: { namespaces: [], exclude: ['tmp'], workloads: [] }, applicationLogs: true, applicationLogsScope: { namespaces: [], exclude: ['tmp', 'legacy'], workloads: [] } }
  assert.equal(scopeTag(both), 'all namespaces - excluding tmp')
  assert.equal(scopeTag({ ...both, applicationLogsScope: { namespaces: [], exclude: [], workloads: [] } }), '')
})

test('the debug exporter is off unless asked for, and always stated', () => {
  // Off from the start: ticking one signal must not also bring an extra exporter on every pipeline that nobody asked for.
  assert.equal(emptyTelemetry.debugVerbosity, '')
})

const scoped = (ns: string[], workloads: { namespace: string; names: string[] }[] = [], exclude: string[] = []): ScopeOverrideInput => ({ namespaces: ns, exclude, workloads })

test('the scope tag spells out a namespace narrowed to workloads, still without commas', () => {
  const t: TelemetryInput = { ...on, traces: true, tracesScope: scoped(['checkout', 'payments'], [{ namespace: 'checkout', names: ['payment-api', 'cart'] }]) }
  assert.equal(scopeTag(t), 'checkout: cart+payment-api; payments')
  assert.doesNotMatch(scopeTag(t), /,/)
  // Another signal that takes the whole namespace makes it whole again.
  const wider: TelemetryInput = { ...t, applicationLogs: true, applicationLogsScope: scoped(['checkout']) }
  assert.equal(scopeTag(wider), 'checkout; payments')
  assert.equal(scopeTag({ ...on, traces: true, tracesScope: scoped(['shop'], [{ namespace: 'shop', names: [] }]) }), 'shop: nothing')
})

test('workload names the chart would refuse are refused here first', () => {
  assert.deepEqual(workloadProblems([{ namespace: 'shop', names: ['cart', 'api-2'] }]), [])
  assert.equal(workloadProblems([{ namespace: 'shop', names: ['Cart'] }]).length, 1)
  assert.equal(workloadProblems([{ namespace: 'shop', names: ['a.b'] }]).length, 1)
  assert.equal(workloadProblems([{ namespace: 'shop', names: [] }, { namespace: 'shop', names: [] }]).length, 1)
  const bad: TelemetryInput = { ...on, traces: true, tracesScope: scoped(['shop'], [{ namespace: 'shop', names: ['A|B'] }]) }
  assert.ok(telemetryProblems(bad).length > 0)
})

test('the scope infrastructure follows: any signal keeping a namespace keeps it, every signal must drop one to drop it', () => {
  const t = (tr: ScopeOverrideInput, lg: ScopeOverrideInput): TelemetryInput => ({ ...on, traces: true, tracesScope: tr, applicationLogs: true, applicationLogsScope: lg })
  assert.deepEqual(combinedScope(t(scoped(['a']), scoped(['b']))), scoped(['a', 'b']))
  assert.deepEqual(combinedScope(t(scoped([], [], ['x', 'y']), scoped([], [], ['y', 'z']))), scoped([], [], ['y']))
  // One signal with no narrowing at all collects everything, so there is nothing to follow.
  assert.deepEqual(combinedScope(t(scoped(['a']), emptyScopeOverride)), emptyScopeOverride)
  assert.deepEqual(combinedScope(on), emptyScopeOverride)
})

test('workloads of a namespace combine only when every signal collecting it narrows it', () => {
  const w = (names: string[]) => [{ namespace: 'a', names }]
  const t = (tr: ScopeOverrideInput, lg: ScopeOverrideInput): TelemetryInput => ({ ...on, traces: true, tracesScope: tr, applicationLogs: true, applicationLogsScope: lg })
  assert.deepEqual(combinedScope(t(scoped(['a'], w(['x'])), scoped(['a'], w(['y'])))).workloads, w(['x', 'y']))
  assert.deepEqual(combinedScope(t(scoped(['a'], w(['x'])), scoped(['a']))).workloads, [])
  // A signal that does not collect the namespace at all does not make it whole.
  assert.deepEqual(combinedScope(t(scoped(['a'], w(['x'])), scoped(['b']))).workloads, w(['x']))
})

/* ---------- one destination per signal type ---------- */

const lane = (endpoint: string, over: Partial<typeof emptyExportTarget> = {}) => ({ ...emptyExportTarget, exportEndpoint: endpoint, ...over })
const routed = (over: Partial<TelemetryInput> = {}): TelemetryInput => ({
  ...emptyTelemetry,
  resourceUsage: true,
  applicationLogs: true,
  traces: true,
  exportSplit: true,
  exportLanes: { metrics: lane('mimir.example.com:4317'), logs: lane('loki.monitoring.svc:3100/otlp', { exportProtocol: 'http', exportInsecure: true }), traces: lane('zipkin.tracing.svc:9411/api/v2/spans', { exportProtocol: 'zipkin', exportInsecure: true }) },
  ...over,
})

test('a lane needs a destination for every signal type that is on, and only a lane that can carry it', () => {
  assert.deepEqual(activeLanes(routed()), ['metrics', 'logs', 'traces'])
  assert.deepEqual(activeLanes(routed({ traces: false })), ['metrics', 'logs'])
  assert.deepEqual(activeLanes({ ...routed(), exportSplit: false }), [])
  const missing = routed({ exportLanes: { ...routed().exportLanes, logs: lane('') } })
  assert.equal(destinationReady(missing), false)
  assert.ok(telemetryProblems(missing).some((p) => /Logs needs a destination/.test(p)))
  const zipkinForMetrics = routed({ exportLanes: { ...routed().exportLanes, metrics: lane('z:9411', { exportProtocol: 'zipkin' }) } })
  assert.ok(telemetryProblems(zipkinForMetrics).some((p) => /Zipkin only carries traces, so it cannot be where metrics go/.test(p)))
  assert.equal(destinationReady(routed()), true)
  // Not routed, the one endpoint is what is needed.
  assert.equal(destinationReady({ ...routed(), exportSplit: false }), false)
})

test('a preset that carries one signal type is fine as that lane and refused as another', () => {
  const jaeger = EXPORT_PRESETS.find((p) => p.id === 'jaeger')!
  const ok = routed({ exportLanes: { ...routed().exportLanes, traces: lane(jaeger.endpointPattern, { exportProtocol: jaeger.protocol }) } })
  assert.deepEqual(telemetryProblems(ok), [])
  const bad = routed({ exportLanes: { ...routed().exportLanes, logs: lane(jaeger.endpointPattern) } })
  assert.ok(telemetryProblems(bad).some((p) => /cannot be where logs go/.test(p)))
})

test('lanes start from the single destination where it fits, and keep what they already have', () => {
  const single: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, traces: true, exportEndpoint: 'gw.example.com:4317', exportAuthSecretName: 'gw-token' }
  const started = startLanes(single)
  assert.equal(started.exportSplit, true)
  assert.equal(started.exportLanes.logs.exportEndpoint, 'gw.example.com:4317')
  assert.equal(started.exportLanes.metrics.exportAuthSecretName, 'gw-token')
  const kept = startLanes({ ...single, exportLanes: { ...single.exportLanes, metrics: lane('mine:4317') } })
  assert.equal(kept.exportLanes.metrics.exportEndpoint, 'mine:4317')
  // Zipkin carries traces only, so the other lanes start empty.
  const zip = startLanes({ ...single, exportEndpoint: 'z:9411/api/v2/spans', exportProtocol: 'zipkin' })
  assert.equal(zip.exportLanes.traces.exportEndpoint, 'z:9411/api/v2/spans')
  assert.equal(zip.exportLanes.metrics.exportEndpoint, '')
  // A regional operator is carried into the lanes too: each lane then states its own mutual-TLS route.
  const op = startLanes({ ...single, exportEndpoint: 'op-eu.continuum-system.svc:4317', exportOperatorId: 'op-eu' })
  assert.equal(op.exportLanes.metrics.exportOperatorId, 'op-eu')
})

test('editing a lane through its view changes only that lane\'s destination', () => {
  const t = routed()
  const edited = withLane(t, 'logs', { ...laneView(t, 'logs'), exportEndpoint: 'new:3100', resourceUsage: false })
  assert.equal(edited.exportLanes.logs.exportEndpoint, 'new:3100')
  assert.equal(edited.exportLanes.metrics.exportEndpoint, 'mimir.example.com:4317')
  assert.equal(edited.resourceUsage, true)
  assert.equal(edited.exportEndpoint, '')
})

test('each lane names its own credential Secret, and lanes that name the same one share it', () => {
  const t = routed({ exportLanes: { metrics: lane('m:4317', { exportAuthSecretName: 'grafana-token' }), logs: lane('l:4317', { exportAuthSecretName: 'grafana-token' }), traces: lane('t:4317', { exportAuthSecretName: 'tempo-token' }) } })
  const secrets = telemetrySecrets(t)
  assert.deepEqual(secrets.map((s) => [s.name, s.variable, s.lanes]), [['grafana-token', `${TELEMETRY_CREDENTIAL_VAR}_METRICS`, ['metrics', 'logs']], ['tempo-token', `${TELEMETRY_CREDENTIAL_VAR}_TRACES`, ['traces']]])
  assert.equal(secrets[0].command.match(/kubectl create secret/g)?.length, 1)
  // The single destination keeps its own variable and a single command.
  const one = telemetrySecrets({ ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'g:4317', exportAuthSecretName: 'tok' })
  assert.deepEqual(one.map((s) => s.variable), [TELEMETRY_CREDENTIAL_VAR])
})

test('the same Secret read under two keys is one command with both', () => {
  const t = routed({ exportLanes: { metrics: lane('m:4317', { exportAuthSecretName: 'shared', exportAuthSecretKey: 'a' }), logs: lane('l:4317', { exportAuthSecretName: 'shared', exportAuthSecretKey: 'b' }), traces: lane('t:4317') } })
  const [secret, ...rest] = telemetrySecrets(t)
  assert.equal(rest.length, 0)
  assert.match(secret.command, /--from-literal=a=.*_METRICS.* --from-literal=b=.*_LOGS/)
})

test('an install that sends signals to several places is seeded as routes, not as one endpoint', () => {
  assert.deepEqual(parseRoutedDestination('gw:4317'), undefined)
  assert.deepEqual(parseRoutedDestination('traces=z:9411,default=gw:4317'), { routes: { traces: 'z:9411' }, fallback: 'gw:4317' })
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces'], { exportEndpoint: 'traces=z:9411,default=gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  assert.equal(seeded.exportSplit, true)
  assert.equal(seeded.exportRoutesInstalled, true)
  assert.equal(seeded.exportLanes.traces.exportEndpoint, 'z:9411')
  assert.equal(seeded.exportLanes.metrics.exportEndpoint, 'gw:4317')
  assert.equal(destinationReady(seeded), true)
  const plain = seedTelemetryFromInstalled(['resourceUsage'], { exportEndpoint: 'gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  assert.equal(plain.exportSplit, false)
  assert.equal(plain.exportEndpoint, 'gw:4317')
})

test('editing a seeded route makes it this draft\'s to state, in full, and only that route', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces'], { exportEndpoint: 'traces=z:9411,default=gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  const edited = withLane(seeded, 'traces', { ...laneView(seeded, 'traces'), exportEndpoint: 'zipkin.obs.svc:9411', exportProtocol: 'zipkin', exportInsecure: true })
  assert.deepEqual(edited.exportLanesKept, ['metrics', 'logs'])
  // Writing a lane back unchanged (what the picker does on every render of it) does not take it over.
  assert.deepEqual(withLane(seeded, 'metrics', laneView(seeded, 'metrics')).exportLanesKept, ['metrics', 'logs', 'traces'])
})


const CFG = { exportEndpoint: 'gw.example.com:4317', redactionEnabled: true, resourceDetectionEnabled: false }
const grant = (over: Record<string, unknown> = {}) => ({ id: 'i', agentId: 'a', name: 'n', status: 'active', namespaces: [], exclude: [], signals: [], destination: { kind: 'external', endpoint: 'granted.example.com:4317' }, createdAt: '', createdBy: '', ...over }) as never

test('seeding: what the agent does not report is kept as installed, so the command leaves it out instead of stating a default', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage'], CFG)
  assert.ok(isKept(seeded, 'tags'), 'an agent that sends no tags says nothing about them')
  assert.ok(isKept(seeded, 'debug'))
  assert.ok(isKept(seeded, 'scopeShared'))
  assert.ok(!isKept(seeded, 'processors'), 'reported, so it is known')
  // Everything reported: nothing is left unknown.
  const full = seedTelemetryFromInstalled(['resourceUsage'], { ...CFG, tags: [], debugVerbosity: '', scope: {} } as never)
  assert.ok(!isKept(full, 'tags') && !isKept(full, 'debug') && !isKept(full, 'scopeShared'))
  // Nothing installed and nothing reported: a fresh draft that states everything.
  assert.deepEqual(seedTelemetryFromInstalled([]).keptAsInstalled, {})
})

test('seeding: the reported tags, debug verbosity and scope come through', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces'], {
    ...CFG,
    tags: [{ key: 'team', value: 'core' }],
    debugVerbosity: 'basic',
    scope: { traces: { namespaces: ['shop'], exclude: ['kube-system'] }, infra: { namespaces: [], exclude: [] } },
  } as never)
  assert.deepEqual(seeded.tags, [{ key: 'team', value: 'core' }])
  assert.equal(seeded.debugVerbosity, 'basic')
  assert.deepEqual(seeded.tracesScope.namespaces, ['shop'])
  assert.equal(seeded.scopeInfrastructure, true)
})

test('seeding: a recorded grant fills in what the agent does not report, and stays out of the command until edited', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage'], undefined, grant())
  assert.equal(seeded.exportEndpoint, 'granted.example.com:4317')
  assert.ok(isKept(seeded, 'destination'))
  assert.ok(!isKept({ ...seeded, exportEndpoint: 'other:4317' }, 'destination'), 'editing it ends the keeping')
  const toOperator = seedTelemetryFromInstalled(['resourceUsage'], undefined, grant({ destination: { kind: 'operator', endpoint: '', targetOperatorId: 'op-eu' } }))
  assert.equal(toOperator.exportOperatorId, 'op-eu')
  // What the agent reports wins over what was once asked for; a revoked grant is not a grant at all.
  assert.equal(seedTelemetryFromInstalled(['resourceUsage'], CFG, grant()).exportEndpoint, 'gw.example.com:4317')
  assert.equal(seedTelemetryFromInstalled(['resourceUsage'], undefined, grant({ status: 'revoked' })).exportEndpoint, '')
})

test('describeTelemetryChanges: says what running the command changes, and nothing when it changes nothing', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage'], CFG)
  assert.deepEqual(describeTelemetryChanges(['resourceUsage'], CFG, seeded), [])
  assert.deepEqual(describeTelemetryChanges(['resourceUsage'], CFG, { ...seeded, traces: true }), ['Turns on Traces', 'Collects traces from all namespaces'])
  assert.deepEqual(describeTelemetryChanges(['resourceUsage'], CFG, { ...seeded, resourceUsage: false }), ['Turns off Resource usage'])
  // Changing a kept setting ends the keeping, and then it is worth saying.
  assert.deepEqual(describeTelemetryChanges(['resourceUsage'], CFG, { ...seeded, exportEndpoint: 'new.example.com:4317' }), ['Sends to new.example.com:4317 instead of gw.example.com:4317'])
  assert.deepEqual(describeTelemetryChanges(['resourceUsage'], CFG, { ...seeded, redaction: false }), ['Changes the redaction or resource-detection processors'])
  // A destination the agent does not report is "set", not "changed".
  const unknown = seedTelemetryFromInstalled(['resourceUsage'], { ...CFG, exportEndpoint: '' })
  assert.deepEqual(describeTelemetryChanges(['resourceUsage'], { ...CFG, exportEndpoint: '' }, { ...unknown, exportEndpoint: 'x.example.com:4317' }), ['Sets the destination to x.example.com:4317, with its protocol, TLS and credential'])
})

test('typed text that reaches the command is validated: endpoints, the credential Secret name, key and header', () => {
  const t: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'otel.example.com:4317' }
  assert.deepEqual(telemetryProblems(t), [])
  // A query string, brackets and braces are fine (the command quotes them); a comma is not (helm --set splits on it).
  assert.deepEqual(telemetryProblems({ ...t, exportEndpoint: 'https://o.example.com/v1?a=1&b=[2]&c={3}' }), [])
  for (const bad of ['a:1,b:2', 'a b', 'a:1/"x"', "a:1/'x'", 'a:1/`x`', 'a:1\\x', 'a:1\nb', ';a']) assert.match(telemetryProblems({ ...t, exportEndpoint: bad }).join(' '), /endpoint/, JSON.stringify(bad))
  assert.match(telemetryProblems({ ...t, exportAuthSecretName: 'my secret' }).join(' '), /Secret name/)
  assert.match(telemetryProblems({ ...t, exportAuthSecretName: 'ok', exportAuthSecretKey: 'a b' }).join(' '), /Secret key/)
  assert.match(telemetryProblems({ ...t, exportAuthSecretName: 'ok', exportAuthHeaderName: 'X Key' }).join(' '), /header name/)
  assert.deepEqual(telemetryProblems({ ...t, exportAuthSecretName: 'ok', exportAuthSecretKey: 'api_key', exportAuthHeaderName: 'X-Key' }), [])
  // The credential fields of a destination kept as installed are not emitted, so they are not held to anything.
  assert.deepEqual(telemetryProblems(keepAsInstalled({ ...t, exportAuthSecretName: 'my secret' }, ['destination'])), [])
  // A sampling percentage that is not a number cannot be printed as one.
  assert.match(telemetryProblems({ ...t, tracesSamplingPercent: NaN }).join(' '), /between 0 and 100/)
})

test('extra processors on an install that has telemetry are left alone until one is added or removed', () => {
  // The agent does not report them, so a draft seeded from an install leaves them alone until one is added or removed.
  const seeded = seedTelemetryFromInstalled(['applicationLogs'], { exportEndpoint: 'x:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  assert.equal(isKept(seeded, 'extraProcessors'), true)
})

test('filter conditions are context-qualified OTTL with quotes and backslashes escaped; resource attributes are spelled as such', () => {
  const mk = (signal: 'metric' | 'log' | 'trace', c: { field: string; op: 'eq' | 'neq' | 'matches'; value: string; level?: 'record' | 'resource' }) =>
    JSON.stringify(processorBody({ ...newProcessorEntry('filter'), name: 'f', config: { signal, conditions: [c] } }))
  assert.match(mk('metric', { field: 'k', op: 'eq', value: 'v' }), /datapoint\.attributes\[\\"k\\"\] == \\"v\\"/)
  assert.match(mk('trace', { field: 'k', op: 'neq', value: 'v' }), /span\.attributes\[\\"k\\"\] != /)
  assert.match(mk('log', { field: 'k8s.namespace.name', op: 'eq', value: 'kube-system', level: 'resource' }), /resource\.attributes\[\\"k8s\.namespace\.name\\"\] == /)
  // A quote or a backslash in a value used to end the OTTL string early (or be eaten as an escape): the rendered body must stay one literal.
  const q = processorBody({ ...newProcessorEntry('filter'), name: 'f', config: { signal: 'log', conditions: [{ field: 'a"b', op: 'matches', value: '^a\\d"$' }] } }) as { log_conditions: string[] }
  assert.equal(q.log_conditions[0], 'IsMatch(log.attributes["a\\"b"], "^a\\\\d\\"$")')
  const tr = processorBody({ ...newProcessorEntry('transform'), name: 't', config: { signal: 'trace', statements: [{ action: 'set', key: 'k"', value: 'a\\b', }] } }) as { trace_statements: { statements: string[] }[] }
  assert.equal(tr.trace_statements[0].statements[0], 'set(attributes["k\\""], "a\\\\b")')
})

test('processor problems: an empty attribute, a regex that does not compile, a raw body that is not an object', () => {
  const f = (conditions: { field: string; op: 'eq' | 'matches'; value: string }[]) => ({ ...newProcessorEntry('filter'), name: 'f', config: { signal: 'log' as const, conditions } })
  assert.match(processorProblems([f([{ field: ' ', op: 'eq', value: 'x' }])]).join(), /no attribute name/)
  assert.match(processorProblems([f([{ field: 'a', op: 'matches', value: '(' }])]).join(), /regular expression/)
  assert.deepEqual(processorProblems([f([{ field: 'a', op: 'matches', value: '^a(b|c)$' }])]), [])
  for (const raw of ['[1]', 'null', '"x"', '3']) assert.match(processorProblems([{ ...newProcessorEntry('filter'), name: 'r', raw }]).join(), /must be a JSON object/, raw)
})

test('seeding: only a draft standing for an install that has telemetry has anything to clear; an unreported source is kept, a reported one is stated', () => {
  assert.equal(seedTelemetryFromInstalled([]).hadTelemetry, false)
  const old = seedTelemetryFromInstalled(['energy', 'accelerators'])
  assert.equal(old.hadTelemetry, true)
  assert.equal(isKept(old, 'energySource'), true)
  assert.equal(isKept(old, 'acceleratorsSource'), true)
  const reported = seedTelemetryFromInstalled(['energy'], { exportEndpoint: 'gw:4317', redactionEnabled: true, resourceDetectionEnabled: false, energySource: 'existing' })
  assert.equal(isKept(reported, 'energySource'), false)
  assert.equal(reported.energySource, 'existing')
})

test('cluster links: throughput tests need no Secret, Ikhnos issues the iperf3 credentials', () => {
  const t: TelemetryInput = { ...emptyTelemetry, clusterLinks: true, clusterLinksThroughput: true, exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(t), [])
})

test('cluster links: credentials of your own are still honoured, and checked when typed', () => {
  const t: TelemetryInput = { ...emptyTelemetry, clusterLinks: true, clusterLinksThroughput: true, exportEndpoint: 'x:4317' }
  assert.equal(telemetryProblems({ ...t, clusterLinksAuthSecret: 'Bad_Name' }).length, 1)
})

test('cluster links: an install that already has them keeps its probe and throughput choices until they are edited', () => {
  const seeded = seedTelemetryFromInstalled(['clusterLinks'], { exportEndpoint: 'gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  assert.equal(seeded.clusterLinks, true)
  assert.ok(isKept(seeded, 'clusterLinksOptions'))
})
