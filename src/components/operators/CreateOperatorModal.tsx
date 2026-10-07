import { ChevronRight } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import OperatorCreated from '@/components/operators/OperatorCreated'
import { type useFusion } from '@/components/operators/FusionPanel'
import { HEARTBEAT_WHAT } from '@/components/operators/HeartbeatCommands'
import { EXPOSURE_OPTIONS } from '@/components/operators/OperatorAddress'
import DestinationPicker, { useEnableAndUse } from '@/components/telemetry/DestinationPicker'
import { TagRows } from '@/components/telemetry/ProcessStep'
import ProcessorEditor from '@/components/telemetry/ProcessorEditor'
import { Button, CheckboxList, ComboField, ErrorBanner, Field, ICON_SM, Input, Modal, Select, WizardSteps } from '@/components/ui/primitives'
import { api, ApiError, type CreatedOperator, type OperatorExposure } from '@/lib/api'
import { buildDestinationCatalog, catalogOperators, destinationKey, fusionForCatalog, layoutDestinations, type DestinationCatalogEntry } from '@/lib/destinationCatalog'
import { EXPORT_PRESETS, unsupportedDestinationNote } from '@/lib/exportPresets'
import { CENTRAL_OPERATOR_ID, fusionSentence, fusionUsable } from '@/lib/fusionStatus'
import { cleanTags, tagProblems, type TagEntry } from '@/lib/install'
import { operatorProcessorProblems } from '@/lib/operatorInstall'
import type { ProcessorEntry } from '@/lib/processorCatalog'
import type { OperatorDestination, RegionalOperator } from '@/lib/types'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

const emptyDestination: OperatorDestination = { kind: 'external', endpoint: '', insecure: false, authHeaderName: '', authSecretName: '', authSecretKey: '' }

/** The destination of an operator that sends to another one: the central operator (FUSION) or a regional one. */
const operatorDestination = (id: string): OperatorDestination => ({ kind: 'operator', endpoint: '', targetOperatorId: id })

/** What is wrong with the destination draft, in words a person can act on. */
function destinationProblems(d: OperatorDestination): string[] {
  if (d.kind === 'operator') return d.targetOperatorId ? [] : ['Choose where it sends']
  return d.endpoint.trim() ? [] : ['Choose where it sends, or give an endpoint']
}

/** The whole create form's shape in one place. */
interface Draft {
  name: string
  sourceClusterIds: string[]
  destination: OperatorDestination
  extraProcessors: ProcessorEntry[]
  /** Name = value tags this operator stamps on everything it forwards (a region, an environment). Fixed at creation: they are
   *  part of the operator's own install. */
  labels: TagEntry[]
  /** Opt in to the heartbeat that lets this server say online/offline. On by default - it is the point of asking - but always
   *  stated next to the box, and sent explicitly either way. */
  heartbeat: boolean
  /** Whether clusters other than the operator's own must reach it; decides the Service type in the install command. */
  exposure: OperatorExposure
}

const STEPS = ['Name and sources', 'Destination', 'Create']
const ALL_SIGNALS = new Set(['metrics', 'logs', 'traces'] as const)

/**
 * "New operator", in three steps: who it collects from, where it sends (picked from a list - FUSION first, then the other operators; any
 * other backend is one disclosure away), and a last screen that says what will be made, with the settings few people change under
 * "Advanced". It is a controlled dialog so the telemetry wizard can open it in place and pick the new operator afterwards: nothing
 * underneath it is lost. After creating, the same dialog turns into the ordered commands to run (OperatorCreated).
 *
 * Source clusters stay required: the server refuses an operator with none, whatever it sends to.
 */
