import type { Modality, TelemetryInput } from './install'
import { EXPORT_PRESETS, presetSupportsModalities, type ExportPreset } from './exportPresets'
import type { QuickStartBackend } from './history'
import { hasQuickStartSpec, quickStartSpec } from './quickStartBackends'
import type { RegionalOperator } from './types'

/**
 * One pickable destination in the guided telemetry wizard's destination step (GuidedWizard.tsx) - a
 * regional operator already active in this org, a known external backend preset, or a backend this org
 * already quick-started. Each carries enough to fill in `TelemetryInput.exportEndpoint`/`exportProtocol`
 * directly, the same two fields the flat grid's own ComboField already sets (TelemetryFields.tsx) - this
 * is a friendlier way to pick one of those same two fields, not a new destination concept.
 */
export type DestinationCatalogEntry =
  | {
      kind: 'operator'
      id: string
      label: string
      /** False once this operator's own acceptedModalities can't carry everything currently enabled -
       *  shown disabled with `reason`, not hidden, so a person sees why it isn't offered rather than
       *  wondering where it went (see operatorSupportsModalities below). */
      compatible: boolean
      reason?: string
      operator: RegionalOperator
      exportEndpoint: string
      exportProtocol: 'grpc'
    }
  | {
      kind: 'external-preset'
      id: string
      label: string
      /** Always true: EXPORT_PRESETS is filtered by presetSupportsModalities before an entry is ever
       *  built, so an incompatible preset never reaches this list at all (unlike the operator case
       *  above, which shows the mismatch instead of hiding it). */
      compatible: true
      preset: ExportPreset
    }
  | {
      kind: 'quickstart'
      id: string
      label: string
      /** False once more than this one modality is enabled (the same "Use as destination" guard
       *  QuickStartBackends.tsx's own SavedBackend applies - there is only ever one exportEndpoint for
       *  every signal together, and a quick-started backend only ever carries one modality). */
      compatible: boolean
      reason?: string
      backend: QuickStartBackend
      exportEndpoint: string
      exportProtocol: 'grpc' | 'http'
    }

export interface DestinationCatalog {
  entries: DestinationCatalogEntry[]
  /** Always true: the "Deploy a new backend" entry point only ever opens TelemetryBackendWizard, which
   *  already gates the actual setup form on being an administrator internally ("Only administrators can
   *  set this up") - there is nothing here worth hiding the entry point itself over. */
  canDeployBackend: boolean
  /** True only for an organisation administrator: creating a regional operator is adminRole-gated
   *  server-side (see backend/internal/server/admin.go), so offering the entry point to anyone else
   *  would just lead to a 403 once they got there. */
  canDeployOperator: boolean
}

/** The host:port a regional operator's own OTLP receiver answers on, in-cluster - the exact same string
 *  the backend's own operatorDestinationCommand builds (admin_operators.go) for pointing an agent's
 *  export at it. Always plain gRPC: the operator chart's receiver has no HTTP listener. */
export function operatorReceiverEndpoint(op: { id: string }): string {
  return `${op.id}.continuum-system.svc:4317`
}

/**
 * Whether `op` can carry every modality in `enabled` - mirrors presetSupportsModalities' exact semantics
 * (lib/exportPresets.ts), just read from acceptedModalities instead of modalities: empty/absent means
 * "accepts anything" (true for any operator that doesn't restrict modalities at all, including one
 * created before this field existed), false the moment one enabled modality isn't in its list.
 */
function operatorSupportsModalities(op: RegionalOperator, enabled: Set<Modality>): boolean {
  if (!op.acceptedModalities || op.acceptedModalities.length === 0) return true
  return [...enabled].every((m) => op.acceptedModalities!.includes(m))
}

/**
 * Merges three existing sources into one modality-filtered destination list for the guided wizard's
 * destination step: regional operators already active in this org, the built-in external-backend
 * presets, and backends this org already quick-started - plus the two "deploy new" entry points'
 * eligibility. A pure function over already-fetched data (never fetches anything itself) so the guided
 * wizard owns when/whether to call api.listOperators (gated on `isAdmin`, since GET /operators is
 * adminRole-only - see admin.go) and this stays trivially testable with plain data.
 */
