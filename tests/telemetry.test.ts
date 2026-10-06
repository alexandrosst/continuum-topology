import assert from 'node:assert/strict'
import { test } from 'node:test'
import { activeLanes, cleanTags, combinedScope, destinationReady, emptyExportTarget, startLanes, withLane, laneView, emptyScopeOverride, emptyTelemetry, scopeOverlap, scopeTag, tagProblems, TAG_LIMIT, telemetryActive, telemetryProblems, withTelemetry, workloadProblems, type ScopeOverrideInput, type TelemetryInput } from '../src/lib/install'
import { newProcessorEntry } from '../src/lib/processorCatalog'
import { applyIntentPreset, parseRoutedDestination, seedTelemetryFromInstalled, telemetrySecrets, TELEMETRY_CREDENTIAL_VAR, TELEMETRY_INTENT_PRESETS, TELEMETRY_SIGNALS, telemetrySecretCommand, telemetryUpgradeCommand } from '../src/lib/consent'
import { EXPORT_PRESETS, unsupportedDestinationNote } from '../src/lib/exportPresets'

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
    'accelerators', 'applicationLogs', 'applicationMetrics', 'energy', 'kubernetesEvents', 'kubernetesState',
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

test('zipkin is a protocol of its own, carried like http, and only valid with traces alone', () => {
  const traces: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'zipkin.obs.svc:9411', exportProtocol: 'zipkin', exportInsecure: true }
  assert.deepEqual(telemetryProblems(traces), [])
  const cmd = withTelemetry(base, traces)
  assert.match(cmd, /--set telemetry\.export\.otlp\.protocol=zipkin/)
  assert.match(cmd, /--set-string telemetry\.export\.otlp\.endpoint=zipkin\.obs\.svc:9411/)
  // Anything else on is refused (and so no command is printed): Zipkin cannot receive metrics or logs.
  const mixed: TelemetryInput = { ...traces, resourceUsage: true, systemLogs: true }
  assert.match(telemetryProblems(mixed).join(' '), /Zipkin only carries traces - turn off metrics and logs/)
  assert.equal(withTelemetry(base, mixed), base)
  // Metrics alone with zipkin is also wrong, and says why rather than printing a command the chart rejects.
  const none: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'z:9411', exportProtocol: 'zipkin' }
  assert.match(telemetryProblems(none).join(' '), /Zipkin only carries traces/)
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

test('processors are stated explicitly, matching the chart defaults, once any signal is on', () => {
  const t: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317' }
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /--set telemetry\.processors\.resourceDetection\.enabled=false/)
  assert.match(cmd, /--set telemetry\.processors\.redaction\.enabled=true/)
  assert.match(cmd, /--set telemetry\.processors\.tracesSampling\.percentage=100/)
})

test('non-default processor settings are carried', () => {
  const t: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317', resourceDetection: true, redaction: false, tracesSamplingPercent: 25 }
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /--set telemetry\.processors\.resourceDetection\.enabled=true/)
  assert.match(cmd, /--set telemetry\.processors\.redaction\.enabled=false/)
  assert.match(cmd, /--set telemetry\.processors\.tracesSampling\.percentage=25/)
})

test('traces sampling out of range is a problem', () => {
  const t: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317', tracesSamplingPercent: 150 }
  assert.deepEqual(telemetryProblems(t), ['Traces sampling must be between 0 and 100'])
})

test('with no extra processors, withTelemetry adds nothing for them (matches the chart default)', () => {
  const t: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317' }
  const cmd = withTelemetry(base, t)
  assert.doesNotMatch(cmd, /extraProcessors/)
  assert.doesNotMatch(cmd, /extraProcessorNames/)
})

test('a filter extra processor renders its JSON body and is referenced in the shared processor-names list, not the traces-only one', () => {
  const filter = { ...newProcessorEntry('filter'), name: 'drop_debug', config: { signal: 'log' as const, conditions: [{ field: 'level', op: 'eq' as const, value: 'debug' }] } }
  const t: TelemetryInput = { ...emptyTelemetry, applicationLogs: true, exportEndpoint: 'x:4317', extraProcessors: [filter] }
  const cmd = withTelemetry(base, t)
  // Built with JSON.stringify rather than a hand-written regex, so the expectation can't itself drift out of
  // sync with how JSON escapes the nested double quotes inside the OTTL condition string.
  const body = JSON.stringify({ 'filter/drop_debug': { error_mode: 'ignore', log_conditions: ['attributes["level"] == "debug"'] } })
  assert.ok(cmd.includes(`--set-json telemetry.processors.extraProcessors='${body}'`), cmd)
  assert.match(cmd, /--set-string telemetry\.processors\.extraProcessorNames\[0\]=filter\/drop_debug/)
  assert.doesNotMatch(cmd, /extraTracesProcessorNames/)
})

