/**
 * What an agent says about itself, and what an administrator may ask of it.
 *
 * Consent lives with the owner of the cluster. The chart's `access.tier`, its RBAC and its scope are a CEILING inside the
 * cluster that this server can never exceed; the server may only NARROW within it (a lower tier, a paused collector, more
 * namespaces left out). Widening is done by the cluster's owner with `helm upgrade`, and this file builds the exact command.
 * Everything here is pure so that it can be tested without a browser.
 */
import { activeLanes, emptyExportTarget, cleanTags, emptyScopeOverride, emptyTelemetry, enabledModalities, isKept, keepAsInstalled, ROUTE_MODALITIES, scopeProblems, splitNames, telemetryActive, telemetryProblems, withTelemetry, type KeptGroup, type Modality, type TelemetryInput, PICKABLE_SIGNALS, TELEMETRY_SIGNALS } from './install'
// Re-exported for every existing `from '@/lib/consent'` import site - Modality/TELEMETRY_SIGNALS/
// enabledModalities now live in install.ts (see its own comment on why), consent.ts just re-exports them.
import { operatorReceiverEndpoint } from './destinationCatalog'
import type { TelemetryIntent } from './types'
import { chainCommands } from './shellChain'
export { enabledModalities, type Modality, PICKABLE_SIGNALS, TELEMETRY_SIGNALS }

export type Severity = 'info' | 'warn' | 'error'

export interface AgentProblem {
  /** Stable: the documentation and this page key on it (rbac_forbidden, informer_not_synced, sync_too_large, ...). */
  code: string
  severity: Severity
  message: string
  since?: string
}

export interface AgentCollector {
  name: string
  /** The cluster's owner installed it (a chart value). */
  configured: boolean
  /** It is running now. */
  enabled: boolean
  pausedByServer?: boolean
  producing: boolean
  reporting: number
  expected: number
  note?: string
  lastData?: string
}

export interface AgentInformer {
  name: string
  module?: string
  synced: boolean
  objects: number
  lastError?: string
}

export interface AgentDiagnostics {
  reportedAt: string
  /** Only what the hello carried: the agent had not yet looked at the cluster. */
  partial?: boolean
  agentVersion: string
  arch?: string
  os?: string
  uptimeSeconds: number
  installedTier: number
  approvedTier: number
  effectiveTier: number
  scope?: { description: string; namespaces: number; inScope: number }
  ownExcludedNamespaces?: number
  pausedCollectors: string[]
  excludedNamespaces: number
  flowDropped?: number
  collectors: AgentCollector[]
  informers: AgentInformer[]
  problems: AgentProblem[]
  /** Telemetry signals this install's chart actually has enabled (telemetry.*.enabled), by name - what
   *  really runs, as opposed to what the install command below merely offers to turn on. Empty: none enabled,
   *  or an agent older than this field. */
  installedTelemetry?: string[]
  /** The effective configuration behind installedTelemetry above - not just which signals are on, but how
   *  each is actually configured right now (export destination, the processor settings that matter most
   *  for "safe by default", and which source backs energy/accelerators). Undefined: no telemetry signal
   *  installed, or an agent older than this field - see seedTelemetryFromInstalled below for where this
   *  actually gets used. */
  installedTelemetryConfig?: AgentTelemetryConfig
  /** Whether each telemetry destination is actually receiving data, as the agent last read it from the
   *  collectors' own export counters. Undefined: the agent is not reading them (telemetry not installed,
   *  telemetry.health off in the chart, or an agent older than this field). See lib/exportHealth.ts. */
  exportHealth?: AgentExportHealth
}

/** The agent's state for one route: waiting (nothing seen go out yet), exporting (data went out recently),
 *  silent (it did before, but not recently - not an error by itself: an idle signal looks like this) or
 *  failing (sends failed recently and none succeeded since). */
export type ExportState = 'waiting' | 'exporting' | 'silent' | 'failing'

/** The server's ExportHealthDoc, one-to-one. */
export interface AgentExportHealth {
  scrapedAt?: string
  /** Collector pods whose counters were read on the last attempt, and how many attempts failed. Reached 0
   *  with failures means the counters cannot be read at all - which says nothing about the data itself. */
  podsReached: number
  podsFailed: number
  lastError?: string
  routes: AgentExportRoute[]
}

export interface AgentExportRoute {
  /** The exporter's id in the collector config: otlp, otlphttp/logs, zipkin/traces. */
  exporter: string
  signal: string
  state: ExportState
  /** Summed over the collector pods since each started, so they can go down when a pod restarts. */
  sent: number
  failed: number
  lastSentAt?: string
  lastFailedAt?: string
}