export function buildDestinationCatalog(opts: {
  operators: RegionalOperator[]
  enabledModalities: Set<Modality>
  quickStartBackends: QuickStartBackend[]
  isAdmin: boolean
}): DestinationCatalog {
  const { operators, enabledModalities, quickStartBackends, isAdmin } = opts

  const operatorEntries: DestinationCatalogEntry[] = operators
    .filter((op) => op.status === 'active')
    .map((op) => {
      const compatible = operatorSupportsModalities(op, enabledModalities)
      return {
        kind: 'operator',
        id: op.id,
        label: op.name,
        compatible,
        reason: compatible
          ? undefined
          : `${op.name} only accepts ${op.acceptedModalities!.join('/')} - turn off the other signals above first.`,
        operator: op,
        exportEndpoint: operatorReceiverEndpoint(op),
        exportProtocol: 'grpc',
      }
    })

  const presetEntries: DestinationCatalogEntry[] = EXPORT_PRESETS.filter((p) => presetSupportsModalities(p, enabledModalities)).map((preset) => ({
    kind: 'external-preset',
    id: preset.id,
    label: preset.label,
    compatible: true,
    preset,
  }))

  // Only a backend with a catalog spec of its own (jaeger/prometheus/loki, never "custom" - see
  // hasQuickStartSpec) has a computable OTLP endpoint to offer here; a "custom" backend only ever carries
  // a person-supplied toolUrl, nothing this step could fill `exportEndpoint` with.
  const quickstartEntries: DestinationCatalogEntry[] = quickStartBackends
    .filter((b) => hasQuickStartSpec(b.kind) && enabledModalities.has(b.modality))
    .map((backend) => {
      const spec = quickStartSpec(backend.kind)
      // Same "Use as destination" guard QuickStartBackends.tsx's own SavedBackend applies: a backend that
      // only ever carries one modality can't be the one shared exportEndpoint once a second modality is
      // also turned on.
      const compatible = enabledModalities.size === 1
      return {
        kind: 'quickstart',
        id: backend.id,
        label: backend.label,
        compatible,
        reason: compatible ? undefined : `${backend.label} only carries ${backend.modality} - turn off the other signals above first.`,
        backend,
        exportEndpoint: spec.exportEndpoint(backend.namespace),
        exportProtocol: spec.exportProtocol,
      }
    })

  return {
    entries: [...operatorEntries, ...presetEntries, ...quickstartEntries],
    canDeployBackend: true,
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
 *  by mTLS and a quick-started in-cluster backend carries none by default. */
export const destinationNeedsCredential = (e: DestinationCatalogEntry): boolean => e.kind === 'external-preset' && !!e.preset.headerName

/** The draft with `e` picked as the destination: its endpoint, its protocol, and (for a preset that names
 *  one) its credential header - the same three fields the flat form's own preset picker fills in.
 *  `exportOperatorId` is set for a regional operator and cleared for every other kind, so it can never
 *  outlive the pick that set it. An operator also resets what only makes sense for an external endpoint
 *  (skipping TLS verification, a credential header and Secret left over from a previously picked preset):
 *  the receiver is mutual TLS, and those would otherwise ride along into its command. */
export function applyDestination(value: TelemetryInput, e: DestinationCatalogEntry): TelemetryInput {
  if (e.kind === 'external-preset') {
    return {
      ...value,
      exportEndpoint: e.preset.endpointPattern,
      exportProtocol: e.preset.protocol,
      exportAuthHeaderName: e.preset.headerName ? e.preset.headerName : value.exportAuthHeaderName,
      exportOperatorId: '',
    }
  }
  if (e.kind === 'operator') {
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
  return { ...value, exportEndpoint: e.exportEndpoint, exportProtocol: e.exportProtocol, exportOperatorId: '' }
}

/**
 * How the destination step lays the catalog out: what is offered up front, what waits behind "show more",
 * and what can't be used right now and why. Entries this organisation already has (a regional operator, a
 * backend it quick-started) come first and are all that is shown by default; the built-in external presets
 * only lead when there is nothing of the organisation's own to offer, and otherwise sit behind "show more".
 * Entries that exist but can't carry the signals turned on stay visible, as `unavailable` with their reason.
 */
export function layoutDestinations(catalog: DestinationCatalog, opts: { clusterId?: string } = {}): {
  /** Shown by default. */
  primary: DestinationCatalogEntry[]
  /** Everything usable, primary first - what "show more" reveals. */
  all: DestinationCatalogEntry[]
  /** Exists, but can't carry what is turned on - always with a reason. */
  unavailable: DestinationCatalogEntry[]
  /** This organisation's own usable destinations (operators and quick-started backends). */
  known: DestinationCatalogEntry[]
  /** Keys of the entries worth recommending - see `isRecommended`. Always a subset of `known`. */
  recommended: Set<string>
} {
  const usable = catalog.entries.filter((e) => e.compatible)
  const recommended = new Set(usable.filter((e) => isRecommended(e, opts.clusterId)).map(destinationKey))
  // Recommended first, otherwise the catalog's own order (operators, then quick-started backends).
  const known = usable.filter((e) => e.kind !== 'external-preset').sort((a, b) => Number(recommended.has(destinationKey(b))) - Number(recommended.has(destinationKey(a))))
  const presets = usable.filter((e) => e.kind === 'external-preset')
  return {
    primary: known.length > 0 ? known : presets.slice(0, 3),
    all: [...known, ...presets],
    unavailable: catalog.entries.filter((e) => !e.compatible),
    known,
    recommended,
  }
}

/**
 * The one thing this catalog actually knows that makes a destination a better pick than another: a regional
 * operator that is already configured to receive this very cluster (the cluster is one of its source
 * clusters, see RegionalOperator.sourceClusterIds). Nothing else qualifies - "healthiest" or "closest" are
 * not facts the catalog holds, and a badge without a reason behind it only trains people to ignore badges.
 */
export const isRecommended = (e: DestinationCatalogEntry, clusterId?: string): boolean => e.kind === 'operator' && !!clusterId && e.operator.sourceClusterIds.includes(clusterId)
