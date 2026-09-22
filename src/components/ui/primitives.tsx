import clsx from 'clsx'
import { Check, ChevronDown, Minus, X } from 'lucide-react'
import {
  Children, isValidElement, useEffect, useId, useRef, useState,
  type ButtonHTMLAttributes, type ChangeEvent, type ComponentProps, type KeyboardEvent as ReactKeyboardEvent, type ReactNode, type SelectHTMLAttributes,
} from 'react'
import { createPortal } from 'react-dom'
import { buttonClass, type ButtonVariant } from '@/components/ui/buttonClass'
import type { Completeness as CompletenessInfo } from '@/lib/completeness'
import { IP_SCOPE_HELP, ipScope, ipScopeLabel, loadBand } from '@/lib/present'
import { EVIDENCE_HELP, EVIDENCE_LABEL, EVIDENCE_TONE, needsEvidenceChip, TONE_CLASS, type EvidenceLevel, type ObsInfo } from '@/lib/provenance'
import { STATUS_COLOR, TIER_COLOR, type Source, type Status, type Tier } from '@/lib/types'

/* ---------- Button ---------- */
export function Button({
  variant = 'secondary',
  size = 'md',
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: 'sm' | 'md' }) {
  return <button {...props} className={buttonClass(variant, size, className)} />
}

/* ---------- Form controls ---------- */
const control =
  'h-9 w-full rounded-md border border-nb-800 bg-nb-925 px-3 text-sm text-nb-300 placeholder:text-nb-500 ' +
  'focus:border-accent/60 focus:outline-none focus:ring-2 focus:ring-accent/20'

export function Input({ className, ...p }: ComponentProps<'input'>) {
  return <input {...p} className={clsx(control, className)} />
}

/**
 * A themed replacement for the browser's <select>: same props and the same change event
 * (`e.target.value`), same <option> children, but the list is drawn by us so it matches the theme.
 * The list is rendered in a portal so modals and scroll containers never clip it, and it opens
 * upwards when there is no room below. Keyboard: arrows, Home/End, Enter/Space, Escape, type-ahead.
 */
type Opt = { value: string; label: string; disabled: boolean }

function readOptions(children: ReactNode): Opt[] {
  const out: Opt[] = []
  Children.forEach(children, (ch) => {
    if (!isValidElement(ch)) return
    const props = ch.props as { value?: string | number; disabled?: boolean; children?: ReactNode }
    if (ch.type === 'option') {
      const label = Children.toArray(props.children).filter((x) => typeof x === 'string' || typeof x === 'number').join('')
      out.push({ value: props.value !== undefined ? String(props.value) : label, label, disabled: !!props.disabled })
    } else if (props.children) {
      out.push(...readOptions(props.children)) // fragments and optgroups
    }
  })
  return out
}

