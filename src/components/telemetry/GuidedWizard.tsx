import clsx from 'clsx'
import { Check } from 'lucide-react'
import { useState } from 'react'
import { Button, WizardSteps } from '@/components/ui/primitives'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import type { TelemetryInput } from '@/lib/install'
import GuidedScope from './GuidedScope'
import { AcceleratorsFields, EnergyFields, SignalRow, type SignalId } from './TelemetryFields'

type Layer = 'infrastructure' | 'application'
type Modality = 'metrics' | 'logs' | 'traces'
type Step = 'layer' | 'modality' | 'kind' | 'scope' | 'review'

const LAYER_CARDS: { value: Layer; label: string; hint: string }[] = [
  { value: 'infrastructure', label: 'Infrastructure', hint: 'The clusters, nodes and Kubernetes objects this agent runs on - not your applications themselves.' },
  { value: 'application', label: 'Application', hint: 'What your own workloads emit - metrics they push, logs, and traces.' },
]

const MODALITY_LABEL: Record<Modality, string> = { metrics: 'Metrics', logs: 'Logs', traces: 'Traces' }
const APP_SCOPED = ['applicationMetrics', 'applicationLogs', 'traces'] as const

/** A single selectable option, laid out as a card: the same "bordered box, filled + checkmark once picked"
 *  language TierLevels' own `layout="cards"` uses for the connect wizard's tier picker, reused here for a
 *  one-of-N choice rather than an ordered ladder. */
function PickCard({ label, hint, selected, onClick, testId }: { label: string; hint: string; selected: boolean; onClick: () => void; testId: string }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onClick}
      data-testid={testId}
      className={clsx(
        'relative flex flex-col items-start gap-1 rounded-xl border p-4 text-left transition-all hover:-translate-y-0.5 hover:shadow-lg hover:shadow-black/20',
        selected ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
      )}
    >
      {selected && (
        <span className="absolute right-3 top-3 flex size-5 items-center justify-center rounded-full bg-accent text-nb-950" aria-hidden>
          <Check size={13} strokeWidth={3} />
        </span>
      )}
      <span className="pr-6 text-sm font-medium text-nb-200">{label}</span>
      <span className="text-xs text-nb-500">{hint}</span>
    </button>
  )
}

/**
 * The navigable guided path into telemetry configuration: target is resolved before this ever mounts (see
 * TelemetryWizard.tsx's own `pick` phase, or a scope handed off from the topology canvas), so this only
 * ever walks layer -> modality -> kind -> scope (only when something picked needs one) -> review, one
 * screen at a time with Back/Next - unlike GuidedScope's older single continuously-scrolling form (still
 * used here for its scope-drafting step, via `hideSignalPicker`, rather than a second, driftable copy of
 * that logic). "Add another", offered once a kind has been picked, loops back to layer so a person can
 * build up e.g. infrastructure metrics + application logs + traces in one guided session, all accumulating
 * into the same TelemetryInput draft (every checkbox here writes straight into `value`, exactly like the
 * flat grid does - there is nothing to "commit", so leaving mid-flow never loses a change already made).
 */