/**
 * The effective configuration behind AgentDiagnostics.installedTelemetry - the server's DiagnosticsDoc.
 * InstalledTelemetryConfig, one-to-one. tracesSamplingPercent/energySource/acceleratorsSource are each
 * only meaningful (and only ever present) when their own signal is itself on - see InstalledTelemetryConfig's
 * own doc comment in agent.proto for why.
 */
export interface AgentTelemetryConfig {
  exportEndpoint: string
  redactionEnabled: boolean
  resourceDetectionEnabled: boolean
  tracesSamplingPercent?: number
  energySource?: 'bundle-kepler' | 'existing'
  acceleratorsSource?: 'bundle-dcgm' | 'existing'
  /** What a newer agent also reports, so that reopening the panel never has to guess: each of these is absent from an agent that does
   *  not, and an absent one is then LEFT OUT of the next command rather than stated as a default (see TelemetryInput.keptAsInstalled). */
  /** The tags stamped on everything it emits (`telemetry.resource.attributes`). */
  tags?: { key: string; value: string }[]
  /** The debug exporter: '' off, 'basic' counts what passed, 'detailed' logs content. */
  debugVerbosity?: '' | 'basic' | 'detailed'
  /** The scope of each application signal that is on, and what the infrastructure signals follow (`telemetry.scope.infra`). */
  scope?: Partial<Record<'applicationMetrics' | 'applicationLogs' | 'traces' | 'infra', { namespaces: string[]; exclude: string[]; workloads?: { namespace: string; names: string[] }[] }>>
}

/** What an administrator has asked one agent to leave out, and whether the agent has caught up with it. */
export interface AgentConsent {
  pausedCollectors: string[]
  excludedNamespaces: string[]
  /**
   * True once the agent's own diagnostics show this narrowing actually in force (or there was nothing
   * narrowed to confirm in the first place). Computed server-side, from the agent's last report - not
   * derived here, so it still reads correctly right after a page load, before any diagnostics have been
   * fetched into this session. See `setAt` for how long it has been waiting.
   */
  confirmed?: boolean
  /**
   * When this narrowing was last changed (ISO 8601), present only while `confirmed` is false - the
   * observability-intent panel's staleness signal, an elapsed-time indicator for a narrowing that has been
   * waiting on the agent, rather than leaving the person to guess whether "not yet confirmed" means five
   * seconds or five days.
   */
  setAt?: string
  /**
   * Excluded namespace names this agent has never actually reported. A likely typo, or one that simply has
   * not rolled out yet - either way a warning, not a block: cleanConsent's own syntax checks already ran
   * server-side, so anything named here was accepted and IS in force, just against a namespace that has
   * not (yet) been seen.
   */
  unknownNamespaces?: string[]
}

export interface AgentExtras {
  diagnostics?: AgentDiagnostics
  consent?: AgentConsent
}

/** The two fields of an agent that only editors and administrators receive. */
export function extrasOf(agents: readonly unknown[] | undefined, id: string): AgentExtras {
  const a = (agents ?? []).find((x) => (x as { id?: string }).id === id) as AgentExtras | undefined
  if (!a) return {}
  return {
    diagnostics: a.diagnostics ? { ...a.diagnostics, pausedCollectors: a.diagnostics.pausedCollectors ?? [], collectors: a.diagnostics.collectors ?? [], informers: a.diagnostics.informers ?? [], problems: a.diagnostics.problems ?? [], installedTelemetry: a.diagnostics.installedTelemetry ?? [] } : undefined,
    consent: a.consent
      ? {
          pausedCollectors: a.consent.pausedCollectors ?? [],
          excludedNamespaces: a.consent.excludedNamespaces ?? [],
          confirmed: a.consent.confirmed,
          setAt: a.consent.setAt,
          unknownNamespaces: a.consent.unknownNamespaces ?? [],
        }
      : undefined,
  }
}

/* ---------- health ---------- */

export interface HealthSummary {
  level: 'unknown' | 'healthy' | 'warn' | 'error'
  /** One line: "Healthy", "1 problem", "3 problems". */
  line: string
  /** Problems that need a person (warnings and errors); notices do not count. */
  count: number
}

/** Agents refresh their self-report every 5 minutes; three missed means what it says is history, not news. */
export const STALE_REPORT_SECONDS = 15 * 60

/**
 * The agent's health in one line. With `fresh` (the time now, and whether the agent is connected) a report that is old, or
 * from an agent that is not connected, is not called "Healthy": it says how old it is, because that is what is known.
 */
