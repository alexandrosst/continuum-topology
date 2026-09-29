import clsx from 'clsx'
import { Check, ChevronDown, Copy, Eye, EyeOff, Minus, Plus, X } from 'lucide-react'
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

/* ---------- Copy to clipboard ---------- */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      size="sm"
      className="whitespace-nowrap"
      onClick={async () => {
        setDone(await copyText(text))
        setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? <Check size={12} className="fade-in text-ok" /> : <Copy size={12} />} {done ? 'Copied' : label}
    </Button>
  )
}

/* ---------- Form controls ---------- */
const control =
  'h-9 w-full rounded-md border border-nb-800 bg-nb-925 px-3 text-sm text-nb-300 placeholder:text-nb-500 ' +
  'focus:border-accent/60 focus:outline-none focus:ring-2 focus:ring-accent/20'

export function Input({ className, ...p }: ComponentProps<'input'>) {
  return <input {...p} className={clsx(control, className)} />
}

/**
 * A password field with a per-field show/hide toggle. Each instance keeps its own `show` state, so
 * several password inputs on the same screen (current/new/confirm) toggle independently.
 */
export function PasswordInput({ className, ...p }: Omit<ComponentProps<'input'>, 'type'>) {
  const [show, setShow] = useState(false)
  return (
    <div className={clsx('relative', className)}>
      <input {...p} type={show ? 'text' : 'password'} className={clsx(control, 'w-full pr-9', className)} />
      <button
        type="button"
        onClick={() => setShow((s) => !s)}
        aria-label={show ? 'Hide password' : 'Show password'}
        className="absolute inset-y-0 right-0 grid w-9 place-items-center text-nb-500 hover:text-nb-300"
      >
        {show ? <EyeOff size={15} /> : <Eye size={15} />}
      </button>
    </div>
  )
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
                  o.disabled ? 'cursor-not-allowed text-nb-700' : i === active ? 'bg-nb-940 text-nb-300' : 'text-nb-300',
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

const COMBO_CUSTOM = '__custom__'

/**
 * A dropdown of the common values for a field (a cloud provider, a Kubernetes distribution) plus an
 * escape hatch for anything the list doesn't cover, instead of forcing free text for everyone to save
 * the rare case - the "pick one, or type your own" shape most cloud consoles use for exactly this kind
 * of field. Starts in the dropdown when the current value matches a listed option (or is empty), and in
 * the text field otherwise - editing an existing record with an uncommon value never hides it. Picking
 * "Other…" clears the value rather than leaving the last selection behind it; "Pick from list" clears
 * the typed text the same way, so switching back and forth never leaves a stale value neither view is
 * showing.
 */
export function ComboField({
  value,
  onChange,
  options,
  placeholder = 'Select…',
  customLabel = 'Other…',
}: {
  value: string
  onChange: (value: string) => void
  options: { value: string; label: string }[]
  placeholder?: string
  customLabel?: string
}) {
  const matches = value === '' || options.some((o) => o.value === value)
  const [customMode, setCustomMode] = useState(!matches)

  if (customMode) {
    return (
      <div className="flex gap-2">
        <Input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className="min-w-0 flex-1" autoFocus />
        <Button type="button" size="sm" onClick={() => { setCustomMode(false); onChange('') }}>Pick from list</Button>
      </div>
    )
  }
  return (
    <Select
      value={value}
      placeholder={placeholder}
      onChange={(e) => {
        if (e.target.value === COMBO_CUSTOM) {
          setCustomMode(true)
          onChange('')
        } else {
          onChange(e.target.value)
        }
      }}
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>{o.label}</option>
      ))}
      <option value={COMBO_CUSTOM}>{customLabel}</option>
    </Select>
  )
}

/**
 * A list of short free-text tokens (namespace names, an exclusion list) edited as individual removable
 * chips with one text box to add more - instead of a line of comma- or space-separated text a person has
 * to punctuate exactly right by eye. Splits on comma, space, or Enter as each is typed (matching how these
 * lists already parse server-side), so pasting "a, b c" still produces three tags. `data-testid`, when
 * given, lands on the actual text box, the same element a plain `Input` would have put it on.
 */
