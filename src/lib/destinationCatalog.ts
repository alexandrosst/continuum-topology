import type { FusionStatus } from './api'
import { detectBackends, type DetectedBackend } from './detectBackends'
import type { ExportProtocol, Modality, TelemetryInput } from './install'
import { EXPORT_PRESETS, type ExportPreset } from './exportPresets'
import { CENTRAL_OPERATOR_ID, type FusionKind, fusionSentence } from './fusionStatus'
import type { QuickStartBackend } from './history'
import { hasQuickStartSpec, quickStartSpec } from './quickStartBackends'
import type { OperatorDestinationEntry, RegionalOperator, Service } from './types'

const ALL_MODALITIES: Modality[] = ['metrics', 'logs', 'traces']

/**
 * One pickable destination in the guided telemetry wizard's destination step (GuidedWizard.tsx) - a
 * receiver discovery already found in the cluster, a regional operator already active in this org, a
 * backend this org already quick-started, or a known self-hosted/cloud backend preset. Each carries enough
 * to fill in `TelemetryInput.exportEndpoint`/`exportProtocol` directly, the same two fields the flat grid's
 * own ComboField already sets (TelemetryFields.tsx) - this is a friendlier way to pick one of those same
 * two fields, not a new destination concept.
 *
 * Every entry says which signals it `accepts`, and when it cannot carry everything turned on it stays in
 * the catalog as `compatible: false` with a `reason`, so the step can show it greyed rather than have it
 * vanish: a person who expects Jaeger to be there should see why it can't take their metrics.
 */
interface EntryBase {
  id: string
  label: string
  compatible: boolean
  /** Why this can't be used right now - always set when `compatible` is false. */
  reason?: string
  /** Every signal this destination can take. */
  accepts: Modality[]
}

export type DestinationCatalogEntry =
  | (EntryBase & {
      kind: 'operator'
      operator: RegionalOperator
      exportEndpoint: string
      exportProtocol: 'grpc'
    })
  | (EntryBase & {
      /** The FUSION bundled with this server, reached through its central operator (id `op-central`). First-class, and shown
       *  whatever its state: only a running or starting one can be sent to, an off one can be switched on from the row. */
      kind: 'fusion'
      fusion: FusionOffer
      /** The central operator as the server lists it, or a stand-in until it exists (it is created the first time FUSION is switched on). */
      operator: RegionalOperator
      exportEndpoint: string
      exportProtocol: 'grpc'
    })
  | (EntryBase & {
      kind: 'external-preset'
      preset: ExportPreset
    })
  | (EntryBase & {
      kind: 'quickstart'
      backend: QuickStartBackend
      exportEndpoint: string
      exportProtocol: ExportProtocol
    })
  | (EntryBase & {
      kind: 'detected'
      detected: DetectedBackend
      exportEndpoint: string
      exportProtocol: ExportProtocol
    })

/** What the picker needs to know of FUSION, whether it came from the administrator's own view of it (GET /fusion: parts, can be switched)
 *  or from the read model every editor has (the central entry of GET /operator-destinations). */
export interface FusionOffer {
  kind: FusionKind
  /** Running or starting: a command that sends to it is safe to run now (collectors buffer until the stores are up). */
  usable: boolean
  /** Why it cannot be used at all (not installed, no access, belongs to another organisation). Only with kind 'unavailable'. */
  message?: string
  otherOrg: boolean
  /** "N of 4" while it starts. */
  parts?: { up: number; wanted: number }
  /** An administrator may switch it on from here. */
  canEnable: boolean
  /** The server's own verdict that this is the one to pick (read model only). */
  recommended?: boolean
}

export interface DestinationCatalog {
  entries: DestinationCatalogEntry[]
  /** True only for an organisation administrator: creating a regional operator is adminRole-gated
   *  server-side (see backend/internal/server/admin.go), so offering the entry point to anyone else
   *  would just lead to a 403 once they got there. */
  canDeployOperator: boolean
}

/** A read-model entry as the regional operator the catalog works with. The read model carries no secrets and no scope, so what only an
 *  administrator has (receiver auth, source clusters) is simply absent - which reads as bearer and as "nothing known to receive it". */