export function Select({ className, children, value, defaultValue, onChange, disabled, name, id, placeholder, ...p }: SelectHTMLAttributes<HTMLSelectElement> & { placeholder?: string }) {
  const options = readOptions(children)
  const [inner, setInner] = useState(String(defaultValue ?? ''))
  const current = value !== undefined ? String(value) : inner
  const selected = options.find((o) => o.value === current)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const [pos, setPos] = useState<{ left: number; width: number; top?: number; bottom?: number; maxH: number } | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const typed = useRef({ text: '', at: 0 })
  const uid = useId()

  const place = () => {
    const r = btn.current?.getBoundingClientRect()
    if (!r) return
    const below = window.innerHeight - r.bottom - 12
    const above = r.top - 12
    const wantH = Math.min(280, options.length * 34 + 8)
    const up = below < wantH && above > below
    setPos({ left: r.left, width: r.width, maxH: Math.max(120, Math.min(280, up ? above : below)), ...(up ? { bottom: window.innerHeight - r.top + 4 } : { top: r.bottom + 4 }) })
  }

  const openList = () => {
    if (disabled) return
    place()
    setActive(Math.max(0, options.findIndex((o) => o.value === current)))
    setOpen(true)
  }
  const close = () => setOpen(false)
  const choose = (o: Opt) => {
    if (o.disabled) return
    if (value === undefined) setInner(o.value)
    const t = { value: o.value, name: name ?? '' }
    onChange?.({ target: t, currentTarget: t } as unknown as ChangeEvent<HTMLSelectElement>)
    close()
    btn.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (!btn.current?.contains(t) && !list.current?.contains(t)) close()
    }
    const onMove = (e: Event) => {
      if (list.current?.contains(e.target as Node)) return // scrolling inside the list is fine
      place()
    }
    document.addEventListener('mousedown', onDown)
    window.addEventListener('resize', onMove)
    window.addEventListener('scroll', onMove, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      window.removeEventListener('resize', onMove)
      window.removeEventListener('scroll', onMove, true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  useEffect(() => {
    if (open) list.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [open, active])

  const step = (from: number, dir: 1 | -1) => {
    for (let i = from + dir; i >= 0 && i < options.length; i += dir) if (!options[i].disabled) return i
    return from
  }

  const onKeyDown = (e: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (!open) {
      if (['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(e.key)) {
        e.preventDefault()
        openList()
      }
      return
    }
    e.stopPropagation() // Escape must close the list, not the dialog around it
    if (e.key === 'Escape') { e.preventDefault(); close() }
    else if (e.key === 'ArrowDown') { e.preventDefault(); setActive((a) => step(a, 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => step(a, -1)) }
    else if (e.key === 'Home') { e.preventDefault(); setActive(step(-1, 1)) }
    else if (e.key === 'End') { e.preventDefault(); setActive(step(options.length, -1)) }
    else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); if (options[active]) choose(options[active]) }
    else if (e.key === 'Tab') close()
    else if (e.key.length === 1) {
      const now = Date.now()
      typed.current = { text: (now - typed.current.at < 700 ? typed.current.text : '') + e.key.toLowerCase(), at: now }
      const i = options.findIndex((o) => !o.disabled && o.label.toLowerCase().startsWith(typed.current.text))
      if (i >= 0) setActive(i)
    }
  }

  return (
    <>
      <button
        {...(p as object)}
        ref={btn}
        id={id}
        type="button"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? `${uid}-list` : undefined}
        aria-activedescendant={open && active >= 0 ? `${uid}-o${active}` : undefined}
        disabled={disabled}
        onClick={() => (open ? close() : openList())}
        onKeyDown={onKeyDown}
        className={clsx(control.replace('w-full ', className && /(^|\s)w-/.test(className) ? '' : 'w-full '), 'relative flex items-center pr-8 text-left disabled:cursor-not-allowed disabled:opacity-50', className)}
      >
        <span className={clsx('min-w-0 flex-1 truncate', !selected && 'text-nb-500')}>{selected ? selected.label : placeholder ?? '—'}</span>
        <ChevronDown size={14} className={clsx('pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-nb-500 transition-transform', open && 'rotate-180')} />
      </button>
      {open && pos &&
        createPortal(
          <div
            ref={list}
            id={`${uid}-list`}
            role="listbox"
            className="fixed z-[70] overflow-y-auto rounded-md border border-nb-800 bg-nb-920 p-1 shadow-2xl shadow-black/50"
            style={{ left: pos.left, width: pos.width, top: pos.top, bottom: pos.bottom, maxHeight: pos.maxH }}
          >
            {options.map((o, i) => (
              <div
                key={o.value + i}
                id={`${uid}-o${i}`}
                role="option"
                aria-selected={o.value === current}
                aria-disabled={o.disabled || undefined}
                data-active={i === active}
                onMouseEnter={() => !o.disabled && setActive(i)}
                onClick={() => choose(o)}
                className={clsx(
                  'flex cursor-pointer items-center justify-between gap-2 rounded px-2.5 py-1.5 text-sm',
                  o.disabled ? 'cursor-not-allowed text-nb-700' : i === active ? 'bg-nb-940 text-white' : 'text-nb-300',
                )}
              >
                <span className="truncate">{o.label || '—'}</span>
                {o.value === current && <Check size={14} className="shrink-0 text-accent" />}
              </div>
            ))}
            {options.length === 0 && <div className="px-2.5 py-1.5 text-sm text-nb-500">No options</div>}
          </div>,
          document.body,
        )}
    </>
  )
}

export function Field({ label, hint, children, className }: { label: string; hint?: string; children: ReactNode; className?: string }) {
  return (
    <label className={clsx('block', className)}>
      <span className="mb-1.5 block text-sm font-medium text-nb-300">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-xs text-nb-500">{hint}</span>}
    </label>
  )
}

/** A form/page-level error message: this is the shape most of the app already uses for "something went wrong,
 * here's why" (as opposed to `role="alert"` text inlined next to whatever it explains). Prefer this over
 * hand-writing the same border/background/text classes again. */
export function ErrorBanner({ children, className, ...p }: ComponentProps<'p'>) {
  return (
    <p role="alert" {...p} className={clsx('rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-300', className)}>
      {children}
    </p>
  )
}

/** A small "why" affordance: keeps a one-line summary from having to also carry the full technical
 * justification. A native title tooltip, matching how this file already explains a disabled tier option or a
 * pinned-image badge, rather than a new mechanism for the same job. */
export function InfoTip({ children }: { children: string }) {
  return (
    <span
      className="ml-1 inline-flex h-3.5 w-3.5 shrink-0 cursor-help items-center justify-center rounded-full border border-nb-700 text-[9px] font-medium leading-none text-nb-500 align-text-top"
      title={children}
      aria-label={children}
    >
      i
    </span>
  )
}

/* ---------- Badges ---------- */
/**
 * A status. `notCurrent` (the record's agent is not being heard from right now: the reason goes in the tooltip) shows the
 * dot hollow and the word as the last one known, because "healthy" said of a picture that has gone quiet is a claim
 * nobody is making any more.
 */
export function StatusDot({ status, withLabel, notCurrent }: { status: Status; withLabel?: boolean; notCurrent?: string }) {
  return (
    <span className="inline-flex items-center gap-2 text-sm" title={notCurrent ? `Last known: ${status}. ${notCurrent}` : undefined}>
      <span className={clsx('size-2 rounded-full', notCurrent && 'border bg-transparent')} style={notCurrent ? { borderColor: STATUS_COLOR[status] } : { background: STATUS_COLOR[status] }} />
      {withLabel && <span className={clsx('capitalize', notCurrent ? 'text-nb-500' : 'text-nb-400')}>{notCurrent ? `was ${status}` : status}</span>}
    </span>
  )
}

export function TierBadge({ tier }: { tier: Tier }) {
  const label = tier === 'far-edge' ? 'Far edge' : tier[0].toUpperCase() + tier.slice(1)
  return (
    <span
      className="inline-flex min-w-[4.75rem] items-center justify-center whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs font-medium leading-5"
      style={{
        color: TIER_COLOR[tier],
        borderColor: `color-mix(in srgb, ${TIER_COLOR[tier]} 35%, transparent)`,
        background: `color-mix(in srgb, ${TIER_COLOR[tier]} 10%, transparent)`,
      }}
    >
      {label}
    </span>
  )
}

/** Where a record came from. Nothing is shown for manual records to keep tables quiet. */
export function SourceBadge({ source, overridden, stacked }: { source: Source; overridden?: boolean; stacked?: boolean }) {
  if (source === 'manual' && !overridden) return null
  const label = source === 'discovered' ? 'Detected' : source === 'imported' ? 'Imported' : 'Manual'
  return (
    <span className={clsx('inline-flex items-center gap-1 align-middle', stacked ? 'mt-1' : 'ml-2')}>
      {source !== 'manual' && (
        <span className="rounded border border-sky-400/30 bg-sky-400/10 px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-sky-300">{label}</span>
      )}
      {overridden && (
        <span
          title="Some values were changed by a person and will survive rediscovery"
          className="rounded border border-accent/30 bg-accent-soft px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-accent"
        >
          Edited
        </span>
      )}
    </span>
  )
}

/**
 * How far a record can be trusted right now: live, disconnected, stale 2 h, revoked or gone. Nothing for records
 * nobody observes (typed by hand): they have nothing to be stale about. The tooltip says why.
 */
export function ObservationChip({ info, className, quiet }: { info: ObsInfo | undefined; className?: string; quiet?: boolean }) {
  if (!info) return null
  // In a table of records that are mostly fine, the ordinary case is a small dot; anything else keeps its chip.
  if (quiet && info.kind === 'live') {
    return (
      <span title="Live: connected and heard from recently." data-observation="live" role="img" aria-label="live" className={clsx('inline-block size-1.5 rounded-full bg-emerald-400 align-middle', className)} />
    )
  }
  return (
    <span
      title={info.reason ?? (info.kind === 'live' ? 'Connected and heard from recently.' : info.label)}
      data-observation={info.kind}
      className={clsx('inline-flex items-center gap-1 whitespace-nowrap rounded border px-1.5 py-px text-[11px] font-medium leading-4', TONE_CLASS[info.tone], className)}
    >
      <span aria-hidden className={clsx('size-1.5 rounded-full', info.kind === 'live' ? 'bg-emerald-400' : info.kind === 'gone' ? 'bg-nb-500' : info.kind === 'revoked' ? 'bg-red-400' : 'bg-amber-400')} />
      {info.label}
    </span>
  )
}

/**
 * How sure the system is of one value: measured, reported, inferred, a guess or unknown. Shown where a value is
 * guessed or unknown (and, with `always`, for every value, as in the Evidence section).
 */
export function EvidenceChip({ level, why, always, className }: { level: EvidenceLevel; why?: string; always?: boolean; className?: string }) {
  if (!always && !needsEvidenceChip(level)) return null
  return (
    <span
      title={why ? `${EVIDENCE_HELP[level]} ${why}` : EVIDENCE_HELP[level]}
      data-evidence={level}
      className={clsx('inline-flex items-center whitespace-nowrap rounded border px-1.5 py-px text-[10px] font-medium uppercase leading-4 tracking-wide', TONE_CLASS[EVIDENCE_TONE[level]], className)}
    >
      {EVIDENCE_LABEL[level]}
    </span>
  )
}

/** The mark on a row a person typed: nobody observes it, so there is no freshness to show. Says so instead of saying nothing. */
export function DeclaredMark({ className }: { className?: string }) {
  return (
    <span title="Declared by a person. No agent observes it, so it has nothing to be stale about." data-declared className={clsx('text-[11px] font-medium uppercase tracking-wide text-nb-500', className)}>
      declared
    </span>
  )
}

/** Which layers of a cluster are known: infrastructure, services, dependencies. */
export function CompletenessBadge({ c, compact }: { c: CompletenessInfo; compact?: boolean }) {
  // A tick for what is known and a dash for what is not, so the column reads as a checklist.
  const item = (on: boolean, text: string) => (
    <span className={clsx('inline-flex items-center gap-1', on ? 'text-nb-300' : 'text-nb-600')}>
      {on ? <Check size={11} className="text-emerald-400" aria-hidden /> : <Minus size={11} aria-hidden />}
      {text}
      <span className="sr-only">{on ? ' known' : ' not known'}</span>
    </span>
  )
  return (
    <span className="inline-flex items-center gap-2.5 whitespace-nowrap text-xs" title={c.label}>
      {item(c.infra, 'Infra')}
      {item(c.services, compact ? 'Svc' : 'Services')}
      {item(c.dependencies, 'Deps')}
    </span>
  )
}

/** A small labelled fact about a service (storage, scaling, …). `warn` marks something an orchestrator must respect. */
export function Trait({ icon, children, title, warn }: { icon: ReactNode; children: ReactNode; title?: string; warn?: boolean }) {
  return (
    <span
      title={title}
      className={clsx(
        'inline-flex items-center gap-1 whitespace-nowrap rounded border px-1.5 py-px text-[11px] leading-4',
        warn ? 'border-amber-400/30 bg-amber-400/10 text-amber-300' : 'border-nb-800 bg-nb-930 text-nb-400',
      )}
    >
      {icon}
      {children}
    </span>
  )
}

/** An IP address with its scope underneath (private / public / …). Nothing extra for hostnames. */
export function IpAddress({ ip, inline }: { ip?: string; inline?: boolean }) {
  if (!ip) return <span className="text-nb-700">—</span>
  const scope = ipScope(ip)
  const label = ipScopeLabel(scope)
  const tone = scope === 'public' ? 'text-amber-300' : 'text-nb-500'
  return (
    <span className={clsx('whitespace-nowrap', inline ? 'inline-flex items-baseline gap-2' : 'inline-flex flex-col')}>
      <span className="font-mono text-xs text-nb-300">{ip}</span>
      {label && <span title={IP_SCOPE_HELP[scope]} className={clsx('text-[11px] leading-4', tone)}>{label}</span>}
    </span>
  )
}

export function Pill({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex items-center whitespace-nowrap rounded-md border border-nb-800 bg-nb-930 px-2 py-0.5 text-xs text-nb-400">
      {children}
    </span>
  )
}

/* ---------- Modal ---------- */
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  width = 'max-w-xl',
}: {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
  width?: string
}) {
  const box = useRef<HTMLDivElement>(null)
  const closeRef = useRef(onClose)
  useEffect(() => { closeRef.current = onClose })
  useEffect(() => {
    if (!open) return
    // Keyboard users must not fall out of a modal: Tab cycles inside it, Escape closes it, and focus goes back to
    // what opened it. (The page behind is hidden from assistive technology by aria-modal.)
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const focusable = () =>
      Array.from(box.current?.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])') ?? []).filter((el) => el.offsetParent !== null)
    if (!box.current?.contains(document.activeElement)) (focusable()[0] ?? box.current)?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        closeRef.current()
        return
      }
      if (e.key !== 'Tab') return
      const f = focusable()
      if (f.length === 0) {
        e.preventDefault()
        return
      }
      const first = f[0], last = f[f.length - 1]
      if (e.shiftKey && (document.activeElement === first || !box.current?.contains(document.activeElement))) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && (document.activeElement === last || !box.current?.contains(document.activeElement))) {
        e.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
      opener?.focus?.()
    }
  }, [open]) // onClose is read through a ref: a new function each render must not re-run this and steal focus

  if (!open) return null
  // Portaled to the document body: some callers (the sidebar's account menu) render this from inside an
  // element that carries a CSS transform, which would otherwise make it the containing block for `fixed`
  // descendants and confine the dialog to that element's box instead of the viewport - the same reason
  // Select's own list is portaled below.
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/60 p-4 pt-[8vh] backdrop-blur-[2px]" onMouseDown={onClose}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={clsx('w-full rounded-xl border border-nb-850 bg-nb-920 shadow-2xl', width)}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4 border-b border-nb-850 px-6 py-4">
          <div>
            <h2 className="text-base font-medium text-white">{title}</h2>
            {description && <p className="mt-1 text-sm text-nb-500">{description}</p>}
          </div>
          <button onClick={onClose} aria-label="Close" className="rounded p-1 text-nb-500 hover:bg-nb-940 hover:text-nb-300">
            <X size={16} />
          </button>
        </div>
        <div className="px-6 py-5">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-nb-850 px-6 py-4">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

