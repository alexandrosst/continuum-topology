import { Layers } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { FusionDot } from '@/components/operators/FusionPanel'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { Button, Field, ICON_SM, InfoTip, Input, SectionLabel, Select, Waiting } from '@/components/ui/primitives'
import { applyDestination, defaultDestination, destinationEndpoint, destinationIsPlain, destinationKey, destinationNeedsCredential, layoutDestinations, type DestinationCatalog, type DestinationCatalogEntry } from '@/lib/destinationCatalog'
import { fusionLabel } from '@/lib/fusionStatus'
import { exportProtocolLabel, type TelemetryInput } from '@/lib/install'
import { operatorLiveness, receiverAuthOf } from '@/lib/operatorHealth'
import type { RegionalOperator } from '@/lib/types'
import DestinationPicker, { type FusionControls, useEnableAndUse } from './DestinationPicker'
import Disclosure from './Disclosure'

const PROTOCOLS = (
  <>
    <option value="grpc">OTLP/gRPC</option>
    <option value="http">OTLP/HTTP</option>
    <option value="zipkin">Zipkin (traces only)</option>
  </>
)

/**
 * "Where to send": ONE list of every place this telemetry can go (see DestinationPicker), the one picked shown in full right under it.
 *
 * Nothing is asked up front: the destination that fits is already picked when the list opens (the one recommended for this cluster, or the
 * only one the organisation has), and the connection details - protocol, credential header and Secret, TLS - wait in a closed section under the
 * picked one, filled in from what it is. Another endpoint and a new regional operator are rows at the end of the list, not separate forms.
 *
 * What was picked is held by the parent as `choice` (see destinationKey), not re-derived from the endpoint text, because a preset's endpoint
 * is a pattern the person edits in place afterwards. With nothing picked and an endpoint already in the draft (editing an install that has
 * one), the step derives the choice from the text so that case still opens on the destination.
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
  fusion,
  onSetUpOperator,
  onRecordAddress,
  bare = false,
}: {
  value: TelemetryInput
  onChange: (v: TelemetryInput) => void
  testIdPrefix: string
  catalog: DestinationCatalog
  /** False while regional operators are still being fetched - the default pick below waits for it,
   *  so it never picks a quick-started backend a moment before the organisation's operator turns up. */
  catalogReady: boolean
  /** The cluster this telemetry is for, when known - lets the list recommend the operator that already receives it. */
  clusterId?: string
  choice: string | null
  onChoose: (key: string | null) => void
  /** What an administrator can do about FUSION from here: switch it on and use it in one step. */
  fusion?: FusionControls
  /** Opens "new regional operator" over this wizard (administrators): the draft is not lost, and the new operator is picked afterwards. */
  onSetUpOperator?: () => void
  /** Opens "where other clusters reach it" for the picked operator, over this wizard. */
  onRecordAddress?: (operator: RegionalOperator) => void
  /** One signal type's destination inside a card (see RoutesStep): no search box, since the lists are short by construction. */
  bare?: boolean
}) {
  const p = `${testIdPrefix}-guided`
  const [autoPicked, setAutoPicked] = useState(false)
  const set = <K extends keyof TelemetryInput>(key: K, v: TelemetryInput[K]) => onChange({ ...value, [key]: v })

  const layout = layoutDestinations(catalog, { clusterId })
  const endpointSet = value.exportEndpoint.trim() !== ''
  // An install that sends to a regional operator or FUSION says which (its intent names it): that is a match even when the address it dials differs.
  // The address an install reports is the one its command dialled: the operator's advertised address when it has one, not the placeholder name.
  const isOp = (e: DestinationCatalogEntry): e is Extract<DestinationCatalogEntry, { kind: 'operator' | 'fusion' }> => e.kind === 'operator' || e.kind === 'fusion'
  const ep = value.exportEndpoint.trim()
  const matched =
    layout.all.find((e) => destinationEndpoint(e) === value.exportEndpoint) ??
    (value.exportOperatorId ? layout.all.find((e) => isOp(e) && e.id === value.exportOperatorId) : undefined) ??
    (ep ? layout.all.find((e) => isOp(e) && e.operator.endpoint === ep) : undefined)
  const activeKey = choice ?? (endpointSet ? (matched ? destinationKey(matched) : 'custom') : null)
  const selected = activeKey && activeKey !== 'custom' ? catalog.entries.find((e) => destinationKey(e) === activeKey) : undefined

  // A destination fits and nothing is picked yet: pick it, once, and say why on its panel. Only ever on a draft with no endpoint of its
  // own - never over a choice. FUSION counts only once it can be sent to (see defaultDestination): an off one is never picked on anyone's behalf.
  const autoDone = useRef(false)
  useEffect(() => {
    if (autoDone.current || !catalogReady) return
    autoDone.current = true
    if (choice !== null || endpointSet) return
    const pick = defaultDestination(layout)
    if (!pick) return
    onChange(applyDestination(value, pick))
    onChoose(destinationKey(pick))
    setAutoPicked(true)
    // Once per mount, after the catalog is ready: deliberately not re-run as `value` changes under it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [catalogReady])

  // A choice made from outside this step (an operator just made in the dialog that opens over it) replaces a pending "Enable and use".
  useEffect(() => {
    if (choice !== null) fusionControls?.cancel?.()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [choice])

  // "Enable and use": FUSION is switched on, and picked as soon as it can be sent to (starting counts).
  const fusionControls = useEnableAndUse(catalog.entries, fusion, (entry) => {
    onChange(applyDestination(value, entry))
    onChoose(destinationKey(entry))
    setAutoPicked(false)
  })

  const isCustom = activeKey === 'custom'
  const isFusion = selected?.kind === 'fusion'
  const isOperator = selected?.kind === 'operator' || isFusion
  const operator = selected?.kind === 'operator' || selected?.kind === 'fusion' ? selected.operator : undefined
  const preset = selected?.kind === 'external-preset' ? selected.preset : undefined
  // A preset's pattern, a detected workload's guessed address and an endpoint of your own are all text to correct.
  const endpointEditable = isCustom || selected?.kind === 'external-preset' || selected?.kind === 'detected'
  const plain = !!selected && destinationIsPlain(selected)
  const unresolved = /<[^>]+>/.test(value.exportEndpoint)
  const name = selected ? selected.label : 'Another OTLP endpoint'
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
    : `${exportProtocolLabel(value.exportProtocol)} · ${secretNamed ? `credential from Secret ${value.exportAuthSecretName.trim()}` : 'no credential'} · ${value.exportInsecure ? (plain ? 'plain in-cluster connection (no TLS)' : 'no TLS (plain connection)') : /^http:\/\//i.test(value.exportEndpoint.trim()) ? 'no TLS (the address is http://)' : 'TLS verified'}`

  const custom = {
    active: isCustom,
    onPick: () => {
      onChange({ ...value, exportEndpoint: isCustom ? value.exportEndpoint : '', exportOperatorId: '' })
      onChoose('custom')
      setAutoPicked(false)
    },
  }

  return (
    <div className="space-y-3" data-testid={`${p}-step-destination`}>
      <DestinationPicker
        catalog={catalog}
        layout={layout}
        activeKey={activeKey}
        onPick={(e) => {
          onChange(applyDestination(value, e))
          onChoose(destinationKey(e))
          setAutoPicked(false)
        }}
        testIdPrefix={p}
        fusion={fusionControls}
        search={!bare}
        custom={custom}
        onSetUpOperator={catalog.canDeployOperator ? onSetUpOperator : undefined}
      />

      {activeKey !== null && (
        <div className="space-y-3" data-testid={`${p}-destination-summary`}>
          <div className="space-y-2 rounded-xl border border-nb-850 bg-nb-925 px-4 py-3">
            <SectionLabel as="p">Sending to</SectionLabel>
            <div className="flex items-center gap-1.5 text-sm font-medium text-nb-200" data-testid={`${p}-destination-name`}>{isFusion && <Layers size={ICON_SM} className="text-nb-500" aria-hidden />}{name}</div>
            {endpointEditable ? (
              <Field label="Endpoint" hint={isCustom ? 'Host and port of an OTLP receiver your cluster can reach.' : undefined}>
                <Input
                  value={value.exportEndpoint}
                  onChange={(e) => onChange({ ...value, exportEndpoint: e.target.value, exportOperatorId: '' })}
                  placeholder={isCustom ? 'otel-gateway.example.com:4317' : undefined}
                  className="font-mono text-xs"
                  data-testid={isCustom ? `${p}-destination-custom-endpoint` : `${p}-destination-endpoint`}
                />
              </Field>
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
                Sends to <span className="font-mono">{operator.address}</span>, the address an administrator recorded for this operator. This page cannot tell that anything answers there: it works only from a cluster that can reach that address, and the check after the command shows whether data arrives.
              </p>
            )}
            {unresolved && (
              <p role="alert" className="text-xs text-warn" data-testid={`${p}-destination-placeholder`}>
                Replace the &lt;…&gt; parts with your own account’s values: no command is printed while one is left.
              </p>
            )}
            {preset?.group === 'self-hosted' && preset.note && <p className="text-xs text-nb-400" data-testid={`${p}-destination-selfhosted-note`}>{preset.note}</p>}
            {selected?.kind === 'detected' && (
              <p className="text-xs text-nb-400" data-testid={`${p}-destination-detected-note`}>
                Found running in this cluster as {selected.detected.service.name} in {selected.detected.service.namespace}. The address is worked out from that name, so check it matches the Service in front of it.
                {selected.detected.kind.note ? ` ${selected.detected.kind.note}` : ''}
              </p>
            )}
            {autoPicked && <p className="text-xs text-nb-400" data-testid={`${p}-destination-auto`}>Picked for you: it is the one that fits these signals. Choose another row to change it.</p>}
            <p className="text-xs text-nb-500" data-testid={`${p}-destination-connection-summary`}>{connSummary}</p>
          </div>

          {/* Remounted per destination, so it opens on its own where a credential is needed (that part cannot be skipped) and stays shut otherwise. */}
          <Disclosure key={activeKey} title="Connection details" defaultOpen={isCustom || (!!selected && destinationNeedsCredential(selected)) || secretNamed} testId={`${p}-destination-connection`}>
            {isOperator ? (
              <div className="space-y-3">
                <p className="text-xs text-nb-400" data-testid={`${p}-destination-operator-note`}>
                  A regional operator takes OTLP/gRPC over mutual TLS, so there is no protocol to set here. The commands that connect this cluster to it are generated on the last step, once you have reviewed everything: administrators only, and each time it issues a fresh client certificate for this cluster (recorded in the audit log). The certificate, its key and the Secret that holds them are part of those commands.
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
                      <Select value={value.exportProtocol} onChange={(e) => set('exportProtocol', e.target.value as TelemetryInput['exportProtocol'])} data-testid={`${testIdPrefix}-export-protocol`}>{PROTOCOLS}</Select>
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
                  <span className="text-nb-300">Send without TLS (plain connection)</span>
                  <InfoTip>Only for an endpoint inside your own cluster or network. Nothing is encrypted: gRPC goes in plain text and HTTP uses http://, so anything on the path can read the data and any credential header. It does not mean &apos;trust a self-signed certificate&apos; - for that, the endpoint&apos;s CA has to be trusted by the collector.</InfoTip>
                </label>
              </>
            )}
          </Disclosure>
        </div>
      )}
    </div>
  )
}