export function operatorFromDestination(d: OperatorDestinationEntry): RegionalOperator {
  return {
    id: d.id,
    orgId: '',
    name: d.name,
    status: 'active',
    sourceClusterIds: [],
    destination: { kind: d.kind === 'central' ? 'fusion' : 'external', endpoint: '' },
    acceptedModalities: d.acceptedModalities,
    createdAt: '',
    createdBy: '',
    health: { state: d.health.state, lastSeenAt: d.health.lastSeenAt, reporting: d.health.state === 'online' || d.health.state === 'offline' },
    endpoint: d.endpoint,
    address: d.reachableFromOtherClusters ? d.endpoint : undefined,
    reachableFromOtherClusters: d.reachableFromOtherClusters,
    addressState: d.addressState,
  }
}

const FUSION_KIND_OF_HEALTH: Partial<Record<OperatorDestinationEntry['health']['state'], FusionKind>> = { online: 'running', starting: 'starting', off: 'off', attention: 'attention' }

/**
 * What the picker shows for FUSION. An administrator reads its state from GET /fusion (live, with the parts); anyone else from the
 * central entry of the read model - and without either, there is nothing to offer. `undefined` means FUSION is not part of this
 * picture at all (the server does not run it, or this person cannot see it), so no row is drawn.
 */
export function fusionOffer(opts: { status: FusionStatus | null; destinations: OperatorDestinationEntry[]; isAdmin: boolean; now?: number }): FusionOffer | undefined {
  const { status, destinations, isAdmin } = opts
  if (isAdmin) {
    // A server that does not run FUSION at all has nothing to offer or to enable: no row, rather than a dead one in every list.
    if (status && !status.available && status.reason === 'not-configured') return undefined
    const sentence = fusionSentence(status, opts.now)
    const parts = (status?.components ?? []).filter((c) => c.component !== 'grafana' && c.desired > 0)
    return {
      kind: sentence.kind,
      usable: sentence.kind === 'running' || sentence.kind === 'starting',
      message: sentence.kind === 'unavailable' ? sentence.text : undefined,
      otherOrg: status?.reason === 'other-org',
      parts: sentence.kind === 'starting' ? { up: parts.filter((c) => c.ready >= c.desired).length, wanted: parts.length } : undefined,
      canEnable: !!status?.available && sentence.kind === 'off',
    }
  }
  const central = destinations.find((d) => d.kind === 'central')
  if (!central) return undefined
  const kind = FUSION_KIND_OF_HEALTH[central.health.state] ?? 'checking'
  return { kind, usable: kind === 'running' || kind === 'starting', otherOrg: false, canEnable: false, recommended: central.recommended }
}

/** Everything `buildDestinationCatalog` needs about FUSION, or undefined when there is no row to draw. */
export function fusionForCatalog(opts: { status: FusionStatus | null; operators: RegionalOperator[]; destinations: OperatorDestinationEntry[]; isAdmin: boolean }): { offer: FusionOffer; central: RegionalOperator } | undefined {
  const offer = fusionOffer(opts)
  return offer ? { offer, central: centralOperator(opts) } : undefined
}

/** The regional operators the catalog is built from, however they were read: the administrator's full list, or the read model's active
 *  entries - never the central operator, which is the FUSION entry's. */
export function catalogOperators(opts: { operators: RegionalOperator[]; destinations: OperatorDestinationEntry[]; isAdmin: boolean }): RegionalOperator[] {
  const list = opts.isAdmin ? opts.operators : opts.destinations.filter((d) => d.kind === 'regional').map(operatorFromDestination)
  return list.filter((o) => o.id !== CENTRAL_OPERATOR_ID)
}

/** The central operator as the FUSION entry carries it: the server's own record once there is one (an administrator's list, or the read
 *  model), otherwise a stand-in that dials the same name. */
function centralOperator(opts: { operators: RegionalOperator[]; destinations: OperatorDestinationEntry[]; status: FusionStatus | null }): RegionalOperator {
  const real = opts.operators.find((o) => o.id === CENTRAL_OPERATOR_ID)
  if (real) return real
  const entry = opts.destinations.find((d) => d.kind === 'central')
  if (entry) return operatorFromDestination(entry)
  return {
    id: CENTRAL_OPERATOR_ID,
    orgId: '',
    name: 'FUSION',
    status: 'active',
    sourceClusterIds: [],
    destination: { kind: 'fusion', endpoint: '' },
    createdAt: '',
    createdBy: '',
    endpoint: opts.status?.central?.endpoint,
    reachableFromOtherClusters: opts.status?.central?.exposed,
  }
}

