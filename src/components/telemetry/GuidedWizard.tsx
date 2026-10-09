import clsx from 'clsx'
import { ChevronLeft } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import CreateOperatorModal from '@/components/operators/CreateOperatorModal'
import { useFusion } from '@/components/operators/FusionPanel'
import { OperatorAddressModal } from '@/components/operators/OperatorAddress'
import { Button, ErrorBanner, ICON_SM, WizardSteps } from '@/components/ui/primitives'
import { atLeast, type CreatedOperator } from '@/lib/api'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import { applyDestination, buildDestinationCatalog, catalogOperators, destinationKey, fusionForCatalog, type DestinationCatalog } from '@/lib/destinationCatalog'
import { CENTRAL_OPERATOR_ID, fusionLabel } from '@/lib/fusionStatus'
import { activeLanes, destinationReady, emptyExportTarget, enabledModalities, laneView, ROUTE_MODALITIES, scopeNarrows, startLanes, withLane, type Modality, type TelemetryInput } from '@/lib/install'
import { reviewNotes, SETUP_STEPS } from '@/lib/telemetrySetup'
import type { RegionalOperator } from '@/lib/types'
import { useOperatorDestinations, useOperators } from '@/lib/useOperators'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'
import CollectStep, { type SignalId } from './CollectStep'
import DestinationStep from './DestinationStep'
import Disclosure from './Disclosure'
import GuidedScope from './GuidedScope'
import ProcessOptions from './ProcessOptions'
import ReviewStep from './ReviewStep'
import RoutesStep from './RoutesStep'

/** "3 destinations, one per signal type" - or "one destination" when they all turned out to be the same. */
function sentTo(t: TelemetryInput): string {
  const n = new Set(activeLanes(t).map((m) => t.exportLanes[m].exportEndpoint.trim())).size
  return n === 1 ? 'one destination, set per signal type' : `${n} destinations, one per signal type`
}

type Phase = 'collect' | 'destination' | 'review' | 'run' | 'check'
const PHASES: Phase[] = ['collect', 'destination', 'review', 'run', 'check']
/** Which dot of the rail each phase is on: "Where from" is the first and is done before this opens; the last three phases are all "Review and install". */
const RAIL: Record<Phase, number> = { collect: 1, destination: 2, review: 3, run: 3, check: 3 }

const APP_SCOPED = ['applicationMetrics', 'applicationLogs', 'traces'] as const
const SCOPE_OF = { applicationMetrics: 'applicationMetricsScope', applicationLogs: 'applicationLogsScope', traces: 'tracesScope' } as const

function StepHeading({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div>
      <h3 className="text-sm font-medium text-nb-200">{title}</h3>
      {children && <p className="mt-0.5 text-xs text-nb-500">{children}</p>}
    </div>
  )
}

/** Back on the left, the one way forward on the right, and - beside it, as words - why that is not available yet. */
function StepNav({ onBack, next, testIdPrefix, why }: { onBack?: () => void; next?: { label: string; onClick: () => void; disabled?: boolean; testId: string }; testIdPrefix: string; why?: string }) {
  return (
    <div className="flex flex-wrap items-center gap-2 pt-1">
      {onBack && (
        <Button variant="ghost" size="sm" onClick={onBack} data-testid={`${testIdPrefix}-guided-back`}>
          <ChevronLeft size={ICON_SM} /> Back
        </Button>
      )}
      {why && <span className="ml-auto text-xs text-nb-500" role="status" data-testid={`${testIdPrefix}-guided-why`}>{why}</span>}
      {next && (
        <Button variant="primary" className={clsx(!why && 'ml-auto')} disabled={next.disabled} onClick={next.onClick} data-testid={next.testId}>
          {next.label}
        </Button>
      )}
    </div>
  )
}

/**
 * The guided setup, the only way to set telemetry up: Where from, What to collect, Where to send, Review and install. "Where from" (the cluster)
 * is chosen before this mounts, by the dialog that wraps it or by the agent row it sits in, so this walks the other three, one screen at a
 * time with Back/Continue. What to collect holds everything about the data itself - the signals, which namespaces, tags and masking - with the
 * less common parts behind disclosures. Review says in plain words what the command changes, and only then comes the command, followed by a
 * check that data arrives. Every control writes straight into `value`, so leaving mid-flow never loses a change already made.
 */
