import { buildExtraProcessors, processorProblems, processorTarget, processorKey, type ProcessorEntry } from './processorCatalog'

/**
 * The install command the server prints, with optional in-cluster parts switched on.
 * They are separate opt-ins because they are the only parts of Continuum that run on every node.
 */
function withFlag(install: string, on: boolean, key: string): string {
  if (!on || install.includes(`${key}=true`)) return install
  return `${install.trimEnd()} \\\n  --set ${key}=true`
}

export const withNodeProbe = (install: string, on: boolean): string => withFlag(install, on, 'nodeProbe.enabled')

/** The traffic observer: counts which workloads talk to which. Needs the agent's highest access tier. */
export const withFlowObserver = (install: string, on: boolean): string => withFlag(install, on, 'flowObserver.enabled')

/** Path measurements: the agent may time TCP connections to addresses the server names (no data is sent). */
export const withMeasurements = (install: string, on: boolean): string => withFlag(install, on, 'measurements.enabled')

/* ---------- Which namespaces the agent may report ---------- */

export interface ScopeInput {
  /** Only these namespaces (plus system ones, which are read for detection but never shown). Empty: all. */
  namespaces: string[]
  /** Never these. Wins over everything else. */
  exclude: string[]
  /** Or the namespaces carrying this label, as `key=value` (or just `key`). Added to `namespaces`. */
  selector: string
}

export const emptyScope: ScopeInput = { namespaces: [], exclude: [], selector: '' }