test('an extra processor value containing a single quote is shell-escaped, not silently dropped', () => {
  // Regression test: withTelemetry()'s shQuote helper previously used a nested template literal whose \'
  // was a no-op escape (caught by oxlint's no-useless-escape) - it quietly dropped every embedded apostrophe
  // instead of POSIX-escaping it, which would have corrupted any processor value containing one.
  const filter = { ...newProcessorEntry('filter'), name: 'drop_named', config: { signal: 'log' as const, conditions: [{ field: 'user', op: 'eq' as const, value: "O'Brien" }] } }
  const t: TelemetryInput = { ...emptyTelemetry, applicationLogs: true, exportEndpoint: 'x:4317', extraProcessors: [filter] }
  const cmd = withTelemetry(base, t)
  const body = JSON.stringify({ 'filter/drop_named': { error_mode: 'ignore', log_conditions: ['attributes["user"] == "O\'Brien"'] } })
  // POSIX single-quote escaping, written independently of install.ts's own shQuote so this test doesn't just
  // re-assert whatever that implementation happens to do: close the quote, emit an escaped literal quote,
  // reopen the quote.
  const posixQuote = (s: string) => "'" + s.split("'").join("'\\''") + "'"
  assert.ok(cmd.includes(`--set-json telemetry.processors.extraProcessors=${posixQuote(body)}`), cmd)
  assert.ok(cmd.includes('Brien'), 'the apostrophe-adjacent text must survive, not vanish along with the quote')
})

test('a tailSampling extra processor is referenced only in the traces-only processor-names list', () => {
  const ts = { ...newProcessorEntry('tailSampling'), name: 'errors_only', config: { decisionWaitSeconds: 10, policies: [{ name: 'errors', type: 'statusCode' as const, probabilisticPercent: 10, statusCodes: 'ERROR', latencyThresholdMs: 500 }] } }
  const t: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317', extraProcessors: [ts] }
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /--set-string telemetry\.processors\.extraTracesProcessorNames\[0\]=tail_sampling\/errors_only/)
  assert.doesNotMatch(cmd, /telemetry\.processors\.extraProcessorNames\[/)
})

test('an unnamed or empty extra processor is a problem, and blocks the command the same way any other problem does', () => {
  // Captures its own entry rather than hard-coding an id string like "proc-1": newProcessorEntry()'s id
  // counter is module-level and shared across every test in this file, so its exact value depends on how
  // many other tests already called it - fragile to hard-code, trivial to read back off the entry itself.
  const entry = newProcessorEntry('filter')
  const t: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317', extraProcessors: [entry] }
  assert.deepEqual(telemetryProblems(t), ['Every processor needs a name.', `"${entry.id}" (filter) has no conditions - it would drop nothing.`])
  assert.equal(withTelemetry(base, t), base, 'a command with unresolved problems is left untouched, same as any other telemetryProblems() failure')
})

test('accelerators pointed at an existing source needs its endpoint, and carries the source + endpoint when valid', () => {
  const missing: TelemetryInput = { ...emptyTelemetry, accelerators: true, acceleratorsSource: 'existing', exportEndpoint: 'x:4317' }
  assert.deepEqual(telemetryProblems(missing), ['The existing Prometheus endpoint is required when accelerators points at an existing source'])
  assert.equal(withTelemetry(base, missing), base)

  const ok: TelemetryInput = { ...missing, acceleratorsExistingEndpoint: 'dcgm-exporter.monitoring:9400/metrics' }
  const cmd = withTelemetry(base, ok)
  assert.match(cmd, /--set telemetry\.accelerators\.metrics\.enabled=true/)
  assert.match(cmd, /--set telemetry\.accelerators\.metrics\.source=existing/)
  assert.match(cmd, /--set-string telemetry\.accelerators\.metrics\.existing\.prometheusEndpoint=dcgm-exporter\.monitoring:9400\/metrics/)

  // the bundled-dcgm default never sets `source` or the existing endpoint at all
  const bundled: TelemetryInput = { ...emptyTelemetry, accelerators: true, exportEndpoint: 'x:4317' }
  const bundledCmd = withTelemetry(base, bundled)
  assert.ok(!bundledCmd.includes('telemetry.accelerators.metrics.source'))
})