export default function GuidedWizard({
  value,
  onChange,
  testIdPrefix,
  initialScope,
  initialDestination,
  clusterId,
  clusterName,
  runSection,
  checkSection,
  review,
  problems = [],
  onBackToCluster,
  onDone,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  /** The agent this telemetry is for, when the caller knows it. */
  agentId?: string
  /** The cluster this telemetry is for, when known: its workloads are offered as scope, and the operator that already receives it is recommended. */
  clusterId?: string
  /** Its name, for the sentences about what changes there. */
  clusterName?: string
  /** A scope pre-filled from outside the wizard (see GuidedScope.tsx's own doc on this same prop). */
  initialScope?: { name: string; namespaces: string[] }
  /** The id of the operator (or FUSION) to send to, when whoever opened the wizard already knows it ("Connect <cluster>"): the destination
   *  step opens with it chosen, once the list it is in has arrived. */
  initialDestination?: string
  /** The command to run, or the button that generates it. Built by the caller, which holds what it depends on. Nothing before it shows a command. */
  runSection?: ReactNode
  /** The check that data arrives, once the command has been run. Built by the caller, which holds what the agent reports. */
  checkSection?: ReactNode
  /** What the command changes against what the install reports: the list, whether anything is installed, and what the form shows but the install does not report. */
  review: { diff: string[]; installed: boolean; kept: string[] }
  /** What is wrong with the draft as it stands (telemetryProblems): said on the review, where the command would otherwise be made. */
  problems?: string[]
  /** Goes back to choosing the cluster - absent when the cluster was not chosen here. */
  onBackToCluster?: () => void
  /** Closes whatever holds this wizard; offered on the last screen. */
  onDone?: () => void
}) {
  const cluster = clusterName ?? 'the cluster'
  const [phase, setPhase] = useState<Phase>('collect')
  const phaseIndex = PHASES.indexOf(phase)

  const onSignals = TELEMETRY_SIGNALS.filter((s) => value[s.id as SignalId])
  // Nothing picked on an install that has telemetry: the one thing left to do is turn it all off.
  const turningOff = onSignals.length === 0 && value.hadTelemetry
  const needsScope = APP_SCOPED.some((k) => value[k])
  const scopeNarrowed = APP_SCOPED.some((k) => scopeNarrows(value[SCOPE_OF[k]]))
  const processEdited = value.tags.length > 0 || value.extraProcessors.length > 0 || value.resourceDetection || !value.redaction || value.tracesSamplingPercent !== 100 || value.debugVerbosity !== ''

  // Where to send: a modality-filtered merge of FUSION, the regional operators, external-backend presets and already-quick-started
  // backends (see destinationCatalog.ts). An administrator reads the operators and FUSION's own state; an editor reads the same
  // thing as a read model (GET /operator-destinations). Both are polled, so a health dot that changes while this is open changes on it.
  const isAdmin = useServer((s) => atLeast(s.role, 'admin'))
  const operatorList = useOperators(isAdmin)
  const destinationList = useOperatorDestinations(!isAdmin)
  const { operators } = operatorList
  const { destinations } = destinationList
  const reloadLists = () => void (isAdmin ? operatorList.reload() : destinationList.reload())
  const loadError = isAdmin ? operatorList.error : destinationList.error
  const fusionState = useFusion(isAdmin, reloadLists)
  // Whether what the list shows has settled (fetched, failed, or never needed) - the default pick waits for it. FUSION counts for an
  // administrator: its state arrives separately.
  const operatorsReady = isAdmin ? operatorList.loaded && (fusionState.status !== null || fusionState.error !== '') : destinationList.loaded

  const enabledModalitySet = enabledModalities(value)
  // Receivers discovery already sees running in this cluster ("Found in your cluster").
  const { services } = useTopology()
  const fusionEntry = fusionForCatalog({ status: fusionState.status, operators, destinations, isAdmin })
  const catalogOf = (modalities: Set<Modality>): DestinationCatalog =>
    buildDestinationCatalog({ services, clusterId, operators: catalogOperators({ operators, destinations, isAdmin }), enabledModalities: modalities, isAdmin, fusion: fusionEntry })
  const catalog = catalogOf(enabledModalitySet)

  // Sending to FUSION while it cannot receive: nothing would be there to take it, so no command is offered until that is fixed.
  const sendsToFusion = value.exportSplit ? activeLanes(value).some((m) => value.exportLanes[m].exportOperatorId === CENTRAL_OPERATOR_ID) : value.exportOperatorId === CENTRAL_OPERATOR_ID
  const fusionBlocked = sendsToFusion && !!fusionEntry && !fusionEntry.offer.usable
  // "Create the command" needs something to put in it: at least one signal, and somewhere to send it that can receive.
  const canCreate = turningOff || (onSignals.length > 0 && destinationReady(value) && !fusionBlocked)

  const fusionControls = {
    busy: fusionState.busy,
    error: fusionState.error || undefined,
    // Only an administrator can switch it, and only when this server can.
    enable: isAdmin && fusionState.status?.available ? async () => void (await fusionState.enable()) : undefined,
  }
  // The new-operator and "reachable at" dialogs open over this wizard, never instead of it: nothing chosen so far is lost, and the operator
  // that was just made is picked afterwards (`pendingPick`), in the lane it was made for when the destinations are split.
  const [creatingFor, setCreatingFor] = useState<{ lane?: Modality } | null>(null)
  const [addressFor, setAddressFor] = useState<RegionalOperator | null>(null)
  const [pendingPick, setPendingPick] = useState<{ id: string; lane?: Modality; /** Dropped, not waited for, when the list turns out not to have it. */ optional?: boolean } | null>(() => (initialDestination ? { id: initialDestination, optional: true } : null))

  // Which catalog entry the person picked (DestinationStep's own destinationKey), or 'custom' - held here,
  // not in the step, so it survives leaving Where to send for Review and coming back.
  const [destChoice, setDestChoice] = useState<string | null>(null)
  // The same, for each signal type while it has a destination of its own.
  const [laneChoices, setLaneChoices] = useState<Record<Modality, string | null>>({ metrics: null, logs: null, traces: null })
  // What can carry just one signal type: a regional operator only if it takes that type, a backend only if it does.
  const catalogFor = (m: Modality) => catalogOf(new Set<Modality>([m]))
  // Picks the operator that was asked for (a new one, or the one "Connect <cluster>" came from) as soon as the list has it. FUSION is only
  // ever picked while it can receive: an off one is left to the list's "Enable and use".
  useEffect(() => {
    if (!pendingPick || !operatorsReady) return
    const entry = (pendingPick.lane ? catalogFor(pendingPick.lane) : catalog).entries.find((e) => (e.kind === 'operator' || e.kind === 'fusion') && e.id === pendingPick.id)
    if (!entry && !pendingPick.optional) return
    setPendingPick(null)
    if (!entry || !entry.compatible || (entry.kind === 'fusion' && !entry.fusion.usable)) return
    const lane = pendingPick.lane
    if (lane && value.exportSplit) {
      onChange(withLane(value, lane, applyDestination(laneView(value, lane), entry)))
      setLaneChoices((c) => ({ ...c, [lane]: destinationKey(entry) }))
    } else {
      onChange(applyDestination(value, entry))
      setDestChoice(destinationKey(entry))
    }
  })
  // Splitting starts every lane from the one destination - except where that is a regional operator which does
  // not take the lane's signal type (a metrics-only operator for logs): that lane starts empty instead.
  const splitDestinations = (v: TelemetryInput): TelemetryInput => {
    const started = startLanes(v)
    const lanes = { ...started.exportLanes }
    for (const m of ROUTE_MODALITIES) {
      const id = lanes[m].exportOperatorId
      if (id && !catalogFor(m).entries.some((e) => (e.kind === 'operator' || e.kind === 'fusion') && e.id === id && e.compatible)) lanes[m] = emptyExportTarget
    }
    return { ...started, exportLanes: lanes }
  }
  // Only worth offering with two or more signal types on - one has nothing to split. A draft that already
  // sends them separately keeps the choice visible even if that is no longer so.
  const canSplit = enabledModalitySet.size > 1 || value.exportSplit
  const missingLanes = activeLanes(value).filter((m) => value.exportLanes[m].exportEndpoint.trim() === '')

  // Which way the step content should slide in: forward or back. "Adjust state during render" (react.dev's own name for this pattern):
  // React discards this render and re-renders once with the new state before painting anything.
  const [renderedIndex, setRenderedIndex] = useState(phaseIndex)
  const [direction, setDirection] = useState<'forward' | 'back'>('forward')
  if (phaseIndex !== renderedIndex) {
    setDirection(phaseIndex >= renderedIndex ? 'forward' : 'back')
    setRenderedIndex(phaseIndex)
  }

  const destinationName = value.exportSplit ? sentTo(value) : (catalog.entries.find((e) => destinationKey(e) === destChoice)?.label ?? value.exportEndpoint.trim()) || 'the destination'
  const routes = value.exportSplit
    ? activeLanes(value).map((m) => `${m} to ${catalogFor(m).entries.find((e) => destinationKey(e) === laneChoices[m])?.label ?? (value.exportLanes[m].exportEndpoint.trim() || 'no destination yet')}`)
    : undefined
  const notes = reviewNotes({ draft: value, diff: review.diff, installed: review.installed, turningOff, destination: destinationName, kept: review.kept, routes })
  const p = testIdPrefix

  return (
    <div className="space-y-4">
      <WizardSteps steps={SETUP_STEPS} currentIndex={RAIL[phase]} testId={`${p}-guided-steps`} />

      <div key={phase} className={clsx('wizard-step-in', direction === 'back' && 'wizard-step-in-back')}>
        {phase === 'collect' && (
          <div className="space-y-4">
            <StepHeading title="What should be collected?">
              {clusterName ? <>From <span className="text-nb-300">{clusterName}</span>. </> : null}
              Pick a starting point or tick signals one by one; you can change this later by running the setup again.
              {onBackToCluster && (
                <>
                  {' '}
                  <button type="button" className="text-accent hover:underline" onClick={onBackToCluster} data-testid={`${p}-guided-change-cluster`}>Change cluster</button>
                </>
              )}
            </StepHeading>
            <CollectStep value={value} onChange={onChange} testIdPrefix={p} />
            {(needsScope || scopeNarrowed || initialScope) && (
              <Disclosure title="Limit to some namespaces" hint="applications only" defaultOpen={!!initialScope || scopeNarrowed} testId={`${p}-guided-scope`}>
                <div data-testid={`${p}-guided-step-scope`}>
                  <GuidedScope value={value} onChange={onChange} testIdPrefix={p} initialDraft={initialScope} clusterId={clusterId} />
                </div>
              </Disclosure>
            )}
            <Disclosure title="Tags, masking and sampling" hint="optional" defaultOpen={processEdited} testId={`${p}-guided-process`}>
              <ProcessOptions value={value} onChange={onChange} testIdPrefix={p} clusterName={clusterName} />
            </Disclosure>
            <StepNav
              testIdPrefix={p}
              why={onSignals.length === 0 && !turningOff ? 'Pick at least one signal.' : undefined}
              next={{ label: turningOff ? 'Review turning everything off' : 'Continue', onClick: () => setPhase(turningOff ? 'review' : 'destination'), disabled: onSignals.length === 0 && !turningOff, testId: `${p}-guided-continue` }}
            />
          </div>
        )}

        {phase === 'destination' && (
          <div className="space-y-3" data-testid={`${p}-guided-step-where`}>
            <StepHeading title="Where should this telemetry go?">
              One list: {[...enabledModalitySet].join(', ')} can go to the places that accept them. Anything that does not says why.
            </StepHeading>
            {loadError && (
              <div className="flex flex-wrap items-start gap-2" data-testid={`${p}-guided-load-error`}>
                <ErrorBanner className="min-w-0 flex-1 basis-60">Your regional operators could not be loaded ({loadError}). Other destinations are still listed; try again to see the operators.</ErrorBanner>
                <Button size="sm" onClick={reloadLists} data-testid={`${p}-guided-load-retry`}>Try again</Button>
              </div>
            )}
            {canSplit && (
              <label className="flex cursor-pointer items-start gap-2.5 text-sm" data-testid={`${p}-guided-mode`}>
                <input
                  type="checkbox"
                  className="mt-0.5 size-4 accent-[var(--color-accent)]"
                  checked={value.exportSplit}
                  onChange={(e) => onChange(e.target.checked ? splitDestinations(value) : { ...value, exportSplit: false })}
                  data-testid={`${p}-guided-split`}
                />
                <span>
                  <span className="text-nb-300">Send each signal type to its own destination</span>
                  <span className="block text-xs text-nb-500">Metrics, logs and traces each get their own place and credential.</span>
                </span>
              </label>
            )}
            {value.exportSplit ? (
              <RoutesStep
                value={value}
                onChange={onChange}
                testIdPrefix={p}
                catalogFor={catalogFor}
                catalogReady={operatorsReady}
                clusterId={clusterId}
                choices={laneChoices}
                onChoose={(m, key) => setLaneChoices((c) => ({ ...c, [m]: key }))}
                shared={{ fusion: fusionControls, onSetUpOperator: isAdmin ? (lane) => setCreatingFor({ lane }) : undefined, onRecordAddress: setAddressFor }}
              />
            ) : (
              <DestinationStep
                value={value}
                onChange={onChange}
                testIdPrefix={p}
                catalog={catalog}
                catalogReady={operatorsReady}
                clusterId={clusterId}
                choice={destChoice}
                onChoose={setDestChoice}
                fusion={fusionControls}
                onSetUpOperator={isAdmin ? () => setCreatingFor({}) : undefined}
                onRecordAddress={setAddressFor}
              />
            )}
            <StepNav
              testIdPrefix={p}
              onBack={() => setPhase('collect')}
              why={destinationReady(value) ? undefined : value.exportSplit ? `Choose where ${missingLanes.join(' and ')} should go.` : 'Choose where to send this.'}
              next={{ label: 'Continue', onClick: () => setPhase('review'), disabled: !destinationReady(value), testId: `${p}-guided-continue` }}
            />
          </div>
        )}

        {phase === 'review' && (
          <div className="space-y-3">
            <StepHeading title="Review before anything is installed">
              {turningOff ? 'Nothing is changed by looking at this.' : 'This is what the command does. Nothing changes until you run it.'}
            </StepHeading>
            <ReviewStep notes={notes} cluster={cluster} testIdPrefix={p}>
              {problems.length > 0 && (
                <ErrorBanner data-testid={`${p}-problems`}>{problems.join('. ')}.</ErrorBanner>
              )}
              {fusionBlocked && fusionEntry && (
                <div className="flex flex-wrap items-center gap-2 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn" role="alert" data-testid={`${p}-guided-fusion-blocked`}>
                  <span>FUSION is {fusionLabel(fusionEntry.offer.kind).toLowerCase()}, so nothing would receive this and no command is generated.</span>
                  {fusionEntry.offer.canEnable && fusionControls.enable ? (
                    <Button size="sm" onClick={() => void fusionControls.enable?.()} disabled={fusionControls.busy} data-testid={`${p}-guided-fusion-enable`}>{fusionControls.busy ? 'Starting…' : 'Enable FUSION'}</Button>
                  ) : (
                    <span>{isAdmin ? 'Go back and choose another destination.' : 'An administrator can turn it on.'}</span>
                  )}
                </div>
              )}
            </ReviewStep>
            <StepNav
              testIdPrefix={p}
              onBack={() => setPhase(turningOff ? 'collect' : 'destination')}
              next={{ label: 'Create the command', onClick: () => setPhase('run'), disabled: !canCreate, testId: `${p}-guided-create-command` }}
            />
          </div>
        )}

        {phase === 'run' && (
          <div className="space-y-4" data-testid={`${p}-guided-step-run`}>
            <StepHeading title={`Run it in ${cluster}`}>
              <span data-testid={`${p}-guided-run-summary`}>
                {turningOff
                  ? 'Every signal turned off. Nothing changes until the command is run.'
                  : `${onSignals.length} ${onSignals.length === 1 ? 'signal' : 'signals'} to ${destinationName}. Nothing changes until the command is run.`}
              </span>
            </StepHeading>
            {runSection}
            <StepNav
              testIdPrefix={p}
              onBack={() => setPhase('review')}
              next={turningOff ? (onDone ? { label: 'Done', onClick: onDone, testId: `${p}-guided-done` } : undefined) : { label: 'I have run it: check that data arrives', onClick: () => setPhase('check'), testId: `${p}-guided-check` }}
            />
          </div>
        )}

        {phase === 'check' && (
          <div className="space-y-3" data-testid={`${p}-guided-step-check`}>
            <StepHeading title="Check that data arrives">
              This reads what the agent in {cluster} reports about its collectors, and updates by itself. The first data usually goes out within a minute or two.
            </StepHeading>
            {checkSection}
            <StepNav testIdPrefix={p} onBack={() => setPhase('run')} next={onDone ? { label: 'Done', onClick: onDone, testId: `${p}-guided-done` } : undefined} />
          </div>
        )}
      </div>

      {creatingFor && (
        <CreateOperatorModal
          operators={operators}
          fusion={fusionState}
          initialSourceClusterIds={clusterId ? [clusterId] : []}
          onCreated={(created: CreatedOperator) => {
            reloadLists()
            setPendingPick({ id: created.operator.id, lane: creatingFor.lane })
          }}
          onClose={() => setCreatingFor(null)}
        />
      )}
      {addressFor && <OperatorAddressModal operator={addressFor} onClose={() => setAddressFor(null)} onDone={reloadLists} />}
    </div>
  )
}
