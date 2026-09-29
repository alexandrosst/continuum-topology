import clsx from 'clsx'
import { Activity, AppWindow, Check, ChevronLeft, FileText, Plus, Server, Waypoints, X, type LucideIcon } from 'lucide-react'
import { useState } from 'react'
import { Button, WizardSteps } from '@/components/ui/primitives'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import type { TelemetryInput } from '@/lib/install'
import GuidedScope from './GuidedScope'
import { AcceleratorsFields, EnergyFields, SignalRow, type SignalId } from './TelemetryFields'

type Layer = 'infrastructure' | 'application'
type Modality = 'metrics' | 'logs' | 'traces'
type Step = 'layer' | 'modality' | 'kind' | 'scope' | 'review'

const LAYER_META: Record<Layer, { label: string; hint: string; icon: LucideIcon }> = {
  infrastructure: { label: 'Infrastructure', hint: 'The clusters, nodes and Kubernetes objects this agent runs on - not your applications themselves.', icon: Server },
  application: { label: 'Application', hint: 'What your own workloads emit - metrics they push, logs, and traces.', icon: AppWindow },
}
const LAYER_CARDS: Layer[] = ['infrastructure', 'application']

const MODALITY_META: Record<Modality, { label: string; icon: LucideIcon }> = {
  metrics: { label: 'Metrics', icon: Activity },
  logs: { label: 'Logs', icon: FileText },
  traces: { label: 'Traces', icon: Waypoints },
}
const APP_SCOPED = ['applicationMetrics', 'applicationLogs', 'traces'] as const

/** A single selectable option, laid out as a card: the same "bordered box, filled + checkmark once picked"
 *  language TierLevels' own `layout="cards"` uses for the connect wizard's tier picker, reused here for a
 *  one-of-N choice rather than an ordered ladder. */
function PickCard({
  label,
  hint,
  icon: Icon,
  selected,
  onClick,
  testId,
}: {
  label: string
  hint: string
  icon?: LucideIcon
  selected: boolean
  onClick: () => void
  testId: string
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onClick}
      data-testid={testId}
      className={clsx(
        'relative flex flex-col items-start gap-1.5 rounded-xl border p-4 text-left transition-all hover:-translate-y-0.5 hover:shadow-lg hover:shadow-black/20',
        selected ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
      )}
    >
      {selected && (
        <span className="absolute right-3 top-3 flex size-5 items-center justify-center rounded-full bg-accent text-nb-950" aria-hidden>
          <Check size={13} strokeWidth={3} />
        </span>
      )}
      {Icon && (
        <span className={clsx('flex size-8 items-center justify-center rounded-lg', selected ? 'bg-accent/15 text-accent' : 'bg-nb-930 text-nb-500')} aria-hidden>
          <Icon size={16} />
        </span>
      )}
      <span className="pr-6 text-sm font-medium text-nb-200">{label}</span>
      {hint && <span className="text-xs text-nb-500">{hint}</span>}
    </button>
  )
}

/** A low-weight "go back" link, not a bordered button - a wizard already has one strong action per screen
 *  (Continue, or a card pick), and a second box of equal visual weight next to it reads as two competing
 *  choices rather than one primary action and an escape hatch. Mirrors the "Change cluster" back-link
 *  TelemetryWizard.tsx's own picker phase already uses for the same reason. */
function BackLink({ onClick, testId }: { onClick: () => void; testId: string }) {
  return (
    <Button variant="ghost" size="sm" onClick={onClick} data-testid={testId}>
      <ChevronLeft size={13} /> Back
    </Button>
  )
}

/** One signal already turned on, anywhere in the flow (not just on the Kind step it was picked from) - a
 *  small removable chip, so "Add another" builds up a visible, editable set instead of a running total a
 *  person can only see by scrolling all the way to Review. Removing here is the same action unchecking its
 *  SignalRow checkbox would be - it writes straight into `value`, there is nothing to "confirm" first. Its
 *  icon is `LAYER_META`'s, looked up by the signal's own layer, so infrastructure- and application-origin
 *  signals stay visually distinguishable even once they're flattened into one list. */