/** The host:port a regional operator's own OTLP receiver answers on, in-cluster - the exact same string
 *  the backend's own operatorDestinationCommand builds (admin_operators.go) for pointing an agent's
 *  export at it. Always plain gRPC: the operator chart's receiver has no HTTP listener. */
export function operatorReceiverEndpoint(op: { id: string }): string {
  return `${op.id}.continuum-system.svc:4317`
}

/** "Does not accept logs or traces." - the one sentence every disabled destination carries: exactly what it cannot take of what was chosen. */
function cannotCarry(accepts: Modality[], enabled: Set<Modality>): string | undefined {
  const missing = ALL_MODALITIES.filter((m) => enabled.has(m) && !accepts.includes(m))
  if (missing.length === 0) return undefined
  return `Does not accept ${missing.join(' or ')}.`
}

/**
 * Merges four sources into one catalog for the guided wizard's destination step: receivers found running in
 * the cluster, regional operators already active in this org, backends this org already quick-started, and
 * the built-in self-hosted and cloud presets - plus the two "deploy new" entry points' eligibility. A pure
 * function over already-fetched data (never fetches anything itself) so the guided wizard owns when/whether
 * to call api.listOperators (gated on `isAdmin`, since GET /operators is adminRole-only - see admin.go) and
 * this stays trivially testable with plain data.
 */
export function buildDestinationCatalog(opts: {
  operators: RegionalOperator[]
  enabledModalities: Set<Modality>
  /** Backends set up through the quick-start wizard. The local operator's wizard no longer offers to set one up,
   *  so it passes none; the code is kept for the FUSION work, which deploys stores the same way. */
  quickStartBackends?: QuickStartBackend[]
  isAdmin: boolean
  /** Discovered workloads, and the cluster to look in - together they switch on the "found in your
   *  cluster" group. Without a cluster there is nowhere to look, so nothing is detected. */
  services?: Service[]
  clusterId?: string
  /** FUSION, as its own first entry (see `fusionOffer`); `central` is the operator record behind it. Without it there is no such entry,
   *  and the central operator is not offered as a plain regional one either. */
  fusion?: { offer: FusionOffer; central: RegionalOperator }
}): DestinationCatalog {
  const { enabledModalities, quickStartBackends = [], isAdmin, services, clusterId, fusion } = opts
  const operators = opts.operators.filter((o) => o.id !== CENTRAL_OPERATOR_ID)

  const detectedEntries: DestinationCatalogEntry[] =
    clusterId && services
      ? detectBackends(services, { clusterId }).map((d) => {
          const reason = cannotCarry(d.kind.modalities, enabledModalities)
          return {
            kind: 'detected',
            id: d.id,
            label: d.kind.label,
            compatible: reason === undefined,
            reason,
            accepts: d.kind.modalities,
            detected: d,
            exportEndpoint: d.endpoint,
            exportProtocol: d.kind.protocol,
          }
        })
      : []

  const operatorEntries: DestinationCatalogEntry[] = operators
    .filter((op) => op.status === 'active')
    .map((op) => {
      // Empty/absent acceptedModalities means "accepts anything" - true for an operator that doesn't restrict
      // modalities at all, including one created before this field existed.
      const accepts: Modality[] = op.acceptedModalities && op.acceptedModalities.length > 0 ? (op.acceptedModalities as Modality[]) : ALL_MODALITIES
      const reason = cannotCarry(accepts, enabledModalities)
      return {
        kind: 'operator',
        id: op.id,
        label: op.name,
        compatible: reason === undefined,
        reason,
        accepts,
        operator: op,
        exportEndpoint: operatorReceiverEndpoint(op),
        exportProtocol: 'grpc',
      }
    })

  const fusionEntries: DestinationCatalogEntry[] = fusion
    ? [
        {
          kind: 'fusion',
          id: CENTRAL_OPERATOR_ID,
          label: 'FUSION - this server',
          // Whatever its state: an off FUSION is still a row (to switch on), not a reason it vanishes. Whether it can be sent to now is `fusion.usable`.
          compatible: true,
          accepts: ALL_MODALITIES,
          fusion: fusion.offer,
          operator: fusion.central,
          exportEndpoint: operatorReceiverEndpoint({ id: CENTRAL_OPERATOR_ID }),
          exportProtocol: 'grpc',
        },
      ]
    : []

  const presetEntries: DestinationCatalogEntry[] = EXPORT_PRESETS.map((preset) => {
    const accepts = preset.modalities ?? ALL_MODALITIES
    const reason = cannotCarry(accepts, enabledModalities)
    return { kind: 'external-preset', id: preset.id, label: preset.label, compatible: reason === undefined, reason, accepts, preset }
  })

  // Only a backend with a catalog spec of its own (jaeger/prometheus/loki, never "custom" - see
  // hasQuickStartSpec) has a computable OTLP endpoint to offer here; a "custom" backend only ever carries
  // a person-supplied toolUrl, nothing this step could fill `exportEndpoint` with.
  const quickstartEntries: DestinationCatalogEntry[] = quickStartBackends
    .filter((b) => hasQuickStartSpec(b.kind))
    .map((backend) => {
      const spec = quickStartSpec(backend.kind)
      // A backend that only ever carries one modality can't be the one shared exportEndpoint once a second
      // modality is also turned on.
      const accepts: Modality[] = [backend.modality]
      const reason = cannotCarry(accepts, enabledModalities)
      return {
        kind: 'quickstart',
        id: backend.id,
        label: backend.label,
        compatible: reason === undefined,
        reason,
        accepts,
        backend,
        exportEndpoint: spec.exportEndpoint(backend.namespace),
        exportProtocol: spec.exportProtocol,
      }
    })

  return {
    entries: [...fusionEntries, ...detectedEntries, ...operatorEntries, ...quickstartEntries, ...presetEntries],
    canDeployOperator: isAdmin,
  }
}

