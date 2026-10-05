import clsx from 'clsx'
import { ChevronLeft, Pencil, X } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { Button, ICON_MD, ICON_SM, WizardSteps } from '@/components/ui/primitives'
import { api, atLeast } from '@/lib/api'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import { buildDestinationCatalog } from '@/lib/destinationCatalog'
import { effectiveAllowedBackendKinds, type QuickStartBackend, type QuickStartKind } from '@/lib/history'
import { activeLanes, destinationReady, enabledModalities, startLanes, withLane, laneView, type Modality, type TelemetryInput } from '@/lib/install'
import { hasQuickStartSpec, quickStartSpec } from '@/lib/quickStartBackends'
import { LAYER_META } from '@/lib/telemetryLayers'
import type { RegionalOperator } from '@/lib/types'
import { useConn, useServer } from '@/store/server'
import { useSettings } from '@/store/settings'
import { useTopology } from '@/store/topology'
import CollectStep from './CollectStep'
import DestinationStep from './DestinationStep'
import GuidedScope from './GuidedScope'
import RoutesStep, { DestinationMode } from './RoutesStep'
import ProcessStep from './ProcessStep'
import { AllowedKindsControl } from './QuickStartBackends'
import type { SignalId } from './TelemetryFields'
import TelemetryBackendWizard from './TelemetryBackendWizard'
import TelemetryReviewPipeline from './TelemetryReviewPipeline'

/** "3 destinations, one per signal type" - or "one destination" when they all turned out to be the same. */
function sentTo(t: TelemetryInput): string {
  const n = new Set(activeLanes(t).map((m) => t.exportLanes[m].exportEndpoint.trim())).size
  return n === 1 ? 'one destination, set per signal type' : `${n} destinations, one per signal type`
}

type Step = 'collect' | 'scope' | 'process' | 'destination' | 'review' | 'run'

const APP_SCOPED = ['applicationMetrics', 'applicationLogs', 'traces'] as const

/** A low-weight "go back" link, not a bordered button - a wizard already has one strong action per screen
 *  (Continue, or a card pick), and a second box of equal visual weight next to it reads as two competing
 *  choices rather than one primary action and an escape hatch. Mirrors the "Change cluster" back-link
 *  TelemetryWizard.tsx's own picker phase already uses for the same reason. */
function BackLink({ onClick, testId }: { onClick: () => void; testId: string }) {
  return (
    <Button variant="ghost" size="sm" onClick={onClick} data-testid={testId}>
      <ChevronLeft size={ICON_SM} /> Back
    </Button>
  )
}

/** One signal already turned on, anywhere in the flow (not just on the Collect step it was picked from) - a
 *  small removable chip, so "Add another" builds up a visible, editable set instead of a running total a
 *  person can only see by scrolling all the way to Review. Removing here is the same action unchecking its
 *  SignalRow checkbox would be - it writes straight into `value`, there is nothing to "confirm" first. Its
 *  icon is `LAYER_META`'s, looked up by the signal's own layer, so infrastructure- and application-origin
 *  signals stay visually distinguishable even once they're flattened into one list. */