export function TagsInput({
  value,
  onChange,
  placeholder,
  'aria-invalid': ariaInvalid,
  'data-testid': dataTestId,
}: {
  value: string[]
  onChange: (value: string[]) => void
  placeholder?: string
  'aria-invalid'?: boolean
  'data-testid'?: string
}) {
  const [draft, setDraft] = useState('')

  const commit = (text: string) => {
    const tokens = text
      .split(/[,\s]+/)
      .map((t) => t.trim())
      .filter(Boolean)
    setDraft('')
    if (tokens.length === 0) return
    const next = [...value]
    for (const t of tokens) if (!next.includes(t)) next.push(t)
    onChange(next)
  }

  return (
    <div className={clsx(control, 'flex h-auto min-h-9 flex-wrap items-center gap-1 py-1.5')}>
      {value.map((tag) => (
        <span key={tag} className="fade-in inline-flex items-center gap-1 rounded bg-nb-940 px-1.5 py-0.5 text-xs text-nb-300">
          {tag}
          <button type="button" onClick={() => onChange(value.filter((t) => t !== tag))} className="text-nb-500 hover:text-bad" aria-label={`Remove ${tag}`}>
            <X size={10} />
          </button>
        </span>
      ))}
      <input
        value={draft}
        onChange={(e) => {
          const v = e.target.value
          if (/[,\s]$/.test(v)) commit(v)
          else setDraft(v)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            commit(draft)
          } else if (e.key === 'Backspace' && draft === '' && value.length > 0) {
            onChange(value.slice(0, -1))
          }
        }}
        onBlur={() => commit(draft)}
        placeholder={value.length === 0 ? placeholder : undefined}
        aria-invalid={ariaInvalid}
        data-testid={dataTestId}
        className="min-w-[6rem] flex-1 bg-transparent text-sm text-nb-300 placeholder:text-nb-500 focus:outline-none"
      />
    </div>
  )
}

/**
 * "Pick zero or more from a fixed, already-loaded list" - a scrollable list of checkboxes, each with an
 * optional one-line hint underneath its label. Distinct from `TagsInput` (free text, no fixed universe)
 * and `ComboField` (exactly one value): this is for a bounded set the caller already has in hand, e.g. a
 * regional operator's source clusters, so nobody has to type an id correctly by hand. Whatever is checked
 * client-side is a convenience only - the server re-validates it against its own current truth regardless.
 */
export function CheckboxList({
  options,
  value,
  onChange,
  emptyLabel = 'Nothing to pick from yet.',
}: {
  options: { value: string; label: string; hint?: string }[]
  value: string[]
  onChange: (value: string[]) => void
  emptyLabel?: string
}) {
  const toggle = (v: string) => onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v])
  if (options.length === 0) return <p className="text-xs text-nb-500">{emptyLabel}</p>
  return (
    <div className={clsx(control, 'h-auto max-h-48 space-y-0.5 overflow-y-auto p-1.5')}>
      {options.map((o) => (
        <label key={o.value} className="flex cursor-pointer items-start gap-2 rounded px-1.5 py-1 text-sm hover:bg-nb-940">
          <input
            type="checkbox"
            className="mt-0.5 size-4 shrink-0 accent-[var(--color-accent)]"
            checked={value.includes(o.value)}
            onChange={() => toggle(o.value)}
            data-testid={`checkbox-${o.value}`}
          />
          <span className="min-w-0">
            <span className="block text-nb-300">{o.label}</span>
            {o.hint && <span className="block text-xs text-nb-500">{o.hint}</span>}
          </span>
        </label>
      ))}
    </div>
  )
}

type LabelPair = { key: string; value: string }
const labelsToRows = (l: Record<string, string>): LabelPair[] => Object.entries(l).map(([key, value]) => ({ key, value }))
const rowsToLabels = (rows: LabelPair[]): Record<string, string> =>
  Object.fromEntries(rows.filter((r) => r.key.trim() !== '').map((r) => [r.key.trim(), r.value.trim()]))