/** Stable identity for one catalog entry across renders - kind-qualified, since an operator id and a preset
 *  id live in different namespaces and could in principle collide. The destination step keeps its own
 *  "which one did the person pick" as this string, not as a match against `exportEndpoint`: a preset's
 *  endpoint is a pattern the person then edits in place (`otlp-gateway-<region>...`), after which the text
 *  no longer equals the entry's endpoint and a text match would forget what they picked. */
export const destinationKey = (e: DestinationCatalogEntry): string => `${e.kind}-${e.id}`

/** The endpoint text picking `e` starts the draft with (a preset's is a pattern still to be filled in). */
export const destinationEndpoint = (e: DestinationCatalogEntry): string => (e.kind === 'external-preset' ? e.preset.endpointPattern : e.exportEndpoint)

/** Whether `e` expects a credential header - only a known external preset says so; an operator authenticates
 *  by mTLS and a quick-started or detected in-cluster backend carries none by default. */
export const destinationNeedsCredential = (e: DestinationCatalogEntry): boolean => e.kind === 'external-preset' && !!e.preset.headerName

/** An in-cluster receiver that speaks plain text: the draft turns "skip TLS verification" on for it, which
 *  for this chart's exporter is what selects http:// (or a plaintext gRPC channel). A regional operator is
 *  mutual TLS and a cloud service is verified TLS, so neither is. */
export const destinationIsPlain = (e: DestinationCatalogEntry): boolean => e.kind === 'quickstart' || e.kind === 'detected' || (e.kind === 'external-preset' && !!e.preset.plain)

/** The draft with `e` picked as the destination: its endpoint, its protocol, and (for a preset that names
 *  one) its credential header - the same three fields the flat form's own preset picker fills in.
 *  `exportOperatorId` is set for a regional operator and cleared for every other kind, so it can never
 *  outlive the pick that set it. An operator also resets what only makes sense for an external endpoint
 *  (skipping TLS verification, a credential header and Secret left over from a previously picked preset):
 *  the receiver is mutual TLS, and those would otherwise ride along into its command. An in-cluster
 *  receiver likewise starts with no credential and with plain transport; a cloud preset starts verified. */
