import { buildUpgradeExtraProcessors, processorBody, processorProblems, processorTarget, processorKey, type ProcessorEntry } from './processorCatalog'
import { EXPORT_PRESETS, presetSupportsModalities } from './exportPresets'

/**
 * The install command the server prints, with optional in-cluster parts switched on.
 * They are separate opt-ins because they are the only parts of Ikhnos that run on every node.
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

export const DNS_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
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

/** One shell word that is exactly `s`: single-quoted, with any single quote inside it closed, escaped and reopened. */
export const shQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`

/**
 * `s` as one shell word, left bare when every character is one the shell passes through untouched (so
 * `otel.example.com:4317` stays readable, and an empty one is left as `flag=` in the printed command) and quoted with `shQuote` otherwise. A value
 * a person or an agent typed goes through this, never straight into a template: a `;`, `&`, `$`, space or
 * newline in it would otherwise end the command, or run another one, when the command is pasted.
 */
export const shArg = (s: string) => (/^[A-Za-z0-9_@%+=:./-]*$/.test(s) ? s : shQuote(s))

/** Helm treats commas in a --set value as list separators: escape them. Only for a list WITH something in it: `--set key='{}'` is NOT an
 *  empty list but a list holding one empty string (`[""]`, checked against Helm 3.22) - see `listFlag`. */
const helmList = (xs: string[]) => `'{${xs.join(',')}}'`

/** Adds the scope to the install command. Without one the agent reports every namespace, as before. */
export function withScope(install: string, s: ScopeInput): string {
  if (!scopeActive(s) || scopeProblems(s).length) return install
  let cmd = install.trimEnd()
  if (s.namespaces.length) cmd += ` \\\n  --set scope.namespaces=${helmList(s.namespaces)}`
  if (s.exclude.length) cmd += ` \\\n  --set scope.exclude=${helmList(s.exclude)}`
  if (s.selector.trim()) cmd += ` \\\n  --set-string scope.selector=${shQuote(s.selector.trim())}`
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
  /** Inside a namespace, only these workloads: a namespace listed with no names keeps nothing of it. A
   *  namespace that is not listed stays whole. A list (not a map) because the chart takes it as one, so a
   *  `helm upgrade --reset-then-reuse-values` replaces it instead of merging into an earlier command's. */
  workloads: WorkloadScope[]
}

export interface WorkloadScope {
  namespace: string
  names: string[]
}

export const emptyScopeOverride: ScopeOverrideInput = { namespaces: [], exclude: [], workloads: [] }

/** A workload (deployment, statefulset, ...) name as the chart's filter accepts it: lowercase letters, digits and hyphens. */
export const WORKLOAD_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

export function workloadProblems(w: WorkloadScope[]): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const e of w) {
    if (!WORKLOAD_NAME.test(e.namespace)) out.push(`"${e.namespace}" is not a valid namespace name`)
    if (seen.has(e.namespace)) out.push(`Workloads for "${e.namespace}" are listed twice`)
    seen.add(e.namespace)
    for (const n of e.names) if (!WORKLOAD_NAME.test(n)) out.push(`"${n}" is not a valid workload name (lowercase letters, digits and hyphens)`)
  }
  return out
}

/** Whether this scope narrows anything at all. */
export const scopeNarrows = (s: ScopeOverrideInput) => s.namespaces.length > 0 || s.exclude.length > 0 || s.workloads.length > 0

/**
 * The one scope the infrastructure signals follow, worked out from the application signals that are on: a
 * namespace is kept if ANY of those signals keeps it (so nothing an application signal collects is cut off
 * from the infrastructure data around it), and dropped only if EVERY one of them drops it. A signal with no
 * narrowing of its own collects everything, so one such signal makes the combined scope the whole cluster.
 * Workloads are narrowed only when every signal that keeps the namespace narrows it to workloads - then to
 * the union of those. Nothing to follow when no application signal is on.
 */
export function combinedScope(t: TelemetryInput): ScopeOverrideInput {
  const scopes = [t.applicationMetrics && t.applicationMetricsScope, t.applicationLogs && t.applicationLogsScope, t.traces && t.tracesScope].filter((s): s is ScopeOverrideInput => !!s)
  if (scopes.length === 0 || scopes.some((s) => !scopeNarrows(s))) return emptyScopeOverride
  const everywhere = scopes.some((s) => s.namespaces.length === 0)
  const namespaces = everywhere ? [] : [...new Set(scopes.flatMap((s) => s.namespaces))].sort()
  // Dropped by all: in every signal's exclude list.
  const exclude = scopes.map((s) => s.exclude).reduce((a, b) => a.filter((n) => b.includes(n))).slice().sort()
  const workloads: WorkloadScope[] = []
  const candidates = [...new Set(scopes.flatMap((s) => s.workloads.map((w) => w.namespace)))].sort()
  for (const ns of candidates) {
    // Every signal that collects this namespace must narrow it, or the namespace stays whole.
    const collecting = scopes.filter((s) => (s.namespaces.length === 0 ? !s.exclude.includes(ns) : s.namespaces.includes(ns)))
    const narrowed = collecting.map((s) => s.workloads.find((w) => w.namespace === ns))
    if (collecting.length === 0 || narrowed.some((w) => !w)) continue
    workloads.push({ namespace: ns, names: [...new Set(narrowed.flatMap((w) => w!.names))].sort() })
  }
  return { namespaces, exclude, workloads }
}

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
  /** Whether the infrastructure signals that can follow a scope (the pod and container parts of resource
   *  usage and node runtime, cluster state, Kubernetes events) follow the one the application signals
   *  combine to (see `combinedScope`). Nodes and host metrics never do. Nothing to follow unless an
   *  application signal is narrowed, so on by default. */
  scopeInfrastructure: boolean
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
  exportProtocol: ExportProtocol
  exportInsecure: boolean
  /** The header a destination expects its credential in (e.g. "Authorization", "X-Api-Key"). Only sent
   *  once `exportAuthSecretName` names a Secret - see it below for why no credential value lives here. */
  exportAuthHeaderName: string
  /** A Secret already created in the release namespace, holding just the header's value - never the value
   *  itself, which would otherwise render into a ConfigMap/Secret-less values file in plain text. Empty
   *  means the destination needs no auth header at all. */
  exportAuthSecretName: string
  exportAuthSecretKey: string
  /** The regional operator picked as the destination in the guided wizard (its id), or '' for anything
   *  else. It does NOT change what withTelemetry emits - the endpoint and protocol above are still the whole
   *  of the client-built command - it records that the destination is an operator, whose receiver needs a
   *  freshly issued client certificate only the server can mint (the panel's "Generate commands" action,
   *  see lib/operatorIntent.ts). Every edit of the endpoint by hand clears it. */
  exportOperatorId: string
  /** Send each signal type to its own destination (`telemetry.export.routes.<metrics|logs|traces>`) instead
   *  of all of them to the one above. The one above stays what it is - the default every signal falls back to
   *  - and a route only overrides it, so turning this off again never loses the single choice. */
  exportSplit: boolean
  /** One destination per signal type, used only while `exportSplit` is on. Kept (not cleared) while it is off,
   *  so switching back and forth is not destructive. */
  exportLanes: Record<Modality, ExportTarget>
  /** Whether the install already has routes (the agent reported a `metrics=...,default=...` destination).
   *  Only used to decide whether a single-destination command must state the routes empty so that they are
   *  cleared: under `helm upgrade --reset-then-reuse-values` an unmentioned route would keep sending. */
  exportRoutesInstalled: boolean
  /** The signal types whose route the install already has, and that nothing here has edited. The agent says where
   *  each goes but not how (protocol, TLS, credential), so the draft cannot state them truthfully: such a route is
   *  left out of the command - `--reset-then-reuse-values` keeps it as installed - until it is edited here, when it is stated
   *  in full like any other. Without this, changing anything at all would restate every seeded route with a
   *  default protocol and no credential, quietly breaking the ones that differ. */
  exportLanesKept: Modality[]
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
  /** Tags the person put on everything this install emits (`telemetry.resource.attributes`). At most
   *  `TAG_LIMIT`, none under the reserved `continuum.` prefix - see `tagProblems`. */
  tags: TagEntry[]
  /** A debug exporter beside the real one, writing to the collector's own log: '' off, 'basic' counts what
   *  passed (no content), 'detailed' logs every record's content. */
  debugVerbosity: DebugVerbosity
  /** Who this telemetry belongs to, stamped as continuum.org.id / continuum.cluster.id. Not a form field: the
   *  panel fills them in from the signed-in organisation and the agent's cluster when it builds the command,
   *  so the wizard never holds or shows them. Empty (a test, a standalone use) means not stamped. */
  resourceOrgId: string
  resourceClusterId: string
  /** Groups of settings this draft shows but does not KNOW the installed value of (an agent that does not report them, or a value only
   *  recorded as a grant), each with what it showed when seeded. While a group still reads as it was seeded the command leaves
   *  it out - `--reset-then-reuse-values` keeps what is installed - instead of stating a default that may widen collection or drop a credential.
   *  Editing any field of a group makes it the draft's own, and it is then stated in full. See `isKept`. */
  keptAsInstalled: Partial<Record<KeptGroup, string>>
  /** Whether this draft stands for an install that already has telemetry configured (it was seeded from one: see
   *  seedTelemetryFromInstalled). Only then does the command have something to CLEAR: under `helm upgrade
   *  --reset-then-reuse-values` a setting the command does not name keeps its old value, so a draft with every signal
   *  unchecked must still state them off, a single destination must state the routes empty, and an emptied processor
   *  list must be stated empty. A fresh install has nothing to clear, so none of that is printed there. */
  hadTelemetry: boolean
}

/** The settings that are kept or stated together: the destination's connection details, the scope (each application signal's own, and
 *  what the infrastructure signals and the scope tag follow), the tags, the debug exporter and the three processor knobs. */
export type KeptGroup = 'destination' | 'scopeShared' | 'scope:applicationMetrics' | 'scope:applicationLogs' | 'scope:traces' | 'tags' | 'debug' | 'processors' | 'extraProcessors' | 'energySource' | 'acceleratorsSource'

function groupValue(t: TelemetryInput, g: KeptGroup): string {
  switch (g) {
    case 'destination':
      return JSON.stringify([t.exportSplit, t.exportEndpoint.trim(), t.exportProtocol, t.exportInsecure, t.exportAuthHeaderName.trim(), t.exportAuthSecretName.trim(), t.exportAuthSecretKey.trim(), t.exportOperatorId])
    case 'scopeShared':
      return JSON.stringify([t.scopeInfrastructure, t.applicationMetricsScope, t.applicationLogsScope, t.tracesScope])
    case 'scope:applicationMetrics':
      return JSON.stringify(t.applicationMetricsScope)
    case 'scope:applicationLogs':
      return JSON.stringify(t.applicationLogsScope)
    case 'scope:traces':
      return JSON.stringify(t.tracesScope)
    case 'tags':
      return JSON.stringify(cleanTags(t.tags))
    case 'debug':
      return t.debugVerbosity
    case 'processors':
      return JSON.stringify([t.resourceDetection, t.redaction, t.tracesSamplingPercent])
    case 'extraProcessors':
      return JSON.stringify(t.extraProcessors.map((e) => [processorKey(e), processorBody(e)]))
    case 'energySource':
      return JSON.stringify([t.energySource, t.energyExistingEndpoint.trim()])
    case 'acceleratorsSource':
      return JSON.stringify([t.acceleratorsSource, t.acceleratorsExistingEndpoint.trim()])
  }
}

/** Marks these groups as shown-but-unknown, as they read right now. */
const KEPT_WORDS: Record<KeptGroup, string> = {
  destination: 'how it connects to the destination (protocol, TLS, credential)',
  scopeShared: 'what the infrastructure signals follow and the scope tag',
  'scope:applicationMetrics': 'where application metrics are collected from',
  'scope:applicationLogs': 'where application logs are collected from',
  'scope:traces': 'where traces are collected from',
  tags: 'the tags',
  debug: 'the debug exporter',
  processors: 'masking, environment enrichment and trace sampling',
  extraProcessors: 'the extra processors (this page cannot see which there are)',
  energySource: 'which Kepler energy reads from',
  acceleratorsSource: 'which DCGM the GPU metrics read from',
}

/** What this draft shows but does not know the installed value of, in words: the settings the command leaves as they are (see `isKept`). */
export function keptSummary(t: TelemetryInput): string[] {
  const groups = (Object.keys(KEPT_WORDS) as KeptGroup[]).filter((g) => isKept(t, g))
  const routes = t.exportSplit ? t.exportLanesKept.filter((m) => activeLanes(t).includes(m)).map((m) => `how ${m} reach their destination (protocol, TLS, credential)`) : []
  return [...groups.map((g) => KEPT_WORDS[g]), ...routes]
}

export const keepAsInstalled = (t: TelemetryInput, groups: KeptGroup[]): TelemetryInput => ({ ...t, keptAsInstalled: Object.fromEntries(groups.map((g) => [g, groupValue(t, g)])) })

/** Whether the command leaves this group out: it was seeded as unknown and nothing in it has been edited since. */
export const isKept = (t: TelemetryInput, g: KeptGroup): boolean => t.keptAsInstalled[g] !== undefined && t.keptAsInstalled[g] === groupValue(t, g)

/** How the one export destination is spoken to. 'zipkin' posts spans to a Zipkin-compatible /api/v2/spans
 *  endpoint with the collector's own zipkin exporter: it carries traces only (see telemetryProblems). */
export type ExportProtocol = 'grpc' | 'http' | 'zipkin'
/** Everything that says where one stream of telemetry goes and how it is spoken to: the fields the single
 *  destination has on TelemetryInput itself, and what each signal type's own destination carries. */
export type ExportTarget = Pick<
  TelemetryInput,
  'exportEndpoint' | 'exportProtocol' | 'exportInsecure' | 'exportAuthHeaderName' | 'exportAuthSecretName' | 'exportAuthSecretKey' | 'exportOperatorId'
>
export const emptyExportTarget: ExportTarget = {
  exportEndpoint: '',
  exportProtocol: 'grpc',
  exportInsecure: false,
  exportAuthHeaderName: '',
  exportAuthSecretName: '',
  exportAuthSecretKey: '',
  exportOperatorId: '',
}
export const exportProtocolLabel = (p: ExportProtocol): string => (p === 'http' ? 'OTLP/HTTP' : p === 'zipkin' ? 'Zipkin (HTTP, traces only)' : 'OTLP/gRPC')

export interface TagEntry {
  key: string
  value: string
}
export type DebugVerbosity = '' | 'basic' | 'detailed'
/** How many tags a person can add: each one is stamped on every record, so it costs storage and cardinality. */
export const TAG_LIMIT = 8
/** Names under this prefix are the provenance the chart stamps itself; a tag of one would be refused by it. */
export const RESERVED_TAG_PREFIX = 'continuum.'

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
  scopeInfrastructure: true,
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
  exportOperatorId: '',
  exportSplit: false,
  exportLanes: { metrics: emptyExportTarget, logs: emptyExportTarget, traces: emptyExportTarget },
  exportRoutesInstalled: false,
  exportLanesKept: [],
  resourceDetection: false,
  redaction: true,
  tracesSamplingPercent: 100,
  extraProcessors: [],
  tags: [],
  // Off from the start. A debug exporter is an extra exporter on every pipeline that nobody asked for (someone who ticks only
  // Kepler would otherwise get a pipeline with two exporters and a collector logging every batch); it is a deliberate choice,
  // made in the Process step, never a default.
  debugVerbosity: '',
  resourceOrgId: '',
  resourceClusterId: '',
  keptAsInstalled: {},
  hadTelemetry: false,
}

/** What is wrong with a tag list, in words a person can act on. */
export function tagProblems(tags: TagEntry[]): string[] {
  const out: string[] = []
  // A row nobody has typed into yet is not a tag (cleanTags drops it from the command too).
  const filled = tags.filter((t) => t.key.trim() !== '' || t.value.trim() !== '')
  if (filled.length > TAG_LIMIT) out.push(`At most ${TAG_LIMIT} tags: each is stamped on every record`)
  const seen = new Set<string>()
  for (const t of filled) {
    const key = t.key.trim()
    if (!key) {
      out.push('Every tag needs a name')
      continue
    }
    if (key.toLowerCase().startsWith(RESERVED_TAG_PREFIX)) out.push(`“${key}” starts with ${RESERVED_TAG_PREFIX}, which is reserved for what Ikhnos adds itself`)
    if (/\s/.test(key)) out.push(`“${key}”: a tag name cannot contain spaces`)
    if (seen.has(key)) out.push(`“${key}” is listed twice`)
    seen.add(key)
    if (!t.value.trim()) out.push(`“${key}” needs a value`)
  }
  return out
}

/** What the tags are stamped as: trimmed, and with nothing half-filled-in left over. */
export const cleanTags = (tags: TagEntry[]): TagEntry[] => tags.map((t) => ({ key: t.key.trim(), value: t.value.trim() })).filter((t) => t.key !== '' || t.value !== '')

/**
 * What this telemetry covers, for the continuum.scope tag: the namespaces of the application signals that are
 * narrowed ("shop; payments"), a namespace narrowed to workloads as "payments: api+worker", plus what they
 * leave out ("- excluding legacy+tmp"). No commas: the value travels through `helm --set`. Empty when nothing
 * is narrowed (everything the agent can see).
 */
export function scopeTag(t: TelemetryInput): string {
  // What the application signals together collect, by the same rule the infrastructure signals follow (combinedScope): a namespace is
  // listed if ANY of them collects it, and left out only if EVERY one leaves it out. A union of each signal's own exclusions would say
  // "excluding tmp" while another signal still collects tmp, and a signal that collects everywhere would not show at all.
  const c = combinedScope(t)
  if (c.namespaces.length === 0 && c.exclude.length === 0 && c.workloads.length === 0) return ''
  const narrowed = new Map(c.workloads.map((w) => [w.namespace, w.names]))
  const wl = (ns: string) => `${ns}: ${narrowed.get(ns)!.length ? narrowed.get(ns)!.join('+') : 'nothing'}`
  const not = c.exclude.length ? ` - excluding ${c.exclude.join('+')}` : ''
  if (c.namespaces.length === 0) {
    const only = [...narrowed.keys()].map((ns) => `${wl(ns)} only`)
    return `all namespaces${only.length ? ` (${only.join('; ')})` : ''}${not}`
  }
  const names = [...new Set([...c.namespaces, ...narrowed.keys()])].sort()
  return `${names.map((n) => (narrowed.has(n) ? wl(n) : n)).join('; ')}${not}`
}

/** The kind of signal a telemetry field carries - metrics, logs, or distributed traces. Lives here (not
 *  consent.ts, which re-exports it) because it is a property of TelemetryInput itself, and exportPresets.ts
 *  (a plain data file with no reason to depend on consent.ts's RBAC-flavoured exports) needs it too. */
export type Modality = 'metrics' | 'logs' | 'traces'

/**
 * Every telemetry signal TelemetryInput can carry, with what it is, what it needs and what modality it
 * belongs to. The UI (consent.ts's re-export, used by TelemetryFields.tsx) reads this for its checkboxes
 * and permission copy; telemetryProblems below reads it to work out which modalities are actually on.
 */
export const TELEMETRY_SIGNALS: { id: string; label: string; layer: 'infrastructure' | 'application'; modality: Modality; scope: 'cluster' | 'node' | 'application'; namespaceScopable?: boolean; /** The chart has nothing that emits it yet: kept in the model (a command still states it off), never offered. */ noEmitter?: boolean; what: string; permissions: string }[] = [
  { id: 'resourceUsage', label: 'Resource usage', layer: 'infrastructure', modality: 'metrics', scope: 'node', what: 'Node and per-container CPU, memory, filesystem and network, from the kubelet and the host. The kubelet\'s certificate is verified: where it serves a certificate of its own (kubeadm\'s default, AKS) add --set telemetry.kubelet.insecureSkipVerify=true to the command.', permissions: 'Read-only access to nodes/stats (the kubelet\'s own stats endpoint).' },
  { id: 'energy', label: 'Energy', layer: 'infrastructure', modality: 'metrics', scope: 'node', what: 'Power draw per node/pod, from Kepler (bundled, or an existing one you already run).', permissions: 'None beyond identity enrichment below - Kepler reads host energy counters directly, never the Kubernetes API.' },
  { id: 'kubernetesState', label: 'Kubernetes state', layer: 'infrastructure', modality: 'metrics', scope: 'cluster', what: 'Pod, deployment and replica status and counts, cluster-wide.', permissions: 'Read-only, cluster-wide access to pods, deployments, replica sets, stateful/daemon sets, jobs, cronjobs and autoscalers.' },
  { id: 'nodeRuntime', label: 'Node runtime', layer: 'infrastructure', modality: 'metrics', scope: 'node', what: 'Pod lifecycle and volume metrics from the kubelet (the same kubelet certificate note as Resource usage applies).', permissions: 'Read-only access to nodes/stats (the kubelet\'s own stats endpoint).' },
  { id: 'networkLatency', label: 'Network latency', layer: 'infrastructure', modality: 'metrics', scope: 'cluster', noEmitter: true, what: "This agent's own path measurements, re-emitted as OTel metrics.", permissions: 'None beyond identity enrichment below - reuses this agent\'s existing measurement capability.' },
  { id: 'applicationMetrics', label: 'Application metrics', layer: 'application', modality: 'metrics', scope: 'application', what: 'Metrics your applications push (OTLP) or that this collector scrapes (Prometheus).', permissions: 'None beyond identity enrichment below.' },
  { id: 'systemLogs', label: 'System logs', layer: 'infrastructure', modality: 'logs', scope: 'node', what: "Each node's own OS/container runtime logs, never application output.", permissions: 'None beyond identity enrichment below - reads local log files only.' },
  { id: 'kubernetesEvents', label: 'Kubernetes events', layer: 'infrastructure', modality: 'logs', scope: 'cluster', what: 'Cluster Events, watched cluster-wide.', permissions: 'Read-only, cluster-wide access to Events only.' },
  { id: 'applicationLogs', label: 'Application logs', layer: 'application', modality: 'logs', scope: 'application', what: 'Logs your applications push directly (OTLP).', permissions: 'None beyond identity enrichment below.' },
  { id: 'traces', label: 'Traces', layer: 'application', modality: 'traces', scope: 'application', what: 'Distributed traces your applications push directly (OTLP).', permissions: 'None beyond identity enrichment below.' },
  { id: 'accelerators', label: 'Accelerators (GPU)', layer: 'infrastructure', modality: 'metrics', scope: 'node', namespaceScopable: true, what: 'GPU utilization, memory, temperature and power per node/pod, from NVIDIA DCGM (bundled, or an existing one you already run).', permissions: 'None beyond identity enrichment below - dcgm-exporter reads GPU hardware and the kubelet\'s pod-resources socket directly, never the Kubernetes API.' },
]

/** The signals a person can turn on: every one that something actually emits. Network latency has a setting but no emitter in the chart yet,
 *  so offering it would only produce a command that reports nothing. */
export const PICKABLE_SIGNALS = TELEMETRY_SIGNALS.filter((s) => !s.noEmitter)

/** Which modalities are actually turned on in a telemetry draft, derived from TELEMETRY_SIGNALS instead of
 *  listed by hand a second time. Used to steer a person away from picking a single destination (there is
 *  only ever one `exportEndpoint` for every signal together) that cannot carry everything they just turned
 *  on - see exportPresets.ts's own `modalities` field and TelemetryFields' use of both. */
export function enabledModalities(t: TelemetryInput): Set<Modality> {
  const on = new Set<Modality>()
  for (const s of TELEMETRY_SIGNALS) {
    if ((t as unknown as Record<string, boolean>)[s.id]) on.add(s.modality)
  }
  return on
}

export const ROUTE_MODALITIES: Modality[] = ['metrics', 'logs', 'traces']

/** The signal types that need a destination of their own while the draft sends each type separately: the ones
 *  with a signal turned on, in a fixed order. Empty while it does not. */
export const activeLanes = (t: TelemetryInput): Modality[] => (t.exportSplit ? ROUTE_MODALITIES.filter((m) => enabledModalities(t).has(m)) : [])

/** A lane seen as a whole draft: the lane's destination in place of the single one, so everything that edits
 *  or explains one destination (the picker, its connection details) works on a lane unchanged. */
export const laneView = (t: TelemetryInput, m: Modality): TelemetryInput => ({ ...t, ...t.exportLanes[m] })

/** Write back what was edited on a `laneView`: only the destination fields, never anything else of the draft. */
export function withLane(t: TelemetryInput, m: Modality, edited: TelemetryInput): TelemetryInput {
  const { exportEndpoint, exportProtocol, exportInsecure, exportAuthHeaderName, exportAuthSecretName, exportAuthSecretKey, exportOperatorId } = edited
  const next: ExportTarget = { exportEndpoint, exportProtocol, exportInsecure, exportAuthHeaderName, exportAuthSecretName, exportAuthSecretKey, exportOperatorId }
  const prev = t.exportLanes[m]
  // Touching a route that was kept as installed makes it this draft's to state, in full.
  const changed = (Object.keys(next) as (keyof ExportTarget)[]).some((k) => next[k] !== prev[k])
  return { ...t, exportLanes: { ...t.exportLanes, [m]: next }, exportLanesKept: changed ? t.exportLanesKept.filter((x) => x !== m) : t.exportLanesKept }
}

/** Whether every destination the draft needs is named: the one, or - sending each signal type separately -
 *  one for every type that has a signal on. */
export const destinationReady = (t: TelemetryInput): boolean =>
  t.exportSplit ? activeLanes(t).every((m) => t.exportLanes[m].exportEndpoint.trim() !== '') : t.exportEndpoint.trim() !== ''

/** Turning "one for each signal type" on: every lane that is still empty starts with the single destination
 *  where that one can actually carry the lane's signals, so a person who already picked something is not
 *  asked for it again; a lane it cannot carry (Zipkin takes traces only) starts empty. */
export function startLanes(t: TelemetryInput): TelemetryInput {
  const lanes = { ...t.exportLanes }
  const single = t.exportEndpoint.trim() !== ''
  for (const m of ROUTE_MODALITIES) {
    if (lanes[m].exportEndpoint.trim() !== '' || !single) continue
    const preset = EXPORT_PRESETS.find((x) => x.endpointPattern === t.exportEndpoint.trim())
    if (t.exportProtocol === 'zipkin' && m !== 'traces') continue
    if (preset && !presetSupportsModalities(preset, new Set([m]))) continue
    lanes[m] = {
      exportEndpoint: t.exportEndpoint,
      exportProtocol: t.exportProtocol,
      exportInsecure: t.exportInsecure,
      exportAuthHeaderName: t.exportAuthHeaderName,
      exportAuthSecretName: t.exportAuthSecretName,
      exportAuthSecretKey: t.exportAuthSecretKey,
      exportOperatorId: t.exportOperatorId,
    }
  }
  return { ...t, exportSplit: true, exportLanes: lanes }
}

/** Whether any signal is on - the export endpoint (and every flag below) only matters once one is. */
export const telemetryActive = (t: TelemetryInput): boolean =>
  t.resourceUsage || t.energy || t.kubernetesState || t.nodeRuntime || t.networkLatency ||
  t.applicationMetrics || t.systemLogs || t.kubernetesEvents || t.applicationLogs || t.traces || t.accelerators

/**
 * What a destination's typed parts may hold. Each one reaches a shell line and a `helm --set`, which splits a value on every comma and
 * reads a backslash as an escape, so those can never be part of one (quoting cannot help: it is Helm, after the shell, that splits). The
 * server holds an operator's destination to the same family of patterns (internal/server/operators.go), tighter for an endpoint because it
 * stores one: a host and a port, or a URL. Here an endpoint is a host or URL as a person pastes it, so a query string with `&` or a bracketed
 * IPv6 host is fine (the command quotes it), and so is a preset's `<tenant>` placeholder opening it (quoted, it is text, not a redirect);
 * whitespace, quotes, backticks, commas, backslashes and control characters are not.
 */
export const EXPORT_ENDPOINT = /^[A-Za-z0-9[<][^\s'"`,\\\u0000-\u001f\u007f]{0,510}$/
export const EXPORT_HEADER_NAME = /^[A-Za-z0-9-]{1,64}$/
/** A Kubernetes Secret name and key, as `kubectl create secret` and the chart accept them. */
export const SECRET_NAME = /^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$/
export const SECRET_KEY = /^[-._a-zA-Z0-9]{1,253}$/