function SelectedChip({ signal, onRemove, testId }: { signal: (typeof TELEMETRY_SIGNALS)[number]; onRemove: () => void; testId: string }) {
  const Icon = LAYER_META[signal.layer].icon
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border border-nb-800 bg-nb-930 py-1 pl-2 pr-1 text-xs text-nb-300" data-testid={testId}>
      <Icon size={ICON_SM} className="text-nb-500" aria-hidden />
      {signal.label}
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove ${signal.label}`}
        className="rounded p-0.5 text-nb-600 hover:bg-nb-940 hover:text-nb-300"
        data-testid={`${testId}-remove`}
      >
        <X size={ICON_MD} />
      </button>
    </span>
  )
}

/**
 * The navigable guided path into telemetry configuration: target is resolved before this ever mounts (see
 * TelemetryWizard.tsx's own `pick` phase, or a scope handed off from the topology canvas), so this only
 * ever walks Collect (every signal on one screen, grouped layer > modality) -> Scope (only when something
 * picked needs one) -> Process -> Destination -> Review -> Run, one screen at a time with Back/Next -
 * reusing GuidedScope for its scope-drafting step alone rather than a second, driftable copy of that logic.
 * Every checkbox writes straight into `value`, exactly like the flat grid does - there is nothing to
 * "commit", so leaving mid-flow never loses a change already made.
 */
export default function GuidedWizard({
  value,
  onChange,
  testIdPrefix,
  initialScope,
  clusterId,
  runSection,
}: {
  /** The last screen's content: the command to run, or the button that generates it. Built by the caller
   *  (TelemetryPanel), which holds what it depends on. Nothing in the wizard shows a command before this. */
  runSection?: ReactNode
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** The agent and cluster this telemetry is for, when the caller knows them. */
  agentId?: string
  clusterId?: string
  /** A scope pre-filled from outside the wizard (see GuidedScope.tsx's own doc on this same prop). */
  initialScope?: { name: string; namespaces: string[] }
}) {
  const needsScope = APP_SCOPED.some((k) => value[k])
  // A scope handed off from outside (the topology canvas's "Define scope from selection") only means
  // something to land on directly when there's already a signal on to attach it to - re-opening an agent
  // that already has application-scoped telemetry configured, say. The common case is the opposite: a scope
  // picked from a fresh, unconfigured selection, where nothing has been turned on yet and "attach a scope"
  // has nothing to attach - that has to start at Collect like any other fresh session, same as
  // the render-time `step` override just below already assumes once something IS on the scope step.
  const [rawStep, setStep] = useState<Step>(() => (initialScope && needsScope ? 'scope' : 'collect'))
  // If the only application-scoped signal gets unchecked while the scope step is showing, there is nothing
  // left to scope - derived at render time (not an effect) so it never needs a second render to catch up:
  // the "Define scope" screen simply never has a moment where it shows with nothing left to attach. This is
  // reachable now: removing a signal's chip (see SelectedChip above) while sitting on the scope step is
  // exactly that case.
  // Same reasoning, but landing on Process (not Review): it, and Destination after it, are still worth
  // seeing even once there is nothing left to scope.
  const step: Step = rawStep === 'scope' && !needsScope ? 'process' : rawStep

  // Whether the rail shows 4 steps or 5 is latched at each actual step transition (see finishCollect and
  // removeSignal below), not derived from `value` on every render like `needsScope` above: reading it live
  // here would reflow the step rail under the user's cursor the instant they ticked an application-scoped
  // checkbox on the Collect step, before they had asked to move on anywhere. Seeded from `needsScope` at mount
  // so a value that already has scoped signals on (editing an existing install, or a scope handed off from
  // outside) starts the rail showing the right step count from the first paint.
  const [scopeStepNeeded, setScopeStepNeeded] = useState<boolean>(needsScope)

  const stepKeys: Step[] = scopeStepNeeded
    ? ['collect', 'scope', 'process', 'destination', 'review', 'run']
    : ['collect', 'process', 'destination', 'review', 'run']
  const stepLabels: Record<Step, string> = { collect: 'Collect', scope: 'Scope', process: 'Process', destination: 'Destination', review: 'Review', run: 'Run' }
  const currentIndex = Math.max(0, stepKeys.indexOf(step))

  const onSignals = TELEMETRY_SIGNALS.filter((s) => value[s.id as SignalId])

  // Review's "Create the command" needs something to put in it: at least one signal, and somewhere to send it.
  const canCreate = onSignals.length > 0 && destinationReady(value)

  // Where Back from Process lands: Scope when this session actually needed one, otherwise Collect.
  const beforeProcess: Step = scopeStepNeeded ? 'scope' : 'collect'

  // Collect's Continue: latch whether the rail gains a Scope step, then go to it (or straight to Process).
  const finishCollect = () => {
    const willNeedScope = APP_SCOPED.some((k) => value[k])
    setScopeStepNeeded(willNeedScope)
    setStep(willNeedScope ? 'scope' : 'process')
  }

  // Destination step: a modality-filtered merge of regional operators, external-backend presets and
  // already-quick-started backends (see destinationCatalog.ts) - operators are fetched here, not read
  // from some wider store, since nothing else in this wizard already holds them and GET /operators is
  // adminRole-gated server-side (admin.go), so a non-administrator never even tries.
  const conn = useConn()
  const isAdmin = useServer((s) => atLeast(s.role, 'admin'))
  const { settings, save, error: settingsError } = useSettings()
  const [operators, setOperators] = useState<RegionalOperator[]>([])
  // Whether the operator list has settled (fetched, failed, or never needed) - the destination step waits
  // for it before auto-picking a lone match, see DestinationStep.
  const [operatorsReady, setOperatorsReady] = useState(!isAdmin)
  useEffect(() => {
    if (!isAdmin) {
      setOperators([])
      setOperatorsReady(true)
      return
    }
    let cancelled = false
    void api
      .listOperators(conn)
      .then((ops) => {
        if (!cancelled) {
          setOperators(ops)
          setOperatorsReady(true)
        }
      })
      .catch(() => {
        // Not fatal - the destination step simply offers no regional operators; the presets, the
        // already-quick-started backends and the custom endpoint all still work.
        if (!cancelled) setOperatorsReady(true)
      })
    return () => {
      cancelled = true
    }
    // Deliberately depend on conn's own identifying fields rather than the conn object itself: useConn's
    // real implementation (store/server.ts) memoizes it by url/org, but a test double or any other caller
    // that hands back a fresh object every render would otherwise re-fire this effect (and, via
    // setOperators, re-render) on every single render - an infinite loop no caller should have to avoid by
    // memoizing just right.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn.url, conn.org, isAdmin])

  const enabledModalitySet = enabledModalities(value)
  // Receivers discovery already sees running in this cluster ("Found in your cluster").
  const { services, clusters } = useTopology()
  const clusterName = clusterId ? clusters?.find((c) => c.id === clusterId)?.name : undefined
  const catalog = buildDestinationCatalog({
    services,
    clusterId,
    operators,
    enabledModalities: enabledModalitySet,
    quickStartBackends: settings.quickStartBackends,
    isAdmin,
  })
  // Which catalog entry the person picked (DestinationStep's own destinationKey), or 'custom' - held here,
  // not in the step, so it survives leaving Destination for Review and coming back.
  const [destChoice, setDestChoice] = useState<string | null>(null)
  // The same, for each signal type while it has a destination of its own.
  const [laneChoices, setLaneChoices] = useState<Record<Modality, string | null>>({ metrics: null, logs: null, traces: null })
  // Which signal type the backend setup opened from, so that what it set up becomes that one's destination.
  const [deployLane, setDeployLane] = useState<Modality | null>(null)
  // What can carry just one signal type. A regional operator is not offered: it needs a client certificate the
  // server issues for the whole agent, which one signal type's own destination cannot ask for.
  const catalogFor = (m: Modality) => {
    const c = buildDestinationCatalog({ services, clusterId, operators, enabledModalities: new Set<Modality>([m]), quickStartBackends: settings.quickStartBackends, isAdmin })
    return { ...c, entries: c.entries.filter((e) => e.kind !== 'operator'), canDeployOperator: false }
  }
  // Only worth offering with two or more signal types on - one has nothing to split. A draft that already
  // sends them separately keeps the choice visible even if that is no longer so.
  const canSplit = enabledModalitySet.size > 1 || value.exportSplit

  const [backendWizardOpen, setBackendWizardOpen] = useState(false)
  const [backendBusy, setBackendBusy] = useState(false)
  // Back from "set up a new backend" lands on the Destination summary with that backend already picked,
  // not on a list the person has to find it in again. Same guard the catalog applies to a quick-started
  // backend: it only ever carries one modality, so it can only be THE destination while that is the only
  // modality turned on - otherwise it stays in the list, shown unavailable with its reason.
  const chooseDeployedBackend = (rec: QuickStartBackend) => {
    if (deployLane) {
      // One signal type's own destination: it only has to carry that type.
      if (!hasQuickStartSpec(rec.kind) || rec.modality !== deployLane) return
      const spec = quickStartSpec(rec.kind)
      const lane = deployLane
      onChange(withLane(value, lane, { ...laneView(value, lane), exportEndpoint: spec.exportEndpoint(rec.namespace), exportProtocol: spec.exportProtocol, exportInsecure: true, exportAuthHeaderName: '', exportAuthSecretName: '', exportAuthSecretKey: '', exportOperatorId: '' }))
      setLaneChoices((c) => ({ ...c, [lane]: `quickstart-${rec.id}` }))
      return
    }
    if (!hasQuickStartSpec(rec.kind) || enabledModalitySet.size !== 1 || !enabledModalitySet.has(rec.modality)) return
    const spec = quickStartSpec(rec.kind)
    onChange({ ...value, exportEndpoint: spec.exportEndpoint(rec.namespace), exportProtocol: spec.exportProtocol, exportOperatorId: '' })
    setDestChoice(`quickstart-${rec.id}`)
  }
  const [kindsBusy, setKindsBusy] = useState(false)
  const saveAllowedKinds = async (kinds: QuickStartKind[]) => {
    setKindsBusy(true)
    try {
      await save(conn, { ...settings, allowedBackendKinds: kinds })
    } finally {
      setKindsBusy(false)
    }
  }
  const saveBackend = async (rec: QuickStartBackend) => {
    setBackendBusy(true)
    try {
      const ok = await save(conn, { ...settings, quickStartBackends: [...settings.quickStartBackends, rec] })
      if (ok) {
        setBackendWizardOpen(false)
        chooseDeployedBackend(rec)
      }
    } finally {
      setBackendBusy(false)
    }
  }

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
      {onSignals.length > 0 && step !== 'collect' && step !== 'review' && step !== 'run' && (
        <div className="flex flex-wrap items-center gap-1.5 border-b border-nb-850 pb-3" data-testid={`${testIdPrefix}-guided-selected`}>
          <span className="text-xs text-nb-600">Turning on:</span>
          {onSignals.map((s) => (
            <SelectedChip key={s.id} signal={s} onRemove={() => removeSignal(s.id as SignalId)} testId={`${testIdPrefix}-guided-chip-${s.id}`} />
          ))}
        </div>
      )}

      <div key={step} className={clsx('wizard-step-in', direction === 'back' && 'wizard-step-in-back')}>
        {step === 'collect' && <CollectStep value={value} onChange={onChange} testIdPrefix={testIdPrefix} onContinue={finishCollect} />}

        {step === 'scope' && (
          <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-scope`}>
            <GuidedScope value={value} onChange={onChange} testIdPrefix={testIdPrefix} initialDraft={initialScope} clusterId={clusterId} />
            <div className="flex items-center gap-2 pt-1">
              <BackLink onClick={() => setStep('collect')} testId={`${testIdPrefix}-guided-back`} />
              <Button variant="primary" className="ml-auto" onClick={() => setStep('process')} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
            </div>
          </div>
        )}

        {step === 'process' && (
          <ProcessStep value={value} onChange={onChange} testIdPrefix={testIdPrefix} clusterName={clusterName} onBack={() => setStep(beforeProcess)} onContinue={() => setStep('destination')} />
        )}

        {step === 'destination' && (
          <div className="space-y-3">
            {canSplit && (
              <div className="space-y-2" data-testid={`${testIdPrefix}-guided-destination-mode`}>
                <div>
                  <h3 className="text-sm font-medium text-nb-200">Where should this telemetry go?</h3>
                  <p className="mt-0.5 text-xs text-nb-500">
                    {value.exportSplit
                      ? 'Each signal type has its own destination.'
                      : 'Everything to one place, unless the place you pick cannot take every signal you turned on.'}
                  </p>
                </div>
                <DestinationMode split={value.exportSplit} onChange={(split) => onChange(split ? startLanes(value) : { ...value, exportSplit: false })} testIdPrefix={`${testIdPrefix}-guided`} />
              </div>
            )}
            {value.exportSplit ? (
              <>
                <RoutesStep
                  value={value}
                  onChange={onChange}
                  testIdPrefix={testIdPrefix}
                  catalogFor={catalogFor}
                  catalogReady={operatorsReady}
                  clusterId={clusterId}
                  choices={laneChoices}
                  onChoose={(m, key) => setLaneChoices((c) => ({ ...c, [m]: key }))}
                  onDeployBackend={(m) => {
                    setDeployLane(m)
                    setBackendWizardOpen(true)
                  }}
                  adminKindsControl={isAdmin ? <AllowedKindsControl allowed={effectiveAllowedBackendKinds(settings.allowedBackendKinds)} busy={kindsBusy} onChange={(kinds) => void saveAllowedKinds(kinds)} /> : undefined}
                />
                {!destinationReady(value) && (
                  <p className="text-xs text-nb-500" data-testid={`${testIdPrefix}-guided-routes-incomplete`}>
                    {activeLanes(value).filter((m) => value.exportLanes[m].exportEndpoint.trim() === '').join(' and ')} still need a destination. You can continue without, but no command is generated until each has one.
                  </p>
                )}
                <div className="flex items-center gap-2 pt-1">
                  <BackLink onClick={() => setStep('process')} testId={`${testIdPrefix}-guided-back`} />
                  <Button variant="primary" className="ml-auto" onClick={() => setStep('review')} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
                </div>
              </>
            ) : (
              <DestinationStep
                value={value}
                onChange={onChange}
                testIdPrefix={testIdPrefix}
                catalog={catalog}
                catalogReady={operatorsReady}
                clusterId={clusterId}
                choice={destChoice}
                onChoose={setDestChoice}
                onDeployBackend={() => {
                  setDeployLane(null)
                  setBackendWizardOpen(true)
                }}
                adminKindsControl={isAdmin ? <AllowedKindsControl allowed={effectiveAllowedBackendKinds(settings.allowedBackendKinds)} busy={kindsBusy} onChange={(kinds) => void saveAllowedKinds(kinds)} /> : undefined}
                onBack={() => setStep('process')}
                onContinue={() => setStep('review')}
                heading={!canSplit}
              />
            )}
          </div>
        )}

        {step === 'review' && (
          <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-review`}>
            {onSignals.length === 0 ? (
              <p className="text-xs text-nb-500">Nothing is turned on yet - go back and pick at least one signal.</p>
            ) : (
              <>
                <p className="text-xs text-nb-500">How this will flow, end to end:</p>
                <TelemetryReviewPipeline value={value} onSignals={onSignals} testIdPrefix={testIdPrefix} />
                {!destinationReady(value) && (
                  <div className="flex flex-wrap items-center gap-2 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn" role="status" data-testid={`${testIdPrefix}-guided-no-destination`}>
                    <span>{value.exportSplit ? `${activeLanes(value).filter((m) => value.exportLanes[m].exportEndpoint.trim() === '').join(' and ')} still need a destination, so no command is generated.` : 'No destination yet, so no command is generated.'}</span>
                    <Button size="sm" onClick={() => setStep('destination')} data-testid={`${testIdPrefix}-guided-choose-destination`}>Choose a destination</Button>
                  </div>
                )}
              </>
            )}
            <div className="flex flex-wrap items-center gap-2 pt-1">
              {/* Destination always sits directly before Review now, whatever scopeStepNeeded is. */}
              <BackLink onClick={() => setStep('destination')} testId={`${testIdPrefix}-guided-back`} />
              <Button onClick={() => setStep('collect')} data-testid={`${testIdPrefix}-guided-edit-signals`}><Pencil size={ICON_SM} /> Change what is collected</Button>
              {/* The command is the last thing, not something drawn under every step: it is only worth
                  reading once everything it contains has been decided. */}
              <Button variant="primary" className="ml-auto" disabled={!canCreate} onClick={() => setStep('run')} data-testid={`${testIdPrefix}-guided-create-command`}>
                Create the command
              </Button>
            </div>
          </div>
        )}

        {step === 'run' && (
          <div className="space-y-4" data-testid={`${testIdPrefix}-guided-step-run`}>
            <div>
              <h3 className="text-sm font-medium text-nb-200">Apply it to the cluster</h3>
              <p className="mt-0.5 text-xs text-nb-500" data-testid={`${testIdPrefix}-guided-run-summary`}>
                {onSignals.length} {onSignals.length === 1 ? 'signal' : 'signals'} to {value.exportSplit ? sentTo(value) : value.exportEndpoint.trim() || 'no destination yet'}. Nothing changes until the command is run.
              </p>
            </div>
            {runSection}
            <div className="flex flex-wrap items-center gap-2 pt-1">
              <BackLink onClick={() => setStep('review')} testId={`${testIdPrefix}-guided-back`} />
            </div>
          </div>
        )}
      </div>
      <TelemetryBackendWizard
        open={backendWizardOpen}
        onClose={() => setBackendWizardOpen(false)}
        allowedKinds={effectiveAllowedBackendKinds(settings.allowedBackendKinds)}
        enabledModalities={deployLane ? new Set<Modality>([deployLane]) : enabledModalitySet}
        existingBackends={settings.quickStartBackends}
        currentEndpoint={value.exportEndpoint}
        currentProtocol={value.exportProtocol}
        extraProcessors={value.extraProcessors}
        admin={isAdmin}
        busy={backendBusy}
        error={settingsError}
        onSave={(rec) => void saveBackend(rec)}
      />
    </div>
  )
}