test('an auth secret name carries the header and secret key, only when actually set', () => {
  const noAuth: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317' }
  assert.ok(!withTelemetry(base, noAuth).includes('telemetry.export.otlp.auth'))

  const withAuth: TelemetryInput = { ...noAuth, exportAuthSecretName: 'telemetry-token', exportAuthHeaderName: 'x-honeycomb-team' }
  const cmd = withTelemetry(base, withAuth)
  assert.match(cmd, /--set-string telemetry\.export\.otlp\.auth\.secretName=telemetry-token/)
  assert.match(cmd, /--set-string telemetry\.export\.otlp\.auth\.secretKey=token/)
  assert.match(cmd, /--set-string telemetry\.export\.otlp\.auth\.headerName=x-honeycomb-team/)

  // the default header (Authorization) is never restated, matching protocol/insecure's own precedent
  const defaultHeader: TelemetryInput = { ...noAuth, exportAuthSecretName: 'telemetry-token' }
  assert.ok(!withTelemetry(base, defaultHeader).includes('telemetry.export.otlp.auth.headerName'))
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
  for (const s of TELEMETRY_SIGNALS) assert.equal((everything as unknown as Record<string, boolean>)[s.id], true, `${s.id} should be on for debug-everything`)
})

test('every export preset resolves to the existing generic export.otlp shape (no destination-specific export mode)', () => {
  for (const preset of EXPORT_PRESETS) {
    // A signal this preset can carry: a metrics-only or logs-only one is refused with traces turned on.
    const signal = { metrics: 'resourceUsage', logs: 'systemLogs', traces: 'traces' }[preset.modalities?.[0] ?? 'traces']
    const t: TelemetryInput = { ...emptyTelemetry, [signal]: true, exportEndpoint: preset.endpointPattern, exportProtocol: preset.protocol }
    const cmd = withTelemetry(base, t)
    assert.match(cmd, /--set-string telemetry\.export\.otlp\.endpoint=/, `${preset.id} should still set telemetry.export.otlp.endpoint`)
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

  // The bug this guards against: seeding from what is actually running, then generating an upgrade command
  // for one additional signal, must not silently turn off everything else that was already on - because
  // withTelemetry states every signal explicitly on every call (see its own comment on why).
  const draft = { ...seedTelemetryFromInstalled(['resourceUsage', 'traces']), energy: true, exportEndpoint: 'otel-gateway.example.com:4317' }
  const cmd = withTelemetry(base, draft)
  assert.match(cmd, /telemetry\.resourceUsage\.metrics\.enabled=true/, 'a signal already installed must survive seeding + one more change')
  assert.match(cmd, /telemetry\.traces\.traces\.enabled=true/, 'a signal already installed must survive seeding + one more change')
  assert.match(cmd, /telemetry\.energy\.metrics\.enabled=true/)

  assert.deepEqual(seedTelemetryFromInstalled([]), emptyTelemetry, 'nothing installed seeds exactly the fresh-install defaults')
})

test('catalog metadata: every signal has a coherent layer/modality/scope, namespaceScopable only where it means something', () => {
  const known: Record<string, { layer: string; modality: string; scope: string }> = {
    resourceUsage: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
    energy: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
    kubernetesState: { layer: 'infrastructure', modality: 'metrics', scope: 'cluster' },
    nodeRuntime: { layer: 'infrastructure', modality: 'metrics', scope: 'node' },
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

test('acceleratorsApplyScope is stated explicitly, like the signal booleans, whether or not accelerators is on', () => {
  const on: TelemetryInput = { ...emptyTelemetry, accelerators: true, acceleratorsApplyScope: true, exportEndpoint: 'x:4317' }
  assert.match(withTelemetry(base, on), /--set telemetry\.accelerators\.metrics\.applyScope=true/)

  // off by default, but still restated as false so a previous true left over from an earlier install
  // doesn't survive a --reuse-values upgrade that merely unchecks accelerators without touching this box
  const off: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317' }
  assert.match(withTelemetry(base, off), /--set telemetry\.accelerators\.metrics\.applyScope=false/)
})

test('per-kind application scope override: stated only while its own kind is on, empty clears a stale prior override', () => {
  const on: TelemetryInput = { ...emptyTelemetry, applicationMetrics: true, applicationLogs: true, traces: true, exportEndpoint: 'x:4317' }
  const cmd = withTelemetry(base, on)
  // nothing set on any override here, but each kind is on - so all six flags are still stated, to '{}',
  // not omitted; omitting them would let a stale prior override survive a --reuse-values upgrade.
  assert.match(cmd, /--set telemetry\.applicationMetrics\.metrics\.scope\.namespaces='\{\}'/)
  assert.match(cmd, /--set telemetry\.applicationMetrics\.metrics\.scope\.exclude='\{\}'/)
  assert.match(cmd, /--set telemetry\.applicationLogs\.logs\.scope\.namespaces='\{\}'/)
  assert.match(cmd, /--set telemetry\.applicationLogs\.logs\.scope\.exclude='\{\}'/)
  assert.match(cmd, /--set telemetry\.traces\.traces\.scope\.namespaces='\{\}'/)
  assert.match(cmd, /--set telemetry\.traces\.traces\.scope\.exclude='\{\}'/)

  const withOverride: TelemetryInput = {
    ...on,
    applicationMetricsScope: { namespaces: ['shop', 'payments'], exclude: ['hr-data'], workloads: [] },
  }
  const cmdOverride = withTelemetry(base, withOverride)
  assert.match(cmdOverride, /--set telemetry\.applicationMetrics\.metrics\.scope\.namespaces='\{shop,payments\}'/)
  assert.match(cmdOverride, /--set telemetry\.applicationMetrics\.metrics\.scope\.exclude='\{hr-data\}'/)

  // the kind itself off: its scope fields are omitted entirely, matching the energy/accelerators-existing-
  // endpoint precedent - the chart's own .enabled gate makes an unstated (or stale) override harmless.
  const off: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'x:4317', applicationMetricsScope: { namespaces: ['shop'], exclude: [], workloads: [] } }
  const cmdOff = withTelemetry(base, off)
  assert.ok(!cmdOff.includes('telemetry.applicationMetrics.metrics.scope'))
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
  assert.ok(withTelemetry(base, tracesOnly).includes('telemetry.export.otlp.endpoint'), 'a valid traces-only destination still produces a command')

  // Metrics turned on too, destination still Jaeger (traces only): this must actually block, not just warn.
  const mismatched: TelemetryInput = { ...tracesOnly, resourceUsage: true }
  assert.deepEqual(telemetryProblems(mismatched), ['Jaeger (traces only) only carries traces - turn off the other signals, or send everything somewhere else'])
  assert.equal(withTelemetry(base, mismatched), base, 'withTelemetry must not emit telemetry flags for a destination that cannot carry everything that is on')

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

test('telemetrySecretCommand creates the Secret the credential flags name, reading the value from the environment', () => {
  const t: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317', exportAuthSecretName: 'telemetry-token', exportAuthSecretKey: '' }
  const cmd = telemetrySecretCommand(t)!
  assert.match(cmd, /^kubectl create secret generic telemetry-token --namespace continuum-system --from-literal=token=/)
  // The value is never in the command: it is read from the shell, and an unset variable stops the command.
  assert.ok(cmd.includes(`"\${${TELEMETRY_CREDENTIAL_VAR}:?set ${TELEMETRY_CREDENTIAL_VAR} to the credential first}"`))
  // Create-or-update, so re-running the command doesn't fail on the Secret already existing.
  assert.match(cmd, /--dry-run=client -o yaml \| kubectl apply -f -$/)
  // The same name and key the upgrade command's own flags point at.
  const upgrade = telemetryUpgradeCommand(undefined, t)
  assert.match(upgrade, /auth\.secretName=telemetry-token/)
  assert.match(upgrade, /auth\.secretKey=token/)
})

test('telemetrySecretCommand is undefined when there is nothing valid to create', () => {
  const on: TelemetryInput = { ...emptyTelemetry, traces: true, exportEndpoint: 'x:4317' }
  assert.equal(telemetrySecretCommand(on), undefined, 'no Secret named')
  assert.equal(telemetrySecretCommand({ ...emptyTelemetry, exportAuthSecretName: 'telemetry-token' }), undefined, 'telemetry off')
  assert.equal(telemetrySecretCommand({ ...on, exportEndpoint: '', exportAuthSecretName: 'telemetry-token' }), undefined, 'draft invalid, so the upgrade command carries no credential flags either')
  // A name that is not a plain Kubernetes name is never pasted into a shell line.
  assert.equal(telemetrySecretCommand({ ...on, exportAuthSecretName: 'x; rm -rf /' }), undefined)
  assert.equal(telemetrySecretCommand({ ...on, exportAuthSecretName: 'ok', exportAuthSecretKey: 'a b' }), undefined)
})

const on: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'otel.example.com:4317' }

test('provenance: org and cluster are stamped for any destination, only when known', () => {
  const cmd = withTelemetry(base, { ...on, resourceOrgId: 'org-1', resourceClusterId: 'cl-1' })
  assert.match(cmd, /--set-string telemetry\.resource\.orgId=org-1/)
  assert.match(cmd, /--set-string telemetry\.resource\.clusterId=cl-1/)
  const bare = withTelemetry(base, on)
  assert.doesNotMatch(bare, /telemetry\.resource\.orgId/)
  assert.doesNotMatch(bare, /telemetry\.resource\.clusterId/)
})

test('tags travel as one JSON list, stated even when empty, so a reuse-values upgrade replaces the set', () => {
  assert.match(withTelemetry(base, on), /--set-json telemetry\.resource\.attributes='\[\]'/)
  const cmd = withTelemetry(base, { ...on, tags: [{ key: ' team ', value: "pay'ments" }, { key: '', value: '' }] })
  assert.match(cmd, /--set-json telemetry\.resource\.attributes='\[\{"key":"team","value":"pay'\\''ments"\}\]'/)
})

test('tag problems: reserved prefix, limit, duplicates, missing parts, spaces', () => {
  assert.deepEqual(tagProblems([{ key: 'team', value: 'x' }]), [])
  assert.match(tagProblems([{ key: 'Continuum.org.id', value: 'x' }])[0], /reserved/)
  assert.match(tagProblems(Array.from({ length: TAG_LIMIT + 1 }, (_, i) => ({ key: `k${i}`, value: 'v' })))[0], /At most 8/)
  assert.match(tagProblems([{ key: 'a', value: '1' }, { key: 'a', value: '2' }])[0], /listed twice/)
  assert.match(tagProblems([{ key: 'a', value: ' ' }])[0], /needs a value/)
  assert.match(tagProblems([{ key: ' ', value: 'v' }])[0], /needs a name/)
  assert.deepEqual(tagProblems([{ key: '', value: '' }]), [], 'an untouched row is not a tag')
  assert.match(tagProblems([{ key: 'a b', value: 'v' }])[0], /spaces/)
  // A bad tag blocks the command, like every other problem.
  assert.equal(withTelemetry(base, { ...on, tags: [{ key: 'continuum.x', value: 'v' }] }), base)
  assert.deepEqual(cleanTags([{ key: ' a ', value: ' b ' }, { key: '', value: '' }]), [{ key: 'a', value: 'b' }])
})

test('the scope tag names the namespaces of the narrowed application signals, with no commas', () => {
  assert.equal(scopeTag(on), '')
  const t: TelemetryInput = { ...on, traces: true, tracesScope: { namespaces: ['shop', 'payments'], exclude: [], workloads: [] }, applicationLogs: true, applicationLogsScope: { namespaces: ['shop'], exclude: ['tmp', 'legacy'], workloads: [] } }
  assert.equal(scopeTag(t), 'payments; shop - excluding legacy+tmp')
  assert.doesNotMatch(scopeTag(t), /,/)
  // A scope on a signal that is off says nothing.
  assert.equal(scopeTag({ ...on, tracesScope: { namespaces: ['shop'], exclude: [], workloads: [] } }), '')
  assert.match(withTelemetry(base, t), /--set-string telemetry\.resource\.scope=payments; shop - excluding legacy\+tmp/)
  assert.match(withTelemetry(base, on), /--set-string telemetry\.resource\.scope=(\s|$)/)
})

test('the debug exporter is count-only unless changed, and always stated', () => {
  assert.equal(emptyTelemetry.debugVerbosity, 'basic')
  assert.match(withTelemetry(base, on), /--set-string telemetry\.debug\.verbosity=basic/)
  assert.match(withTelemetry(base, { ...on, debugVerbosity: 'detailed' }), /telemetry\.debug\.verbosity=detailed/)
  assert.match(withTelemetry(base, { ...on, debugVerbosity: '' }), /telemetry\.debug\.verbosity=(\s|$)/)
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

test('a signal with a workload scope states it as JSON, and clearing it states an empty list', () => {
  const t: TelemetryInput = { ...on, traces: true, tracesScope: scoped(['checkout'], [{ namespace: 'checkout', names: ['cart'] }]) }
  assert.match(withTelemetry(base, t), /--set-json telemetry\.traces\.traces\.scope\.workloads='\[\{"namespace":"checkout","names":\["cart"\]\}\]'/)
  assert.match(withTelemetry(base, { ...t, tracesScope: scoped(['checkout']) }), /--set-json telemetry\.traces\.traces\.scope\.workloads='\[\]'/)
  // Not stated for a signal that is off.
  assert.doesNotMatch(withTelemetry(base, on), /scope\.workloads/)
})

test('workload names the chart would refuse are refused here first', () => {
  assert.deepEqual(workloadProblems([{ namespace: 'shop', names: ['cart', 'api-2'] }]), [])
  assert.equal(workloadProblems([{ namespace: 'shop', names: ['Cart'] }]).length, 1)
  assert.equal(workloadProblems([{ namespace: 'shop', names: ['a.b'] }]).length, 1)
  assert.equal(workloadProblems([{ namespace: 'shop', names: [] }, { namespace: 'shop', names: [] }]).length, 1)
  const bad: TelemetryInput = { ...on, traces: true, tracesScope: scoped(['shop'], [{ namespace: 'shop', names: ['A|B'] }]) }
  assert.ok(telemetryProblems(bad).length > 0)
  assert.equal(withTelemetry(base, bad), base)
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

test('infrastructure scope is stated every time: the combined scope when followed, empty when not or when nothing narrows', () => {
  const t: TelemetryInput = { ...on, traces: true, tracesScope: scoped(['checkout'], [{ namespace: 'checkout', names: ['cart'] }]) }
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /--set telemetry\.scope\.infra\.namespaces='\{checkout\}'/)
  assert.match(cmd, /--set-json telemetry\.scope\.infra\.workloads='\[\{"namespace":"checkout","names":\["cart"\]\}\]'/)
  const off = withTelemetry(base, { ...t, scopeInfrastructure: false })
  assert.match(off, /--set telemetry\.scope\.infra\.namespaces='\{\}'/)
  assert.match(off, /--set-json telemetry\.scope\.infra\.workloads='\[\]'/)
  assert.match(withTelemetry(base, on), /--set telemetry\.scope\.infra\.namespaces='\{\}'/)
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

test('sending each signal type separately states every route in full and leaves the default alone', () => {
  const cmd = withTelemetry(base, routed())
  assert.doesNotMatch(cmd, /telemetry\.export\.otlp\.endpoint/)
  assert.match(cmd, /--set-string telemetry\.export\.routes\.metrics\.endpoint=mimir\.example\.com:4317/)
  assert.match(cmd, /--set telemetry\.export\.routes\.metrics\.protocol=grpc/)
  assert.match(cmd, /--set telemetry\.export\.routes\.metrics\.tls\.insecure=false/)
  assert.match(cmd, /--set telemetry\.export\.routes\.logs\.protocol=http/)
  assert.match(cmd, /--set telemetry\.export\.routes\.logs\.tls\.insecure=true/)
  assert.match(cmd, /--set telemetry\.export\.routes\.traces\.protocol=zipkin/)
  // No credential named: stated empty, so one an earlier command set does not linger.
  assert.match(cmd, /--set-string telemetry\.export\.routes\.metrics\.auth\.secretName=(\s|\\|$)/)
})

test('a signal type with nothing on has its route stated empty, so an old one stops sending', () => {
  const cmd = withTelemetry(base, routed({ traces: false }))
  assert.match(cmd, /--set-string telemetry\.export\.routes\.traces\.endpoint=(\s|\\|$)/)
  assert.doesNotMatch(cmd, /routes\.traces\.protocol/)
})

test('a single destination states no routes, unless the install has some to clear', () => {
  const single: TelemetryInput = { ...emptyTelemetry, resourceUsage: true, exportEndpoint: 'gw.example.com:4317' }
  assert.doesNotMatch(withTelemetry(base, single), /export\.routes/)
  const cleared = withTelemetry(base, { ...single, exportRoutesInstalled: true })
  for (const m of ['metrics', 'logs', 'traces']) assert.match(cleared, new RegExp(`--set-string telemetry\\.export\\.routes\\.${m}\\.endpoint=(\\s|\\\\|$)`))
  assert.match(cleared, /--set-string telemetry\.export\.otlp\.endpoint=gw\.example\.com:4317/)
})

test('a lane needs a destination for every signal type that is on, and only a lane that can carry it', () => {
  assert.deepEqual(activeLanes(routed()), ['metrics', 'logs', 'traces'])
  assert.deepEqual(activeLanes(routed({ traces: false })), ['metrics', 'logs'])
  assert.deepEqual(activeLanes({ ...routed(), exportSplit: false }), [])
  const missing = routed({ exportLanes: { ...routed().exportLanes, logs: lane('') } })
  assert.equal(destinationReady(missing), false)
  assert.ok(telemetryProblems(missing).some((p) => /Logs needs a destination/.test(p)))
  assert.equal(withTelemetry(base, missing), base)
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
  const cmd = withTelemetry(base, t)
  assert.match(cmd, /routes\.metrics\.auth\.secretName=grafana-token/)
  assert.match(cmd, /routes\.traces\.auth\.secretName=tempo-token/)
  assert.match(cmd, /routes\.traces\.auth\.headerName=Authorization/)
  const secrets = telemetrySecrets(t)
  assert.deepEqual(secrets.map((s) => [s.name, s.variable, s.lanes]), [['grafana-token', `${TELEMETRY_CREDENTIAL_VAR}_METRICS`, ['metrics', 'logs']], ['tempo-token', `${TELEMETRY_CREDENTIAL_VAR}_TRACES`, ['traces']]])
  assert.equal(secrets[0].command.match(/kubectl create secret/g)?.length, 1)
  assert.equal(telemetrySecretCommand(t)?.split('\n').length, 2)
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

test('a seeded routed install is not restated: changing something else leaves its routes as installed', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces'], { exportEndpoint: 'traces=z:9411,default=gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  const cmd = telemetryUpgradeCommand(undefined, { ...seeded, kubernetesState: true })
  // The agent does not say how a route is spoken to (protocol, TLS, credential), so stating the route would
  // reset those to defaults: it is left out, and --reuse-values keeps what is installed.
  assert.doesNotMatch(cmd, /telemetry\.export\.routes\.(metrics|traces)\./)
  assert.doesNotMatch(cmd, /routes\.\w+\.(protocol|tls|auth)/)
  assert.match(cmd, /telemetry\.kubernetesState\.metrics\.enabled=true/)
})

test('editing a seeded route makes it this draft\'s to state, in full, and only that route', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces'], { exportEndpoint: 'traces=z:9411,default=gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  const edited = withLane(seeded, 'traces', { ...laneView(seeded, 'traces'), exportEndpoint: 'zipkin.obs.svc:9411', exportProtocol: 'zipkin', exportInsecure: true })
  assert.deepEqual(edited.exportLanesKept, ['metrics', 'logs'])
  const cmd = telemetryUpgradeCommand(undefined, edited)
  assert.match(cmd, /--set-string telemetry\.export\.routes\.traces\.endpoint=zipkin\.obs\.svc:9411/)
  assert.match(cmd, /telemetry\.export\.routes\.traces\.protocol=zipkin/)
  assert.match(cmd, /telemetry\.export\.routes\.traces\.tls\.insecure=true/)
  assert.doesNotMatch(cmd, /telemetry\.export\.routes\.metrics\./)
  // Writing a lane back unchanged (what the picker does on every render of it) does not take it over.
  assert.deepEqual(withLane(seeded, 'metrics', laneView(seeded, 'metrics')).exportLanesKept, ['metrics', 'logs', 'traces'])
})

test('a kept route whose signal type is turned off is still cleared', () => {
  const seeded = seedTelemetryFromInstalled(['resourceUsage', 'traces'], { exportEndpoint: 'traces=z:9411,default=gw:4317', redactionEnabled: true, resourceDetectionEnabled: false })
  const cmd = telemetryUpgradeCommand(undefined, { ...seeded, traces: false })
  assert.match(cmd, /--set-string telemetry\.export\.routes\.traces\.endpoint=( |$|\\)/m)
})

