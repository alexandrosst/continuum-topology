import clsx from 'clsx'
import { Activity, FileText, Waypoints, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import type { DestinationCatalog } from '@/lib/destinationCatalog'
import { activeLanes, laneView, TELEMETRY_SIGNALS, withLane, type Modality, type TelemetryInput } from '@/lib/install'
import { Button, ICON_SM } from '@/components/ui/primitives'
import DestinationStep from './DestinationStep'

const LANE_META: Record<Modality, { icon: LucideIcon; label: string; plural: string }> = {
  metrics: { icon: Activity, label: 'Metrics', plural: 'metrics' },
  logs: { icon: FileText, label: 'Logs', plural: 'logs' },
  traces: { icon: Waypoints, label: 'Traces', plural: 'traces' },
}

/**
 * The two ways to send telemetry, as one choice at the top of the Destination step: everything to one
 * place, or each signal type (metrics, logs, traces) to its own. Only offered when two or more signal types
 * are on - with one there is nothing to split.
 */
export function DestinationMode({
  split,
  onChange,
  testIdPrefix,
}: {
  split: boolean
  onChange: (split: boolean) => void
  testIdPrefix: string
}) {
  const option = (on: boolean, label: string, id: string) => (
    <button
      type="button"
      role="radio"
      aria-checked={split === on}
      onClick={() => split !== on && onChange(on)}
      data-testid={`${testIdPrefix}-mode-${id}`}
      className={clsx('rounded-md px-3 py-1.5 text-xs font-medium transition-colors', split === on ? 'bg-nb-850 text-nb-100' : 'text-nb-400 hover:text-nb-200')}
    >
      {label}
    </button>
  )
  return (
    <div role="radiogroup" aria-label="How to send" className="inline-flex gap-0.5 rounded-lg border border-nb-850 bg-nb-925 p-0.5" data-testid={`${testIdPrefix}-mode`}>
      {option(false, 'One destination for everything', 'single')}
      {option(true, 'One per signal type', 'split')}
    </div>
  )
}

/**
 * The Destination step while each signal type has its own destination: one card per type that has a signal
 * turned on, each holding the same picker the single destination uses (so a lane is chosen, edited and
 * explained exactly like it), filtered to what can carry that type. A type with no signal on has no card
 * and needs nothing. Switching back to one destination keeps what was chosen here.
 */
export default function RoutesStep({
  value,
  onChange,
  testIdPrefix,
  catalogFor,
  catalogReady,
  clusterId,
  choices,
  onChoose,
  onDeployBackend,
  adminKindsControl,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** The catalog of destinations that can carry just this signal type. */
  catalogFor: (m: Modality) => DestinationCatalog
  catalogReady: boolean
  clusterId?: string
  choices: Record<Modality, string | null>
  onChoose: (m: Modality, key: string | null) => void
  onDeployBackend: (m: Modality) => void
  adminKindsControl?: ReactNode
}) {
  const lanes = activeLanes(value)
  return (
    <div className="space-y-3" data-testid={`${testIdPrefix}-routes`}>
      <p className="text-xs text-nb-500">
        Each signal type goes to one destination of its own, with its own credential. A backend that only takes traces (Jaeger, Zipkin) can be where traces go while metrics and logs go elsewhere.
      </p>
      {lanes.map((m) => {
        const { icon: Icon, label } = LANE_META[m]
        const picked = TELEMETRY_SIGNALS.filter((s) => s.modality === m).filter((s) => (value as unknown as Record<string, boolean>)[s.id])
        const lane = value.exportLanes[m]
        const secret = lane.exportAuthSecretName.trim()
        // Another lane's credential Secret, offered for reuse: the same backend usually takes metrics and logs under one.
        const reusable = lanes
          .filter((o) => o !== m && value.exportLanes[o].exportAuthSecretName.trim() !== '' && value.exportLanes[o].exportAuthSecretName.trim() !== secret)
          .map((o) => ({ from: o, ...value.exportLanes[o] }))[0]
        return (
          <section key={m} className="space-y-3 rounded-xl border border-nb-850 bg-nb-925 p-4" data-testid={`${testIdPrefix}-lane-${m}`} aria-label={`${label} destination`}>
            <header className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-nb-930 text-nb-300" aria-hidden>
                <Icon size={ICON_SM + 2} />
              </span>
              <h4 className="text-sm font-medium text-nb-200">{label}</h4>
              <span className="text-xs text-nb-500" data-testid={`${testIdPrefix}-lane-${m}-signals`}>{picked.map((s) => s.label).join(', ')}</span>
            </header>
            <DestinationStep
              bare
              value={laneView(value, m)}
              onChange={(v) => onChange(withLane(value, m, v))}
              testIdPrefix={`${testIdPrefix}-lane-${m}`}
              catalog={catalogFor(m)}
              catalogReady={catalogReady}
              clusterId={clusterId}
              choice={choices[m]}
              onChoose={(k) => onChoose(m, k)}
              onDeployBackend={() => onDeployBackend(m)}
              adminKindsControl={adminKindsControl}
              onBack={() => undefined}
              onContinue={() => undefined}
            />
            {reusable && lane.exportEndpoint.trim() !== '' && (
              <div className="flex flex-wrap items-center gap-2 text-xs text-nb-500">
                <span>{secret ? `Reads its credential from ${secret}.` : 'No credential named.'}</span>
                <Button
                  size="sm"
                  onClick={() => onChange(withLane(value, m, { ...laneView(value, m), exportAuthSecretName: reusable.exportAuthSecretName, exportAuthSecretKey: reusable.exportAuthSecretKey, exportAuthHeaderName: reusable.exportAuthHeaderName }))}
                  data-testid={`${testIdPrefix}-lane-${m}-reuse`}
                >
                  Use {reusable.exportAuthSecretName.trim()} (from {LANE_META[reusable.from].plural})
                </Button>
              </div>
            )}
          </section>
        )
      })}
    </div>
  )
}