export function healthSummary(d?: AgentDiagnostics, fresh?: { now: number; connected?: boolean }): HealthSummary {
  if (!d) return { level: 'unknown', line: 'No report yet', count: 0 }
  const serious = d.problems.filter((p) => p.severity !== 'info')
  if (fresh) {
    const age = (fresh.now - Date.parse(d.reportedAt)) / 1000
    if (fresh.connected === false || (Number.isFinite(age) && age > STALE_REPORT_SECONDS)) {
      const line = Number.isFinite(age) ? `Last report ${uptimeWords(Math.max(0, age))} ago` : 'Report date unknown'
      return { level: 'unknown', line: fresh.connected === false ? `Not connected · ${line.toLowerCase()}` : line, count: serious.length }
    }
  }
  if (serious.length === 0) return { level: 'healthy', line: 'Healthy', count: 0 }
  const worst = serious.some((p) => p.severity === 'error') ? 'error' : 'warn'
  return { level: worst, line: `${serious.length} problem${serious.length === 1 ? '' : 's'}`, count: serious.length }
}

const RANK: Record<Severity, number> = { error: 0, warn: 1, info: 2 }
/** Worst first, then in the order the agent sent them. */
export const sortProblems = (ps: readonly AgentProblem[]): AgentProblem[] => [...ps].sort((a, b) => RANK[a.severity] - RANK[b.severity])

/* ---------- discovery ---------- */

export interface DiscoveryStatus {
  /**
   * 'done' once every watch this agent runs has completed its first full read of the cluster - a one-time
   * milestone, not a health check (a watch can be synced and the agent still unhealthy, or still discovering
   * and otherwise fine). 'unknown' before the agent has said anything substantive, or for an older agent
   * that reports no watches at all.
   */
  state: 'unknown' | 'discovering' | 'done'
  synced: number
  total: number
}

/** Whether the agent has finished looking at the cluster for the first time. Separate from `healthSummary`
 *  (is anything wrong) and from the live counts on screen (how much there is right now, which keeps changing). */
export function discoveryStatus(d?: AgentDiagnostics): DiscoveryStatus {
  if (!d || d.partial || d.informers.length === 0) return { state: 'unknown', synced: 0, total: d?.informers.length ?? 0 }
  const synced = d.informers.filter((i) => i.synced).length
  return { state: synced === d.informers.length ? 'done' : 'discovering', synced, total: d.informers.length }
}

/* ---------- tiers ---------- */

export const TIER_NAMES = ['Registered only', 'Infrastructure', 'Services', 'Dependencies', 'Control'] as const
export const tierName = (t: number) => TIER_NAMES[t] ?? `Tier ${t}`

export interface InstallInfo {
  chartFile?: string
  chartRef?: string
  chartVersion?: string
}

/**
 * The command the cluster's owner runs to raise the ceiling of an agent's install. It mirrors what the server prints in its
 * own refusal, so what a person reads here and there is the same. `--reuse-values` keeps everything else about the install.
 */
export function helmUpgradeCommand(install: InstallInfo | undefined, tier: number): string {
  const ref = install?.chartRef || `./${install?.chartFile || 'continuum-agent.tgz'}`
  const version = install?.chartRef && !install.chartRef.endsWith('.tgz') && install.chartVersion ? ` --version ${install.chartVersion}` : ''
  return `helm upgrade continuum-agent ${ref}${version} --namespace continuum-system --reuse-values --set access.tier=${tier}`
}

/** What the agent is doing at a tier, against what was approved: the reason it may be lower is worth saying. */
export function effectiveNote(d: AgentDiagnostics): string | undefined {
  if (d.effectiveTier < d.approvedTier) {
    return d.effectiveTier < d.installedTier
      ? 'The agent has not yet applied the approved access.'
      : `Held back by the install: the cluster's owner allows only ${tierName(d.installedTier).toLowerCase()}.`
  }
  return undefined
}

/* ---------- the optional collectors ---------- */

export const COLLECTORS: { id: string; label: string; what: string; chart: string }[] = [
  { id: 'probes', label: 'Node probe', what: 'Facts about each machine (virtual machine or bare metal, model, kernel).', chart: 'nodeProbe.enabled' },
  { id: 'flow', label: 'Traffic observer', what: 'Which workloads talk to which, from the nodes.', chart: 'flowObserver.enabled' },
  { id: 'measure', label: 'Path measurements', what: 'Timing of TCP connections to addresses this server names.', chart: 'measurements.enabled' },
]

export interface CollectorState {
  tone: 'off' | 'paused' | 'ok' | 'warn'
  label: string
}

export function collectorState(c: AgentCollector | undefined): CollectorState {
  if (!c || !c.configured) return { tone: 'off', label: 'Not installed in this cluster' }
  if (c.pausedByServer) return { tone: 'paused', label: 'Paused by an administrator' }
  if (!c.enabled) return { tone: 'off', label: 'Off' }
  if (c.name !== 'measure' && c.expected > 0) {
    const of = `${c.reporting} of ${c.expected} nodes`
    return c.producing ? { tone: c.reporting < c.expected ? 'warn' : 'ok', label: `On, reporting from ${of}` } : { tone: 'warn', label: `On, but silent (${of} reporting)` }
  }
  return c.producing ? { tone: 'ok', label: c.note ? `On, ${c.note}` : 'On' } : { tone: 'warn', label: c.note ? `On, ${c.note}` : 'On, nothing yet' }
}