export default function GuidedWizard({
  value,
  onChange,
  testIdPrefix,
  initialScope,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** A scope pre-filled from outside the wizard (see GuidedScope.tsx's own doc on this same prop). */
  initialScope?: { name: string; namespaces: string[] }
}) {
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })
  // A scope handed off from outside (the topology canvas's "Define scope from selection") means a target
  // and its signals are already implied - land straight on the scope step to attach it, rather than making
  // the person re-walk layer/modality/kind for signals that were the whole reason this wizard opened guided.
  const [rawStep, setStep] = useState<Step>(() => (initialScope ? 'scope' : 'layer'))
  const [layer, setLayer] = useState<Layer | undefined>()
  const [modality, setModality] = useState<Modality | undefined>()

  const needsScope = APP_SCOPED.some((k) => value[k])
  // If the only application-scoped signal gets unchecked while the scope step is showing, there is nothing
  // left to scope - derived at render time (not an effect) so it never needs a second render to catch up:
  // the "Define scope" screen simply never has a moment where it shows with nothing left to attach. (In
  // practice GuidedScope offers no control that can toggle a signal while it's the one mounted, so this is
  // a safety net rather than a path a person can actually trigger today.)
  const step: Step = rawStep === 'scope' && !needsScope ? 'review' : rawStep

  // Whether the rail shows 4 steps or 5 is latched at each actual step transition (see finishKind below),
  // not derived from `value` on every render like `needsScope` above: reading it live here would reflow
  // the step rail under the user's cursor the instant they ticked an application-scoped checkbox on the
  // Kind step, before they had asked to move on anywhere. Seeded from `needsScope` at mount so a value
  // that already has scoped signals on (editing an existing install, or a scope handed off from outside)
  // starts the rail showing the right step count from the first paint.
  const [scopeStepNeeded, setScopeStepNeeded] = useState<boolean>(needsScope)

  const stepKeys: Step[] = scopeStepNeeded ? ['layer', 'modality', 'kind', 'scope', 'review'] : ['layer', 'modality', 'kind', 'review']
  const stepLabels: Record<Step, string> = { layer: 'Layer', modality: 'Modality', kind: 'Kind', scope: 'Scope', review: 'Review' }
  const currentIndex = Math.max(0, stepKeys.indexOf(step))

  const modalities = layer ? [...new Set(TELEMETRY_SIGNALS.filter((s) => s.layer === layer).map((s) => s.modality))] : []
  const kindSignals = layer && modality ? TELEMETRY_SIGNALS.filter((s) => s.layer === layer && s.modality === modality) : []
  const onSignals = TELEMETRY_SIGNALS.filter((s) => value[s.id as SignalId])

  const pickLayer = (l: Layer) => {
    setLayer(l)
    setModality(undefined)
    setStep('modality')
  }
  const pickModality = (m: Modality) => {
    setModality(m)
    setStep('kind')
  }
  const addAnother = () => {
    setLayer(undefined)
    setModality(undefined)
    setStep('layer')
  }
  const finishKind = () => {
    setScopeStepNeeded(needsScope)
    setStep(needsScope ? 'scope' : 'review')
  }

  return (
    <div className="space-y-4">
      <WizardSteps steps={stepKeys.map((k) => stepLabels[k])} currentIndex={currentIndex} testId={`${testIdPrefix}-guided-steps`} />

      {step === 'layer' && (
        <div data-testid={`${testIdPrefix}-guided-step-layer`}>
          <p className="mb-2.5 text-xs text-nb-500">What kind of thing is this signal about?</p>
          <div className="grid gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Layer">
            {LAYER_CARDS.map((c) => (
              <PickCard key={c.value} label={c.label} hint={c.hint} selected={layer === c.value} onClick={() => pickLayer(c.value)} testId={`${testIdPrefix}-guided-layer-${c.value}`} />
            ))}
          </div>
        </div>
      )}

      {step === 'modality' && layer && (
        <div data-testid={`${testIdPrefix}-guided-step-modality`}>
          <p className="mb-2.5 text-xs text-nb-500">And which modality?</p>
          <div className="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Modality">
            {modalities.map((m) => (
              <PickCard key={m} label={MODALITY_LABEL[m]} hint="" selected={modality === m} onClick={() => pickModality(m)} testId={`${testIdPrefix}-guided-modality-${m}`} />
            ))}
          </div>
          <Button className="mt-3" onClick={() => setStep('layer')} data-testid={`${testIdPrefix}-guided-back`}>Back</Button>
        </div>
      )}

      {step === 'kind' && layer && modality && (
        <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-kind`}>
          <p className="text-xs text-nb-500">
            {LAYER_CARDS.find((c) => c.value === layer)?.label} · {MODALITY_LABEL[modality]} - pick everything this covers that you want.
          </p>
          <div className="space-y-2.5">
            {kindSignals.map((s) => (
              <SignalRow key={s.id} signal={s} checked={value[s.id as SignalId]} onChange={(v) => set(s.id as SignalId, v)} testIdPrefix={testIdPrefix} />
            ))}
          </div>
          {kindSignals.some((s) => s.id === 'energy') && value.energy && <EnergyFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
          {kindSignals.some((s) => s.id === 'accelerators') && value.accelerators && <AcceleratorsFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
          <div className="flex flex-wrap items-center gap-2 pt-1">
            <Button onClick={() => setStep('modality')} data-testid={`${testIdPrefix}-guided-back`}>Back</Button>
            <Button onClick={addAnother} data-testid={`${testIdPrefix}-guided-add-another`}>+ Add another layer/modality</Button>
            <Button variant="primary" className="ml-auto" onClick={finishKind} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
          </div>
        </div>
      )}

      {step === 'scope' && (
        <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-scope`}>
          <GuidedScope value={value} onChange={onChange} testIdPrefix={testIdPrefix} initialDraft={initialScope} />
          <div className="flex items-center gap-2 pt-1">
            <Button onClick={() => setStep('kind')} data-testid={`${testIdPrefix}-guided-back`}>Back</Button>
            <Button variant="primary" className="ml-auto" onClick={() => setStep('review')} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
          </div>
        </div>
      )}

      {step === 'review' && (
        <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-review`}>
          {onSignals.length === 0 ? (
            <p className="text-xs text-nb-500">Nothing is turned on yet - go back and pick at least one signal.</p>
          ) : (
            <>
              <p className="text-xs text-nb-500">Everything this will turn on:</p>
              <ul className="flex flex-wrap gap-1.5" data-testid={`${testIdPrefix}-guided-review-list`}>
                {onSignals.map((s) => (
                  <li key={s.id} title={s.what} className="rounded border border-nb-800 bg-nb-930 px-1.5 py-0.5 text-xs text-nb-300">
                    {s.label}
                  </li>
                ))}
              </ul>
              <p className="text-xs text-nb-600">Set where this is sent below, then finish from there.</p>
            </>
          )}
          <div className="flex flex-wrap items-center gap-2 pt-1">
            <Button onClick={() => setStep(scopeStepNeeded ? 'scope' : 'kind')} data-testid={`${testIdPrefix}-guided-back`}>Back</Button>
            <Button onClick={addAnother} data-testid={`${testIdPrefix}-guided-add-another`}>+ Add another layer/modality</Button>
          </div>
        </div>
      )}
    </div>
  )
}
