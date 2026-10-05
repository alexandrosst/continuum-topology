import { ArrowDown, ArrowRight } from 'lucide-react'
import { ICON_SM } from '@/components/ui/primitives'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import { cleanTags, exportProtocolLabel, scopeTag, type TelemetryInput } from '@/lib/install'
import { EXPORT_PRESETS } from '@/lib/exportPresets'
import { PROCESSOR_KINDS, processorTarget, type ProcessorEntry } from '@/lib/processorCatalog'
import { LAYER_CARDS, LAYER_META } from '@/lib/telemetryLayers'

type SignalRec = (typeof TELEMETRY_SIGNALS)[number]

/** Per-signal scope overrides worth calling out on Review - see ScopeOverrideInput in install.ts. Each
 *  entry here is only shown once its own override actually differs from "falls back to the install's
 *  global scope" (i.e. it has something in `namespaces` or `exclude`). */
const APP_SCOPE_FIELDS: { key: 'applicationMetricsScope' | 'applicationLogsScope' | 'tracesScope'; label: string }[] = [
  { key: 'applicationMetricsScope', label: 'Application metrics' },
  { key: 'applicationLogsScope', label: 'Application logs' },
  { key: 'tracesScope', label: 'Traces' },
]

/** One stage of the pipeline - a bordered card with a small caps label, used identically for all three
 *  stages below so Collect/Process/Send read as one connected shape rather than three unrelated boxes. */
function Stage({ title, children, testId }: { title: string; children: React.ReactNode; testId: string }) {
  return (
    <div className="flex-1 rounded-lg border border-nb-850 bg-nb-925 p-3" data-testid={testId}>
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">{title}</div>
      {children}
    </div>
  )
}

/** The arrow between two stages - a right-pointing chevron once stages sit side by side (sm and up), a
 *  downward one while they're stacked on narrow screens, so the "flows into" shape reads correctly either way. */
function StageArrow() {
  return (
    <div className="flex items-center justify-center text-nb-600" aria-hidden>
      <ArrowDown size={ICON_SM} className="sm:hidden" />
      <ArrowRight size={ICON_SM} className="hidden sm:block" />
    </div>
  )
}

/**
 * The guided wizard's Review step, redrawn as what it actually is - a three-stage pipeline (collect, then
 * process, then send), rather than one flat list of signal chips that said nothing about scope, processing
 * or where any of it ends up. Reads straight off the same `value: TelemetryInput` the rest of the form
 * already holds - the destination and processor fields live below this wizard in TelemetryFields.tsx, not
 * inside it, but their current values are already here, so Review can finally show them instead of just
 * saying "set this below" and showing nothing.
 *
 * `onSignals` is handed in rather than re-derived, so this always agrees with whatever GuidedWizard's own
 * chip strip just showed immediately above it.
 */
export default function TelemetryReviewPipeline({
  value,
  onSignals,
  testIdPrefix,
}: {
  value: TelemetryInput
  onSignals: SignalRec[]
  testIdPrefix: string
}) {
  const scopedOverrides = APP_SCOPE_FIELDS.map(({ key, label }) => ({ label, scope: value[key] })).filter(
    ({ scope }) => scope.namespaces.length > 0 || scope.exclude.length > 0,
  )

  // Always first: where it came from is stamped on everything, whatever else is or isn't switched on.
  const processingSteps: string[] = ['Stamp where it came from (organisation, cluster' + (scopeTag(value) ? ', scope' : '') + ')']
  const tags = cleanTags(value.tags)
  if (tags.length > 0) processingSteps.push(`Add ${tags.length} ${tags.length === 1 ? 'tag' : 'tags'}: ${tags.map((t) => t.key).join(', ')}`)
  if (value.resourceDetection) processingSteps.push('Enrich with collector environment')
  if (value.redaction) processingSteps.push('Mask likely secrets')
  if (value.traces && value.tracesSamplingPercent < 100) processingSteps.push(`Sample ${value.tracesSamplingPercent}% of traces`)
  const extraProcessors = value.extraProcessors.map((e: ProcessorEntry) => ({
    id: e.id,
    name: e.name,
    label: PROCESSOR_KINDS.find((k) => k.id === e.kind)?.label ?? e.kind,
    tracesOnly: processorTarget(e) === 'extraTracesProcessorNames',
  }))

  const destination = value.exportEndpoint.trim()
  const preset = EXPORT_PRESETS.find((p) => p.endpointPattern === destination)

  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:items-stretch" data-testid={`${testIdPrefix}-review-pipeline`}>
      <Stage title="Collect" testId={`${testIdPrefix}-review-collect`}>
        <div className="space-y-1.5">
          {LAYER_CARDS.filter((l) => onSignals.some((s) => s.layer === l)).map((l) => {
            const Icon = LAYER_META[l].icon
            return (
              <div key={l} className="flex flex-wrap items-center gap-1.5">
                <span className="inline-flex items-center gap-1 text-xs text-nb-500">
                  <Icon size={ICON_SM} /> {LAYER_META[l].label}
                </span>
                {onSignals
                  .filter((s) => s.layer === l)
                  .map((s) => (
                    <span key={s.id} title={s.what} className="rounded border border-nb-800 bg-nb-930 px-1.5 py-0.5 text-xs text-nb-300">
                      {s.label}
                    </span>
                  ))}
              </div>
            )
          })}
          {scopedOverrides.length > 0 && (
            <div className="space-y-0.5 border-t border-nb-850 pt-1.5" data-testid={`${testIdPrefix}-review-scope`}>
              {scopedOverrides.map(({ label, scope }) => (
                <p key={label} className="text-xs text-nb-500">
                  <span className="text-nb-400">{label}:</span>{' '}
                  {[scope.namespaces.length > 0 ? `only ${scope.namespaces.join(', ')}` : '', scope.exclude.length > 0 ? `never ${scope.exclude.join(', ')}` : ''].filter(Boolean).join('; ')}
                </p>
              ))}
            </div>
          )}
        </div>
      </Stage>

      <StageArrow />

      <Stage title="Process" testId={`${testIdPrefix}-review-process`}>
        <ul className="space-y-1 text-xs text-nb-300">
          {processingSteps.map((s) => (
            <li key={s}>{s}</li>
          ))}
          {value.debugVerbosity && (
            <li data-testid={`${testIdPrefix}-review-debug`} className={value.debugVerbosity === 'detailed' ? 'text-warn' : undefined}>
              {value.debugVerbosity === 'detailed' ? 'Log every record’s content in the collector’s log' : 'Count what passes in the collector’s log'}
            </li>
          )}
          {extraProcessors.map((e) => (
            <li key={e.id} title={e.tracesOnly ? 'Applies to traces only' : undefined}>
              {e.label}{e.name ? `: ${e.name}` : ''}{e.tracesOnly ? ' (traces only)' : ''}
            </li>
          ))}
        </ul>
      </Stage>

      <StageArrow />

      <Stage title="Send" testId={`${testIdPrefix}-review-send`}>
        {destination ? (
          <div className="space-y-1">
            <p className="break-all text-xs text-nb-300">{preset?.label ?? destination}</p>
            <p className="text-xs text-nb-500">
              {exportProtocolLabel(value.exportProtocol)}
              {value.exportInsecure ? ' · TLS verification skipped' : ''}
            </p>
            {value.exportAuthSecretName.trim() && (
              <p className="text-xs text-nb-600">Authenticated via {value.exportAuthHeaderName || 'Authorization'}</p>
            )}
          </div>
        ) : (
          <p className="text-xs text-warn">Not set yet - go back to Destination.</p>
        )}
      </Stage>
    </div>
  )
}