/** Whether the "measure" collector is actually running for this agent right now - the source of truth for
 *  TelemetryFields' measurementsOn once telemetry is configured after install, as opposed to the connect
 *  wizard's own local toggle (which only means "will be enabled", not "is enabled"). */
export function measurementsRunning(d?: AgentDiagnostics): boolean | undefined {
  return d?.collectors.find((c) => c.name === 'measure')?.enabled
}

/* ---------- telemetry ---------- */

/**
 * The catalog shown in the telemetry panel and the wizard's telemetry section. `id` matches the key used
 * everywhere else this signal is named: TelemetryInput's own field, the chart's `telemetry.<id>.enabled`
 * path, and the exact string the agent self-reports in `installedTelemetry` - one vocabulary, not three.
 */
/**
 * Four axes, kept honest rather than a free 4D cross-product: `id` (kind) is the only one a person actually
 * chooses. `modality` is a physical fact about each kind's receiver (a metrics receiver never produces
 * traces) - read-only metadata, not a toggle. `layer` is the architectural/RBAC classification (renamed
 * from the chart's own `domain` field, same meaning). `scope` is where a signal is collected: 'cluster'
 * (a singleton receiver, watched once, cluster-wide, never filterable), 'node' (a DaemonSet - already
 * schedulable onto a subset of nodes via the chart's own nodeSelector/tolerations, just not through this
 * form), or 'application' (namespace-filterable via telemetry.scope or, for these three kinds, a per-signal
 * override - see TelemetryFields). `namespaceScopable` is the one honestly-named exception: accelerators is
 * layer: infrastructure (deployment shape, no RBAC) but its GPU metrics can carry namespace/pod identity,
 * so it can optionally opt into namespace scoping too (telemetry.accelerators.metrics.applyScope) without
 * pretending that changes its architectural layer.
 */
/** The one RBAC grant every signal above shares once ANY of them is on: read-only pods/namespaces/nodes
 *  access so the collector can tag what it collects with the pod/namespace/node it came from. */
export const TELEMETRY_UNIVERSAL_PERMISSION = 'Once any signal above is on: read-only access to pods, namespaces and nodes, to tag collected data with the pod/namespace/node it came from.'

/**
 * A few named, one-click combinations of the signals above - "alter it anytime" starts from one of these
 * as often as from a blank form. Applying one sets every signal to exactly this combination (not just
 * turning listed ones on), so switching between presets never leaves a stale signal from a previous pick;
 * everything else on the form (export destination, processors) is left as the person already set it.
 */
export interface TelemetryIntentPreset {
  id: string
  label: string
  description: string
  signals: string[]
}

export const TELEMETRY_INTENT_PRESETS: TelemetryIntentPreset[] = [
  { id: 'minimal', label: 'Minimal / cost-aware', description: 'Resource usage and Kubernetes state only - the cheapest useful baseline.', signals: ['resourceUsage', 'kubernetesState'] },
  { id: 'full-infra', label: 'Full infrastructure', description: 'Every infrastructure-scoped signal: resource usage, energy, cluster state, node runtime, events, system logs.', signals: ['resourceUsage', 'energy', 'kubernetesState', 'nodeRuntime', 'kubernetesEvents', 'systemLogs'] },
  { id: 'full-infra-app', label: 'Full infrastructure + app tracing', description: 'Everything in "Full infrastructure" plus application metrics, logs and traces.', signals: ['resourceUsage', 'energy', 'kubernetesState', 'nodeRuntime', 'kubernetesEvents', 'systemLogs', 'applicationMetrics', 'applicationLogs', 'traces'] },
  { id: 'debug-everything', label: 'Debug everything', description: 'Every signal on - for a short-lived, deep-dive investigation, not a steady state.', signals: PICKABLE_SIGNALS.map((s) => s.id) },
]

/** Sets every signal to exactly the preset's combination; everything else on the form (export
 *  destination, processors) is left untouched. */
export function applyIntentPreset(current: TelemetryInput, preset: TelemetryIntentPreset): TelemetryInput {
  const next: TelemetryInput = { ...current }
  const rec = next as unknown as Record<string, boolean>
  for (const s of TELEMETRY_SIGNALS) rec[s.id] = preset.signals.includes(s.id)
  return next
}

/**
 * What the agent reports as its destination when signals go to more than one place: `traces=zipkin:9411,default=gw:4317`
 * (a route per signal type that has its own, then the default for the rest). A plain `host:port` is the one
 * destination of an install without routes and returns undefined here. Anything unrecognised is ignored
 * rather than guessed at.
 */
