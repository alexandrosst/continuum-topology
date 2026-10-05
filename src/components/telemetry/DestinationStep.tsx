import clsx from 'clsx'
import { Activity, Check, ChevronDown, ChevronLeft, ExternalLink, FileText, Rocket, Search, Waypoints, type LucideIcon } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { buttonClass } from '@/components/ui/buttonClass'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { Button, Field, ICON_MD, ICON_SM, InfoTip, Input, Select } from '@/components/ui/primitives'
import { applyDestination, destinationEndpoint, destinationIsPlain, destinationKey, destinationNeedsCredential, layoutDestinations, searchDestinations, type DestinationCatalog, type DestinationCatalogEntry } from '@/lib/destinationCatalog'
import { imageRepository } from '@/lib/detectBackends'
import { exportProtocolLabel, type Modality, type TelemetryInput } from '@/lib/install'
import { operatorLiveness, receiverAuthOf } from '@/lib/operatorHealth'

type Mode = 'list' | 'custom' | 'new'

const SIGNAL_ICON: Record<Modality, { icon: LucideIcon; label: string }> = {
  metrics: { icon: Activity, label: 'Metrics' },
  logs: { icon: FileText, label: 'Logs' },
  traces: { icon: Waypoints, label: 'Traces' },
}
const MODALITIES: Modality[] = ['metrics', 'logs', 'traces']

/** The small tag at the right of a row: what sort of destination it is. */
function kindLabel(e: DestinationCatalogEntry): string {
  if (e.kind === 'operator') return 'Regional operator'
  if (e.kind === 'quickstart') return 'Quick-started'
  if (e.kind === 'detected') return 'Detected'
  return e.preset.group === 'self-hosted' ? 'Self-hosted' : 'Cloud'
}

/** A destination's initials in a tile - no logos, which would need licensing and keeping up to date, and
 *  would make the one custom-built row look less finished than the rest. */
function monogram(label: string): string {
  const words = label.replace(/[()·-]/g, ' ').split(/\s+/).filter(Boolean)
  const first = words[0] ?? '?'
  return (words.length > 1 ? first[0] + words[1][0] : first.slice(0, 2)).toUpperCase()
}

/** Which of the three signals a destination takes: lit for what it carries, dimmed for what it does not. */
function SignalChips({ accepts, testId }: { accepts: Modality[]; testId: string }) {
  return (
    <span className="flex shrink-0 items-center gap-1" data-testid={testId} aria-label={`Takes ${accepts.join(', ')}`}>
      {MODALITIES.map((m) => {
        const { icon: Icon, label } = SIGNAL_ICON[m]
        const on = accepts.includes(m)
        return (
          <span key={m} title={on ? `Takes ${label.toLowerCase()}` : `Doesn’t take ${label.toLowerCase()}`} data-on={on} className={clsx('flex size-5 items-center justify-center rounded', on ? 'bg-nb-930 text-nb-300' : 'text-nb-700 opacity-60')}>
            <Icon size={ICON_SM} aria-hidden />
          </span>
        )
      })}
    </span>
  )
}

/** The second line of a destination row - what a person needs to tell it apart from the others without
 *  opening anything. A regional operator's line also says what its opt-in heartbeat last told this server
 *  (Online, or Offline with when it was last seen); one that does not report says nothing about health at
 *  all - never a guessed state. Other destinations carry no health claim: nothing here knows it. */
function entryMeta(e: DestinationCatalogEntry): string {
  if (e.kind === 'operator') {
    const m = e.operator.acceptedModalities
    const base = m && m.length > 0 ? `Accepts ${m.join(', ')}` : 'Accepts any signal'
    const live = operatorLiveness(e.operator)
    return live.kind === 'unreported' ? base : `${base} · ${live.text}`
  }
  if (e.kind === 'quickstart') return `Quick-started here · ${e.backend.modality}`
  if (e.kind === 'detected') return `${e.exportEndpoint} · ${imageRepository(e.detected.service.image)}`
  const proto = e.preset.httpOnly ? 'OTLP/HTTP only' : e.preset.protocol === 'http' ? 'OTLP/HTTP' : 'OTLP/gRPC'
  if (e.preset.group === 'self-hosted') return `${proto} · ${e.preset.endpointPattern}`
  return e.preset.headerName ? `${proto} · needs a credential` : proto
}

