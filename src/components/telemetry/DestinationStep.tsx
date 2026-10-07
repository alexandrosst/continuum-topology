import { Check, ChevronDown, ChevronLeft, Layers } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { FusionDot } from '@/components/operators/FusionPanel'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { Button, Field, ICON_MD, ICON_SM, InfoTip, Input, Select, Waiting } from '@/components/ui/primitives'
import { applyDestination, destinationEndpoint, destinationIsPlain, destinationKey, destinationNeedsCredential, layoutDestinations, type DestinationCatalog, type DestinationCatalogEntry } from '@/lib/destinationCatalog'
import { fusionLabel } from '@/lib/fusionStatus'
import { exportProtocolLabel, type TelemetryInput } from '@/lib/install'
import { operatorLiveness, receiverAuthOf } from '@/lib/operatorHealth'
import type { RegionalOperator } from '@/lib/types'
import DestinationPicker, { type FusionControls, useEnableAndUse } from './DestinationPicker'

type Mode = 'list' | 'custom'

/**
 * The guided wizard's Destination step: one decision up front, everything else behind a disclosure.
 *
 * It opens as a short list of the destinations this organisation already has (regional operators and
 * backends it quick-started), with the built-in external presets behind "show more". Picking one collapses
 * the list into a "Sending to" summary with a Change button, and the connection details (protocol, credential
 * header and Secret, TLS) wait in a closed section underneath, pre-filled from the preset. A custom endpoint
 * and "set up a new destination" are one quiet button each below the list, not permanent fields.
 *
 * What was picked is held by the parent as `choice` (see destinationKey), not re-derived from the endpoint
 * text, because a preset's endpoint is a pattern the person edits in place afterwards. With nothing picked
 * and an endpoint already in the draft (editing an install that has one), the step derives the choice from
 * the text so that case still opens on the summary instead of an empty list.
 */