export function parseRoutedDestination(s: string): { routes: Partial<Record<Modality, string>>; fallback: string } | undefined {
  if (!s.includes('=')) return undefined
  const routes: Partial<Record<Modality, string>> = {}
  let fallback = ''
  for (const part of s.split(',')) {
    const [k, ...rest] = part.split('=')
    const v = rest.join('=').trim()
    if (!v) continue
    if (k.trim() === 'default') fallback = v
    else if ((ROUTE_MODALITIES as string[]).includes(k.trim())) routes[k.trim() as Modality] = v
  }
  return { routes, fallback }
}

const APP_SCOPE_KEYS = ['applicationMetrics', 'applicationLogs', 'traces'] as const

/**
 * Seeds a TelemetryInput for reopening an install: which signals are actually on (`installedTelemetry`), and - when the agent is new
 * enough to send it - the effective configuration behind them (`installedTelemetryConfig`): the export destination, the processor
 * settings, which source backs energy/accelerators and (newer agents) the scope, tags and debug exporter. A real self-reported value
 * always takes over from the default. `intent` is the agent's active TelemetryIntent: a recorded grant, used only to show what the
 * agent does not report (the destination, the scope).
 *
 * Without this seed the panel would start blank, and because withTelemetry states every signal explicitly, the command from a blank
 * draft would turn off whatever the person did not re-check. The same holds for every setting the draft does not KNOW: the ones
 * the agent does not report, and everything that came only from the intent, are marked `keptAsInstalled`, so the command leaves
 * them out (and `--reuse-values` keeps the installed value) until they are edited - stating a default there would silently
 * widen the scope, drop the tags, or lose a credential. Nothing installed and nothing reported is a fresh draft: all of it is stated.
 */
export function seedTelemetryFromInstalled(installed: string[], config?: AgentTelemetryConfig, intent?: TelemetryIntent): TelemetryInput {
  let next: TelemetryInput = { ...emptyTelemetry }
  const rec = next as unknown as Record<string, boolean>
  // A signal nothing emits yet is never seeded on, even when an older install has it: it would sit in the draft unseen and block the
  // command (it needs path measurements), and turning it off is what the command then states.
  for (const s of TELEMETRY_SIGNALS) rec[s.id] = !s.noEmitter && installed.includes(s.id)
  if (config) {
    const routed = parseRoutedDestination(config.exportEndpoint)
    if (routed) {
      // Signals go to more than one place: the draft keeps that shape, so that running its command does not
      // quietly send everything to one of them. How each is spoken to (protocol, TLS, credential) is not
      // reported, so none is restated until it is edited: see TelemetryInput.exportLanesKept.
      next.exportSplit = true
      next.exportRoutesInstalled = true
      next.exportLanesKept = [...ROUTE_MODALITIES]
      next.exportEndpoint = routed.fallback
      next.exportLanes = {
        metrics: { ...emptyExportTarget, exportEndpoint: routed.routes.metrics ?? routed.fallback },
        logs: { ...emptyExportTarget, exportEndpoint: routed.routes.logs ?? routed.fallback },
        traces: { ...emptyExportTarget, exportEndpoint: routed.routes.traces ?? routed.fallback },
      }
    } else {
      next.exportEndpoint = config.exportEndpoint
    }
    next.redaction = config.redactionEnabled
    next.resourceDetection = config.resourceDetectionEnabled
    if (config.tracesSamplingPercent !== undefined) next.tracesSamplingPercent = config.tracesSamplingPercent
    if (config.energySource) next.energySource = config.energySource
    if (config.acceleratorsSource) next.acceleratorsSource = config.acceleratorsSource
    if (config.tags) next.tags = config.tags.map((t) => ({ ...t }))
    if (config.debugVerbosity !== undefined) next.debugVerbosity = config.debugVerbosity
    const scope = config.scope
    if (scope) {
      const own = (k: 'applicationMetrics' | 'applicationLogs' | 'traces' | 'infra') => (scope[k] ? { namespaces: [...scope[k]!.namespaces], exclude: [...scope[k]!.exclude], workloads: (scope[k]!.workloads ?? []).map((w) => ({ namespace: w.namespace, names: [...w.names] })) } : undefined)
      next.applicationMetricsScope = own('applicationMetrics') ?? emptyScopeOverride
      next.applicationLogsScope = own('applicationLogs') ?? emptyScopeOverride
      next.tracesScope = own('traces') ?? emptyScopeOverride
      next.scopeInfrastructure = scope.infra !== undefined
    }
  }
  // Only a recorded grant, never a fact about the cluster: it can say what was meant without the command ever having been run. It
  // fills in what the agent does not report - as something to SHOW. Anything that came from here is still not known to be installed,
  // so it stays out of the command until it is edited (see keptAsInstalled below).
  if (intent && intent.status === 'active') {
    if (!config || config.exportEndpoint.trim() === '') {
      const d = intent.destination
      if (d.kind === 'operator' && d.targetOperatorId) {
        next.exportEndpoint = operatorReceiverEndpoint({ id: d.targetOperatorId })
        next.exportOperatorId = d.targetOperatorId
      } else if (d.endpoint) {
        next.exportEndpoint = d.endpoint
        next.exportInsecure = !!d.insecure
        next.exportAuthHeaderName = d.authHeaderName ?? ''
        next.exportAuthSecretName = d.authSecretName ?? ''
        next.exportAuthSecretKey = d.authSecretKey ?? ''
      }
    }
    if (!config?.scope && (intent.namespaces.length > 0 || intent.exclude.length > 0)) {
      const granted = { namespaces: [...intent.namespaces], exclude: [...intent.exclude], workloads: [] }
      for (const k of APP_SCOPE_KEYS) {
        const key = `${k}Scope` as 'applicationMetricsScope' | 'applicationLogsScope' | 'tracesScope'
        if (rec[k]) next[key] = granted
      }
    }
  }
  // Nothing installed and nothing reported: a fresh draft, whose defaults are exactly what is stated.
  if (installed.length === 0 && !config) return next
  const kept: KeptGroup[] = []
  // A destination sent per signal type keeps its routes as installed lane by lane (exportLanesKept), so only the single one is a group.
  if (!next.exportSplit) kept.push('destination')
  if (!config?.scope) kept.push('scopeShared', ...APP_SCOPE_KEYS.filter((k) => rec[k]).map((k) => `scope:${k}` as const))
  if (!config || config.tags === undefined) kept.push('tags')
  if (!config || config.debugVerbosity === undefined) kept.push('debug')
  if (!config) kept.push('processors')
  next = keepAsInstalled(next, kept)
  return next
}

