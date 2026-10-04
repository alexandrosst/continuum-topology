import type { Modality } from './install'
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
function operatorReceiverEndpoint(op: RegionalOperator): string {
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