export default function DestinationStep({
  value,
  onChange,
  testIdPrefix,
  catalog,
  catalogReady,
  clusterId,
  choice,
  onChoose,
  onBack,
  onContinue,
  fusion,
  onSetUpOperator,
  onRecordAddress,
  bare = false,
  heading = true,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  catalog: DestinationCatalog
  /** False while regional operators are still being fetched - the lone-match auto-pick below waits for it,
   *  so it never picks a quick-started backend a moment before the organisation's operator turns up. */
  catalogReady: boolean
  /** The cluster this telemetry is for, when known - lets the list recommend the operator that already receives it. */
  clusterId?: string
  choice: string | null
  onChoose: (key: string | null) => void
  onBack: () => void
  onContinue: () => void
  /** What an administrator can do about FUSION from here: switch it on and use it in one step. */
  fusion?: FusionControls
  /** Opens "new regional operator" over this wizard (administrators): the draft is not lost, and the new operator is picked afterwards. */
  onSetUpOperator?: () => void
  /** Opens "where other clusters reach it" for the picked operator, over this wizard. */
  onRecordAddress?: (operator: RegionalOperator) => void
  /** One signal type's destination inside a card (see RoutesStep): no heading and no Back/Continue of its
   *  own, and no word about what happens next - the step around it has those. */
  bare?: boolean
  /** Whether the step shows its own "Where should this telemetry go?" - off when the wizard already has. */
  heading?: boolean
}) {
  const p = `${testIdPrefix}-guided`
  const [mode, setMode] = useState<Mode>('list')
  const [picking, setPicking] = useState(false)
  const [connOpen, setConnOpen] = useState(false)
  const [customDraft, setCustomDraft] = useState('')
  const [autoPicked, setAutoPicked] = useState(false)
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })

  const layout = layoutDestinations(catalog, { clusterId })
  const endpointSet = value.exportEndpoint.trim() !== ''
  const matched = layout.all.find((e) => destinationEndpoint(e) === value.exportEndpoint)
  const activeKey = choice ?? (endpointSet ? (matched ? destinationKey(matched) : 'custom') : null)
  const selected = activeKey && activeKey !== 'custom' ? catalog.entries.find((e) => destinationKey(e) === activeKey) : undefined

  // Exactly one destination of this organisation's own fits the signals and nothing is picked yet: pick it,
  // once, and say so on the summary. Only ever on a draft with no endpoint of its own - never over a choice.
  // FUSION counts only once it can be sent to (see layoutDestinations): an off one is never picked on anyone's behalf.
  const autoDone = useRef(false)
  useEffect(() => {
    if (autoDone.current || !catalogReady) return
    autoDone.current = true
    // Only the organisation's own (a detected address is a guess from a workload's name), and only when it is
    // the sole thing on offer at all.
    if (choice !== null || endpointSet || layout.known.length !== 1 || layout.own.length !== 1) return
    onChange(applyDestination(value, layout.own[0]))
    onChoose(destinationKey(layout.own[0]))
    setAutoPicked(true)
    // Once per mount, after the catalog is ready: deliberately not re-run as `value` changes under it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [catalogReady])

  // A choice made from outside this step (a backend just set up in the wizard that opens over it) always
  // lands on the summary, whatever panel was open underneath.
  useEffect(() => {
    if (choice !== null) {
      setMode('list')
      setPicking(false)
      fusionControls?.cancel?.() // a choice made by hand: a pending "Enable and use" must not replace it
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [choice])

  const choose = (e: DestinationCatalogEntry) => {
    onChange(applyDestination(value, e))
    onChoose(destinationKey(e))
    setPicking(false)
    setMode('list')
    setAutoPicked(false)
    // A destination that needs a credential opens its connection details straight away - that is the part
    // that can't be skipped - and every other one leaves them closed.
    setConnOpen(destinationNeedsCredential(e))
  }

  // "Enable and use": FUSION is switched on, and picked as soon as it can be sent to (starting counts).
  const fusionControls = useEnableAndUse(catalog.entries, fusion, (entry) => {
    onChange(applyDestination(value, entry))
    onChoose(destinationKey(entry))
    setPicking(false)
    setMode('list')
    setAutoPicked(false)
  })

  const openCustom = () => {
    setCustomDraft(activeKey === 'custom' ? value.exportEndpoint : '')
    setMode('custom')
  }
  const useCustom = () => {
    if (!customDraft.trim()) return
    fusionControls?.cancel?.()
    onChange({ ...value, exportEndpoint: customDraft.trim(), exportOperatorId: '' })
    onChoose('custom')
    setPicking(false)
    setMode('list')
    setAutoPicked(false)
    setConnOpen(false)
  }

  // Driven by the choice, not by the endpoint text: clearing the endpoint field to retype it must not
  // collapse the summary the person is editing back into the list.
  const showSummary = mode === 'list' && activeKey !== null && !picking
  const isFusion = selected?.kind === 'fusion'
  const isOperator = selected?.kind === 'operator' || isFusion
  const operator = selected?.kind === 'operator' || selected?.kind === 'fusion' ? selected.operator : undefined
  const preset = selected?.kind === 'external-preset' ? selected.preset : undefined
  // A preset's pattern and a detected workload's guessed address are both starting points to correct.
  const endpointEditable = !selected || selected.kind === 'external-preset' || selected.kind === 'detected'
  const plain = !!selected && destinationIsPlain(selected)
  const unresolved = /<[^>]+>/.test(value.exportEndpoint)
  const name = selected ? selected.label : 'Custom endpoint'
  // How the chosen operator's receiver authenticates this agent. Only a bearer one (every operator from before
  // certificate-only receivers, and any whose receiver auth is not known) takes a token on top of the
  // certificate; a certificate-only one asks for none, so the field is not offered and a leftover name is ignored.
  const operatorAuth = operator ? receiverAuthOf(operator) : undefined
  const operatorBearer = isOperator && operatorAuth === 'bearer' && !isFusion
  const secretNamed = value.exportAuthSecretName.trim() !== '' && (!isOperator || operatorBearer)
  const operatorLive = operator && !isFusion ? operatorLiveness(operator) : undefined
  const fusionOffer = selected?.kind === 'fusion' ? selected.fusion : undefined
  // Reachable from other clusters only with a recorded address (the central operator: once exposed). Otherwise the commands dial the
  // in-cluster name, which resolves in the operator's own cluster alone - said on the card, not behind a disclosure, since it decides
  // whether this works at all.
  const reachable = !!operator && operator.reachableFromOtherClusters === true && !!operator.address
  const dialled = operator?.endpoint

  const connSummary = isOperator
    ? operatorBearer
      ? `OTLP/gRPC · mTLS${secretNamed ? ` · receiver token from Secret ${value.exportAuthSecretName.trim()}` : ''}`
      : 'OTLP/gRPC · mTLS · client certificate only'
    : `${exportProtocolLabel(value.exportProtocol)} · ${secretNamed ? `credential from Secret ${value.exportAuthSecretName.trim()}` : 'no credential'} · ${value.exportInsecure ? (plain ? 'plain in-cluster connection (no TLS)' : 'TLS not verified') : 'TLS verified'}`

  return (
    <div className="space-y-3" data-testid={`${p}-step-destination`}>
      {!bare && heading && (
        <div>
          <h3 className="text-sm font-medium text-nb-200">Where should this telemetry go?</h3>
          <p className="mt-0.5 text-xs text-nb-500">Only destinations that can carry the signals you turned on are offered.</p>
        </div>
      )}

      {mode === 'list' && !showSummary && (
        <div className="space-y-3">
          {picking && endpointSet && (
            <button type="button" className="text-xs text-accent hover:underline" onClick={() => { fusionControls?.cancel?.(); setPicking(false) }} data-testid={`${p}-destination-keep`}>
              Keep {name}
            </button>
          )}
          <DestinationPicker catalog={catalog} layout={layout} activeKey={activeKey} onPick={choose} testIdPrefix={p} fusion={fusionControls} />

          <div className="flex flex-wrap items-center gap-2 border-t border-nb-850 pt-3">
            <span className="mr-1 text-xs text-nb-500">Not listed?</span>
            <Button type="button" size="sm" onClick={openCustom} data-testid={`${p}-destination-custom`}>Use a custom endpoint</Button>
            {catalog.canDeployOperator && onSetUpOperator && (
              <Button type="button" size="sm" onClick={onSetUpOperator} data-testid={`${p}-deploy-operator`}>Set up a regional operator</Button>
            )}
          </div>
        </div>
      )}

      {mode === 'custom' && (
        <div className="space-y-3" data-testid={`${p}-destination-custom-panel`}>
          <Button variant="ghost" size="sm" onClick={() => setMode('list')} data-testid={`${p}-destination-back-to-list`}>
            <ChevronLeft size={ICON_SM} /> All destinations
          </Button>
          <Field label="Endpoint" hint="Host and port of an OTLP receiver your agents can reach - an existing collector gateway or observability backend.">
            <Input value={customDraft} onChange={(e) => setCustomDraft(e.target.value)} placeholder="otel-gateway.example.com:4317" className="font-mono" data-testid={`${p}-destination-custom-endpoint`} />
          </Field>
          <Field label="Protocol">
            <Select value={value.exportProtocol} onChange={(e) => set('exportProtocol', e.target.value as TelemetryInput['exportProtocol'])} data-testid={`${testIdPrefix}-export-protocol`}>
              <option value="grpc">OTLP/gRPC</option>
              <option value="http">OTLP/HTTP</option>
              <option value="zipkin">Zipkin (traces only)</option>
            </Select>
          </Field>
          <Button variant="primary" onClick={useCustom} disabled={!customDraft.trim()} data-testid={`${p}-destination-custom-use`}>Use this endpoint</Button>
        </div>
      )}

      {showSummary && (
        <div className="space-y-3" data-testid={`${p}-destination-summary`}>
          <div className="flex flex-wrap items-start gap-3 rounded-xl border border-accent bg-accent-soft p-4 ring-1 ring-accent/40 sm:flex-nowrap">
            <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-accent text-nb-950" aria-hidden>
              <Check size={ICON_MD} strokeWidth={3} />
            </span>
            <div className="min-w-0 flex-1 basis-44 space-y-1">
              <div className="text-[11px] font-medium uppercase tracking-wide text-nb-400">Sending to</div>
              <div className="flex items-center gap-1.5 text-sm font-medium text-nb-200" data-testid={`${p}-destination-name`}>{isFusion && <Layers size={ICON_SM} className="text-nb-500" aria-hidden />}{name}</div>
              {endpointEditable ? (
                <Input
                  value={value.exportEndpoint}
                  onChange={(e) => onChange({ ...value, exportEndpoint: e.target.value, exportOperatorId: '' })}
                  aria-label="Endpoint"
                  className="font-mono text-xs"
                  data-testid={`${p}-destination-endpoint`}
                />
              ) : (
                <div className="break-all font-mono text-xs text-nb-400" data-testid={`${p}-destination-endpoint`}>{dialled ? dialled : value.exportEndpoint}</div>
              )}
              {fusionOffer && (
                <div className="text-xs text-nb-400" data-testid={`${p}-destination-fusion`} data-fusion={fusionOffer.kind}>
                  {fusionOffer.kind === 'starting' ? (
                    <>
                      <Waiting testId={`${p}-destination-fusion-waiting`}>
                        Starting{fusionOffer.parts ? ` - ${fusionOffer.parts.up} of ${fusionOffer.parts.wanted} parts are up` : ''}.
                      </Waiting>
                      <span className="mt-1 block" data-testid={`${p}-destination-fusion-safe`}>The commands are safe to run now: collectors keep what they cannot deliver yet and send it once FUSION is up.</span>
                    </>
                  ) : fusionOffer.usable ? (
                    <span className="inline-flex items-center gap-1.5"><FusionDot kind={fusionOffer.kind} /> {fusionLabel(fusionOffer.kind)}</span>
                  ) : (
                    <div role="alert" className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-warn" data-testid={`${p}-destination-fusion-blocked`}>
                      <p>
                        {fusionOffer.kind === 'off' ? 'FUSION is off, so nothing receives this yet.' : fusionOffer.kind === 'attention' ? 'FUSION needs attention, so nothing can be sent to it yet.' : fusionOffer.message ?? 'FUSION cannot be used from here.'}
                        {fusionOffer.canEnable ? '' : fusionOffer.kind === 'off' ? ' An administrator can turn it on.' : ''}
                      </p>
                      {fusionOffer.canEnable && fusionControls?.enable && (
                        <Button size="sm" className="mt-2" onClick={() => void fusionControls.enable?.()} disabled={fusionControls.busy} data-testid={`${p}-destination-fusion-enable`}>{fusionControls.busy ? 'Starting…' : 'Enable FUSION'}</Button>
                      )}
                    </div>
                  )}
                  {fusion?.error && <p role="alert" className="mt-1 text-bad">{fusion.error}</p>}
                </div>
              )}
              {operatorLive && operatorLive.kind !== 'unreported' && operator && (
                <div data-testid={`${p}-destination-health`}><OperatorHealth operator={operator} testId={`${p}-destination-health-chip`} /></div>
              )}
              {operatorLive?.kind === 'offline' && (
                <p className="text-xs text-nb-400" data-testid={`${p}-destination-offline-note`}>
                  This operator has not reported recently, so agents may not be able to deliver to it until it does. You can still choose it.
                </p>
              )}
              {/* Not while FUSION cannot be sent to at all: the box above already says that, and a second amber box about where it lives only buries it. */}
              {operator && !reachable && !(fusionOffer && !fusionOffer.usable) && (
                <p role="note" className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn" data-testid={`${p}-destination-operator-address`}>
                  {isFusion ? 'FUSION' : 'This operator'} is reachable inside its own cluster only{dialled ? <> (<span className="font-mono">{dialled}</span>)</> : ''}. If this cluster is a different one, record where it is reachable first, and the commands will use that address.
                  {onRecordAddress && !isFusion && catalog.canDeployOperator && (
                    <Button size="sm" className="ml-2 align-middle" onClick={() => onRecordAddress(operator)} data-testid={`${p}-destination-record-address`}>Record an address</Button>
                  )}
                </p>
              )}
              {operator && reachable && (
                <p className="text-xs text-nb-400" data-testid={`${p}-destination-operator-address`}>
                  Reached at <span className="font-mono">{operator.address}</span>, the address recorded for this operator, so this works from any cluster that can reach it.
                </p>
              )}
              {unresolved && (
                <p role="alert" className="text-xs text-warn" data-testid={`${p}-destination-placeholder`}>
                  Replace the &lt;…&gt; parts with your own account’s values.
                </p>
              )}
              {preset?.group === 'self-hosted' && preset.note && <p className="text-xs text-nb-400" data-testid={`${p}-destination-selfhosted-note`}>{preset.note}</p>}
              {selected?.kind === 'detected' && (
                <p className="text-xs text-nb-400" data-testid={`${p}-destination-detected-note`}>
                  Found running in this cluster as {selected.detected.service.name} in {selected.detected.service.namespace}. The address is worked out from that name, so check it matches the Service in front of it.
                  {selected.detected.kind.note ? ` ${selected.detected.kind.note}` : ''}
                </p>
              )}
              {autoPicked && <p className="text-xs text-nb-400" data-testid={`${p}-destination-auto`}>The only destination in your organisation that fits these signals, so it was picked for you.</p>}
            </div>
            <Button size="sm" onClick={() => setPicking(true)} data-testid={`${p}-destination-change`}>Change</Button>
          </div>

          <details className="group rounded-lg border border-nb-850" open={connOpen} onToggle={(e) => setConnOpen(e.currentTarget.open)} data-testid={`${p}-destination-connection`}>
            <summary className="flex cursor-pointer select-none items-center justify-between gap-3 px-3 py-2.5 marker:content-none">
              <span>
                <span className="block text-xs font-medium text-nb-300">Connection details</span>
                <span className="block text-xs text-nb-500">{connSummary}</span>
              </span>
              <ChevronDown size={ICON_MD} className="shrink-0 text-nb-500 transition-transform group-open:rotate-180" aria-hidden />
            </summary>
            <div className="space-y-3 border-t border-nb-850 p-3">
              {isOperator ? (
                <div className="space-y-3">
                  <p className="text-xs text-nb-400" data-testid={`${p}-destination-operator-note`}>
                    A regional operator takes OTLP/gRPC over mutual TLS, so there is no protocol to set here. The commands that connect this cluster to it are generated on the wizard’s last step, once you have reviewed everything: administrators only, and each time it issues a fresh client certificate for this cluster (recorded in the audit log). The certificate, its key and the Secret that holds them are part of those commands.
                  </p>
                  {operatorBearer ? (
                    <Field label="Receiver token Secret (optional)" hint="This operator was created with a receiver bearer token, which it checks on top of the certificate. Name the Secret that will hold it; the generated commands create it from TELEMETRY_EXPORT_TOKEN, which you set to Bearer followed by the token. The token itself never goes through this page.">
                      <Input value={value.exportAuthSecretName} onChange={(e) => set('exportAuthSecretName', e.target.value)} placeholder="operator-receiver-token" className="font-mono" data-testid={`${testIdPrefix}-export-auth-secret`} />
                    </Field>
                  ) : (
                    <p className="text-xs text-nb-400" data-testid={`${p}-destination-operator-mtls`}>
                      This operator&apos;s receiver authenticates this cluster by the client certificate the generated commands install - there is no receiver token to name or supply.
                    </p>
                  )}
                </div>
              ) : (
                <>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label="Protocol" className="sm:col-span-2">
                      {preset?.httpOnly ? (
                        <p className="text-sm text-nb-300">OTLP/HTTP <span className="text-nb-500">· {preset.label} doesn’t accept gRPC</span></p>
                      ) : (
                        <Select value={value.exportProtocol} onChange={(e) => set('exportProtocol', e.target.value as TelemetryInput['exportProtocol'])} data-testid={`${testIdPrefix}-export-protocol`}>
                          <option value="grpc">OTLP/gRPC</option>
                          <option value="http">OTLP/HTTP</option>
                          <option value="zipkin">Zipkin (traces only)</option>
                        </Select>
                      )}
                    </Field>
                    <Field label="Credential header" hint={preset?.headerName ? `Filled in for ${preset.label}.` : 'Which header the destination expects its credential in. Leave empty if it needs none.'}>
                      <Input value={value.exportAuthHeaderName} onChange={(e) => set('exportAuthHeaderName', e.target.value)} placeholder="Authorization" className="font-mono" data-testid={`${testIdPrefix}-export-auth-header`} />
                    </Field>
                    <Field label="Kubernetes Secret holding it" hint="Just its name. The credential itself never goes through this page.">
                      <Input value={value.exportAuthSecretName} onChange={(e) => set('exportAuthSecretName', e.target.value)} placeholder="telemetry-export-token" className="font-mono" data-testid={`${testIdPrefix}-export-auth-secret`} />
                    </Field>
                  </div>
                  {preset?.note && <p className="text-xs text-nb-500" data-testid={`${p}-destination-preset-note`}>{preset.note}</p>}
                  <label className="flex cursor-pointer items-center gap-2 text-sm">
                    <input type="checkbox" className="size-4 accent-[var(--color-accent)]" checked={value.exportInsecure} onChange={(e) => set('exportInsecure', e.target.checked)} data-testid={`${testIdPrefix}-export-insecure`} />
                    <span className="text-nb-300">Skip TLS verification for this endpoint</span>
                    <InfoTip>Only for a self-signed or internal endpoint you already trust by other means - the connection is still encrypted, its certificate is just not checked.</InfoTip>
                  </label>
                </>
              )}
            </div>
          </details>

          {!bare && <p className="text-xs text-nb-500" data-testid={`${p}-destination-next`}>
            <span className="font-medium text-nb-400">Next:</span> review how this flows, then create the command.{' '}
            {isOperator
              ? catalog.canDeployOperator
                ? 'For this operator the server generates it: that issues this cluster’s client certificate, and nothing is generated until you ask.'
                : 'For this operator an administrator has to generate it: it includes a client certificate only an administrator can issue, so none is shown for you.'
              : 'You run it in the cluster yourself; this page never runs anything.'}
            {secretNamed && !isOperator && ' It also creates the Secret above - set TELEMETRY_EXPORT_TOKEN to your credential first.'}
            {secretNamed && operatorBearer && ' It also creates the receiver token Secret above - set TELEMETRY_EXPORT_TOKEN to Bearer followed by the operator’s token first.'}
          </p>}
        </div>
      )}

      {!bare && !endpointSet && mode === 'list' && (
        <p className="text-xs text-nb-500" data-testid={`${p}-destination-skip-note`}>
          You can continue without one, but no command is generated until a destination is set.
        </p>
      )}

      {!bare && (
        <div className="flex items-center gap-2 pt-1">
          <Button variant="ghost" size="sm" onClick={onBack} data-testid={`${testIdPrefix}-guided-back`}>
            <ChevronLeft size={ICON_SM} /> Back
          </Button>
          <Button variant="primary" className="ml-auto" onClick={onContinue} disabled={!!fusionOffer && !fusionOffer.usable} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
        </div>
      )}
    </div>
  )
}