export function applyDestination(value: TelemetryInput, e: DestinationCatalogEntry): TelemetryInput {
  if (e.kind === 'external-preset') {
    return {
      ...value,
      exportEndpoint: e.preset.endpointPattern,
      exportProtocol: e.preset.protocol,
      exportInsecure: !!e.preset.plain,
      exportAuthHeaderName: e.preset.headerName ? e.preset.headerName : value.exportAuthHeaderName,
      exportOperatorId: '',
    }
  }
  if (e.kind === 'operator' || e.kind === 'fusion') {
    return {
      ...value,
      exportEndpoint: e.exportEndpoint,
      exportProtocol: e.exportProtocol,
      exportInsecure: false,
      exportAuthHeaderName: '',
      exportAuthSecretName: '',
      exportAuthSecretKey: '',
      exportOperatorId: e.id,
    }
  }
  return {
    ...value,
    exportEndpoint: e.exportEndpoint,
    exportProtocol: e.exportProtocol,
    exportInsecure: true,
    exportAuthHeaderName: '',
    exportAuthSecretName: '',
    exportAuthSecretKey: '',
    exportOperatorId: '',
  }
}

export type DestinationGroup = 'fusion' | 'cluster' | 'org' | 'self' | 'cloud'

export const GROUP_TITLE: Record<DestinationGroup, string> = {
  fusion: 'This server',
  cluster: 'Found in your cluster',
  org: 'Your organisation',
  self: 'Self-hosted',
  cloud: 'Cloud services',
}

export function destinationGroup(e: DestinationCatalogEntry): DestinationGroup {
  if (e.kind === 'fusion') return 'fusion'
  if (e.kind === 'detected') return 'cluster'
  if (e.kind === 'operator' || e.kind === 'quickstart') return 'org'
  return e.preset.group === 'self-hosted' ? 'self' : 'cloud'
}

const GROUP_ORDER: DestinationGroup[] = ['fusion', 'cluster', 'org', 'self', 'cloud']
/** How many of a presets group are shown before "show more" when nothing of the organisation's own exists. */
const PRESET_PREVIEW = 3

export interface DestinationSection {
  group: DestinationGroup
  title: string
  /** Everything usable in this group. */
  entries: DestinationCatalogEntry[]
  /** The part shown before "show more". */
  shown: DestinationCatalogEntry[]
}

/**
 * How the destination step lays the catalog out: what is offered up front, what waits behind "show more",
 * and what can't be used right now and why. What this organisation or this cluster already has (found in
 * the cluster, a regional operator, a backend it quick-started) comes first and is all that is shown by
 * default; the self-hosted and cloud presets only lead, a few each, when there is nothing of the
 * organisation's own to offer, and otherwise sit behind "show more". Entries that exist but can't carry the
 * signals turned on stay visible, as `unavailable` with their reason.
 */
export function layoutDestinations(catalog: DestinationCatalog, opts: { clusterId?: string } = {}): {
  /** The sections, in display order, empty ones left out. */
  sections: DestinationSection[]
  /** Shown by default - what the sections show before "show more". */
  primary: DestinationCatalogEntry[]
  /** Everything usable, in display order - what "show more" reveals. */
  all: DestinationCatalogEntry[]
  /** Exists, but can't carry what is turned on - always with a reason. */
  unavailable: DestinationCatalogEntry[]
  /** What is already in this organisation or cluster: found in the cluster, operators, quick-started backends. */
  known: DestinationCatalogEntry[]
  /** The organisation's own usable destinations only (operators and quick-started backends) - the ones
   *  worth picking for someone, since a detected address is a guess from a workload's name. */
  own: DestinationCatalogEntry[]
  /** Keys of the entries worth recommending - see `isRecommended`. Always a subset of `known`. */
  recommended: Set<string>
} {
  const usable = catalog.entries.filter((e) => e.compatible)
  const recommended = new Set(usable.filter((e) => isRecommended(e, opts.clusterId, catalog.entries)).map(destinationKey))
  // Recommended first within a group, otherwise the catalog's own order.
  const rank = (e: DestinationCatalogEntry) => Number(!recommended.has(destinationKey(e)))
  const byGroup = (g: DestinationGroup) => usable.filter((e) => destinationGroup(e) === g).sort((a, b) => rank(a) - rank(b))
  // FUSION is always a row, but only counts as something the organisation HAS (to pick for someone, or to count as "there is one thing
  // on offer") once it can be sent to: an off FUSION is never picked on anyone's behalf.
  const fusionRows = byGroup('fusion')
  const fusionUsable = fusionRows.filter((e) => e.kind === 'fusion' && e.fusion.usable)
  const known = [...byGroup('cluster'), ...fusionUsable, ...byGroup('org')]
  const hasKnown = known.length > 0

  const sections: DestinationSection[] = GROUP_ORDER.map((group) => {
    const entries = byGroup(group)
    const preset = group === 'self' || group === 'cloud'
    const shown = preset ? (hasKnown ? [] : entries.slice(0, PRESET_PREVIEW)) : entries
    return { group, title: GROUP_TITLE[group], entries, shown }
  }).filter((s) => s.entries.length > 0)

  return {
    sections,
    primary: sections.flatMap((s) => s.shown),
    all: sections.flatMap((s) => s.entries),
    unavailable: catalog.entries.filter((e) => !e.compatible),
    known,
    own: [...fusionUsable, ...byGroup('org')],
    recommended,
  }
}