const SCOPE_LABEL: Record<(typeof APP_SCOPE_KEYS)[number], string> = { applicationMetrics: 'application metrics', applicationLogs: 'application logs', traces: 'traces' }
const scopeWords1 = (s: { namespaces: string[]; exclude: string[] }) => `${s.namespaces.length ? s.namespaces.join(', ') : 'all namespaces'}${s.exclude.length ? `, without ${s.exclude.join(', ')}` : ''}`

/**
 * What running the command will change, one short line each, for the summary shown before it is copied. It compares the draft with
 * what the install reports, and says plainly where it cannot: a setting the agent does not report is described as "set" rather than
 * "changed", and one that is still kept as installed is not mentioned, because the command leaves it alone. Empty when the command
 * changes nothing the install is known to have.
 */
export function describeTelemetryChanges(installed: string[], config: AgentTelemetryConfig | undefined, draft: TelemetryInput): string[] {
  const out: string[] = []
  const label = (ids: string[]) => ids.map((id) => TELEMETRY_SIGNALS.find((s) => s.id === id)?.label ?? id).join(', ')
  const on = PICKABLE_SIGNALS.filter((s) => (draft as unknown as Record<string, boolean>)[s.id]).map((s) => s.id)
  const added = on.filter((id) => !installed.includes(id))
  const removed = installed.filter((id) => !on.includes(id) && PICKABLE_SIGNALS.some((s) => s.id === id))
  if (added.length) out.push(`Turns on ${label(added)}`)
  if (removed.length) out.push(`Turns off ${label(removed)}`)
  if (!draft.exportSplit && !isKept(draft, 'destination')) {
    const from = config && !config.exportEndpoint.includes('=') ? config.exportEndpoint.trim() : ''
    const to = draft.exportEndpoint.trim()
    if (from && from !== to) out.push(`Sends to ${to} instead of ${from}`)
    else if (from) out.push(`States how it connects to ${to} again: protocol, TLS and credential`)
    else out.push(`Sets the destination to ${to}, with its protocol, TLS and credential`)
  }
  for (const k of APP_SCOPE_KEYS) {
    if (!(draft as unknown as Record<string, boolean>)[k] || isKept(draft, `scope:${k}`)) continue
    const mine = draft[`${k}Scope` as 'applicationMetricsScope']
    const theirs = config?.scope?.[k]
    if (theirs && scopeWords1(theirs) === scopeWords1(mine)) continue
    if (theirs) out.push(`Changes where ${SCOPE_LABEL[k]} are collected from ${scopeWords1(theirs)} to ${scopeWords1(mine)}`)
    else out.push(`Collects ${SCOPE_LABEL[k]} from ${scopeWords1(mine)}`)
  }
  if (!isKept(draft, 'tags') && config?.tags !== undefined && JSON.stringify(cleanTags(draft.tags)) !== JSON.stringify(cleanTags(config.tags))) out.push('Changes the tags stamped on everything it sends')
  if (!isKept(draft, 'tags') && config?.tags === undefined && draft.tags.length > 0) out.push('Sets the tags stamped on everything it sends')
  if (!isKept(draft, 'debug') && config?.debugVerbosity !== undefined && draft.debugVerbosity !== config.debugVerbosity) out.push(`Changes the debug exporter to ${draft.debugVerbosity || 'off'}`)
  if (!isKept(draft, 'processors') && config && (draft.redaction !== config.redactionEnabled || draft.resourceDetection !== config.resourceDetectionEnabled)) out.push('Changes the redaction or resource-detection processors')
  return out
}