/** A preset's `<tenant>`-style placeholder left in an endpoint: it is text to the shell, so the command would print and run, and send
 *  everything to a host that does not exist. */
export const PLACEHOLDER = /<[^<>]*>/

/** The address of a Prometheus endpoint that already exists: a host and a port, nothing else. The chart puts it, verbatim, in the
 *  collector's scrape `targets`, where a scheme or a path is not a valid hostname and stops the collector from starting; the collector
 *  always scrapes /metrics. */
export const SCRAPE_TARGET = /^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:.]+\]):[0-9]{1,5}$/
const scrapeTargetProblem = (value: string, what: string): string[] => {
  const v = value.trim()
  if (!v) return []
  return SCRAPE_TARGET.test(v) ? [] : [`${what} is a host and port only (kepler.monitoring:9102): no http://, no path - the collector scrapes /metrics itself`]
}

/** What is wrong with the typed parts of one destination (the single one, or one signal type's), worded for the field they are in. */
function destinationProblems(d: ExportTarget, label: string, credential: boolean): string[] {
  const out: string[] = []
  const endpoint = d.exportEndpoint.trim()
  if (endpoint && !EXPORT_ENDPOINT.test(endpoint)) out.push(`${label}: the endpoint is a host and port (otlp.example.com:4317) or a URL, without spaces, quotes, commas or backslashes`)
  else if (PLACEHOLDER.test(endpoint)) out.push(`${label}: replace ${endpoint.match(PLACEHOLDER)![0]} in the endpoint with the real value`)
  const secret = d.exportAuthSecretName.trim()
  if (credential && secret) {
    if (!SECRET_NAME.test(secret)) out.push(`${label}: the credential Secret name uses lowercase letters, digits, - and . only`)
    const key = d.exportAuthSecretKey.trim()
    if (key && !SECRET_KEY.test(key)) out.push(`${label}: the credential Secret key uses letters, digits, . _ and - only`)
    const header = d.exportAuthHeaderName.trim()
    if (header && !EXPORT_HEADER_NAME.test(header)) out.push(`${label}: the credential header name uses letters, digits and - only`)
  }
  return out
}