/**
 * A structured editor for small key/value maps (Kubernetes-style labels and selectors): one row per pair, its
 * own key and value input, and a remove button - instead of a single "key=value, key=value" text field a person
 * has to punctuate exactly right by eye. Mirrors the read-only `KeyValueChips` presentation (see Inspector.tsx)
 * so the same data takes the same shape whether it's being read or edited. Uncontrolled after mount, the same
 * way callers already hold a local draft for the old text field - `onChange` fires with the derived record on
 * every edit, and it's the caller's job to persist it (typically on save, like every other field here).
 */
export function LabelsEditor({
  value,
  onChange,
  keyPlaceholder = 'key',
  valuePlaceholder = 'value',
}: {
  value: Record<string, string>
  onChange: (value: Record<string, string>) => void
  keyPlaceholder?: string
  valuePlaceholder?: string
}) {
  const [rows, setRows] = useState<LabelPair[]>(() => labelsToRows(value))
  const update = (next: LabelPair[]) => {
    setRows(next)
    onChange(rowsToLabels(next))
  }
  return (
    <div className="space-y-1.5">
      {rows.map((row, i) => (
        <div key={i} className="fade-in flex items-center gap-1.5">
          <Input
            value={row.key}
            onChange={(e) => update(rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))}
            placeholder={keyPlaceholder}
            className="min-w-0 flex-1"
            aria-label="Label key"
          />
          <span className="shrink-0 text-sm text-nb-600">=</span>
          <Input
            value={row.value}
            onChange={(e) => update(rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
            placeholder={valuePlaceholder}
            className="min-w-0 flex-1"
            aria-label="Label value"
          />
          <button
            type="button"
            onClick={() => update(rows.filter((_, j) => j !== i))}
            className="shrink-0 rounded-md p-1.5 text-nb-500 transition-colors hover:bg-nb-850 hover:text-bad"
            aria-label={`Remove ${row.key || 'label'}`}
          >
            <X size={14} />
          </button>
        </div>
      ))}
      <Button type="button" size="sm" onClick={() => update([...rows, { key: '', value: '' }])}>
        <Plus size={12} /> Add label
      </Button>
    </div>
  )
}

/** `adornment` sits right after the label text - e.g. an EvidenceChip marking the field's value as a guess or
 * unknown, so a person editing it sees the same "how sure are we" signal this app already shows in tables and
 * the inspector, without a separate lookup. Generic on purpose: Field itself knows nothing about evidence. */
export function Field({ label, hint, children, className, adornment }: { label: string; hint?: string; children: ReactNode; className?: string; adornment?: ReactNode }) {
  return (
    <label className={clsx('block', className)}>
      <span className="mb-1.5 flex flex-wrap items-center gap-1.5 text-sm font-medium text-nb-300">
        {label}
        {adornment}
      </span>
      {children}
      {hint && <span className="mt-1 block text-xs text-nb-500">{hint}</span>}
    </label>
  )
}

/**
 * A small "it worked" confirmation after a save - the same short ease-in as a popover opening (see `.menu-pop` in
 * index.css), so it registers as a change rather than snapping into place. Always announced to assistive tech.
 * Several pages hand-roll this same `{saved && <span role="status">Saved…</span>}` shape; sharing it here means
 * the touch stays consistent instead of each caller inventing its own timing (or none).
 */
export function SavedNote({ children, tone = 'ok', className, ...p }: ComponentProps<'span'> & { tone?: 'ok' | 'error' }) {
  return (
    <span {...p} role="status" className={clsx('fade-in text-sm', tone === 'ok' ? 'text-ok' : 'text-bad', className)}>
      {children}
    </span>
  )
}

/** A form/page-level error message: this is the shape most of the app already uses for "something went wrong,
 * here's why" (as opposed to `role="alert"` text inlined next to whatever it explains). Prefer this over
 * hand-writing the same border/background/text classes again. */
export function ErrorBanner({ children, className, ...p }: ComponentProps<'p'>) {
  return (
    <p role="alert" {...p} className={clsx('rounded-md border border-bad/30 bg-bad/10 px-3 py-2 text-sm text-bad', className)}>
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
/**
 * True for a moment right after `value` changes from what it was, so a status swap can flash briefly instead of
 * cutting over instantly. Stays false on the first render (nothing to flash yet) and while `value` is unchanged.
 */
export function useFlash<T>(value: T, ms = 800): boolean {
  const prev = useRef(value)
  const [flash, setFlash] = useState(false)
  useEffect(() => {
    if (prev.current === value) return
    prev.current = value
    setFlash(true)
    const t = setTimeout(() => setFlash(false), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return flash
}

export function StatusDot({ status, withLabel, notCurrent }: { status: Status; withLabel?: boolean; notCurrent?: string }) {
  const flash = useFlash(status)
  return (
    <span className="inline-flex items-center gap-2 text-sm" title={notCurrent ? `Last known: ${status}. ${notCurrent}` : undefined}>
      <span className={clsx('size-2 rounded-full', flash && 'flash-ring', notCurrent && 'border bg-transparent')} style={notCurrent ? { borderColor: STATUS_COLOR[status] } : { background: STATUS_COLOR[status] }} />
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
        <span className="rounded border border-info/30 bg-info/10 px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-info">{label}</span>
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
 * A small round dot in an arbitrary color, with an optional pulsing "ping" ring around it — the same animation
 * LiveStatus uses for "this is current right now". Pass `pulse` only for the one state that means actively
 * live/connected, not for idle, error or neutral dots, so the blink stays a meaningful signal rather than
 * decoration everywhere. Distinct from StatusDot below, which renders one of the app's own named Status values
 * (healthy/degraded/offline/...) with its fixed palette; this one takes any Tailwind color class directly.
 */
export function PulseDot({ color, pulse, size = 'size-2', className }: { color: string; pulse?: boolean; size?: string; className?: string }) {
  return (
    <span className={clsx('relative inline-flex shrink-0', size, className)} aria-hidden>
      {pulse && <span className={clsx('absolute inline-flex size-full animate-ping rounded-full opacity-75', color)} />}
      <span className={clsx('relative inline-flex rounded-full', size, color)} />
    </span>
  )
}

/**
 * How far a record can be trusted right now: live, disconnected, stale 2 h, revoked or gone. Nothing for records
 * nobody observes (typed by hand): they have nothing to be stale about. The tooltip says why.
 */
export function ObservationChip({ info, className, quiet }: { info: ObsInfo | undefined; className?: string; quiet?: boolean }) {
  const flash = useFlash(info?.kind)
  if (!info) return null
  // In a table of records that are mostly fine, the ordinary case is a small dot; anything else keeps its chip.
  if (quiet && info.kind === 'live') {
    return (
      <span title="Live: connected and heard from recently." data-observation="live" role="img" aria-label="live" className={clsx('inline-block align-middle rounded-full', flash && 'flash-ring')}>
        <PulseDot color="bg-ok" pulse size="size-1.5" className={className} />
      </span>
    )
  }
  return (
    <span
      title={info.reason ?? (info.kind === 'live' ? 'Connected and heard from recently.' : info.label)}
      data-observation={info.kind}
      className={clsx('inline-flex items-center gap-1 whitespace-nowrap rounded border px-1.5 py-px text-[11px] font-medium leading-4', TONE_CLASS[info.tone], flash && 'flash-bg', className)}
    >
      <PulseDot
        color={info.kind === 'live' ? 'bg-ok' : info.kind === 'gone' ? 'bg-nb-500' : info.kind === 'revoked' ? 'bg-bad' : 'bg-warn'}
        pulse={info.kind === 'live'}
        size="size-1.5"
      />
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
      {on ? <Check size={11} className="text-ok" aria-hidden /> : <Minus size={11} aria-hidden />}
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
        warn ? 'border-warn/30 bg-warn/10 text-warn' : 'border-nb-800 bg-nb-930 text-nb-400',
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
  const tone = scope === 'public' ? 'text-warn' : 'text-nb-500'
  return (
    <span className={clsx('whitespace-nowrap', inline ? 'inline-flex items-baseline gap-2' : 'inline-flex flex-col')}>
      <span className="font-mono text-xs text-nb-300">{ip}</span>
      {label && <span title={IP_SCOPE_HELP[scope]} className={clsx('text-[11px] leading-4', tone)}>{label}</span>}
    </span>
  )
}

export function Pill({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span title={title} className="inline-flex items-center whitespace-nowrap rounded-md border border-nb-800 bg-nb-930 px-2 py-0.5 text-xs text-nb-400">
      {children}
    </span>
  )
}

/* ---------- Popovers ---------- */
/**
 * The dropdown a toolbar button opens (Filter, Views, view Options, Add - one row of several in the Topology
 * toolbar): an invisible full-screen click-outside backdrop plus the panel itself, positioned off the button
 * that opened it and easing in on open. Pass what's specific to one menu (width, padding, whether it clips its
 * content) through `className`.
 *
 * Both sit above the mobile Inspector sheet (z-30), which can be open behind any of these on a narrow
 * viewport, and the panel is capped so it can never run off a narrow screen either.
 */
export function MenuPanel({
  open,
  onClose,
  className,
  children,
  ...aria
}: {
  open: boolean
  onClose: () => void
  className?: string
  children: ReactNode
} & ComponentProps<'div'>) {
  if (!open) return null
  return (
    <>
      <div className="fixed inset-0 z-40" onClick={onClose} />
      {/* A small rotated-square "caret" pointing back at whichever button opened this - a plain sibling,
          not a pseudo-element on the panel itself, specifically so it's never clipped by a caller's own
          `overflow-hidden` (most of MenuPanel's callers pass one, to round/scroll their own content) and
          never needs each caller to route width/overflow/padding through two different elements. Sits
          right at the panel's top edge (same top-11 anchor, pulled up by half its own height) so the
          panel's higher DOM order - and thus paint order, at the same z-index - covers its bottom half,
          leaving only the upward-pointing triangle visible above the panel. Fixed 16px from the right edge:
          every caller right-aligns its panel to its trigger button (`right-0` on this and the panel below,
          both relative to that button's own `relative` wrapper), so this reads as "pointing at the button"
          regardless of the button's or panel's width. */}
      <div className="menu-pop absolute right-4 top-11 z-[41] size-2.5 -translate-y-1/2 rotate-45 border-l border-t border-nb-850 bg-nb-920" aria-hidden="true" />
      <div {...aria} className={clsx('menu-pop absolute right-0 top-11 z-[41] max-w-[calc(100vw-2rem)] rounded-lg border border-nb-850 bg-nb-920 shadow-xl', className)}>
        {children}
      </div>
    </>
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
  dismissible = true,
}: {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
  width?: string
  /**
   * false for a modal a person must resolve from its own footer buttons (a one-time choice with no safe
   * default). Hides the header close button and stops the backdrop click and Escape from doing anything -
   * without this, all three still looked clickable/pressable while silently doing nothing, which reads as
   * broken rather than intentional. Default true: every other modal keeps closing exactly as before.
   */
  dismissible?: boolean
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
        if (dismissible) closeRef.current()
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
  }, [open, dismissible]) // onClose is read through a ref: a new function each render must not re-run this and steal focus

  if (!open) return null
  // Portaled to the document body: some callers (the sidebar's account menu) render this from inside an
  // element that carries a CSS transform, which would otherwise make it the containing block for `fixed`
  // descendants and confine the dialog to that element's box instead of the viewport - the same reason
  // Select's own list is portaled below.
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/60 p-4 pt-[8vh] backdrop-blur-[2px]" onMouseDown={dismissible ? onClose : undefined}>
      <div
        ref={box}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={clsx('modal-pop w-full rounded-xl border border-nb-850 bg-nb-920 shadow-2xl', width)}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4 border-b border-nb-850 px-6 py-4">
          <div>
            <h2 className="text-base font-medium text-nb-300">{title}</h2>
            {description && <p className="mt-1 text-sm text-nb-500">{description}</p>}
          </div>
          {dismissible && (
            <button onClick={onClose} aria-label="Close" className="rounded p-1 text-nb-500 hover:bg-nb-940 hover:text-nb-300">
              <X size={16} />
            </button>
          )}
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
        <h1 className="text-2xl font-medium text-nb-300">{title}</h1>
        {description && <p className="mt-1 max-w-2xl text-sm text-nb-500">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center rounded-xl border border-dashed border-nb-850 px-6 py-14 text-center">
      <h3 className="text-base font-medium text-nb-300">{title}</h3>
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

/* ---------- Loading skeletons ---------- */
/**
 * A shimmering placeholder shaped like what it will become, so a page doesn't jump-cut from "Loading…" text to
 * real content once data arrives. `aria-hidden`: the loading state itself is announced once, by the container
 * around these (`role="status"` on TableSkeleton/SkeletonLines), not once per placeholder bar.
 */
export function SkeletonBlock({ className }: { className?: string }) {
  return <div className={clsx('skeleton-shimmer rounded', className)} aria-hidden />
}

/**
 * A Table-shaped placeholder: the same rounded/bordered container as the real table that will replace it, so
 * nothing reflows when data arrives. Pass `cols` when the real table below also fixes its column widths with
 * one; otherwise just say how many columns with `colCount`.
 */
export function TableSkeleton({ cols, colCount, rows = 5 }: { cols?: string[]; colCount?: number; rows?: number }) {
  const n = cols?.length ?? colCount ?? 4
  return (
    <Table cols={cols} role="status" aria-label="Loading">
      <tbody>
        {Array.from({ length: rows }, (_, r) => (
          <tr key={r} className={r > 0 ? 'border-t border-nb-850/60' : undefined}>
            {Array.from({ length: n }, (_, c) => (
              <Td key={c}><SkeletonBlock className={clsx('h-3.5', c === 0 ? 'w-24' : 'w-full max-w-[10rem]')} /></Td>
            ))}
          </tr>
        ))}
      </tbody>
    </Table>
  )
}

/** A few placeholder text lines, for a small loading area that isn't a table (a panel, a list, a page body). */
export function SkeletonLines({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div className={clsx('space-y-2', className)} role="status" aria-label="Loading">
      {Array.from({ length: lines }, (_, i) => (
        <SkeletonBlock key={i} className={clsx('h-3', i === lines - 1 && lines > 1 ? 'w-2/3' : 'w-full')} />
      ))}
    </div>
  )
}

/** A whole page's worth of placeholder, for a route-level Suspense fallback that doesn't know which page is coming. */
export function PageSkeleton() {
  return (
    <div className="p-6 sm:p-8" role="status" aria-label="Loading">
      <SkeletonBlock className="mb-2 h-7 w-56" />
      <SkeletonBlock className="mb-6 h-4 w-full max-w-md" />
      <SkeletonBlock className="h-64 w-full" />
    </div>
  )
}

/**
 * A capacity figure with an optional bar for how much of it is already requested by pods.
 * The number comes first and the unit is quieter, so a column of them reads at a glance.
 */
export function Meter({ value, unit, pct, title }: { value: string; unit?: string; pct?: number; title?: string }) {
  const color = pct === undefined ? '' : { ok: 'bg-ok', warn: 'bg-warn', hot: 'bg-bad' }[loadBand(pct)]
  // A refreshed reading fades the new number in rather than popping over the old one; the bar itself just
  // transitions its width/color in CSS (meter-bar), no JS tweening needed for that part.
  const flash = useFlash(value)
  return (
    <div className="w-[5rem]" title={title}>
      <div className="flex items-baseline gap-1 whitespace-nowrap">
        <span className={clsx('text-sm font-medium tabular-nums text-nb-300', flash && 'fade-in')}>{value}</span>
        {unit && <span className="text-xs text-nb-500">{unit}</span>}
        {pct !== undefined && <span className={clsx('ml-auto text-[11px] tabular-nums text-nb-500', flash && 'fade-in')}>{pct}%</span>}
      </div>
      <div className={clsx('mt-1 h-1 overflow-hidden rounded-full', pct !== undefined && 'bg-nb-850')}>
        {pct !== undefined && <div className={clsx('h-full rounded-full meter-bar', color)} style={{ width: `${pct}%` }} />}
      </div>
    </div>
  )
}

/**
 * A labeled figure tile: label above, a large tabular-nums value, an optional caption below - the one
 * definition three pages (AgentsPage, PlacementPage, SettingsPage) each used to grow their own slightly
 * different copy of, with incompatibly-named/polarized boolean props (`warn` vs `good`). `tone` replaces
 * both: 'warn' reddens the value (and, while `bordered`, the tile's own border too), 'ok' greens it,
 * unset stays neutral. `bordered` is off for a tile that already sits inside its own bordered container
 * (e.g. PlacementPage's plan-summary Card) - everywhere else wants the tile to be its own standalone box.
 * The value fades in on change (see useFlash) so a figure that updates on the page's own poll is seen to
 * change rather than silently flipping underneath the viewer.
 */
export function StatTile({
  label,
  value,
  sub,
  tone,
  bordered = true,
  className,
  'data-testid': testId,
}: {
  label: string
  value: string
  sub?: string
  tone?: 'ok' | 'warn'
  bordered?: boolean
  className?: string
  'data-testid'?: string
}) {
  const flash = useFlash(value)
  return (
    <div
      className={clsx(bordered && 'h-full rounded-xl border bg-nb-925 px-5 py-4', bordered && (tone === 'warn' ? 'border-warn/30' : 'border-nb-850'), className)}
      data-testid={testId}
    >
      <div className="text-xs text-nb-500">{label}</div>
      <div className={clsx('mt-1 text-2xl font-medium tabular-nums', tone === 'warn' ? 'text-warn' : tone === 'ok' ? 'text-ok' : 'text-nb-300', flash && 'fade-in')}>{value}</div>
      {sub && <div className="mt-0.5 text-xs tabular-nums text-nb-500">{sub}</div>}
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

/**
 * A navigable step indicator: numbered circles joined by a line, done/current/upcoming styling - the same
 * visual language as ConnectClusterWizard's own Stepper, generalized to a plain `currentIndex` a person can
 * move through with their own Back/Next controls, instead of that one's server-driven async `phase`. Meant
 * for any wizard that walks someone through a small number of named steps in order (see the guided
 * telemetry wizard, GuidedWizard.tsx, for the first caller) - purely presentational, the caller owns the
 * step state and which content renders for it.
 */
export function WizardSteps({ steps, currentIndex, testId = 'wizard-steps' }: { steps: string[]; currentIndex: number; testId?: string }) {
  return (
    <div className="mb-4 flex items-center" data-testid={testId}>
      {steps.map((label, i) => {
        const done = i < currentIndex
        const current = i === currentIndex
        return (
          <div key={label} className={clsx('flex items-center', i < steps.length - 1 && 'flex-1')}>
            <span className="relative flex size-5 shrink-0 items-center justify-center">
              {current && <span className="absolute inline-flex size-full animate-ping rounded-full bg-accent/40" />}
              <span
                className={clsx(
                  'relative flex size-5 items-center justify-center rounded-full border text-[10px] font-medium',
                  done ? 'border-ok/50 bg-ok/15 text-ok' : current ? 'border-accent bg-accent-soft text-accent' : 'border-nb-800 text-nb-600',
                )}
              >
                {done ? <Check size={11} /> : i + 1}
              </span>
            </span>
            <span className={clsx('ml-1.5 whitespace-nowrap text-[11px]', done ? 'text-nb-400' : current ? 'text-nb-200' : 'text-nb-600')}>{label}</span>
            {i < steps.length - 1 && <span className={clsx('mx-2 h-px flex-1', done ? 'bg-ok/30' : 'bg-nb-850')} />}
          </div>
        )
      })}
    </div>
  )
}