/** "shop, payments  ops" → ["shop","payments","ops"]: split on commas, spaces and newlines, dropping empties and repeats. */
export const splitNames = (text: string): string[] => [...new Set(text.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean))]

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
/** A Kubernetes label key: optional DNS-subdomain prefix, then a name of at most 63 characters. */
const LABEL_KEY = /^([a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?\/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$/
/** The agent only reads labels on its allow-list, so it refuses a selector on any other key rather than silently matching nothing. */
const SELECTABLE_EXACT = ['app', 'k8s-app', 'istio-injection', 'istio.io/rev', 'istio.io/dataplane-mode']
const SELECTABLE_PREFIX = ['continuum.io/', 'app.kubernetes.io/', 'topology.kubernetes.io/']
export const selectableKey = (k: string) => SELECTABLE_EXACT.includes(k) || SELECTABLE_PREFIX.some((p) => k.startsWith(p))

const LABEL_VALUE = /^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$/

/** Every name that isn't a valid Kubernetes namespace (DNS-1123 label), worded for a person to act on. */
export function namespaceListProblems(names: string[]): string[] {
  return names.filter((n) => !DNS_LABEL.test(n)).map((n) => `"${n}" is not a valid namespace name`)
}

/** What is wrong with the scope, in words a person can act on; empty when it is fine. */
export function scopeProblems(s: ScopeInput): string[] {
  const out: string[] = [...namespaceListProblems([...s.namespaces, ...s.exclude])]
  if (s.selector.trim()) {
    const [k, ...rest] = s.selector.trim().split('=')
    if (!LABEL_KEY.test(k)) out.push(`"${k}" is not a valid label key`)
    else if (!selectableKey(k)) out.push(`The agent only reads a fixed list of labels, so select on one starting with continuum.io/ (for example continuum.io/scope=yes), not "${k}"`)
    else if (rest.length > 1 || !LABEL_VALUE.test(rest[0] ?? '')) out.push('Write the label as key=value')
  }
  return out
}

export const scopeActive = (s: ScopeInput) => s.namespaces.length > 0 || s.exclude.length > 0 || !!s.selector.trim()

/** Helm treats commas in a --set value as list separators: escape them. */
const helmList = (xs: string[]) => `'{${xs.join(',')}}'`

/** Adds the scope to the install command. Without one the agent reports every namespace, as before. */
export function withScope(install: string, s: ScopeInput): string {
  if (!scopeActive(s) || scopeProblems(s).length) return install
  let cmd = install.trimEnd()
  if (s.namespaces.length) cmd += ` \\\n  --set scope.namespaces=${helmList(s.namespaces)}`
  if (s.exclude.length) cmd += ` \\\n  --set scope.exclude=${helmList(s.exclude)}`
  if (s.selector.trim()) cmd += ` \\\n  --set-string scope.selector='${s.selector.trim()}'`
  return cmd
}

/* ---------- Telemetry ---------- */

/**
 * The ten independent telemetry signals a chart install can turn on. Each maps to one
 * `telemetry.<signal>.<metrics|logs|traces>.enabled` value in the chart - see its values.yaml for exactly
 * which, and for what each signal actually collects. `applicationMetrics.scrapeTargets` (a structured list
 * with no form-control precedent in this app) and `telemetry.scope` (a second, telemetry-only namespace
 * filter, separate from the agent-wide one above) are deliberately left out of this form for the same
 * reason the wizard's own scope section already leaves out label selectors on unusual keys: they stay an
 * advanced, hand-written `--set` a person adds themselves, same as any other chart value this UI doesn't cover.
 */
/**
 * A per-signal override of the shared telemetry scope (see `applicationMetricsScope` etc. below): each
 * field falls back to the install's global `telemetry.scope` field-by-field when left empty here, exactly
 * like the chart's own values.yaml fallback. Distinct from `ScopeInput` above - no `selector`, since the
 * chart's per-signal override doesn't support one either (same reasoning as the global scope: not
 * implementable cleanly after the fact).
 */
export interface ScopeOverrideInput {
  namespaces: string[]
  exclude: string[]
}

export const emptyScopeOverride: ScopeOverrideInput = { namespaces: [], exclude: [] }

/**
 * The namespace names two scope overrides both explicitly include, or [] if none - the guided telemetry
 * wizard's overlap check (see GuidedScope.tsx). Only `namespaces` is compared: two overrides that merely
 * exclude the same namespace aren't in tension the way two that both claim to *include* it are, and an
 * empty `namespaces` list means "everything" in the chart's own fallback semantics, which every other
 * override would trivially "overlap" if it were included here.
 */
export function scopeOverlap(a: ScopeOverrideInput, b: ScopeOverrideInput): string[] {
  const bs = new Set(b.namespaces)
  return a.namespaces.filter((n) => bs.has(n))
}

export interface TelemetryInput {
  resourceUsage: boolean
  energy: boolean
  /** Only meaningful when `energy` is on: deploy the bundled Kepler DaemonSet, or scrape one that already exists. */
  energySource: 'bundle-kepler' | 'existing'
  /** Required when `energySource` is 'existing': host:port/path already serving Kepler-shaped metrics. */
  energyExistingEndpoint: string
  kubernetesState: boolean
  nodeRuntime: boolean
  /** Re-emits the "Path measurements" extra's own TCP timings as OTel metrics; reports nothing without it. */
  networkLatency: boolean
  applicationMetrics: boolean
  /** Per-signal scope override for applicationMetrics - see `ScopeOverrideInput`. */
  applicationMetricsScope: ScopeOverrideInput
  systemLogs: boolean
  kubernetesEvents: boolean
  applicationLogs: boolean
  /** Per-signal scope override for applicationLogs - see `ScopeOverrideInput`. */
  applicationLogsScope: ScopeOverrideInput
  traces: boolean
  /** Per-signal scope override for traces - see `ScopeOverrideInput`. */
  tracesScope: ScopeOverrideInput
  /** GPU/accelerator utilization, memory and power, via NVIDIA DCGM - bundled, or an existing one already scraped. */
  accelerators: boolean
  /** Only meaningful when `accelerators` is on: deploy the bundled dcgm-exporter DaemonSet, or scrape one that already exists. */
  acceleratorsSource: 'bundle-dcgm' | 'existing'
  /** Required when `acceleratorsSource` is 'existing': host:port/path already serving dcgm-shaped metrics. */
  acceleratorsExistingEndpoint: string
  /** Off by default. Accelerators is infrastructure-domain (no namespace filtering) unless this is on, in
   *  which case dcgm-exporter's own pod-label enrichment is turned on and the install's namespace scope
   *  (global or nothing - accelerators has no override of its own) reaches GPU metrics too. */
  acceleratorsApplyScope: boolean
  /** Where every enabled signal is sent (`telemetry.export.otlp.*`). Required once any signal above is on. */
  exportEndpoint: string
  exportProtocol: 'grpc' | 'http'
  exportInsecure: boolean
  /** The header a destination expects its credential in (e.g. "Authorization", "X-Api-Key"). Only sent
   *  once `exportAuthSecretName` names a Secret - see it below for why no credential value lives here. */
  exportAuthHeaderName: string
  /** A Secret already created in the release namespace, holding just the header's value - never the value
   *  itself, which would otherwise render into a ConfigMap/Secret-less values file in plain text. Empty
   *  means the destination needs no auth header at all. */
  exportAuthSecretName: string
  exportAuthSecretKey: string
  /* ---------- Pipeline processors (telemetry.processors.*): independent of which signals above are on,
     applied whenever any of them is. See the chart's own values.yaml for exactly what each one does. ---------- */
  /** Off by default: enriches every signal with resource attributes about the collector's own environment. */
  resourceDetection: boolean
  /** On by default (matches the chart): masks values of keys that look like secrets/tokens/credentials
   *  before anything leaves the cluster - the safety net for an OTLP receiver that, by default, has no
   *  auth of its own (see telemetry.receiver below). */
  redaction: boolean
  /** Traces only. 100 = no sampling (every span kept), the same as if this were never set. */
  tracesSamplingPercent: number
  /** Extra processors beyond the three fixed knobs above (filter, tail_sampling, transform/redaction) -
   *  see processorCatalog.ts. Routed into the chart's own extraProcessors/extraProcessorNames escape
   *  hatch; the chart's own memory_limiter-first/batch-last pipeline skeleton is never touched. */
  extraProcessors: ProcessorEntry[]
}

export const emptyTelemetry: TelemetryInput = {
  resourceUsage: false,
  energy: false,
  energySource: 'bundle-kepler',
  energyExistingEndpoint: '',
  kubernetesState: false,
  nodeRuntime: false,
  networkLatency: false,
  applicationMetrics: false,
  applicationMetricsScope: emptyScopeOverride,
  systemLogs: false,
  kubernetesEvents: false,
  applicationLogs: false,
  applicationLogsScope: emptyScopeOverride,
  traces: false,
  tracesScope: emptyScopeOverride,
  accelerators: false,
  acceleratorsSource: 'bundle-dcgm',
  acceleratorsExistingEndpoint: '',
  acceleratorsApplyScope: false,
  exportEndpoint: '',
  exportProtocol: 'grpc',
  exportInsecure: false,
  exportAuthHeaderName: '',
  exportAuthSecretName: '',
  exportAuthSecretKey: '',
  resourceDetection: false,
  redaction: true,
  tracesSamplingPercent: 100,
  extraProcessors: [],
}

/** Whether any signal is on - the export endpoint (and every flag below) only matters once one is. */
export const telemetryActive = (t: TelemetryInput): boolean =>
  t.resourceUsage || t.energy || t.kubernetesState || t.nodeRuntime || t.networkLatency ||
  t.applicationMetrics || t.systemLogs || t.kubernetesEvents || t.applicationLogs || t.traces || t.accelerators

/**
 * What is wrong with the telemetry selection, in words a person can act on; empty when it is fine.
 * `measurementsOn` is whether the wizard's own "Path measurements" extra is (or will be) enabled - pass it
 * whenever the caller also controls that toggle, so turning on networkLatency without it is caught here
 * instead of silently reporting nothing once installed.
 */
export function telemetryProblems(t: TelemetryInput, measurementsOn?: boolean): string[] {
  if (!telemetryActive(t)) return []
  const out: string[] = []
  if (!t.exportEndpoint.trim()) out.push('An export endpoint is required once any telemetry signal is on')
  if (t.energy && t.energySource === 'existing' && !t.energyExistingEndpoint.trim()) out.push('The existing Prometheus endpoint is required when energy points at an existing source')
  if (t.accelerators && t.acceleratorsSource === 'existing' && !t.acceleratorsExistingEndpoint.trim()) out.push('The existing Prometheus endpoint is required when accelerators points at an existing source')
  if (t.networkLatency && measurementsOn === false) out.push('Network latency re-emits the path measurements extra, so turn that on too, or it will report nothing')
  if (t.tracesSamplingPercent < 0 || t.tracesSamplingPercent > 100) out.push('Traces sampling must be between 0 and 100')
  out.push(...processorProblems(t.extraProcessors))
  if (t.applicationMetrics) out.push(...namespaceListProblems([...t.applicationMetricsScope.namespaces, ...t.applicationMetricsScope.exclude]))
  if (t.applicationLogs) out.push(...namespaceListProblems([...t.applicationLogsScope.namespaces, ...t.applicationLogsScope.exclude]))
  if (t.traces) out.push(...namespaceListProblems([...t.tracesScope.namespaces, ...t.tracesScope.exclude]))
  return out
}

/**
 * Adds telemetry to the install command: the export target, then every signal's enabled flag, explicitly
 * true or false - not just the ones turned on. This is deliberate, not just belt-and-braces: the same
 * function also builds the post-install "change telemetry" command (telemetryUpgradeCommand in consent.ts),
 * which runs as `helm upgrade --reuse-values` - Helm only changes what a --set actually names, so a signal
 * left unmentioned because it was merely unchecked would keep running. Stating every signal explicitly makes
 * unchecking one in that panel actually turn it off, and costs nothing on a fresh install (an explicit
 * `=false` for a signal that was already going to default to false is a no-op). Without any signal on at
 * all, this returns the command unchanged, exactly as before.
 */
export function withTelemetry(install: string, t: TelemetryInput, measurementsOn?: boolean): string {
  if (!telemetryActive(t) || telemetryProblems(t, measurementsOn).length) return install
  let cmd = install.trimEnd()
  const add = (flag: string) => { cmd += ` \\\n  --set ${flag}` }
  const addString = (flag: string, value: string) => { cmd += ` \\\n  --set-string ${flag}=${value}` }
  addString('telemetry.export.otlp.endpoint', t.exportEndpoint.trim())
  if (t.exportProtocol !== 'grpc') add(`telemetry.export.otlp.protocol=${t.exportProtocol}`)
  if (t.exportInsecure) add('telemetry.export.otlp.tls.insecure=true')
  add(`telemetry.resourceUsage.metrics.enabled=${t.resourceUsage}`)
  add(`telemetry.energy.metrics.enabled=${t.energy}`)
  if (t.energy && t.energySource === 'existing') {
    add('telemetry.energy.metrics.source=existing')
    addString('telemetry.energy.metrics.existing.prometheusEndpoint', t.energyExistingEndpoint.trim())
  }
  add(`telemetry.kubernetesState.metrics.enabled=${t.kubernetesState}`)
  add(`telemetry.nodeRuntime.metrics.enabled=${t.nodeRuntime}`)
  add(`telemetry.networkLatency.metrics.enabled=${t.networkLatency}`)
  add(`telemetry.applicationMetrics.metrics.enabled=${t.applicationMetrics}`)
  // Stated unconditionally while the kind itself is on (even when both lists are empty) - an empty
  // helmList still renders to a valid '{}' the chart accepts as "no override, fall back to global scope",
  // and stating it is what lets clearing an override back to empty actually take effect on --reuse-values;
  // leaving it unstated whenever empty would let a stale prior override survive. Omitted entirely while the
  // kind itself is off, matching the existing energy/accelerators-existing-endpoint precedent - the chart's
  // own gating (parent .enabled check) makes a stale value harmless there.
  if (t.applicationMetrics) {
    add(`telemetry.applicationMetrics.metrics.scope.namespaces=${helmList(t.applicationMetricsScope.namespaces)}`)
    add(`telemetry.applicationMetrics.metrics.scope.exclude=${helmList(t.applicationMetricsScope.exclude)}`)
  }
  add(`telemetry.systemLogs.logs.enabled=${t.systemLogs}`)
  add(`telemetry.kubernetesEvents.logs.enabled=${t.kubernetesEvents}`)
  add(`telemetry.applicationLogs.logs.enabled=${t.applicationLogs}`)
  if (t.applicationLogs) {
    add(`telemetry.applicationLogs.logs.scope.namespaces=${helmList(t.applicationLogsScope.namespaces)}`)
    add(`telemetry.applicationLogs.logs.scope.exclude=${helmList(t.applicationLogsScope.exclude)}`)
  }
  add(`telemetry.traces.traces.enabled=${t.traces}`)
  if (t.traces) {
    add(`telemetry.traces.traces.scope.namespaces=${helmList(t.tracesScope.namespaces)}`)
    add(`telemetry.traces.traces.scope.exclude=${helmList(t.tracesScope.exclude)}`)
  }
  add(`telemetry.accelerators.metrics.enabled=${t.accelerators}`)
  if (t.accelerators && t.acceleratorsSource === 'existing') {
    add('telemetry.accelerators.metrics.source=existing')
    addString('telemetry.accelerators.metrics.existing.prometheusEndpoint', t.acceleratorsExistingEndpoint.trim())
  }
  // Stated explicitly and unconditionally, like the 11 signal flags above (not gated on t.accelerators) -
  // the same --reuse-values staleness reasoning: a previous applyScope=true left unmentioned would survive
  // a later edit that turns accelerators off and back on without re-checking this box.
  add(`telemetry.accelerators.metrics.applyScope=${t.acceleratorsApplyScope}`)
  if (t.exportAuthSecretName.trim()) {
    addString('telemetry.export.otlp.auth.secretName', t.exportAuthSecretName.trim())
    addString('telemetry.export.otlp.auth.secretKey', t.exportAuthSecretKey.trim() || 'token')
    if (t.exportAuthHeaderName.trim() && t.exportAuthHeaderName.trim() !== 'Authorization') {
      addString('telemetry.export.otlp.auth.headerName', t.exportAuthHeaderName.trim())
    }
  }
  // Processors: stated explicitly like the signals above (not conditionally, like protocol/insecure),
  // for the same --reuse-values reason - a sampling percentage or a redaction toggle left unmentioned
  // because it was reset back to its default in this panel would otherwise keep its old value.
  add(`telemetry.processors.resourceDetection.enabled=${t.resourceDetection}`)
  add(`telemetry.processors.redaction.enabled=${t.redaction}`)
  add(`telemetry.processors.tracesSampling.percentage=${t.tracesSamplingPercent}`)
  // Extra processors: the raw bodies all go in one --set-json (a map keyed by processorKey()), single-quoted
  // for the shell like any other multi-character value pasted into a terminal (unlike the simple tokens
  // addString/helmList above handle, a processor's JSON body can contain arbitrary characters, including a
  // single quote, so it gets the one place in this function that actually escapes for the shell). Each
  // entry's key is then separately referenced, in order, in whichever pipeline list it belongs in -
  // tailSampling only ever the traces-only list, filter/transform the shared every-pipeline one (see
  // processorTarget() in processorCatalog.ts for why they can't share one list). Omitted entirely when
  // there are none, same as the chart's own default - an empty --set-json '{}' is harmless but adds
  // nothing worth stating.
  if (t.extraProcessors.length) {
    const shQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`
    cmd += ` \
  --set-json telemetry.processors.extraProcessors=${shQuote(JSON.stringify(buildExtraProcessors(t.extraProcessors)))}`
    const byTarget = { extraProcessorNames: [] as string[], extraTracesProcessorNames: [] as string[] }
    for (const e of t.extraProcessors) byTarget[processorTarget(e)].push(processorKey(e))
    for (const [target, keys] of Object.entries(byTarget)) keys.forEach((key, i) => addString(`telemetry.processors.${target}[${i}]`, key))
  }
  return cmd
}