/**
 * What is wrong with the telemetry selection, in words a person can act on; empty when it is fine.
 * `measurementsOn` is whether the wizard's own "Path measurements" extra is (or will be) enabled - pass it
 * whenever the caller also controls that toggle, so turning on networkLatency without it is caught here
 * instead of silently reporting nothing once installed.
 */
export function telemetryProblems(t: TelemetryInput, measurementsOn?: boolean): string[] {
  if (!telemetryActive(t)) return []
  const out: string[] = []
  if (!t.exportSplit && !t.exportEndpoint.trim()) out.push('An export endpoint is required once any telemetry signal is on')
  if (t.energy && t.energySource === 'existing' && !t.energyExistingEndpoint.trim()) out.push('The existing Prometheus endpoint is required when energy points at an existing source')
  if (t.accelerators && t.acceleratorsSource === 'existing' && !t.acceleratorsExistingEndpoint.trim()) out.push('The existing Prometheus endpoint is required when accelerators points at an existing source')
  // Typed text that ends up in the printed command (see destinationProblems). A destination as installed (kept) still has its endpoint stated.
  if (!t.exportSplit) out.push(...destinationProblems(t, 'Destination', !isKept(t, 'destination')))
  if (t.energy && t.energySource === 'existing') out.push(...scrapeTargetProblem(t.energyExistingEndpoint, 'Energy: the existing Prometheus endpoint'))
  if (t.accelerators && t.acceleratorsSource === 'existing') out.push(...scrapeTargetProblem(t.acceleratorsExistingEndpoint, 'Accelerators: the existing Prometheus endpoint'))
  if (t.networkLatency && measurementsOn === false) out.push('Network latency re-emits the path measurements extra, so turn that on too, or it will report nothing')
  // An emptied field is NaN (kept as such, not turned into 0: that would silently mean "drop every trace").
  if (!(typeof t.tracesSamplingPercent === 'number' && t.tracesSamplingPercent >= 0 && t.tracesSamplingPercent <= 100)) out.push('Traces sampling must be between 0 and 100')
  out.push(...processorProblems(t.extraProcessors))
  out.push(...tagProblems(t.tags))
  if (t.applicationMetrics) out.push(...namespaceListProblems([...t.applicationMetricsScope.namespaces, ...t.applicationMetricsScope.exclude]), ...workloadProblems(t.applicationMetricsScope.workloads))
  if (t.applicationLogs) out.push(...namespaceListProblems([...t.applicationLogsScope.namespaces, ...t.applicationLogsScope.exclude]), ...workloadProblems(t.applicationLogsScope.workloads))
  if (t.traces) out.push(...namespaceListProblems([...t.tracesScope.namespaces, ...t.tracesScope.exclude]), ...workloadProblems(t.tracesScope.workloads))
  // There is only ever one exportEndpoint for every signal together (see TelemetryInput) - so a known
  // preset that only carries a subset of modalities (Jaeger: traces) has to actually block the generated
  // command, not just show a warning next to the field (TelemetryFields.tsx shows the same thing inline,
  // with friendlier wording, but withTelemetry below only consults this function - a cosmetic-only warning
  // there would let an invalid preset+signal combo stay copyable/saveable).
  if (t.exportSplit) {
    for (const m of activeLanes(t)) {
      const lane = t.exportLanes[m]
      const endpoint = lane.exportEndpoint.trim()
      if (!endpoint) {
        out.push(`${m[0].toUpperCase()}${m.slice(1)} needs a destination`)
        continue
      }
      // A route kept as installed is left out of the command, so what it holds is not typed text going into one.
      if (!t.exportLanesKept.includes(m)) out.push(...destinationProblems(lane, `${m[0].toUpperCase()}${m.slice(1)}`, true))
      if (lane.exportProtocol === 'zipkin' && m !== 'traces') out.push(`Zipkin only carries traces, so it cannot be where ${m} go`)
      const lp = EXPORT_PRESETS.find((p) => p.endpointPattern === endpoint)
      if (lp && !presetSupportsModalities(lp, new Set([m]))) out.push(`${lp.label} only carries ${lp.modalities!.join('/')}, so it cannot be where ${m} go`)
    }
    return out
  }
  if (t.exportProtocol === 'zipkin') {
    const others = [...enabledModalities(t)].filter((m) => m !== 'traces')
    if (others.length > 0) out.push(`Zipkin only carries traces - turn off ${others.join(' and ')}, or send everything somewhere that speaks OTLP`)
    else if (!t.traces) out.push('Zipkin only carries traces, and traces are not turned on')
  }
  const preset = EXPORT_PRESETS.find((p) => p.endpointPattern === t.exportEndpoint.trim())
  if (preset && !presetSupportsModalities(preset, enabledModalities(t))) {
    out.push(`${preset.label} only carries ${preset.modalities!.join('/')} - turn off the other signals, or send everything somewhere else`)
  }
  return out
}