/** A destination row: the same bordered-box language as GuidedWizard's PickCard, laid out as a compact list
 *  row, since this step shows more than a handful of them and a grid of tall cards pushes everything else
 *  below the fold. */
function DestinationRow({ entry, selected, badge, onPick, testId }: { entry: DestinationCatalogEntry; selected: boolean; /** Why this one is recommended, when it is. */ badge?: string; onPick: () => void; testId: string }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onPick}
      data-testid={testId}
      className={clsx(
        'flex w-full items-center gap-3 rounded-xl border px-3.5 py-3 text-left transition-colors',
        selected ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
      )}
    >
      <span className={clsx('flex size-8 shrink-0 items-center justify-center rounded-lg', selected ? 'bg-accent/15 text-accent' : 'bg-nb-930 text-nb-500')} aria-hidden>
        <span className="text-[11px] font-semibold tracking-tight">{monogram(entry.label)}</span>
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium text-nb-200">{entry.label}</span>
        <span className="block truncate text-xs text-nb-500">{entryMeta(entry)}</span>
      </span>
      {badge && <span className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-medium text-accent" data-testid={`${testId}-recommended`}>{badge}</span>}
      <SignalChips accepts={entry.accepts} testId={`${testId}-signals`} />
      <span className={clsx('w-24 shrink-0 text-right text-xs', entry.kind === 'detected' ? 'font-medium text-accent' : 'text-nb-500')}>{kindLabel(entry)}</span>
    </button>
  )
}

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
  onDeployBackend,
  adminKindsControl,
  onBack,
  onContinue,
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
  onDeployBackend: () => void
  /** The "which backend kinds this organisation allows" control - administrators only (the parent passes
   *  nothing to anyone else). It lives under "set up a new destination" because it is a setting about what
   *  can be deployed, not about where this telemetry goes. */
  adminKindsControl?: ReactNode
  onBack: () => void
  onContinue: () => void
}) {
  const p = `${testIdPrefix}-guided`
  const [mode, setMode] = useState<Mode>('list')
  const [picking, setPicking] = useState(false)
  const [more, setMore] = useState(false)
  const [unavailableOpen, setUnavailableOpen] = useState(false)
  const [connOpen, setConnOpen] = useState(false)
  const [query, setQuery] = useState('')
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
    }
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
  const openCustom = () => {
    setCustomDraft(activeKey === 'custom' ? value.exportEndpoint : '')
    setMode('custom')
  }
  const useCustom = () => {
    if (!customDraft.trim()) return
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
  const moreCount = layout.all.length - layout.primary.length
  const searching = query.trim() !== ''
  const found = searching ? searchDestinations(catalog, query) : undefined
  const rowFor = (entry: DestinationCatalogEntry) => (
    <DestinationRow key={destinationKey(entry)} entry={entry} selected={destinationKey(entry) === activeKey} badge={layout.recommended.has(destinationKey(entry)) ? 'Already receives this cluster' : undefined} onPick={() => choose(entry)} testId={`${p}-destination-${destinationKey(entry)}`} />
  )
  const unavailableRows = (entries: DestinationCatalogEntry[]) => (
    <ul className="space-y-1.5" data-testid={searching ? `${p}-destination-search-unavailable` : `${p}-destination-unavailable`}>
      {entries.map((e) => (
        <li key={destinationKey(e)} className="flex items-center gap-3 rounded-xl border border-dashed border-nb-850 px-3.5 py-2 text-xs text-nb-500" data-testid={`${p}-destination-${destinationKey(e)}`}>
          <span className="min-w-0 flex-1 truncate text-nb-400">{e.label}</span>
          <span className="text-right">{e.reason}</span>
          <SignalChips accepts={e.accepts} testId={`${p}-destination-${destinationKey(e)}-signals`} />
        </li>
      ))}
    </ul>
  )
  const isOperator = selected?.kind === 'operator'
  const preset = selected?.kind === 'external-preset' ? selected.preset : undefined
  // A preset's pattern and a detected workload's guessed address are both starting points to correct.
  const endpointEditable = !selected || selected.kind === 'external-preset' || selected.kind === 'detected'
  const plain = !!selected && destinationIsPlain(selected)
  const unresolved = /<[^>]+>/.test(value.exportEndpoint)
  const name = selected ? selected.label : 'Custom endpoint'
  // How the chosen operator's receiver authenticates this agent. Only a bearer one (every operator from before
  // certificate-only receivers, and any whose receiver auth is not known) takes a token on top of the
  // certificate; a certificate-only one asks for none, so the field is not offered and a leftover name is ignored.
  const operatorAuth = selected?.kind === 'operator' ? receiverAuthOf(selected.operator) : undefined
  const operatorBearer = isOperator && operatorAuth === 'bearer'
  const secretNamed = value.exportAuthSecretName.trim() !== '' && (!isOperator || operatorBearer)
  const operatorLive = selected?.kind === 'operator' ? operatorLiveness(selected.operator) : undefined

  const connSummary = isOperator
    ? operatorBearer
      ? `OTLP/gRPC · mTLS${secretNamed ? ` · receiver token from Secret ${value.exportAuthSecretName.trim()}` : ''}`
      : 'OTLP/gRPC · mTLS · client certificate only'
    : `${exportProtocolLabel(value.exportProtocol)} · ${secretNamed ? `credential from Secret ${value.exportAuthSecretName.trim()}` : 'no credential'} · ${value.exportInsecure ? (plain ? 'plain in-cluster connection (no TLS)' : 'TLS not verified') : 'TLS verified'}`

  return (
    <div className="space-y-3" data-testid={`${p}-step-destination`}>
      <div>
        <h3 className="text-sm font-medium text-nb-200">Where should this telemetry go?</h3>
        <p className="mt-0.5 text-xs text-nb-500">Only destinations that can carry the signals you turned on are offered.</p>
      </div>

      {mode === 'list' && !showSummary && (
        <div className="space-y-3" data-testid={`${p}-destination-list`}>
          {picking && endpointSet && (
            <button type="button" className="text-xs text-accent hover:underline" onClick={() => setPicking(false)} data-testid={`${p}-destination-keep`}>
              Keep {name}
            </button>
          )}
          <div className="relative">
            <Search size={ICON_SM} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-nb-500" aria-hidden />
            <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search - a name, a signal, “loki”, “logs”…" aria-label="Search destinations" className="pl-9" data-testid={`${p}-destination-search`} />
          </div>

          {found ? (
            <div className="space-y-2" data-testid={`${p}-destination-results`}>
              {found.usable.length > 0 && (
                <div className="space-y-2" role="radiogroup" aria-label="Destination">
                  {found.usable.map(rowFor)}
                </div>
              )}
              {found.unavailable.length > 0 && unavailableRows(found.unavailable)}
              {found.usable.length === 0 && found.unavailable.length === 0 && (
                <div className="rounded-xl border border-dashed border-nb-800 p-4 text-sm text-nb-400" data-testid={`${p}-destination-no-match`}>
                  Nothing matches “{query.trim()}”. Use a custom endpoint below if yours isn’t listed.
                </div>
              )}
            </div>
          ) : layout.all.length > 0 ? (
            <div className="space-y-4" role="radiogroup" aria-label="Destination">
              {layout.sections.map((section) => {
                const rows = more ? section.entries : section.shown
                if (rows.length === 0) return null
                return (
                  <section key={section.group} className="space-y-2" data-testid={`${p}-destination-group-${section.group}`}>
                    <h4 className="text-[11px] font-medium uppercase tracking-wide text-nb-500">{section.title}</h4>
                    {section.group === 'cluster' && <p className="-mt-1 text-xs text-nb-500">Receivers discovery already sees running here. The address is worked out from the workload’s name - check it before you rely on it.</p>}
                    {rows.map(rowFor)}
                  </section>
                )
              })}
            </div>
          ) : (
            <div className="rounded-xl border border-dashed border-nb-800 p-4 text-sm text-nb-400" data-testid={`${p}-destination-empty`}>
              Nothing in this organisation can carry these signals yet. Set up a destination below, or send straight to an endpoint you already run.
            </div>
          )}

          {!searching && (moreCount > 0 || layout.unavailable.length > 0) && (
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
              {moreCount > 0 && (
                <button type="button" className="text-accent hover:underline" onClick={() => setMore((m) => !m)} data-testid={`${p}-destination-more`}>
                  {more ? 'Show fewer' : `Show ${moreCount} more`}
                </button>
              )}
              {layout.unavailable.length > 0 && (
                <button type="button" className="text-nb-500 underline underline-offset-2 hover:text-nb-300" aria-expanded={unavailableOpen} onClick={() => setUnavailableOpen((o) => !o)} data-testid={`${p}-destination-unavailable-toggle`}>
                  {unavailableOpen ? 'Hide the ones that don’t fit' : `${layout.unavailable.length} can’t carry these signals`}
                </button>
              )}
            </div>
          )}
          {!searching && unavailableOpen && unavailableRows(layout.unavailable)}

          <div className="flex flex-wrap items-center gap-2 border-t border-nb-850 pt-3">
            <span className="mr-1 text-xs text-nb-500">Not listed?</span>
            <Button type="button" size="sm" onClick={openCustom} data-testid={`${p}-destination-custom`}>Use a custom endpoint</Button>
            <Button type="button" size="sm" onClick={() => setMode('new')} data-testid={`${p}-destination-new`}>Set up a new destination</Button>
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

      {mode === 'new' && (
        <div className="space-y-3" data-testid={`${p}-destination-new-panel`}>
          <Button variant="ghost" size="sm" onClick={() => setMode('list')} data-testid={`${p}-destination-back-to-list`}>
            <ChevronLeft size={ICON_SM} /> All destinations
          </Button>
          <div className={clsx('grid gap-3', catalog.canDeployOperator && 'sm:grid-cols-2')}>
            <div className="flex flex-col gap-2 rounded-xl border border-nb-850 bg-nb-930 p-4">
              <span className="text-sm font-medium text-nb-200">A new backend</span>
              <span className="flex-1 text-xs text-nb-500">We set up Prometheus, Jaeger, Zipkin or Loki in a cluster you pick, then point this telemetry at it.</span>
              <div>
                <Button type="button" variant="primary" size="sm" onClick={onDeployBackend} data-testid={`${p}-deploy-backend`}>
                  <Rocket size={ICON_SM} /> Choose a backend
                </Button>
              </div>
            </div>
            {catalog.canDeployOperator && (
              <div className="flex flex-col gap-2 rounded-xl border border-nb-850 bg-nb-930 p-4">
                <span className="text-sm font-medium text-nb-200">A new regional operator</span>
                <span className="flex-1 text-xs text-nb-500">A collector that gathers telemetry from several clusters and forwards it on. Set up on the Operators page.</span>
                <div>
                  <Link to="/operators" className={buttonClass('secondary', 'sm')} data-testid={`${p}-deploy-operator`}>
                    <ExternalLink size={ICON_SM} /> Open Operators
                  </Link>
                </div>
              </div>
            )}
          </div>
          {adminKindsControl && (
            <details className="group rounded-lg border border-nb-850" data-testid={`${p}-allowed-kinds`}>
              <summary className="flex cursor-pointer select-none items-center gap-1.5 px-3 py-2 text-xs font-medium text-nb-400 hover:text-nb-300 marker:content-none">
                <ChevronDown size={ICON_SM} className="transition-transform group-open:rotate-180" aria-hidden />
                Which backend kinds can be set up (administrators)
              </summary>
              <div className="border-t border-nb-850 px-3 pt-3">{adminKindsControl}</div>
            </details>
          )}
        </div>
      )}

      {showSummary && (
        <div className="space-y-3" data-testid={`${p}-destination-summary`}>
          <div className="flex items-start gap-3 rounded-xl border border-accent bg-accent-soft p-4 ring-1 ring-accent/40">
            <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-accent text-nb-950" aria-hidden>
              <Check size={ICON_MD} strokeWidth={3} />
            </span>
            <div className="min-w-0 flex-1 space-y-1">
              <div className="text-[11px] font-medium uppercase tracking-wide text-nb-400">Sending to</div>
              <div className="text-sm font-medium text-nb-200" data-testid={`${p}-destination-name`}>{name}</div>
              {endpointEditable ? (
                <Input
                  value={value.exportEndpoint}
                  onChange={(e) => onChange({ ...value, exportEndpoint: e.target.value, exportOperatorId: '' })}
                  aria-label="Endpoint"
                  className="font-mono text-xs"
                  data-testid={`${p}-destination-endpoint`}
                />
              ) : (
                <div className="break-all font-mono text-xs text-nb-400" data-testid={`${p}-destination-endpoint`}>{value.exportEndpoint}</div>
              )}
              {operatorLive && operatorLive.kind !== 'unreported' && selected?.kind === 'operator' && (
                <div data-testid={`${p}-destination-health`}><OperatorHealth operator={selected.operator} testId={`${p}-destination-health-chip`} /></div>
              )}
              {operatorLive?.kind === 'offline' && (
                <p className="text-xs text-nb-400" data-testid={`${p}-destination-offline-note`}>
                  This operator has not reported recently, so agents may not be able to deliver to it until it does. You can still choose it.
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
            <Button size="sm" onClick={() => { setPicking(true); setMore(false) }} data-testid={`${p}-destination-change`}>Change</Button>
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

          <p className="text-xs text-nb-500" data-testid={`${p}-destination-next`}>
            <span className="font-medium text-nb-400">Next:</span> review how this flows, then create the command.{' '}
            {isOperator
              ? catalog.canDeployOperator
                ? 'For this operator the server generates it: that issues this cluster’s client certificate, and nothing is generated until you ask.'
                : 'For this operator an administrator has to generate it: it includes a client certificate only an administrator can issue, so none is shown for you.'
              : 'You run it in the cluster yourself; this page never runs anything.'}
            {secretNamed && !isOperator && ' It also creates the Secret above - set TELEMETRY_EXPORT_TOKEN to your credential first.'}
            {secretNamed && operatorBearer && ' It also creates the receiver token Secret above - set TELEMETRY_EXPORT_TOKEN to Bearer followed by the operator’s token first.'}
          </p>
        </div>
      )}

      {!endpointSet && mode === 'list' && (
        <p className="text-xs text-nb-500" data-testid={`${p}-destination-skip-note`}>
          You can continue without one, but no command is generated until a destination is set.
        </p>
      )}

      <div className="flex items-center gap-2 pt-1">
        <Button variant="ghost" size="sm" onClick={onBack} data-testid={`${testIdPrefix}-guided-back`}>
          <ChevronLeft size={ICON_SM} /> Back
        </Button>
        <Button variant="primary" className="ml-auto" onClick={onContinue} data-testid={`${testIdPrefix}-guided-continue`}>Continue</Button>
      </div>
    </div>
  )
}