function SelectedChip({ signal, onRemove, testId }: { signal: (typeof TELEMETRY_SIGNALS)[number]; onRemove: () => void; testId: string }) {
  const Icon = LAYER_META[signal.layer].icon
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border border-nb-800 bg-nb-930 py-1 pl-2 pr-1 text-xs text-nb-300" data-testid={testId}>
      <Icon size={12} className="text-nb-500" aria-hidden />
      {signal.label}
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove ${signal.label}`}
        className="rounded p-0.5 text-nb-600 hover:bg-nb-940 hover:text-nb-300"
        data-testid={`${testId}-remove`}
      >
        <X size={11} />
      </button>
    </span>
  )
}

/**
 * The navigable guided path into telemetry configuration: target is resolved before this ever mounts (see
 * TelemetryWizard.tsx's own `pick` phase, or a scope handed off from the topology canvas), so this only
 * ever walks layer -> modality -> kind -> scope (only when something picked needs one) -> review, one
 * screen at a time with Back/Next - reusing GuidedScope for its scope-drafting step alone (its old
 * standalone signal-picker path is gone; this is its only caller now) rather than a second, driftable copy
 * of that logic. "Add another", offered once a kind has been picked, loops back to layer so a person can
 * build up e.g. infrastructure metrics + application logs + traces in one guided session, all accumulating
 * into the same TelemetryInput draft (every checkbox here writes straight into `value`, exactly like the
 * flat grid does - there is nothing to "commit", so leaving mid-flow never loses a change already made).
 *
 * Every application-layer modality happens to map to exactly one signal (applicationMetrics, applicationLogs
 * and traces are each their own modality's only member - see TELEMETRY_SIGNALS) - there is no real "which
 * one(s)" decision left to make once that modality is picked, so picking it turns that one signal on and
 * skips straight past what would otherwise be a Kind screen holding a single, already-obvious checkbox.
 * Infrastructure's modalities are never this trivial (metrics alone covers six signals), so its Kind step
 * is unchanged.
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
  const needsScope = APP_SCOPED.some((k) => value[k])
  // A scope handed off from outside (the topology canvas's "Define scope from selection") only means
  // something to land on directly when there's already a signal on to attach it to - re-opening an agent
  // that already has application-scoped telemetry configured, say. The common case is the opposite: a scope
  // picked from a fresh, unconfigured selection, where nothing has been turned on yet and "attach a scope"
  // has nothing to attach - that has to start at layer/modality/kind like any other fresh session, same as
  // the render-time `step` override just below already assumes once something IS on the scope step.
  const [rawStep, setStep] = useState<Step>(() => (initialScope && needsScope ? 'scope' : 'layer'))
  const [layer, setLayer] = useState<Layer | undefined>()
  const [modality, setModality] = useState<Modality | undefined>()
  // If the only application-scoped signal gets unchecked while the scope step is showing, there is nothing
  // left to scope - derived at render time (not an effect) so it never needs a second render to catch up:
  // the "Define scope" screen simply never has a moment where it shows with nothing left to attach. This is
  // reachable now: removing a signal's chip (see SelectedChip above) while sitting on the scope step is
  // exactly that case.
  const step: Step = rawStep === 'scope' && !needsScope ? 'review' : rawStep

  // Whether the rail shows 4 steps or 5 is latched at each actual step transition (see finishKind and
  // removeSignal below), not derived from `value` on every render like `needsScope` above: reading it live
  // here would reflow the step rail under the user's cursor the instant they ticked an application-scoped
  // checkbox on the Kind step, before they had asked to move on anywhere. Seeded from `needsScope` at mount
  // so a value that already has scoped signals on (editing an existing install, or a scope handed off from
  // outside) starts the rail showing the right step count from the first paint.
  const [scopeStepNeeded, setScopeStepNeeded] = useState<boolean>(needsScope)

  const stepKeys: Step[] = scopeStepNeeded ? ['layer', 'modality', 'kind', 'scope', 'review'] : ['layer', 'modality', 'kind', 'review']
  const stepLabels: Record<Step, string> = { layer: 'Layer', modality: 'Modality', kind: 'Kind', scope: 'Scope', review: 'Review' }
  const currentIndex = Math.max(0, stepKeys.indexOf(step))

  const modalities = layer ? [...new Set(TELEMETRY_SIGNALS.filter((s) => s.layer === layer).map((s) => s.modality))] : []
  const kindSignals = layer && modality ? TELEMETRY_SIGNALS.filter((s) => s.layer === layer && s.modality === modality) : []
  const onSignals = TELEMETRY_SIGNALS.filter((s) => value[s.id as SignalId])

  const advancePastKind = (justTurnedOn?: SignalId) => {
    const willNeedScope = APP_SCOPED.some((k) => k === justTurnedOn || value[k])
    setScopeStepNeeded(willNeedScope)
    setStep(willNeedScope ? 'scope' : 'review')
  }

  const pickLayer = (l: Layer) => {
    setLayer(l)
    setModality(undefined)
    setStep('modality')
  }
  const pickModality = (m: Modality) => {
    setModality(m)
    const matches = layer ? TELEMETRY_SIGNALS.filter((s) => s.layer === layer && s.modality === m) : []
    if (matches.length === 1) {
      // The only kind this modality has - nothing left to choose, so turn it on and skip straight past
      // what would otherwise be a Kind screen holding one, already-obvious, pre-checked box.
      const id = matches[0].id as SignalId
      if (!value[id]) set(id, true)
      advancePastKind(id)
    } else {
      setStep('kind')
    }
  }
  const addAnother = () => {
    setLayer(undefined)
    setModality(undefined)
    setStep('layer')
  }
  const finishKind = () => advancePastKind()
  const removeSignal = (id: SignalId) => {
    const next = { ...value, [id]: false }
    onChange(next)
    setScopeStepNeeded(APP_SCOPED.some((k) => next[k]))
  }

  // Which way the step content should slide in: forward (into the next step) or back (returning to a
  // prior one). "Adjust state during render" (react.dev's own name for this exact pattern - comparing a
  // prop/derived value to a snapshot of its own last-seen value, entirely within render, no effect) rather
  // than a ref read during render: a ref's `current` isn't tracked by React's render purity model, so
  // reading it while rendering can disagree with what actually got committed last (React may re-run a
  // render without committing it) - a real, if subtle, correctness gap for something as fast-changing as a
  // wizard step. Calling setState here, mid-render, is what react.dev specifically documents for this: React
  // discards this render immediately and re-renders once more with the new state before painting anything,
  // so it costs one extra render pass, never an extra paint.
  const [renderedIndex, setRenderedIndex] = useState(currentIndex)
  const [direction, setDirection] = useState<'forward' | 'back'>('forward')
  if (currentIndex !== renderedIndex) {
    setDirection(currentIndex >= renderedIndex ? 'forward' : 'back')
    setRenderedIndex(currentIndex)
  }

  return (
    <div className="space-y-4">
      <WizardSteps steps={stepKeys.map((k) => stepLabels[k])} currentIndex={currentIndex} testId={`${testIdPrefix}-guided-steps`} />

      {/* Hidden on Review: that screen is this same set, already grouped and spelled out in full below -
          repeating it as a chip strip right above would just say the same thing twice in a row. */}
      {onSignals.length > 0 && step !== 'review' && (
        <div className="flex flex-wrap items-center gap-1.5 border-b border-nb-850 pb-3" data-testid={`${testIdPrefix}-guided-selected`}>
          <span className="text-xs text-nb-600">Turning on:</span>
          {onSignals.map((s) => (
            <SelectedChip key={s.id} signal={s} onRemove={() => removeSignal(s.id as SignalId)} testId={`${testIdPrefix}-guided-chip-${s.id}`} />
          ))}
        </div>
      )}

      <div key={step} className={clsx('wizard-step-in', direction === 'back' && 'wizard-step-in-back')}>
        {step === 'layer' && (
          <div data-testid={`${testIdPrefix}-guided-step-layer`}>
            <p className="mb-2.5 text-xs text-nb-500">What kind of thing is this signal about?</p>
            <div className="grid gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Layer">
              {LAYER_CARDS.map((l) => (
                <PickCard key={l} label={LAYER_META[l].label} hint={LAYER_META[l].hint} icon={LAYER_META[l].icon} selected={layer === l} onClick={() => pickLayer(l)} testId={`${testIdPrefix}-guided-layer-${l}`} />
              ))}
            </div>
          </div>
        )}

        {step === 'modality' && layer && (
          <div data-testid={`${testIdPrefix}-guided-step-modality`}>
            <p className="mb-2.5 text-xs text-nb-500">And which modality?</p>
            <div className="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Modality">
              {modalities.map((m) => (
                <PickCard key={m} label={MODALITY_META[m].label} hint="" icon={MODALITY_META[m].icon} selected={modality === m} onClick={() => pickModality(m)} testId={`${testIdPrefix}-guided-modality-${m}`} />
              ))}
            </div>
            <div className="mt-3">
              <BackLink onClick={() => setStep('layer')} testId={`${testIdPrefix}-guided-back`} />
            </div>
          </div>
        )}

        {step === 'kind' && layer && modality && (
          <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-kind`}>
            <p className="text-xs text-nb-500">
              {LAYER_META[layer].label} · {MODALITY_META[modality].label} - pick everything this covers that you want.
            </p>
            <div className="space-y-2.5">
              {kindSignals.map((s) => (
                <SignalRow key={s.id} signal={s} checked={value[s.id as SignalId]} onChange={(v) => set(s.id as SignalId, v)} testIdPrefix={testIdPrefix} />
              ))}
            </div>
            {kindSignals.some((s) => s.id === 'energy') && value.energy && <EnergyFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
            {kindSignals.some((s) => s.id === 'accelerators') && value.accelerators && <AcceleratorsFields value={value} onChange={onChange} testIdPrefix={testIdPrefix} />}
            <div className="flex flex-wrap items-center gap-2 pt-1">
              <BackLink onClick={() => setStep('modality')} testId={`${testIdPrefix}-guided-back`} />
              <Button onClick={addAnother} data-testid={`${testIdPrefix}-guided-add-another`}><Plus size={13} /> Add another</Button>
              <Button variant="primary" className="ml-auto" onClick={finishKind} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
            </div>
          </div>
        )}

        {step === 'scope' && (
          <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-scope`}>
            <GuidedScope value={value} onChange={onChange} testIdPrefix={testIdPrefix} initialDraft={initialScope} />
            <div className="flex items-center gap-2 pt-1">
              {/* Kind only ever renders with both a layer and a modality picked (see its own gate below) -
                  arriving here straight from `initialScope` (a scope handed off from outside, e.g. the
                  topology canvas) skips both, so there is no Kind screen to go back to yet. Falling back to
                  Layer instead of unconditionally targeting 'kind' avoids landing on a blank step with no
                  controls at all - the dead end this used to be. */}
              <BackLink onClick={() => setStep(layer && modality ? 'kind' : 'layer')} testId={`${testIdPrefix}-guided-back`} />
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
                <div className="space-y-2" data-testid={`${testIdPrefix}-guided-review-list`}>
                  {LAYER_CARDS.filter((l) => onSignals.some((s) => s.layer === l)).map((l) => {
                    const Icon = LAYER_META[l].icon
                    return (
                      <div key={l} className="flex flex-wrap items-center gap-1.5">
                        <span className="inline-flex items-center gap-1 text-xs font-medium uppercase tracking-wide text-nb-500">
                          <Icon size={12} /> {LAYER_META[l].label}
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
                </div>
                <p className="text-xs text-nb-600">Set where this is sent below, then finish from there.</p>
              </>
            )}
            <div className="flex flex-wrap items-center gap-2 pt-1">
              {/* Same reasoning as Scope's own Back button above: 'kind' only ever has something to show
                  once a layer and a modality are both picked. */}
              <BackLink onClick={() => setStep(scopeStepNeeded ? 'scope' : layer && modality ? 'kind' : 'layer')} testId={`${testIdPrefix}-guided-back`} />
              <Button onClick={addAnother} data-testid={`${testIdPrefix}-guided-add-another`}><Plus size={13} /> Add another</Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