/**
 * The one thing this catalog actually knows that makes a destination a better pick than another: a regional
 * operator that is already configured to receive this very cluster (the cluster is one of its source
 * clusters, see RegionalOperator.sourceClusterIds), and FUSION once it runs and nothing else receives the cluster. Nothing else qualifies - "healthiest" or "closest" are
 * not facts the catalog holds, and a badge without a reason behind it only trains people to ignore badges.
 */
export function isRecommended(e: DestinationCatalogEntry, clusterId?: string, entries: DestinationCatalogEntry[] = []): boolean {
  if (e.kind === 'operator') return !!clusterId && e.operator.sourceClusterIds.includes(clusterId)
  // FUSION is the one to pick when it is running and nothing else already receives this cluster - or, for someone who cannot see the
  // operators' scopes, when the server says so.
  if (e.kind === 'fusion') {
    if (e.fusion.kind !== 'running') return false
    if (e.fusion.recommended !== undefined) return e.fusion.recommended
    return !!clusterId && !entries.some((o) => o.kind === 'operator' && o.operator.sourceClusterIds.includes(clusterId))
  }
  return false
}

/**
 * The destination to start from when nothing was chosen: the one entry recommended for this cluster, or else the only thing the
 * organisation has that fits. Never a guess among several, and never a destination that cannot be sent to yet.
 */
export function defaultDestination(layout: ReturnType<typeof layoutDestinations>): DestinationCatalogEntry | undefined {
  const picks = layout.all.filter((e) => layout.recommended.has(destinationKey(e)) && !(e.kind === 'fusion' && !e.fusion.usable))
  if (picks.length === 1) return picks[0]
  return layout.known.length === 1 && layout.own.length === 1 ? layout.own[0] : undefined
}

/** What the search box matches an entry against: its name, address, kind of thing it is and the signals it takes. */
function searchText(e: DestinationCatalogEntry): string {
  const words = [e.label, destinationEndpoint(e), GROUP_TITLE[destinationGroup(e)], e.accepts.join(' ')]
  if (e.kind === 'external-preset') words.push(...(e.preset.keywords ?? []))
  if (e.kind === 'detected') words.push(e.detected.service.name, e.detected.service.namespace)
  return words.join(' ').toLowerCase()
}

/**
 * The catalog filtered by what was typed: every word has to appear somewhere in an entry's name, address,
 * group or signals ("grafana" finds Loki, Tempo and Mimir; "logs self" finds the self-hosted log backends).
 * Searching looks across everything, including what "show more" would otherwise hide, and still reports the
 * matches that can't carry the turned-on signals separately, with their reasons.
 */
export function searchDestinations(catalog: DestinationCatalog, query: string): { usable: DestinationCatalogEntry[]; unavailable: DestinationCatalogEntry[] } {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const hits = catalog.entries.filter((e) => {
    const text = searchText(e)
    return words.every((w) => text.includes(w))
  })
  return { usable: hits.filter((e) => e.compatible), unavailable: hits.filter((e) => !e.compatible) }
}