/** Every signal's switch, by its chart value: what `withTelemetryOff` states off. */
const SIGNAL_FLAGS = [
  'telemetry.resourceUsage.metrics.enabled',
  'telemetry.energy.metrics.enabled',
  'telemetry.kubernetesState.metrics.enabled',
  'telemetry.nodeRuntime.metrics.enabled',
  'telemetry.networkLatency.metrics.enabled',
  'telemetry.applicationMetrics.metrics.enabled',
  'telemetry.systemLogs.logs.enabled',
  'telemetry.kubernetesEvents.logs.enabled',
  'telemetry.applicationLogs.logs.enabled',
  'telemetry.traces.traces.enabled',
  'telemetry.accelerators.metrics.enabled',
]

/** The command that turns all telemetry off: every signal stated false, and the routes and extra-processor lists stated empty so that a
 *  destination or processor chain an earlier command set does not come back, unseen, the next time telemetry is turned on. */
export const withTelemetryOff = (install: string): string => {
  let cmd = SIGNAL_FLAGS.reduce((c, f) => `${c} \\\n  --set ${f}=false`, install.trimEnd())
  for (const m of ROUTE_MODALITIES) cmd += ` \\\n  --set-string telemetry.export.routes.${m}.endpoint=`
  cmd += ` \\\n  --set-json telemetry.processors.extraProcessorNames='[]' \\\n  --set-json telemetry.processors.extraTracesProcessorNames='[]'`
  return cmd
}

