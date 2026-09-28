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

/** What is wrong with the scope, in words a person can act on; empty when it is fine. */
export function scopeProblems(s: ScopeInput): string[] {
  const out: string[] = []
  for (const n of [...s.namespaces, ...s.exclude]) if (!DNS_LABEL.test(n)) out.push(`"${n}" is not a valid namespace name`)
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
  systemLogs: boolean
  kubernetesEvents: boolean
  applicationLogs: boolean
  traces: boolean
  /** Where every enabled signal is sent (`telemetry.export.otlp.*`). Required once any signal above is on. */
  exportEndpoint: string
  exportProtocol: 'grpc' | 'http'
  exportInsecure: boolean
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
  systemLogs: false,
  kubernetesEvents: false,
  applicationLogs: false,
  traces: false,
  exportEndpoint: '',
  exportProtocol: 'grpc',
  exportInsecure: false,
}

/** Whether any signal is on - the export endpoint (and every flag below) only matters once one is. */
export const telemetryActive = (t: TelemetryInput): boolean =>
  t.resourceUsage || t.energy || t.kubernetesState || t.nodeRuntime || t.networkLatency ||
  t.applicationMetrics || t.systemLogs || t.kubernetesEvents || t.applicationLogs || t.traces

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
  if (t.networkLatency && measurementsOn === false) out.push('Network latency re-emits the path measurements extra, so turn that on too, or it will report nothing')
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
  add(`telemetry.systemLogs.logs.enabled=${t.systemLogs}`)
  add(`telemetry.kubernetesEvents.logs.enabled=${t.kubernetesEvents}`)
  add(`telemetry.applicationLogs.logs.enabled=${t.applicationLogs}`)
  add(`telemetry.traces.traces.enabled=${t.traces}`)
  return cmd
}