/** Where an agent's Helm release actually lives, when the server knows (it is what the agent reported). */
export interface ReleaseTarget {
  namespace?: string
  release?: string
}

const KUBE_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

/**
 * The namespace and release a command should target: the given ones when they are plain Kubernetes names,
 * otherwise the chart's documented defaults. The values come from a server response (which got them from
 * the agent) and end up in a shell line, so anything else is dropped rather than pasted.
 */
export function releaseTarget(t?: ReleaseTarget): { namespace: string; release: string } {
  return {
    namespace: t?.namespace && KUBE_NAME.test(t.namespace) ? t.namespace : 'continuum-system',
    release: t?.release && KUBE_NAME.test(t.release) ? t.release : 'continuum-agent',
  }
}

/**
 * The command to change an agent's telemetry after install - `--reuse-values` keeps everything else,
 * mirroring `helmUpgradeCommand`'s tier-widening shape exactly. Command-generation only, like that one:
 * nothing here is ever pushed live (see the panel that uses this).
 */
export function telemetryUpgradeCommand(install: InstallInfo | undefined, t: TelemetryInput, measurementsOn?: boolean, target?: ReleaseTarget): string {
  const { namespace, release } = releaseTarget(target)
  const ref = install?.chartRef || `./${install?.chartFile || 'continuum-agent.tgz'}`
  const version = install?.chartRef && !install.chartRef.endsWith('.tgz') && install.chartVersion ? ` --version ${install.chartVersion}` : ''
  const base = `helm upgrade ${release} ${ref}${version} --namespace ${namespace} --reuse-values`
  return withTelemetry(base, t, measurementsOn)
}

/** The environment variable the credential is read from in `telemetrySecretCommand` - named here so the
 *  wizard's own hint and the generated command can never disagree about it. */
export const TELEMETRY_CREDENTIAL_VAR = 'TELEMETRY_EXPORT_TOKEN'

/**
 * The command that creates (or updates) the Secret `withTelemetry`'s credential flags point at, to run just
 * before the upgrade - or undefined when there is nothing to create (no Secret named, telemetry off, or the
 * draft not valid yet, in which case `telemetryUpgradeCommand` doesn't emit the credential flags either).
 *
 * The credential value itself never passes through this page: the command reads it from an environment
 * variable the person sets in their own shell, and `:?` makes the shell stop with a message rather than
 * create an empty Secret when it is unset. `--dry-run=client -o yaml | kubectl apply` makes it safe to
 * re-run (create-or-update) where a bare `kubectl create secret` would fail the second time. A Secret name
 * or key that isn't a plain Kubernetes name yields undefined rather than being pasted into a shell line.
 */
export function telemetrySecretCommand(t: TelemetryInput, measurementsOn?: boolean, target?: ReleaseTarget): string | undefined {
  const cmds = telemetrySecrets(t, measurementsOn, target).map((s) => s.command)
  // Joined so that one failing (a credential variable unset) stops the rest, and the upgrade after them.
  return cmds.length ? chainCommands(cmds) : undefined
}

export interface TelemetrySecret {
  /** The Secret's name. */
  name: string
  /** The environment variable the credential is read from for this one. */
  variable: string
  /** The signal types whose destination reads this Secret (empty for the single destination). */
  lanes: Modality[]
  command: string
}

const SECRET_NAME = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/
const SECRET_KEY = /^[-._a-zA-Z0-9]+$/

/**
 * Every Secret the command's credential flags point at, each with the command that creates it: one for the
 * single destination, or - sending each signal type separately - one per Secret name the lanes use. Two lanes
 * that name the same Secret share it (one command, one variable): that is how one credential serves a
 * backend that takes metrics and logs. A lane that needs a different credential under the same Secret name
 * is a different key in it, read from its own variable. Nothing is returned for a draft that has no
 * valid command, for the reason `telemetrySecretCommand` gives.
 */