/**
 * Adds telemetry to the install command: the export target, then every signal's enabled flag, explicitly
 * true or false - not just the ones turned on. This is deliberate, not just belt-and-braces: the same
 * function also builds the post-install "change telemetry" command (telemetryUpgradeCommand in consent.ts),
 * which runs as `helm upgrade --reset-then-reuse-values` - Helm only changes what a --set actually names, so a signal
 * left unmentioned because it was merely unchecked would keep running. Stating every signal explicitly makes
 * unchecking one in that panel actually turn it off, and costs nothing on a fresh install (an explicit
 * `=false` for a signal that was already going to default to false is a no-op). Without any signal on at
 * all, this returns the command unchanged, exactly as before.
 */
export function withTelemetry(install: string, t: TelemetryInput, measurementsOn?: boolean): string {
  // Every signal unchecked on an install that has telemetry: nothing to export any more, so only the switches are stated - all off. Left
  // as the bare upgrade it would change nothing, and the collectors would keep running as they were.
  if (!telemetryActive(t)) return t.hadTelemetry ? withTelemetryOff(install) : install
  if (telemetryProblems(t, measurementsOn).length) return install
  let cmd = install.trimEnd()
  const add = (flag: string) => { cmd += ` \\\n  --set ${flag}` }
  // Every value goes through shArg: it is typed by a person (an endpoint, a Secret name) or reported by the agent (the scope tag is built
  // from namespace names and holds `; : -` and spaces), and a bare `;` in a printed command ends it there.
  const addString = (flag: string, value: string) => { cmd += ` \\\n  --set-string ${flag}=${shArg(value)}` }
  const addJson = (flag: string, value: unknown) => { cmd += ` \\\n  --set-json ${flag}=${shQuote(JSON.stringify(value))}` }
  // A list of names. An empty one must be `--set-json flag='[]'`: `--set flag='{}'` gives Helm a list holding one EMPTY STRING, which the chart
  // reads as "keep only the namespace named ''" (every record dropped) and as a processor with no name (a collector that will not start).
  const addList = (flag: string, xs: string[]) => (xs.length ? add(`${flag}=${helmList(xs)}`) : addJson(flag, []))
  // Sending each signal type to its own destination: the routes below say where everything goes, so the
  // default is left alone (it is only used by a signal without a route, and there is none here).
  if (!t.exportSplit) addString('telemetry.export.otlp.endpoint', t.exportEndpoint.trim())
  // Who this belongs to, for every destination (an operator's server-built fragment states the same two, and
  // the intent id, after this; helm takes the last). The scope and the tags are stated even when empty, for
  // the --reset-then-reuse-values reason below: clearing them has to actually clear them.
  if (t.resourceOrgId) addString('telemetry.resource.orgId', t.resourceOrgId)
  if (t.resourceClusterId) addString('telemetry.resource.clusterId', t.resourceClusterId)
  if (!isKept(t, 'scopeShared')) addString('telemetry.resource.scope', scopeTag(t))
  // The single destination is stated IN FULL every time (protocol, TLS, mutual TLS, CA, credential): under --reset-then-reuse-values anything left
  // unmentioned keeps its earlier value, so moving from a destination with a client certificate, plain HTTP or a credential to one without
  // would carry those over. Only a destination seeded from an install whose connection details are not known, and not edited since, is
  // stated by its endpoint alone.
  if (!t.exportSplit && !isKept(t, 'destination')) {
    const base = 'telemetry.export.otlp'
    add(`${base}.protocol=${shArg(t.exportProtocol)}`)
    add(`${base}.tls.insecure=${t.exportInsecure}`)
    // A regional operator's receiver is mutual TLS, which only the server's own fragment (it holds the Secret and the name to verify) can
    // state; everything else states it off, so a certificate an earlier command installed does not linger.
    if (!t.exportOperatorId) {
      add(`${base}.tls.mtls.enabled=false`)
      addString(`${base}.tls.mtls.secretName`, '')
      addString(`${base}.tls.serverName`, '')
      addString(`${base}.tls.caFile`, '')
    }
    const secret = t.exportAuthSecretName.trim()
    addString(`${base}.auth.secretName`, secret)
    if (secret) {
      addString(`${base}.auth.secretKey`, t.exportAuthSecretKey.trim() || 'token')
      addString(`${base}.auth.headerName`, t.exportAuthHeaderName.trim() || 'Authorization')
    }
  }
  // The routes. Every route is stated on every command (an unmentioned one would keep sending under
  // `--reset-then-reuse-values`): a route in use in full - protocol, TLS and credential too, so that what an earlier
  // command set cannot linger - and one not in use as an empty endpoint, which is how the chart reads "no route".
  // A single destination states them empty whenever the install has telemetry (it may have routes the agent did not report, or that an
  // earlier command here set): a fresh install has none to clear. One kept as installed is left as it is, routes included.
  const lanes = new Set(activeLanes(t))
  if (t.exportSplit || (!isKept(t, 'destination') && (t.exportRoutesInstalled || t.hadTelemetry))) {
    for (const m of ROUTE_MODALITIES) {
      const base = `telemetry.export.routes.${m}`
      if (!lanes.has(m)) {
        addString(`${base}.endpoint`, '')
        continue
      }
      if (t.exportLanesKept.includes(m)) continue // as installed: unmentioned, so --reset-then-reuse-values keeps it
      const lane = t.exportLanes[m]
      addString(`${base}.endpoint`, lane.exportEndpoint.trim())
      add(`${base}.protocol=${shArg(lane.exportProtocol)}`)
      add(`${base}.tls.insecure=${lane.exportInsecure}`)
      // A regional operator's route is mutual TLS, which only the server's own fragment can state (it holds the
      // Secret); every other route states it off, so one an earlier command turned on does not linger.
      if (!lane.exportOperatorId) {
        add(`${base}.tls.mtls.enabled=false`)
        addString(`${base}.tls.mtls.secretName`, '')
        addString(`${base}.tls.serverName`, '')
        addString(`${base}.tls.caFile`, '')
      }
      const secret = lane.exportAuthSecretName.trim()
      addString(`${base}.auth.secretName`, secret)
      if (secret) {
        addString(`${base}.auth.secretKey`, lane.exportAuthSecretKey.trim() || 'token')
        addString(`${base}.auth.headerName`, lane.exportAuthHeaderName.trim() || 'Authorization')
      }
    }
  }
  add(`telemetry.resourceUsage.metrics.enabled=${t.resourceUsage}`)
  add(`telemetry.energy.metrics.enabled=${t.energy}`)
  // The source is stated whenever the signal is on, 'bundle-kepler' too: leaving it out when it is the default would keep an earlier
  // 'existing' under --reset-then-reuse-values, and the cluster would go on scraping the old endpoint while the draft says Kepler is bundled.
  // Only a source the install did not report (an older agent) is left as it is, until edited.
  if (t.energy && !isKept(t, 'energySource')) {
    add(`telemetry.energy.metrics.source=${t.energySource}`)
    if (t.energySource === 'existing') addString('telemetry.energy.metrics.existing.prometheusEndpoint', t.energyExistingEndpoint.trim())
  }
  add(`telemetry.kubernetesState.metrics.enabled=${t.kubernetesState}`)
  add(`telemetry.nodeRuntime.metrics.enabled=${t.nodeRuntime}`)
  add(`telemetry.networkLatency.metrics.enabled=${t.networkLatency}`)
  add(`telemetry.applicationMetrics.metrics.enabled=${t.applicationMetrics}`)
  // Stated unconditionally while the kind itself is on (even when both lists are empty) - an empty
  // an empty list is stated as `--set-json ...='[]'`, which the chart accepts as "no override, fall back to global scope",
  // and stating it is what lets clearing an override back to empty actually take effect on --reset-then-reuse-values;
  // leaving it unstated whenever empty would let a stale prior override survive. Omitted entirely while the
  // kind itself is off, matching the existing energy/accelerators-existing-endpoint precedent - the chart's
  // own gating (parent .enabled check) makes a stale value harmless there.
  if (t.applicationMetrics && !isKept(t, 'scope:applicationMetrics')) {
    addList('telemetry.applicationMetrics.metrics.scope.namespaces', t.applicationMetricsScope.namespaces)
    addList('telemetry.applicationMetrics.metrics.scope.exclude', t.applicationMetricsScope.exclude)
    addJson('telemetry.applicationMetrics.metrics.scope.workloads', t.applicationMetricsScope.workloads)
  }
  add(`telemetry.systemLogs.logs.enabled=${t.systemLogs}`)
  add(`telemetry.kubernetesEvents.logs.enabled=${t.kubernetesEvents}`)
  add(`telemetry.applicationLogs.logs.enabled=${t.applicationLogs}`)
  if (t.applicationLogs && !isKept(t, 'scope:applicationLogs')) {
    addList('telemetry.applicationLogs.logs.scope.namespaces', t.applicationLogsScope.namespaces)
    addList('telemetry.applicationLogs.logs.scope.exclude', t.applicationLogsScope.exclude)
    addJson('telemetry.applicationLogs.logs.scope.workloads', t.applicationLogsScope.workloads)
  }
  add(`telemetry.traces.traces.enabled=${t.traces}`)
  if (t.traces && !isKept(t, 'scope:traces')) {
    addList('telemetry.traces.traces.scope.namespaces', t.tracesScope.namespaces)
    addList('telemetry.traces.traces.scope.exclude', t.tracesScope.exclude)
    addJson('telemetry.traces.traces.scope.workloads', t.tracesScope.workloads)
  }
  // What the infrastructure signals follow: the application signals' combined scope when that is switched on,
  // otherwise nothing - stated either way (lists replace under --reset-then-reuse-values, so an old one is cleared).
  if (!isKept(t, 'scopeShared')) {
    const infra = t.scopeInfrastructure ? combinedScope(t) : emptyScopeOverride
    addList('telemetry.scope.infra.namespaces', infra.namespaces)
    addList('telemetry.scope.infra.exclude', infra.exclude)
    addJson('telemetry.scope.infra.workloads', infra.workloads)
  }
  add(`telemetry.accelerators.metrics.enabled=${t.accelerators}`)
  if (t.accelerators && !isKept(t, 'acceleratorsSource')) {
    add(`telemetry.accelerators.metrics.source=${t.acceleratorsSource}`)
    if (t.acceleratorsSource === 'existing') addString('telemetry.accelerators.metrics.existing.prometheusEndpoint', t.acceleratorsExistingEndpoint.trim())
  }
  // Stated explicitly and unconditionally, like the 11 signal flags above (not gated on t.accelerators) -
  // the same --reset-then-reuse-values staleness reasoning: a previous applyScope=true left unmentioned would survive
  // a later edit that turns accelerators off and back on without re-checking this box.
  add(`telemetry.accelerators.metrics.applyScope=${t.acceleratorsApplyScope}`)
  // Processors, debug and tags: stated explicitly like the signals above (not conditionally, like the routes), for the same
  // --reset-then-reuse-values reason - a sampling percentage or a redaction toggle left unmentioned because it was reset back to its default in
  // this panel would otherwise keep its old value. A group seeded as unknown and not edited is the one exception: stating a default
  // there would silently replace what the install really has.
  if (!isKept(t, 'processors')) {
    add(`telemetry.processors.resourceDetection.enabled=${t.resourceDetection}`)
    add(`telemetry.processors.redaction.enabled=${t.redaction}`)
    add(`telemetry.processors.tracesSampling.percentage=${Number(t.tracesSamplingPercent)}`)
  }
  if (!isKept(t, 'debug')) addString('telemetry.debug.verbosity', t.debugVerbosity)
  // The tags are one JSON list (not a --set per key) because a list REPLACES what an earlier command set,
  // where a map would keep every key since removed - the chart's field is a list for exactly this reason.
  if (!isKept(t, 'tags')) addJson('telemetry.resource.attributes', cleanTags(t.tags))
  // Extra processors: the bodies all go in one --set-json (a map keyed by processorKey()), single-quoted for the shell like any other
  // multi-character value (a body can hold any character, a single quote included). Each key is then referenced in whichever pipeline list it
  // belongs in - tailSampling only ever the traces-only list, filter/transform the shared every-pipeline one (see processorTarget() in
  // processorCatalog.ts). Both LISTS are stated whole, empty when need be (`--set name={a,b}` replaces a list, where an indexed `name[0]=`
  // would leave the tail of a longer one): under --reset-then-reuse-values a processor taken out here would otherwise stay in the pipelines.
  // A body is merged with the earlier one by key, so the fields a body does not use are stated null (see buildUpgradeExtraProcessors).
  // Left out entirely while the processors are kept as installed (the agent does not report them, and this page cannot show them), and on
  // a fresh install with none.
  if (!isKept(t, 'extraProcessors') && (t.extraProcessors.length || t.hadTelemetry)) {
    if (t.extraProcessors.length) addJson('telemetry.processors.extraProcessors', buildUpgradeExtraProcessors(t.extraProcessors))
    const byTarget = { extraProcessorNames: [] as string[], extraTracesProcessorNames: [] as string[] }
    for (const e of t.extraProcessors) byTarget[processorTarget(e)].push(processorKey(e))
    // A fresh install states only the lists that have something in them; one with telemetry already states both, so that emptying a list works.
    for (const [target, keys] of Object.entries(byTarget)) if (keys.length || t.hadTelemetry) addList(`telemetry.processors.${target}`, keys)
  }
  return cmd
}