export default function CreateOperatorModal({
  operators,
  fusion,
  initialSourceClusterIds = [],
  onCreated,
  onConnect,
  onClose,
}: {
  /** The organisation's regional operators, for "send to another operator". */
  operators: RegionalOperator[]
  fusion: ReturnType<typeof useFusion>
  initialSourceClusterIds?: string[]
  /** Called as soon as the operator exists (not when this dialog closes), so the caller can reload its lists and pick it. */
  onCreated?: (created: CreatedOperator) => void
  /** Starts the telemetry wizard for a source cluster's agent with this operator chosen. */
  onConnect?: (agentId: string, operatorId: string) => void
  onClose: () => void
}) {
  const conn = useServer((s) => s.conn)
  const { agents, clusters } = useTopology()
  const [step, setStep] = useState(0)
  const [draft, setDraft] = useState<Draft>({ name: '', sourceClusterIds: initialSourceClusterIds, destination: emptyDestination, extraProcessors: [], labels: [], heartbeat: true, exposure: 'cluster' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState<{ result: CreatedOperator; extraProcessors: ProcessorEntry[]; exposure: OperatorExposure } | null>(null)
  const [otherOpen, setOtherOpen] = useState(false)
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))

  // Only clusters a currently-approved agent actually reports, by cluster id - the set the server checks a source cluster against.
  const clusterOptions = clusters
    .filter((cl) => agents.some((a) => a.status === 'approved' && a.clusterId === cl.id))
    .map((cl) => ({ value: cl.id, label: cl.name, hint: cl.region || undefined }))

  // The picker offers FUSION and the other operators; every other backend is the disclosure below it.
  const catalog = useMemo(() => {
    const full = buildDestinationCatalog({
      operators: catalogOperators({ operators, destinations: [], isAdmin: true }),
      enabledModalities: ALL_SIGNALS,
      isAdmin: true,
      fusion: fusionForCatalog({ status: fusion.status, operators, destinations: [], isAdmin: true }),
    })
    return { ...full, entries: full.entries.filter((e) => e.kind === 'fusion' || e.kind === 'operator') }
  }, [operators, fusion.status])
  const layout = layoutDestinations(catalog)
  const target = draft.destination.kind === 'operator' ? catalog.entries.find((e) => e.id === draft.destination.targetOperatorId) : undefined
  const activeKey = target ? destinationKey(target) : null

  const pick = (e: DestinationCatalogEntry) => {
    setOtherOpen(false)
    set({ destination: operatorDestination(e.id) })
  }
  const fusionControls = useEnableAndUse(
    catalog.entries,
    { busy: fusion.busy, error: fusion.error, enable: fusion.status?.available ? async () => void (await fusion.enable()) : undefined },
    pick,
  )

  // FUSION is the one thing worth picking for someone, once it can be sent to: preselected once, never while it is off.
  const preselected = useRef(false)
  const fusionRow = catalog.entries.find((e) => e.kind === 'fusion')
  const fusionReady = fusionRow?.kind === 'fusion' && fusionRow.fusion.usable
  useEffect(() => {
    if (preselected.current || !fusionReady) return
    preselected.current = true
    setDraft((d) => (d.destination.kind === 'external' && d.destination.endpoint === '' ? { ...d, destination: operatorDestination(CENTRAL_OPERATOR_ID) } : d))
  }, [fusionReady])

  const toCentral = draft.destination.kind === 'operator' && draft.destination.targetOperatorId === CENTRAL_OPERATOR_ID
  const note = draft.destination.kind === 'external' ? unsupportedDestinationNote(draft.destination.endpoint) : undefined
  const sourceProblems = [draft.name.trim().length < 2 ? 'A name of at least two characters is required' : ''].filter(Boolean)
  const destProblems = [
    ...destinationProblems(draft.destination),
    toCentral && !fusionUsable(fusion.status) ? 'FUSION is not running, so nothing could receive this' : '',
  ].filter(Boolean)
  const advancedProblems = [...operatorProcessorProblems(draft.extraProcessors), ...tagProblems(draft.labels)]
  const problems = step === 0 ? sourceProblems : step === 1 ? destProblems : [...sourceProblems, ...destProblems, ...advancedProblems]
  const canCreate = sourceProblems.length + destProblems.length + advancedProblems.length === 0 && !busy

  const create = async () => {
    const c = conn()
    if (!c || !canCreate) return
    setBusy(true)
    setError('')
    try {
      const r = await api.createOperator(c, draft.name.trim(), draft.sourceClusterIds, draft.destination, { heartbeat: draft.heartbeat, labels: cleanTags(draft.labels), exposure: draft.exposure })
      setCreated({ result: r, extraProcessors: draft.extraProcessors, exposure: draft.exposure })
      onCreated?.(r)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not create the regional operator.')
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    return (
      <OperatorCreated
        created={created.result}
        extraProcessors={created.extraProcessors}
        exposure={created.exposure}
        fusionKind={fusionSentence(fusion.status).kind}
        onConnect={onConnect ? (agentId) => onConnect(agentId, created.result.operator.id) : undefined}
        onClose={onClose}
      />
    )
  }

  const destinationName = draft.destination.kind === 'operator' ? target?.label ?? draft.destination.targetOperatorId : draft.destination.endpoint
  const last = step === STEPS.length - 1

  return (
    <Modal
      open
      onClose={onClose}
      // While the request is out the credentials are being minted: closing now would lose them (the server shows them once, to this very call).
      dismissible={!busy}
      title="New operator"
      description="It gathers what its source clusters already export and passes it on to one destination. It does not change any cluster's own settings for you."
      width="max-w-2xl"
      footer={
        <>
          {step === 0 ? <Button onClick={onClose} disabled={busy}>Cancel</Button> : <Button onClick={() => setStep(step - 1)} disabled={busy} data-testid="operator-back">Back</Button>}
          {last ? (
            <Button variant="primary" onClick={() => void create()} disabled={!canCreate} data-testid="operator-create">{busy ? 'Creating…' : 'Create operator'}</Button>
          ) : (
            <Button variant="primary" onClick={() => setStep(step + 1)} disabled={problems.length > 0} data-testid="operator-next">Next</Button>
          )}
        </>
      }
    >
      <WizardSteps steps={STEPS} currentIndex={step} testId="operator-steps" />
      {/* No implicit submission. Enter in a field of this form (a label, a processor setting) used to submit all of it through the hidden button
          below, creating the operator and minting its credentials from a stray keypress; the only way to create is the Create button. Enter in the name
          field still moves on to the next step, the one place a person types a single value and expects it to. */}
      <form
        className="space-y-4"
        onSubmit={(e) => e.preventDefault()}
        onKeyDown={(e) => {
          if (e.key !== 'Enter' || !(e.target instanceof HTMLInputElement)) return // a text area keeps its new line; a button its click
          e.preventDefault()
          if (step === 0 && problems.length === 0 && e.target.dataset.testid === 'operator-name') setStep(1)
        }}
      >
        {step === 0 && (
          <div className="space-y-4" data-testid="operator-step-sources">
            <Field label="Name"><Input value={draft.name} onChange={(e) => set({ name: e.target.value })} maxLength={80} autoFocus data-testid="operator-name" /></Field>
            <Field label="Source clusters" hint="Optional. Clusters whose already-exported telemetry this operator gathers; you can point more clusters at it later. Only clusters with a connected agent are listed.">
              <CheckboxList options={clusterOptions} value={draft.sourceClusterIds} onChange={(v) => set({ sourceClusterIds: v })} emptyLabel="No cluster has an approved agent yet - approve one on the Agents page first." />
            </Field>
          </div>
        )}

        {step === 1 && (
          <div className="space-y-3" data-testid="operator-step-destination">
            <p className="text-xs text-nb-500">Where should this operator send what it gathers?</p>
            <DestinationPicker
              catalog={catalog}
              layout={layout}
              activeKey={activeKey}
              onPick={pick}
              testIdPrefix="operator"
              fusion={fusionControls}
              search={false}
              emptyText="There is no other operator to send to. Use another backend below."
            />
            <details className="group rounded-lg border border-nb-850" open={otherOpen} onToggle={(e) => setOtherOpen(e.currentTarget.open)} data-testid="operator-other-backend">
              <summary className="flex cursor-pointer select-none items-center gap-1.5 px-3 py-2.5 text-sm text-nb-300 marker:content-none">
                <ChevronRight size={ICON_SM} className="text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
                Not listed? Another backend
              </summary>
              <div className="grid gap-3 border-t border-nb-850 p-3 sm:grid-cols-2">
                <Field label="Send aggregated telemetry to" hint="Pick a known backend to fill in its endpoint pattern and credential header, or type your own." className="sm:col-span-2">
                  <ComboField
                    value={draft.destination.kind === 'external' ? draft.destination.endpoint : ''}
                    onChange={(v) => {
                      const preset = EXPORT_PRESETS.find((p) => p.endpointPattern === v)
                      const base = draft.destination.kind === 'external' ? draft.destination : emptyDestination
                      fusionControls?.cancel?.() // typed by hand: a pending "Enable and use" must not replace it later
                      set({ destination: { ...base, endpoint: v, authHeaderName: preset?.headerName ? preset.headerName : base.authHeaderName } })
                    }}
                    placeholder="otel-gateway.example.com:4317"
                    options={EXPORT_PRESETS.map((p) => ({ value: p.endpointPattern, label: p.label }))}
                  />
                </Field>
                {note && <p role="alert" className="text-xs text-warn sm:col-span-2">{note}</p>}
                <label className="flex cursor-pointer items-center gap-2 text-sm sm:col-span-2">
                  <input type="checkbox" className="size-4 accent-[var(--color-accent)]" checked={!!draft.destination.insecure} onChange={(e) => set({ destination: { ...draft.destination, kind: 'external', insecure: e.target.checked } })} data-testid="operator-export-insecure" />
                  <span className="text-nb-300">Send without TLS (plain connection)</span>
                </label>
                <Field label="Credential header" hint="Which header the destination expects its credential in.">
                  <Input value={draft.destination.authHeaderName ?? ''} onChange={(e) => set({ destination: { ...draft.destination, kind: 'external', authHeaderName: e.target.value } })} placeholder="Authorization" />
                </Field>
                <Field label="Secret holding it" hint="A Secret you create in the release namespace, outside this chart - never the credential value itself.">
                  <Input value={draft.destination.authSecretName ?? ''} onChange={(e) => set({ destination: { ...draft.destination, kind: 'external', authSecretName: e.target.value } })} placeholder="telemetry-export-token" />
                </Field>
              </div>
            </details>
            {toCentral && (
              <p className="text-xs leading-relaxed text-nb-500" data-testid="operator-central">
                FUSION is the one door into this server&apos;s own stores: Prometheus for metrics, Loki for logs, Tempo for traces. This operator sends to it over mutual TLS; the client certificate is issued when you create it and shown with the commands.
              </p>
            )}
          </div>
        )}

        {step === 2 && (
          <div className="space-y-4" data-testid="operator-step-create">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm" data-testid="operator-summary">
              <dt className="text-nb-500">Name</dt><dd className="text-nb-200">{draft.name.trim()}</dd>
              <dt className="text-nb-500">Collects from</dt><dd className="text-nb-200">{draft.sourceClusterIds.length > 0 ? draft.sourceClusterIds.map((id) => clusters.find((c) => c.id === id)?.name ?? id).join(', ') : <span className="text-nb-500">No cluster yet - you can point clusters at it later</span>}</dd>
              <dt className="text-nb-500">Sends to</dt><dd className="break-all text-nb-200">{destinationName}</dd>
            </dl>
            <p className="rounded-md border border-nb-850 bg-nb-930 px-3 py-2 text-xs leading-relaxed text-nb-400" data-testid="operator-create-no-rbac-note">
              This needs no Kubernetes API access at all - no RBAC is applied and no ServiceAccount token is requested. Where it sends, how its receiver
              authenticates agents (normally a client certificate issued by this server) and the health reporting below are the complete list of what it is granted.
            </p>
            <details className="group rounded-lg border border-nb-850" data-testid="operator-advanced">
              <summary className="flex cursor-pointer select-none items-center gap-1.5 px-3 py-2.5 text-sm text-nb-300 marker:content-none">
                <ChevronRight size={ICON_SM} className="text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
                Advanced
              </summary>
              <div className="space-y-4 border-t border-nb-850 p-3">
                <Field label="Reachable from other clusters" hint={EXPOSURE_OPTIONS.find((o) => o.id === draft.exposure)?.hint}>
                  <Select value={draft.exposure} onChange={(e) => set({ exposure: e.target.value as OperatorExposure })} data-testid="operator-exposure">
                    {EXPOSURE_OPTIONS.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
                  </Select>
                </Field>
                <label className="flex cursor-pointer items-start gap-2 text-sm">
                  <input type="checkbox" className="mt-0.5 size-4 accent-[var(--color-accent)]" checked={draft.heartbeat} onChange={(e) => set({ heartbeat: e.target.checked })} data-testid="operator-heartbeat" />
                  <span>
                    <span className="text-nb-300">Report this operator&apos;s health to this server</span>
                    <span className="mt-0.5 block text-xs leading-relaxed text-nb-500" data-testid="operator-heartbeat-explain">
                      Sends {HEARTBEAT_WHAT} It is the only thing this operator ever sends to this server, so this page can show online or offline. You can also turn it on later.
                    </span>
                  </span>
                </label>
                <div>
                  <div className="mb-1 text-xs font-medium uppercase tracking-wide text-nb-500">Labels</div>
                  <p className="mb-2 text-xs leading-relaxed text-nb-500" data-testid="operator-labels-explain">
                    Added to every metric, log and trace this operator forwards, so that data can be told apart downstream (a region, an environment). They cannot be changed afterwards: they live in the operator&apos;s install.
                  </p>
                  <TagRows tags={draft.labels} onChange={(labels) => set({ labels })} testIdPrefix="operator-label" noun="label" />
                </div>
                <div>
                  <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">Extra processors</div>
                  <ProcessorEditor entries={draft.extraProcessors} onChange={(extraProcessors) => set({ extraProcessors })} testIdPrefix="operator" />
                </div>
              </div>
            </details>
          </div>
        )}

        {/* What is still missing, said quietly: it is the state of an empty form, not an error, so it is neither red nor announced as an alert. */}
        {problems.length > 0 && <p className="text-xs text-nb-500" data-testid="operator-problems">{problems.join('. ')}.</p>}
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  )
}