export function telemetrySecrets(t: TelemetryInput, measurementsOn?: boolean, target?: ReleaseTarget): TelemetrySecret[] {
  if (!telemetryActive(t) || telemetryProblems(t, measurementsOn).length) return []
  const namespace = releaseTarget(target).namespace
  const uses = t.exportSplit
    ? activeLanes(t).map((m) => ({ lane: m as Modality | undefined, name: t.exportLanes[m].exportAuthSecretName.trim(), key: t.exportLanes[m].exportAuthSecretKey.trim() || 'token' }))
    : [{ lane: undefined, name: t.exportAuthSecretName.trim(), key: t.exportAuthSecretKey.trim() || 'token' }]
  const byName = new Map<string, { key: string; variable: string; lanes: Modality[] }[]>()
  for (const u of uses) {
    if (!u.name || !SECRET_NAME.test(u.name) || !SECRET_KEY.test(u.key)) continue
    const entries = byName.get(u.name) ?? []
    const same = entries.find((e) => e.key === u.key)
    if (same) {
      if (u.lane) same.lanes.push(u.lane)
    } else {
      entries.push({ key: u.key, variable: u.lane ? `${TELEMETRY_CREDENTIAL_VAR}_${u.lane.toUpperCase()}` : TELEMETRY_CREDENTIAL_VAR, lanes: u.lane ? [u.lane] : [] })
    }
    byName.set(u.name, entries)
  }
  return [...byName.entries()].map(([name, entries]) => ({
    name,
    variable: entries[0].variable,
    lanes: entries.flatMap((e) => e.lanes),
    command: `kubectl create secret generic ${name} --namespace ${namespace} ${entries.map((e) => `--from-literal=${e.key}="\${${e.variable}:?set ${e.variable} to the credential first}"`).join(' ')} --dry-run=client -o yaml | kubectl apply -f -`,
  }))
}

/* ---------- what an administrator may ask ---------- */

/** kube-system and friends: the agent always reads them to recognise the cluster's own components, so they cannot be left out. */
const SYSTEM_NAMESPACES = ['kube-system', 'kube-public', 'kube-node-lease']
export const MAX_EXCLUDED = 200

export interface ExclusionInput {
  names: string[]
  problems: string[]
}

/** "shop, batch" → the names, and what is wrong with them (uses the same rules as the install wizard's scope). */
export function parseExclusions(text: string): ExclusionInput {
  const names = splitNames(text)
  const problems = scopeProblems({ namespaces: [], exclude: names, selector: '' })
  for (const n of names) if (SYSTEM_NAMESPACES.includes(n)) problems.push(`${n} is a system namespace: the agent always reads those to recognise the cluster's own components`)
  if (names.length > MAX_EXCLUDED) problems.push(`At most ${MAX_EXCLUDED} namespaces can be left out from here; use the install's own scope for more`)
  return { names: [...names].sort(), problems }
}

const sameSet = (a: readonly string[], b: readonly string[]) => a.length === b.length && [...a].sort().every((x, i) => x === [...b].sort()[i])

export interface ConsentDraft {
  tier: number
  paused: string[]
  excluded: string[]
}

export interface ConsentChange {
  tier: boolean
  overrides: boolean
}

/** What of the draft differs from what is stored; nothing to save when both are false. */
export function consentChange(draft: ConsentDraft, approvedTier: number, stored: AgentConsent | undefined): ConsentChange {
  return {
    tier: draft.tier !== approvedTier,
    overrides: !sameSet(draft.paused, stored?.pausedCollectors ?? []) || !sameSet(draft.excluded, stored?.excludedNamespaces ?? []),
  }
}

/**
 * Whether the agent has confirmed what was asked (its own account of what is in force matches). Until it has, the page says the change
 * is on its way rather than done. Approved tier is compared with what the agent says it collects at, bounded by its ceiling.
 */
export function inForce(consent: AgentConsent | undefined, approvedTier: number, d: AgentDiagnostics | undefined): boolean {
  if (!d || d.partial) return false
  const want = Math.min(approvedTier, d.installedTier)
  return d.effectiveTier === want && sameSet(d.pausedCollectors, consent?.pausedCollectors ?? []) && d.excludedNamespaces === (consent?.excludedNamespaces.length ?? 0)
}

/** "2 of 30 namespaces" and how the rest are left out, in words. */
export function scopeWords(d: AgentDiagnostics): string {
  const s = d.scope
  if (!s) return 'not reported'
  const own = d.ownExcludedNamespaces ?? 0
  const extra = d.excludedNamespaces
  const rest = [own > 0 && `${own} left out by the install`, extra > 0 && `${extra} more by an administrator`].filter(Boolean).join(', ')
  const count = s.namespaces > 0 ? `${s.inScope} of ${s.namespaces} namespaces in view` : 'no namespaces read at this tier'
  return `${count}${rest ? ` (${rest})` : ''}`
}

/** "3h", "12 min": for an uptime. */
export function uptimeWords(seconds: number): string {
  if (seconds < 90) return `${Math.max(0, Math.round(seconds))} s`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min`
  if (seconds < 129_600) return `${Math.round(seconds / 3600)} h`
  return `${Math.round(seconds / 86_400)} d`
}
