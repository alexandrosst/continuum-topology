import clsx from 'clsx'
import { Activity, FileText, Layers, Search, Waypoints, type LucideIcon } from 'lucide-react'
import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { FusionDot } from '@/components/operators/FusionPanel'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { Button, ICON_SM, Input } from '@/components/ui/primitives'
import { destinationKey, searchDestinations, type DestinationCatalog, type DestinationCatalogEntry, type layoutDestinations } from '@/lib/destinationCatalog'
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

/** The small tag at the right of a row: what sort of destination it is. */
function kindLabel(e: DestinationCatalogEntry): string {
  if (e.kind === 'fusion') return 'This server'
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

const tile = (selected: boolean) => clsx('flex size-8 shrink-0 items-center justify-center rounded-lg', selected ? 'bg-accent/15 text-accent' : 'bg-nb-930 text-nb-500')

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

/** A destination row: the same bordered-box language as GuidedWizard's PickCard, laid out as a compact list row, since this step shows
 *  more than a handful of them and a grid of tall cards pushes everything else below the fold. FUSION while it is off is not a radio:
 *  there is nothing to send to yet, so the row says so and (for an administrator) offers to switch it on and use it in one click. */
export function DestinationRow({ entry, selected, badge, onPick, testId, fusion }: { entry: DestinationCatalogEntry; selected: boolean; /** Why this one is recommended, when it is. */ badge?: string; onPick: () => void; testId: string; fusion?: FusionControls }) {
  if (entry.kind === 'fusion' && !entry.fusion.usable) {
    const f = entry.fusion
    const why = f.otherOrg ? 'FUSION belongs to another organisation.' : f.kind === 'unavailable' ? f.message : f.kind === 'checking' ? 'Checking…' : f.kind === 'attention' ? 'Needs attention before anything can be sent to it.' : 'Nothing receives data until it is on.'
    return (
      <div className="flex w-full flex-wrap items-center gap-3 rounded-xl border border-dashed border-nb-800 bg-nb-925 px-3.5 py-3" data-testid={testId} data-fusion={f.kind}>
        <span className={tile(false)} aria-hidden><Layers size={ICON_SM + 2} /></span>
        <span className="min-w-0 flex-1 basis-40">
          <span className="block truncate text-sm font-medium text-nb-300" title={entry.label}>{entry.label}</span>
          <span className="flex flex-wrap items-center gap-x-1.5 text-xs text-nb-500"><FusionDot kind={f.kind} /><span>{fusionLabel(f.kind)}</span><span className="text-nb-600">· {why}</span></span>
        </span>
        {f.canEnable && fusion?.enable ? (
          <Button size="sm" onClick={() => void fusion.enable?.()} disabled={fusion.busy} data-testid={`${testId}-enable`}>{fusion.busy ? 'Starting…' : 'Enable and use'}</Button>
        ) : (
          f.kind === 'off' && <span className="text-xs text-nb-500" data-testid={`${testId}-ask`}>An administrator can turn it on.</span>
        )}
        {fusion?.error && <p role="alert" className="w-full text-xs text-bad">{fusion.error}</p>}
      </div>
    )
  }
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onPick}
      data-testid={testId}
      className={clsx(
        'flex w-full flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border px-3.5 py-3 text-left transition-colors sm:flex-nowrap',
        selected ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-nb-850 bg-nb-925 hover:border-nb-800 hover:bg-nb-930',
      )}
    >
      <span className={tile(selected)} aria-hidden>
        {entry.kind === 'fusion' ? <Layers size={ICON_SM + 2} /> : <span className="text-[11px] font-semibold tracking-tight">{monogram(entry.label)}</span>}
      </span>
      <span className="min-w-0 flex-1 basis-40">
        <span className="block truncate text-sm font-medium text-nb-200" title={entry.label}>{entry.label}</span>
        <span className="block text-xs text-nb-500 sm:truncate"><EntryMeta entry={entry} /></span>
      </span>
      {badge && <span className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-medium text-accent" data-testid={`${testId}-recommended`}>{badge}</span>}
      <SignalChips accepts={entry.accepts} testId={`${testId}-signals`} className="max-sm:basis-full max-sm:pl-11" />
      {/* The kind says what the row is; a row that carries a badge has said enough, and the width goes to its name. */}
      {!badge && <span className={clsx('hidden w-24 shrink-0 text-right text-xs sm:block', entry.kind === 'detected' ? 'font-medium text-accent' : 'text-nb-500')}>{kindLabel(entry)}</span>}
    </button>
  )
}

/** Arrow keys move between the rows of a group (each is still a tab stop, and Space or Enter picks), as a radio group is expected to. */
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

/**
 * The catalog as a list a person picks one row from: search, the sections in order (FUSION first, then what the organisation and the
 * cluster already have, then the built-in presets behind "show more"), and the destinations that cannot carry what is turned on, with
 * their reasons. Shared by the guided wizard's Destination step and by the new-operator dialog, so a destination is picked the same
 * way everywhere. It holds only what the list itself needs (the search text, "show more"); what was picked is the caller's.
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
}) {
  const [more, setMore] = useState(false)
  const [unavailableOpen, setUnavailableOpen] = useState(false)
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

  return (
    <div className="space-y-3" data-testid={`${p}-destination-list`}>
      {search && (
        <div className="relative">
          <Search size={ICON_SM} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-nb-500" aria-hidden />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search a name or a signal" aria-label="Search destinations" className="pl-9" data-testid={`${p}-destination-search`} />
        </div>
      )}

      {found ? (
        <div className="space-y-2" data-testid={`${p}-destination-results`}>
          {found.usable.length > 0 && (
            <div className="space-y-2" role="radiogroup" aria-label="Destination" onKeyDown={moveFocus}>
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
        <div className="space-y-4" role="radiogroup" aria-label="Destination" onKeyDown={moveFocus}>
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
          {emptyText ?? 'Nothing in this organisation can carry these signals yet. Set up a regional operator, or send straight to an endpoint you already run.'}
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
    </div>
  )
}