/* ---------- Page chrome ---------- */
export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-medium text-white">{title}</h1>
        {description && <p className="mt-1 max-w-2xl text-sm text-nb-500">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center rounded-xl border border-dashed border-nb-850 px-6 py-14 text-center">
      <h3 className="text-base font-medium text-white">{title}</h3>
      <p className="mt-1 max-w-md text-sm text-nb-500">{description}</p>
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

/* ---------- Table ---------- */
/**
 * `cols` (a width class per column, e.g. `['w-48', 'w-36', '']`; an empty string takes the rest) fixes the layout,
 * so two tables on one page line their columns up.
 */
export function Table({ children, cols, ...p }: ComponentProps<'div'> & { children: ReactNode; cols?: string[] }) {
  return (
    <div {...p} className="relative overflow-x-auto rounded-xl border border-nb-850 bg-nb-925">
      <table className={clsx('w-full text-left text-sm', cols && 'table-fixed')}>
        {cols && (
          <colgroup>
            {cols.map((c, i) => <col key={i} className={c} />)}
          </colgroup>
        )}
        {children}
      </table>
    </div>
  )
}
export const Th = ({ children, className, actionsLabel = 'Actions', ...p }: ComponentProps<'th'> & { actionsLabel?: string }) => (
  <th scope="col" {...p} className={clsx('whitespace-nowrap border-b border-nb-850 px-3 py-3 text-left text-xs font-medium uppercase tracking-wide text-nb-500', className)}>{children ?? <span className="sr-only">{actionsLabel}</span>}</th>
)
export const Td = ({ children, className, valign = 'middle', ...p }: ComponentProps<'td'> & { valign?: 'top' | 'middle' }) => (
  <td {...p} className={clsx('border-b border-nb-850/60 px-3 py-3 text-nb-300', valign === 'top' ? 'align-top' : 'align-middle', className)}>{children}</td>
)

/**
 * A capacity figure with an optional bar for how much of it is already requested by pods.
 * The number comes first and the unit is quieter, so a column of them reads at a glance.
 */
export function Meter({ value, unit, pct, title }: { value: string; unit?: string; pct?: number; title?: string }) {
  const color = pct === undefined ? '' : { ok: 'bg-emerald-400', warn: 'bg-amber-400', hot: 'bg-red-400' }[loadBand(pct)]
  return (
    <div className="w-[5rem]" title={title}>
      <div className="flex items-baseline gap-1 whitespace-nowrap">
        <span className="text-sm font-medium tabular-nums text-nb-300">{value}</span>
        {unit && <span className="text-xs text-nb-500">{unit}</span>}
        {pct !== undefined && <span className="ml-auto text-[11px] tabular-nums text-nb-500">{pct}%</span>}
      </div>
      <div className={clsx('mt-1 h-1 overflow-hidden rounded-full', pct !== undefined && 'bg-nb-850')}>
        {pct !== undefined && <div className={clsx('h-full rounded-full', color)} style={{ width: `${pct}%` }} />}
      </div>
    </div>
  )
}

/** A short list that never grows a row: the first few items, then "+N". */
export function ChipList({ items, max = 2 }: { items: string[]; max?: number }) {
  if (items.length === 0) return <span className="text-nb-700">—</span>
  const shown = items.slice(0, max)
  const rest = items.length - shown.length
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap" title={items.join(', ')}>
      {shown.map((i) => (
        <span key={i} className="max-w-[9rem] truncate rounded bg-nb-940 px-1.5 py-0.5 text-xs text-nb-400">{i}</span>
      ))}
      {rest > 0 && <span className="text-xs text-nb-500">+{rest}</span>}
    </span>
  )
}
