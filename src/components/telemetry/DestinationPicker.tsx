import clsx from 'clsx'
import { Activity, FileText, Plus, Search, Waypoints, type LucideIcon } from 'lucide-react'
import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { FusionDot } from '@/components/operators/FusionPanel'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { Button, ICON_SM, Input } from '@/components/ui/primitives'
import { destinationGroup, GROUP_TITLE, destinationKey, searchDestinations, type DestinationCatalog, type DestinationCatalogEntry, type DestinationGroup, type layoutDestinations } from '@/lib/destinationCatalog'
import { imageRepository } from '@/lib/detectBackends'
import { fusionLabel } from '@/lib/fusionStatus'
import type { Modality } from '@/lib/install'
import { operatorLiveness } from '@/lib/operatorHealth'

const SIGNAL_ICON: Record<Modality, { icon: LucideIcon; label: string }> = {
  metrics: { icon: Activity, label: 'Metrics' },
  logs: { icon: FileText, label: 'Logs' },
  traces: { icon: Waypoints, label: 'Traces' },
}
const MODALITIES: Modality[] = ['metrics', 'logs', 'traces']

/** Which of the three signals a destination takes: lit for what it carries, dimmed for what it does not. */
export function SignalChips({ accepts, testId, className }: { accepts: Modality[]; testId: string; className?: string }) {
  return (
    <span className={clsx('flex shrink-0 items-center gap-1', className)} data-testid={testId} aria-label={`Takes ${accepts.join(', ')}`}>
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

/** The second line of a destination row - what a person needs to tell it apart from the others without opening anything. A regional
 *  operator's line says which signals it accepts and, as a dot and words, what its heartbeat last told this server; one that does not
 *  report says nothing about health at all - never a guessed state. FUSION says its state in the product's one vocabulary. Other
 *  destinations carry no health claim: nothing here knows it. */
function EntryMeta({ entry }: { entry: DestinationCatalogEntry }) {
  if (entry.kind === 'fusion') {
    const f = entry.fusion
    return (
      <span className="flex flex-wrap items-center gap-x-1.5 sm:flex-nowrap">
        <FusionDot kind={f.kind} />
        <span className="shrink-0">{f.parts ? `${fusionLabel(f.kind)} - ${f.parts.up} of ${f.parts.wanted}` : fusionLabel(f.kind)}</span>
        <span className="text-nb-600 sm:truncate">· metrics, logs and traces saved on this server</span>
      </span>
    )
  }
  if (entry.kind === 'operator') {
    const m = entry.operator.acceptedModalities
    const base = m && m.length > 0 ? `Accepts ${m.join(', ')}` : 'Accepts any signal'
    const live = operatorLiveness(entry.operator)
    return (
      <span className="flex flex-wrap items-center gap-x-1.5 sm:flex-nowrap">
        <span className="shrink-0">{base}{live.kind !== 'unreported' && ' · '}</span>
        {live.kind !== 'unreported' && <OperatorHealth operator={entry.operator} className="min-w-0 sm:truncate" testId={`${destinationKey(entry)}-health`} />}
        {entry.operator.addressState === 'pending' && <span className="shrink-0 text-warn">· Needs an address</span>}
      </span>
    )
  }
  if (entry.kind === 'quickstart') return <>Quick-started here · {entry.backend.modality}</>
  if (entry.kind === 'detected') return <>{entry.exportEndpoint} · {imageRepository(entry.detected.service.image)}</>
  const proto = entry.preset.httpOnly ? 'OTLP/HTTP only' : entry.preset.protocol === 'http' ? 'OTLP/HTTP' : 'OTLP/gRPC'
  if (entry.preset.group === 'self-hosted') return <>{proto} · {entry.preset.endpointPattern}</>
  return <>{entry.preset.headerName ? `${proto} · needs a credential` : proto}</>
}

/** What an administrator can do about FUSION from the row itself, and what is in the way. */
export interface FusionControls {
  /** Switches FUSION on and resolves once the choice can be made (the row then selects it). Absent for anyone who may not. */
  enable?: () => Promise<void>
  busy: boolean
  error?: string
  /** Forgets a pending "Enable and use": whoever chose something else by hand in the meantime must not have it replaced when FUSION comes up. */
  cancel?: () => void
}

/** "Enable and use": switches FUSION on and, as soon as it is something that can be sent to (starting counts), hands its entry to `onUsable` -
 *  never before, and never when switching it on failed, and never once the person has chosen something else by hand (`cancel`, which every
 *  manual pick calls: a FUSION that comes up a minute later - or recovers from a failure - must not overwrite a later choice). Returns the controls the rows and the Sending-to card draw their button from. */
export function useEnableAndUse(entries: DestinationCatalogEntry[], controls: FusionControls | undefined, onUsable: (e: Extract<DestinationCatalogEntry, { kind: 'fusion' }>) => void): FusionControls | undefined {
  const [want, setWant] = useState(false)
  const entry = entries.find((e): e is Extract<DestinationCatalogEntry, { kind: 'fusion' }> => e.kind === 'fusion')
  const pick = useRef(onUsable)
  useEffect(() => {
    pick.current = onUsable
  })
  const usable = !!entry?.fusion.usable
  useEffect(() => {
    if (want && entry && usable) {
      setWant(false)
      pick.current(entry)
    }
    // Only when FUSION's own state moves: whatever else changes under it must not pick again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [want, usable])
  if (!controls) return undefined
  return {
    ...controls,
    cancel: () => setWant(false),
    enable: controls.enable
      ? async () => {
          setWant(true)
          try {
            await controls.enable!()
          } catch {
            setWant(false)
          }
        }
      : undefined,
  }
}

/** The radio mark every row of a pick-one list shares (cluster and destination alike). */
function Radio({ on, disabled }: { on: boolean; disabled?: boolean }) {
  return (
    <span className={clsx('flex size-4 shrink-0 items-center justify-center rounded-full border', on ? 'border-accent' : disabled ? 'border-nb-850' : 'border-nb-700')} aria-hidden>
      {on && <span className="size-2 rounded-full bg-accent" />}
    </span>
  )
}

/** A row that cannot be picked, with the one sentence that says why. FUSION while it is off is one: there is nothing to send to yet, so the row
 *  says so and (for an administrator) offers to switch it on and use it in one click. */
function DisabledRow({ entry, testId, fusion }: { entry: DestinationCatalogEntry; testId: string; fusion?: FusionControls }) {
  const f = entry.kind === 'fusion' ? entry.fusion : undefined
  const why = f
    ? f.otherOrg ? 'FUSION belongs to another organisation.' : f.kind === 'unavailable' ? f.message : f.kind === 'checking' ? 'Checking…' : f.kind === 'attention' ? 'Needs attention before anything can be sent to it.' : 'Nothing receives data until it is on.'
    : entry.reason
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3" aria-disabled="true" data-testid={testId} data-fusion={f?.kind}>
      <Radio on={false} disabled />
      <span className="min-w-0 flex-1 basis-48">
        <span className="block truncate text-sm text-nb-500" title={entry.label}>{entry.label}</span>
        <span className="flex flex-wrap items-center gap-x-1.5 text-xs text-nb-500">
          {f && <><FusionDot kind={f.kind} /><span>{fusionLabel(f.kind)}</span><span aria-hidden>·</span></>}
          <span data-testid={`${testId}-reason`}>{why}</span>
        </span>
      </span>
      {f?.canEnable && fusion?.enable ? (
        <Button size="sm" onClick={() => void fusion.enable?.()} disabled={fusion.busy} data-testid={`${testId}-enable`}>{fusion.busy ? 'Starting…' : 'Enable and use'}</Button>
      ) : (
        f?.kind === 'off' && <span className="text-xs text-nb-500" data-testid={`${testId}-ask`}>An administrator can turn it on.</span>
      )}
      <SignalChips accepts={entry.accepts} testId={`${testId}-signals`} className="opacity-70 max-sm:basis-full max-sm:pl-7" />
      {fusion?.error && f && <p role="alert" className="w-full text-xs text-bad">{fusion.error}</p>}
    </div>
  )
}

const rowClass = (on: boolean) => clsx('flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left transition-colors sm:flex-nowrap', on ? 'bg-accent-soft' : 'hover:bg-nb-930')

/** A destination that can be picked. */
export function DestinationRow({ entry, selected, badge, onPick, testId, fusion }: { entry: DestinationCatalogEntry; selected: boolean; /** Why this one is recommended, when it is. */ badge?: string; onPick: () => void; testId: string; fusion?: FusionControls }) {
  if (entry.kind === 'fusion' && !entry.fusion.usable) return <DisabledRow entry={entry} testId={testId} fusion={fusion} />
  return (
    <button type="button" role="radio" aria-checked={selected} onClick={onPick} data-testid={testId} className={rowClass(selected)}>
      <Radio on={selected} />
      <span className="min-w-0 flex-1 basis-48">
        <span className="block truncate text-sm font-medium text-nb-300" title={entry.label}>{entry.label}</span>
        <span className="block text-xs text-nb-500 sm:truncate"><EntryMeta entry={entry} /></span>
      </span>
      {badge && <span className="shrink-0 rounded-full border border-accent/30 bg-accent-soft px-2 py-0.5 text-[11px] font-medium text-accent" data-testid={`${testId}-recommended`}>{badge}</span>}
      <SignalChips accepts={entry.accepts} testId={`${testId}-signals`} className="max-sm:basis-full max-sm:pl-7" />
    </button>
  )
}

/** Arrow keys move between the rows of the list (each is still a tab stop, and Space or Enter picks), as a radio group is expected to. */
function moveFocus(e: KeyboardEvent<HTMLElement>) {
  const step = e.key === 'ArrowDown' || e.key === 'ArrowRight' ? 1 : e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 0
  if (step === 0) return
  const rows = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('[role="radio"]'))
  const at = rows.indexOf(document.activeElement as HTMLElement)
  if (at < 0) return
  e.preventDefault()
  rows[(at + step + rows.length) % rows.length].focus()
}

/** The badge a recommended row carries, and why. */
const badgeFor = (e: DestinationCatalogEntry) => (e.kind === 'fusion' ? 'Recommended' : 'Already receives this cluster')

const Heading = ({ children }: { children: string }) => <h4 className="bg-nb-930 px-4 py-1.5 text-[11px] font-medium uppercase tracking-wide text-nb-500">{children}</h4>

/**
 * The catalog as ONE list a person picks one row from, grouped: FUSION first, then what the organisation and the cluster already have, then the
 * built-in presets behind "show more". What cannot carry the signals chosen is not hidden: it stays in its group, disabled, with the one sentence
 * that says what it does not accept. The way to something not listed (another endpoint, a new regional operator) is the last group, so every way
 * to send is in this one list. Shared by the wizard's "Where to send" step and by the new-operator dialog. It holds only what the list itself
 * needs (the search text, "show more"); what was picked is the caller's.
 */
export default function DestinationPicker({
  catalog,
  layout,
  activeKey,
  onPick,
  testIdPrefix: p,
  fusion,
  search = true,
  emptyText,
  custom,
  onSetUpOperator,
}: {
  catalog: DestinationCatalog
  layout: ReturnType<typeof layoutDestinations>
  /** The key of the picked entry (destinationKey), if any. */
  activeKey: string | null
  onPick: (e: DestinationCatalogEntry) => void
  testIdPrefix: string
  fusion?: FusionControls
  /** The search box; off where the list is short by construction. */
  search?: boolean
  /** What to say when nothing can be offered at all. */
  emptyText?: string
  /** Offers "another endpoint" as the last choices of the list. */
  custom?: { active: boolean; onPick: () => void }
  /** Offers to deploy a new regional operator (administrators): opens its dialog over the wizard. */
  onSetUpOperator?: () => void
}) {
  const [more, setMore] = useState(false)
  const [query, setQuery] = useState('')
  const moreCount = layout.all.length - layout.primary.length
  const searching = query.trim() !== ''
  const found = searching ? searchDestinations(catalog, query) : undefined
  const rowFor = (entry: DestinationCatalogEntry) => (
    <DestinationRow
      key={destinationKey(entry)}
      entry={entry}
      selected={destinationKey(entry) === activeKey}
      badge={layout.recommended.has(destinationKey(entry)) ? badgeFor(entry) : undefined}
      onPick={() => {
        fusion?.cancel?.()
        onPick(entry)
      }}
      testId={`${p}-destination-${destinationKey(entry)}`}
      fusion={fusion}
    />
  )
  const disabledRow = (e: DestinationCatalogEntry) => <DisabledRow key={destinationKey(e)} entry={e} testId={`${p}-destination-${destinationKey(e)}`} fusion={fusion} />
  const isPreset = (g: DestinationGroup) => g === 'self' || g === 'cloud'

  // What the list shows: by group, the usable rows then the disabled ones. Presets that wait behind "show more" take their disabled ones with them.
  const groups = (found ? [] : layout.sections).map((section) => ({
    group: section.group,
    title: section.title,
    usable: more ? section.entries : section.shown,
    off: layout.unavailable.filter((e) => destinationGroup(e) === section.group && (more || !isPreset(section.group))),
  }))
  // A group that has only disabled rows (an operator that takes no logs, when logs are chosen) still shows.
  for (const e of found ? [] : layout.unavailable) {
    const g = destinationGroup(e)
    if (!groups.some((x) => x.group === g) && (more || !isPreset(g))) groups.push({ group: g, title: GROUP_TITLE[g], usable: [], off: layout.unavailable.filter((o) => destinationGroup(o) === g) })
  }
  const empty = found ? found.usable.length === 0 && found.unavailable.length === 0 : groups.every((g) => g.usable.length + g.off.length === 0)

  return (
    <div className="space-y-3" data-testid={`${p}-destination-list`}>
      {search && (
        <div className="relative">
          <Search size={ICON_SM} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-nb-500" aria-hidden />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search a name or a signal" aria-label="Search destinations" className="pl-9" data-testid={`${p}-destination-search`} />
        </div>
      )}

      <div className="divide-y divide-nb-850 overflow-hidden rounded-xl border border-nb-850 bg-nb-925" role="radiogroup" aria-label="Destination" onKeyDown={moveFocus}>
        {found ? (
          <div data-testid={`${p}-destination-results`} className="divide-y divide-nb-850">
            {found.usable.map(rowFor)}
            {found.unavailable.map(disabledRow)}
            {empty && <p className="px-4 py-3 text-sm text-nb-400" data-testid={`${p}-destination-no-match`}>Nothing matches “{query.trim()}”. Use another endpoint below if yours isn’t listed.</p>}
          </div>
        ) : (
          groups.map((g) =>
            g.usable.length + g.off.length === 0 ? null : (
              <section key={g.group} data-testid={`${p}-destination-group-${g.group}`} className="divide-y divide-nb-850">
                <Heading>{g.title}</Heading>
                {g.group === 'cluster' && <p className="px-4 py-2 text-xs text-nb-500">Receivers discovery already sees running here. The address is worked out from the workload’s name - check it before you rely on it.</p>}
                {g.usable.map(rowFor)}
                {g.off.map(disabledRow)}
              </section>
            ),
          )
        )}
        {empty && !found && (
          <p className="px-4 py-3 text-sm text-nb-400" data-testid={`${p}-destination-empty`}>{emptyText ?? 'Nothing in this organisation can carry these signals yet. Deploy a regional operator, or send straight to an endpoint you already run.'}</p>
        )}
        {(custom || onSetUpOperator) && (
          <section className="divide-y divide-nb-850" data-testid={`${p}-destination-group-other`}>
            <Heading>Not listed</Heading>
            {onSetUpOperator && (
              <button type="button" onClick={onSetUpOperator} className={rowClass(false)} data-testid={`${p}-deploy-operator`}>
                <span className="flex size-4 shrink-0 items-center justify-center text-nb-400" aria-hidden><Plus size={ICON_SM} /></span>
                <span className="min-w-0 flex-1 basis-48">
                  <span className="block text-sm font-medium text-nb-300">Deploy a new regional operator for me</span>
                  <span className="block text-xs text-nb-500">It receives from several clusters and forwards. Ikhnos gives you the commands.</span>
                </span>
              </button>
            )}
            {custom && (
              <button type="button" role="radio" aria-checked={custom.active} onClick={() => { fusion?.cancel?.(); custom.onPick() }} className={rowClass(custom.active)} data-testid={`${p}-destination-custom`}>
                <Radio on={custom.active} />
                <span className="min-w-0 flex-1 basis-48">
                  <span className="block text-sm font-medium text-nb-300">Another OTLP endpoint</span>
                  <span className="block text-xs text-nb-500">An existing collector gateway or observability backend that your cluster can reach.</span>
                </span>
              </button>
            )}
          </section>
        )}
      </div>

      {!searching && moreCount > 0 && (
        <button type="button" className="text-xs text-accent hover:underline" onClick={() => setMore((m) => !m)} data-testid={`${p}-destination-more`}>
          {more ? 'Show fewer' : `Show ${moreCount} more`}
        </button>
      )}
    </div>
  )
}
