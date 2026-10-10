import clsx from 'clsx'
import { Check, ChevronDown, Cloud, Copy, Cpu, Eye, EyeOff, Loader2, Minus, Plus, Server, X, type LucideIcon } from 'lucide-react'
import {
  Children, isValidElement, useEffect, useId, useRef, useState,
  type ButtonHTMLAttributes, type ChangeEvent, type ComponentProps, type KeyboardEvent as ReactKeyboardEvent, type ReactNode, type SelectHTMLAttributes,
} from 'react'
import { createPortal } from 'react-dom'
import { buttonClass, type ButtonVariant } from '@/components/ui/buttonClass'
import { copyToClipboard } from '@/lib/clipboard'
import type { Completeness as CompletenessInfo } from '@/lib/completeness'
import { IP_SCOPE_HELP, ipScope, ipScopeLabel, loadBand } from '@/lib/present'
import { rttLabel } from '@/lib/metrics'
import { ageLabel, EVIDENCE_HELP, EVIDENCE_LABEL, EVIDENCE_TONE, needsEvidenceChip, TONE_CLASS, type EvidenceLevel, type ObsInfo, type Tone } from '@/lib/provenance'
import { shownStatus, type Alert } from '@/lib/detail'
import { TIER_COLOR, type Source, type Status, type Tier } from '@/lib/types'

/** The app's whole icon-size scale: every lucide-react (and brand.tsx) icon's `size` prop should come
 *  from one of these two constants rather than a one-off literal - the 10-18px range previously in use
 *  had nine distinct values with no rule for which a given spot got. ICON_SM is for an icon inline with
 *  text (a label, a row, a dense badge); ICON_MD is for one standing alone (an icon-only button, an empty
 *  state, a toolbar control). A handful of call sites outside 10-18px (a big empty-state illustration, a
 *  tiny status glyph) are deliberately outside this scale and keep their own literal. */
export const ICON_SM = 14
export const ICON_MD = 16

/* ---------- Button ---------- */
export function Button({
  variant = 'secondary',
  size = 'md',
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: 'sm' | 'md' }) {
  // type="button" unless the caller says otherwise: a button inside a <form> submits it by default, and most of ours (a switch, a copy, a
  // dialog's Cancel) are not the form's submit.
  return <button type="button" {...props} className={buttonClass(variant, size, className)} />
}

/* ---------- Copy to clipboard ---------- */
/**
 * What a copy button knows about its last press: 'copied' (the browser took the text), 'manual' (it could not - no Clipboard API on a plain-http
 * page, or it refused - so the person has to press Ctrl+C themselves) or 'idle'. See lib/clipboard.ts for the routes tried.
 * 'copied' fades after a moment; 'manual' stays until the next press or until `dismiss`, because a person needs time to act on it.
 */
export function useCopy(): { state: 'idle' | 'copied' | 'manual'; copy: (text: string, select?: Element | null) => Promise<void>; dismiss: () => void } {
  const [state, setState] = useState<'idle' | 'copied' | 'manual'>('idle')
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const copy = async (text: string, select?: Element | null) => {
    window.clearTimeout(timer.current)
    const outcome = await copyToClipboard(text, select)
    setState(outcome)
    if (outcome === 'copied') timer.current = window.setTimeout(() => setState('idle'), 1500)
  }
  return { state, copy, dismiss: () => setState('idle') }
}

/** The words that stand in for "Copied" when copying failed: said where the person is looking, not only in a console. */
export const MANUAL_COPY_HINT = 'Press Ctrl+C to copy'

/**
 * Where there is no visible text to leave selected (a copy button beside a block, or in a table cell), the failed copy shows the text here,
 * already selected, with the instruction: a small read-only box under the button. Leaving it (blur, Escape) dismisses it.
 */
function ManualCopy({ text, onDone }: { text: string; onDone: () => void }) {
  const area = useRef<HTMLTextAreaElement>(null)
  useEffect(() => { area.current?.focus(); area.current?.select() }, [])
  return (
    <span className="absolute right-0 top-full z-30 mt-1 block w-64 rounded-md border border-nb-800 bg-nb-920 p-2 shadow-xl" data-testid="manual-copy">
      <span role="status" className="mb-1 block text-xs text-warn">{MANUAL_COPY_HINT}</span>
      <textarea
        ref={area}
        readOnly
        rows={3}
        value={text}
        aria-label="Text to copy"
        onBlur={onDone}
        onKeyDown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); onDone() } }}
        className="block w-full resize-none rounded border border-nb-800 bg-nb-950 p-1 font-mono text-[11px] text-nb-300"
      />
    </span>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const { state, copy, dismiss } = useCopy()
  return (
    <span className="relative inline-flex">
      <Button size="sm" className="whitespace-nowrap" onClick={() => void copy(text)}>
        {state === 'copied' ? <Check size={ICON_SM} className="fade-in text-ok" /> : <Copy size={ICON_SM} />} {state === 'copied' ? 'Copied' : state === 'manual' ? 'Not copied' : label}
      </Button>
      {state === 'manual' && <ManualCopy text={text} onDone={dismiss} />}
    </span>
  )
}

/** Icon-only sibling of CopyButton, for a dense row/table cell where a labeled button would be too heavy -
 *  an identifier (a CIDR, an endpoint, an image digest) sitting right next to its own copy affordance rather
 *  than needing a separate, wider control. Same copy-then-checkmark feedback, `stopPropagation`'d so it never
 *  also triggers whatever the row itself does on click (selecting it, opening a detail view). */
export function CopyIconButton({ text, title = 'Copy' }: { text: string; title?: string }) {
  const { state, copy, dismiss } = useCopy()
  return (
    <span className="relative inline-flex shrink-0" onClick={(e) => e.stopPropagation()}>
      <button
        type="button"
        title={state === 'copied' ? 'Copied' : state === 'manual' ? MANUAL_COPY_HINT : title}
        onClick={(e) => {
          e.stopPropagation()
          void copy(text)
        }}
        className="rounded p-0.5 text-nb-600 transition-colors hover:bg-nb-850 hover:text-nb-300"
      >
        {state === 'copied' ? <Check size={ICON_MD} className="fade-in text-ok" /> : <Copy size={ICON_MD} className={state === 'manual' ? 'text-warn' : undefined} />}
      </button>
      {state === 'manual' && <ManualCopy text={text} onDone={dismiss} />}
    </span>
  )
}

/**
 * A value shown on screen with its own Copy button: a token, an invitation link. Where the browser will not copy (no Clipboard API on a
 * plain-http page), the value is left selected and the hint says so, so a secret that is shown once is never lost to a button that did nothing.
 */
export function CopyValue({ value, testId, icon }: { value: string; testId?: string; icon?: ReactNode }) {
  const { state, copy } = useCopy()
  const text = useRef<HTMLElement>(null)
  return (
    <div>
      <div className="flex items-center gap-2 rounded-md border border-nb-800 bg-nb-950 px-3 py-2">
        <code ref={text} className="flex-1 select-all break-all font-mono text-xs text-nb-300" data-testid={testId}>{value}</code>
        <Button size="sm" onClick={() => void copy(value, text.current)}>{icon} {state === 'copied' ? 'Copied' : 'Copy'}</Button>
      </div>
      {state === 'manual' && <p role="status" className="mt-1 text-xs text-warn" data-testid={testId ? `${testId}-manual` : undefined}>{MANUAL_COPY_HINT}: it is selected above.</p>}
    </div>
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
        {show ? <EyeOff size={ICON_MD} /> : <Eye size={ICON_MD} />}
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
        <ChevronDown size={ICON_SM} className={clsx('pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-nb-500 transition-transform', open && 'rotate-180')} />
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
                {o.value === current && <Check size={ICON_SM} className="shrink-0 text-accent" />}
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
  'aria-describedby': ariaDescribedBy,
  'data-testid': dataTestId,
}: {
  value: string[]
  onChange: (value: string[]) => void
  placeholder?: string
  'aria-invalid'?: boolean
  'aria-describedby'?: string
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
        <Pill key={tag} className="fade-in gap-1 text-xs text-nb-300">
          {tag}
          <button type="button" onClick={() => onChange(value.filter((t) => t !== tag))} className="text-nb-500 hover:text-bad" aria-label={`Remove ${tag}`}>
            <X size={ICON_MD} />
          </button>
        </Pill>
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
        aria-describedby={ariaDescribedBy}
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
            <X size={ICON_MD} />
          </button>
        </div>
      ))}
      <Button type="button" size="sm" onClick={() => update([...rows, { key: '', value: '' }])}>
        <Plus size={ICON_SM} /> Add label
      </Button>
    </div>
  )
}

/** `adornment` sits right after the label text - e.g. an EvidenceChip marking the field's value as a guess or
 * unknown, so a person editing it sees the same "how sure are we" signal this app already shows in tables and
 * the inspector, without a separate lookup. Generic on purpose: Field itself knows nothing about evidence. */
export function Field({ label, hint, hintId, children, className, adornment }: { label: string; hint?: string; hintId?: string; children: ReactNode; className?: string; adornment?: ReactNode }) {
  return (
    <label className={clsx('block', className)}>
      <span className="mb-1.5 flex flex-wrap items-center gap-1.5 text-sm font-medium text-nb-300">
        {label}
        {adornment}
      </span>
      {children}
      {hint && <span id={hintId} className="mt-1 block text-xs text-nb-500">{hint}</span>}
    </label>
  )
}

/**
 * A small uppercase section/subsection heading - "Define scope", "Attach", "History", "Extra processors"
 * and the like. The exact className this renders (`text-xs font-medium uppercase tracking-wide text-nb-500`)
 * was independently hand-rolled on a div, span, p or legend in well over a dozen places across the app before
 * this existed; sharing it here doesn't migrate every one of those (most are fine left alone), but gives new
 * call sites, and any of those existing ones worth touching later, one definition instead of another copy.
 */
export function SectionLabel({ children, as: As = 'div', className }: { children: ReactNode; as?: 'div' | 'span' | 'p' | 'legend'; className?: string }) {
  return <As className={clsx('text-xs font-medium uppercase tracking-wide text-nb-500', className)}>{children}</As>
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
export function ErrorBanner({ children, className, onDismiss, ...p }: ComponentProps<'p'> & { /** Adds a Dismiss button, for an error about something that already happened (a failed action) and would otherwise stay until the page is left. */ onDismiss?: () => void }) {
  return (
    <p role="alert" {...p} className={clsx('rounded-md border border-bad/30 bg-bad/10 px-3 py-2 text-sm text-bad', onDismiss && 'flex items-start gap-3', className)}>
      {onDismiss ? <span className="min-w-0 flex-1">{children}</span> : children}
      {onDismiss && (
        <button type="button" onClick={onDismiss} aria-label="Dismiss" className="shrink-0 rounded p-0.5 text-bad/80 hover:bg-bad/10 hover:text-bad">
          <X size={ICON_SM} aria-hidden />
        </button>
      )}
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

/** `alert` is what is wrong inside a thing that reports itself well (see shownStatus): the dot and the word then say that instead of "healthy". */
export function StatusDot({ status, alert, withLabel, notCurrent }: { status: Status; alert?: Alert; withLabel?: boolean; notCurrent?: string }) {
  const flash = useFlash(status)
  const shown = shownStatus(status, notCurrent ? undefined : alert)
  return (
    <span className="inline-flex items-center gap-2 text-sm" title={notCurrent ? `Last known: ${status}. ${notCurrent}` : undefined}>
      <span className={clsx('size-2 rounded-full', flash && 'flash-ring', notCurrent && 'border bg-transparent')} style={notCurrent ? { borderColor: shown.color } : { background: shown.color }} />
      {withLabel && <span className={notCurrent ? 'capitalize text-nb-500' : 'text-nb-400'}>{notCurrent ? `was ${status}` : shown.word}</span>}
    </span>
  )
}

/** One icon per tier, standing in for the hardware class that tier actually implies - not just a color swatch
 *  with a name next to it. Smallest/most-constrained first: `far-edge` is IoT-class hardware at the literal edge
 *  of the network (a chip, drawn as one), `edge` is a rack of on-prem/near-edge servers, and `cloud` is the
 *  hyperscale/datacenter tier the name already says. Same family NodesPage/nodes.tsx already use for a node's
 *  own machine kind (Cpu for 'edge-device', Server for 'vm') - this is the cluster-level equivalent. */
export const TIER_ICON: Record<Tier, LucideIcon> = { cloud: Cloud, edge: Server, 'far-edge': Cpu }

export function TierBadge({ tier }: { tier: Tier }) {
  const label = tier === 'far-edge' ? 'Far edge' : tier[0].toUpperCase() + tier.slice(1)
  const Icon = TIER_ICON[tier]
  return (
    <span
      className="inline-flex min-w-[4.75rem] items-center justify-center gap-1 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs font-medium leading-5"
      style={{
        color: TIER_COLOR[tier],
        borderColor: `color-mix(in srgb, ${TIER_COLOR[tier]} 35%, transparent)`,
        background: `color-mix(in srgb, ${TIER_COLOR[tier]} 10%, transparent)`,
      }}
    >
      <Icon size={ICON_SM} aria-hidden="true" />
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
 * How an agent, an operator or FUSION is doing, as one dot: the ONE vocabulary for "is it alive" across the product. Green
 * and pulsing = online (the only thing that moves while everything is fine), red = offline, amber = late or needs a look,
 * hollow amber with a slow fade = coming up, hollow grey = not reporting / switched off. Shape repeats what colour says
 * (filled and pulsing, filled, hollow), so none of it rests on telling green from red. The words beside a dot carry the
 * meaning; the dot only repeats it. Use this, not a hand-picked colour, wherever an entity has a liveness.
 */
export type LiveKind = 'online' | 'offline' | 'late' | 'starting' | 'idle'
export function LiveDot({ kind, size = 'size-1.5', className }: { kind: LiveKind; size?: string; className?: string }) {
  // A change of state rings the dot once (the same cue StatusDot gives), so a status that flips while someone is looking is noticed.
  const flash = useFlash(kind)
  const cls = clsx(className, flash && 'flash-ring rounded-full')
  switch (kind) {
    case 'online':
      return <PulseDot color="bg-ok" pulse size={size} className={cls} />
    case 'offline':
      return <PulseDot color="bg-bad" size={size} className={cls} />
    case 'late':
      return <PulseDot color="bg-warn" size={size} className={cls} />
    case 'starting':
      return <span className={clsx('inline-block shrink-0 rounded-full border border-warn animate-pulse', size, cls)} aria-hidden />
    default:
      return <span className={clsx('inline-block shrink-0 rounded-full border border-nb-500', size, cls)} aria-hidden />
  }
}

/**
 * Something is being waited for: the same accent spinner the discovery wizard shows while an agent connects, with the
 * words beside it. One spinner per view - elsewhere on the page the same wait is a LiveDot, which does not spin.
 */
export function Waiting({ children, className, testId }: { children: ReactNode; className?: string; testId?: string }) {
  return (
    <span className={clsx('inline-flex items-center gap-2', className)} role="status" data-testid={testId}>
      <Loader2 size={ICON_SM} className="shrink-0 animate-spin text-accent" aria-hidden />
      <span>{children}</span>
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

/**
 * How sure we are of a fact, and why: the one "Source / Confidence / freshness" strip every entity kind used
 * to reimplement on its own (Inspector's old Origin, WeakValues, Why, a node's bespoke "Not sure" prose, an
 * external endpoint's bespoke "seen in traffic" line, and a dependency's un-colored "Found by"/"Confidence"
 * rows) - one component instead of six wordings for the same question. `source` is the origin or detection
 * method ("Detected", "node probe", "eBPF + conntrack"); `sourceBadge` is for something genuinely specific to
 * that one entity riding along next to it ("via eBPF") rather than a second wording system of its own;
 * `confidence` is how sure that source is, colored through the app's one ok/warn/bad/muted palette (never a
 * new one); `freshness` is when it was last (or first) seen. Any of `confidence`/`freshness` can be omitted
 * where the fact genuinely carries none (a hand-typed record has no freshness; a declared value with no
 * per-field evidence has no confidence) - this says nothing rather than fabricating one, the same rule
 * `formatAttr` already follows for an unknown value.
 */
export function Provenance({
  source,
  sourceTitle,
  sourceBadge,
  confidence,
  freshness,
}: {
  source: ReactNode
  sourceTitle?: string
  sourceBadge?: ReactNode
  confidence?: { tone: Tone; label: string; title?: string }
  freshness?: { label: 'Seen' | 'First seen'; at: string; note?: string }
}) {
  return (
    <div className="py-1.5 text-sm" data-testid="provenance">
      <div className="flex items-baseline justify-between gap-4">
        <span className="shrink-0 text-nb-500">Provenance</span>
        <span className="flex min-w-0 items-center justify-end gap-1.5 text-right text-nb-300">
          <span className="scrollbar-none min-w-0 overflow-x-auto whitespace-nowrap" title={sourceTitle}>{source}</span>
          {sourceBadge}
          {confidence && (
            <span
              title={confidence.title}
              data-testid="provenance-confidence"
              className={clsx('inline-flex items-center whitespace-nowrap rounded border px-1.5 py-px text-[10px] font-medium uppercase leading-4 tracking-wide', TONE_CLASS[confidence.tone])}
            >
              {confidence.label}
            </span>
          )}
        </span>
      </div>
      {freshness && (
        <div className="text-right text-[11px] leading-4 text-nb-500" data-testid="provenance-freshness">
          {freshness.label} {ageLabel(freshness.at)} ago{freshness.note ? ` \u00b7 ${freshness.note}` : ''}
        </div>
      )}
    </div>
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
      {on ? <Check size={ICON_MD} className="text-ok" aria-hidden /> : <Minus size={ICON_MD} aria-hidden />}
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

/** The shared "small rounded label" shape - a border, a slightly-lighter-than-canvas background, rounded-md,
 *  comfortable padding - used for every plain descriptive tag across the app (a role, a kind, a status
 *  line) rather than each spot hand-rolling its own near-duplicate of the same four classes. Default
 *  (no `className`) renders exactly as every existing call site already expects: 12px nb-400 text. A
 *  caller that passes `className` takes over BOTH size and color (and anything else: gap for an inline
 *  icon/button, max-width/truncate, a tone color) - it is a full override of those two, not a merge, so a
 *  Pill around interactive content (a tag with its own remove button) or multi-part content (a dimmer key
 *  next to a brighter value) can still get the shared shape without fighting the default text styling. */
export function Pill({ children, title, className }: { children: ReactNode; title?: string; className?: string }) {
  return (
    <span title={title} className={clsx('inline-flex items-center whitespace-nowrap rounded-md border border-nb-800 bg-nb-930 px-2 py-0.5', className ?? 'text-xs text-nb-400')}>
      {children}
    </span>
  )
}

/** A label/value row shared by the Inspector's sidebar (`dense` omitted: roomier `py-1.5 text-sm`) and a
 *  hover card's compact stack of rows (`dense`: tighter `py-0.5 text-xs`) - the same label/value color pair
 *  either way, since density changes only size and padding, never the color pair. `labelTitle` puts a
 *  tooltip on the label itself (several of EdgeHoverCard's rows explain what a measurement means right on
 *  the label) without wrapping it in an extra element, so the label stays the direct previous sibling of
 *  the value for anything that walks the row's own DOM structure. `wrap` lets prose (detection reasons,
 *  traffic summaries) break onto a second line instead of scrolling horizontally; `badge`/`copy` are kept
 *  out of that scrolling region so a value+badge (or value+copy) pair stays right after the value no matter
 *  how long the value is. */
export function DetailRow({
  label,
  labelTitle,
  children,
  wrap,
  badge,
  copy,
  dense,
}: {
  label: string
  labelTitle?: string
  children: ReactNode
  wrap?: boolean
  badge?: ReactNode
  copy?: string
  dense?: boolean
}) {
  const trailing = badge || copy ? (
    <>
      {badge}
      {copy && <CopyIconButton text={copy} title={`Copy ${label.toLowerCase()}`} />}
    </>
  ) : null
  return (
    <div className={clsx('flex items-baseline justify-between gap-4', dense ? 'py-0.5 text-xs' : 'py-1.5 text-sm')}>
      <span title={labelTitle} className="shrink-0 text-nb-500">{label}</span>
      {trailing ? (
        <span className="inline-flex min-w-0 items-center justify-end gap-1.5 text-right text-nb-300">
          <span className="scrollbar-none min-w-0 overflow-x-auto whitespace-nowrap">{children}</span>
          {trailing}
        </span>
      ) : (
        <span className={wrap ? 'min-w-0 break-words text-right text-nb-300' : 'scrollbar-none min-w-0 overflow-x-auto whitespace-nowrap text-right text-nb-300'}>{children}</span>
      )}
    </div>
  )
}

/** Words for a ClusterLink's (or a single Dependency.tunnelLink's) inferred encryption posture -
 *  "unknown" is only ever a driver kind outside the backend's own closed list, not a missing
 *  measurement, so it reads as a plain fact rather than a warning the way "plaintext" does. Shared by
 *  TunnelEvidence below (and anything else quoting this same field) so a link's encryption reads
 *  identically wherever it is shown. */
export const ENCRYPTION_WORDS: Record<'encrypted' | 'plaintext' | 'unknown', string> = {
  encrypted: 'encrypted by design',
  plaintext: 'no encryption of its own',
  unknown: 'driver type not recognized',
}

/** Tone/label/explanation for a ClusterPairConnectivity's four possible `status` values - the single
 *  source of truth so a connectivity verdict reads identically wherever it is shown (today: the
 *  Inspector's "Cluster connectivity" section). "tunnel"/"subnet" are the same confirmed fact a
 *  ClusterLink's own evidence (TunnelEvidence below) already proves - this is just the broader
 *  pair-level verdict wrapping it; "unexplained"/"unknown" are the two honest outcomes a ClusterLink
 *  alone never surfaces at all. See the backend's model.ClusterPairConnectivity doc for exactly what
 *  distinguishes them. */
export const CONNECTIVITY_TONE: Record<'tunnel' | 'subnet' | 'unexplained' | 'unknown', Tone> = {
  tunnel: 'ok',
  subnet: 'ok',
  unexplained: 'warn',
  unknown: 'muted',
}

export const CONNECTIVITY_LABEL: Record<'tunnel' | 'subnet' | 'unexplained' | 'unknown', string> = {
  tunnel: 'confirmed tunnel',
  subnet: 'confirmed subnet',
  unexplained: 'unexplained',
  unknown: 'unknown',
}

export const CONNECTIVITY_HELP: Record<'tunnel' | 'subnet' | 'unexplained' | 'unknown', string> = {
  tunnel: 'An overlay/tunnel interface on each side was independently confirmed to reach the other - see the evidence below.',
  subnet: 'Both sides report the exact same routable network, with no tunnel involved - see the evidence below.',
  unexplained: 'Traffic crosses this pair, and both sides reported enough of their own network facts to check, but neither a tunnel nor a shared subnet corroborates it - something explains this traffic that this server cannot see.',
  unknown: 'Not enough network evidence was collected from one or both sides to say whether a path exists, let alone how.',
}

/** The small status pill for a ClusterPairConnectivity row - the same bordered, uppercase, tone-colored
 *  badge every other confidence/evidence marker in this file already uses (EvidenceChip, Provenance's
 *  own confidence chip), so a connectivity verdict reads in the same visual language as everything
 *  around it rather than inventing a new one. */
export function ConnectivityStatusBadge({ status }: { status: 'tunnel' | 'subnet' | 'unexplained' | 'unknown' }) {
  return (
    <span
      title={CONNECTIVITY_HELP[status]}
      className={clsx('inline-flex items-center whitespace-nowrap rounded border px-1.5 py-px text-[10px] font-medium uppercase leading-4 tracking-wide', TONE_CLASS[CONNECTIVITY_TONE[status]])}
    >
      {CONNECTIVITY_LABEL[status]}
    </span>
  )
}

/**
 * The full evidence behind a confirmed cluster-link tunnel/subnet relationship, or the narrower subset a
 * single Dependency.tunnelLink mirrors onto its own edge (see each one's own doc in lib/types.ts): via,
 * inferred encryption, the confirming node names and tunnel addresses, redundancy, and - ClusterLink
 * only - the live flow rollup. Extracted so the topology Inspector's per-cluster "Cluster links" list and
 * EdgeHoverCard's hover card (and the Inspector's own per-dependency "Cluster link" section) never show a
 * different subset of the exact same backend fact, the way they used to before this existed: every field
 * below renders the same way, with the same label and the same explanatory title, regardless of which of
 * those three call sites reaches it. A field left undefined (nodeA/nodeB, addressA/addressB,
 * flowsObserved and its two averages - never populated for a "subnet" link or for Dependency.tunnelLink's
 * own narrower shape) simply renders no row at all, rather than a blank or fabricated one. `dense`
 * matches DetailRow's own flag (EdgeHoverCard's compact card passes it; the roomier Inspector call sites
 * don't).
 */
export function TunnelEvidence({
  dense,
  via,
  encryption,
  redundancy,
  nodeA,
  nodeB,
  addressA,
  addressB,
  flowsObserved,
  avgRttMs,
  avgLossPct,
  avgRtoRetransmitsPerMin,
  avgMssBytes,
}: {
  dense?: boolean
  via: string
  encryption?: 'encrypted' | 'plaintext' | 'unknown'
  redundancy: number
  nodeA?: string
  nodeB?: string
  addressA?: string
  addressB?: string
  flowsObserved?: number
  avgRttMs?: number
  avgLossPct?: number
  avgRtoRetransmitsPerMin?: number
  avgMssBytes?: number
}) {
  return (
    <>
      <DetailRow
        dense={dense}
        label="Via"
        labelTitle="The specific evidence behind this link - a tunnel interface's name and kind, or the shared subnet prefix - confirmed from both clusters' own routing/address data, never a guess"
      >
        <span title={via}>{via}</span>
      </DetailRow>
      {encryption && (
        <DetailRow
          dense={dense}
          label="Encryption"
          labelTitle="Inferred from the tunnel's driver type alone - WireGuard and IPsec encrypt by design, VXLAN/GRE and similar carry none of their own - never a measurement of a live handshake"
        >
          {encryption === 'plaintext' ? <span className="text-warn">{ENCRYPTION_WORDS[encryption]}</span> : ENCRYPTION_WORDS[encryption]}
        </DetailRow>
      )}
      {nodeA && nodeB && (
        <DetailRow
          dense={dense}
          label="Confirmed by"
          labelTitle="The specific node on each side whose tunnel (or shared subnet) first corroborated this link - named so the evidence points at an actual machine, not only a cluster pair and a driver name"
        >
          {nodeA} ↔ {nodeB}
        </DetailRow>
      )}
      {addressA && addressB && (
        <DetailRow
          dense={dense}
          label="Tunnel addresses"
          labelTitle="The two tunnel interfaces' own addresses that confirmed this link"
        >
          <span title={`${addressA} ↔ ${addressB}`}>{addressA} ↔ {addressB}</span>
        </DetailRow>
      )}
      {redundancy > 1 && (
        <DetailRow
          dense={dense}
          label="Redundancy"
          labelTitle="How many independently corroborating node pairs back this link - more than one means more than one path between these clusters, not a single point of failure"
        >
          {redundancy} independent paths
        </DetailRow>
      )}
      {!!flowsObserved && (
        <DetailRow
          dense={dense}
          label="Flows observed"
          labelTitle="Live dependency flows actually matched onto this link's confirmed tunnel interface(s), scoped by the actual cluster pair on both ends - never by interface name alone"
        >
          {flowsObserved} flow{flowsObserved === 1 ? '' : 's'}
          {avgRttMs !== undefined ? ` \u00b7 ${rttLabel(avgRttMs)} avg RTT` : ''}
          {avgLossPct !== undefined ? ` \u00b7 ${avgLossPct < 10 ? avgLossPct.toFixed(1) : Math.round(avgLossPct)}% avg loss` : ''}
          {!!avgRtoRetransmitsPerMin && (
            <span title="RTO-timer-fired retransmits per minute, averaged across these flows - no ACK at all came back within a full round-trip-plus-backoff, not just ordinary reordering a fast retransmit recovered from instantly. The real sign a link is degrading, which the loss figure above cannot tell apart from on its own.">
              {` \u00b7 ${avgRtoRetransmitsPerMin < 10 ? avgRtoRetransmitsPerMin.toFixed(1) : Math.round(avgRtoRetransmitsPerMin)} RTO/min`}
            </span>
          )}
          {!!avgMssBytes && (
            <span title="Effective segment size actually in use on these flows, averaged - bounded by the plain interface MTU on a healthy direct path, but shrunk further by this tunnel's own encapsulation headers (VXLAN/WireGuard/GRE). A falling value over time is a real, measured sign of growing per-packet overhead on this specific tunnel.">
              {` \u00b7 ${Math.round(avgMssBytes)}B MSS`}
            </span>
          )}
        </DetailRow>
      )}
    </>
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
/** The open modals, oldest first. A dialog opened over another one (the new-operator form over the telemetry wizard) is the only one
 *  that answers Escape and Tab: otherwise one Escape would close both and take the work underneath with it. */
const modalStack: object[] = []

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
  const dismissibleRef = useRef(dismissible)
  useEffect(() => {
    closeRef.current = onClose
    dismissibleRef.current = dismissible
  })
  useEffect(() => {
    if (!open) return
    // Keyboard users must not fall out of a modal: Tab cycles inside it, Escape closes it, and focus goes back to
    // what opened it. (The page behind is hidden from assistive technology by aria-modal.)
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const focusable = () =>
      Array.from(box.current?.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])') ?? []).filter((el) => el.offsetParent !== null)
    if (!box.current?.contains(document.activeElement)) (focusable()[0] ?? box.current)?.focus()
    const me = {}
    modalStack.push(me)
    const onKey = (e: KeyboardEvent) => {
      if (modalStack[modalStack.length - 1] !== me) return
      if (e.key === 'Escape') {
        e.stopPropagation()
        if (dismissibleRef.current) closeRef.current()
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
      modalStack.splice(modalStack.indexOf(me), 1)
      opener?.focus?.()
    }
  }, [open]) // onClose and dismissible are read through refs: a change in either must not re-run this - it would steal focus, and put this dialog back on top of the stack over one opened above it

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
              <X size={ICON_MD} />
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
export function Table({ children, cols, stack, ...p }: ComponentProps<'div'> & { children: ReactNode; cols?: string[]; /** Below the md breakpoint each row becomes a block of its own (a card) instead of a row to scroll sideways: the header goes, and the row lays out its own cells. */ stack?: boolean }) {
  return (
    <div {...p} className="relative overflow-x-auto rounded-xl border border-nb-850 bg-nb-925">
      <table className={clsx('w-full text-left text-sm', cols && 'table-fixed', stack && 'max-md:block max-md:[&_thead]:hidden max-md:[&_tbody]:block max-md:[&_tr]:block')}>
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
  children,
  'data-testid': testId,
}: {
  label: string
  value: string
  sub?: string
  tone?: 'ok' | 'warn'
  bordered?: boolean
  className?: string
  /** Something to show under the figure, such as a status chip. */
  children?: ReactNode
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
      {children && <div className="mt-2">{children}</div>}
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
        <Pill key={i} className="max-w-[9rem] truncate text-xs text-nb-400">{i}</Pill>
      ))}
      {rest > 0 && <span className="text-xs text-nb-500">+{rest}</span>}
    </span>
  )
}

/**
 * A navigable step indicator: numbered circles joined by a line, done/current/upcoming styling, plus an
 * optional failed step (a red mark instead of being folded into "done" - a row of green checkmarks next
 * to "this failed" would tell the opposite story of whatever explains the failure underneath it, and
 * nothing after a failed step is implied to have happened either). Generalized from
 * ConnectClusterWizard's own original Stepper (now migrated onto this) to a plain `currentIndex` a person
 * can move through with their own Back/Next controls, instead of that one's server-driven async `phase`.
 * Meant for any wizard that walks someone through a small number of named steps in order (see the guided
 * telemetry wizard, GuidedWizard.tsx, for the first navigable caller) - purely presentational, the caller
 * owns the step state and which content renders for it.
 */
export function WizardSteps({ steps, currentIndex, failedIndex, testId = 'wizard-steps' }: { steps: string[]; currentIndex: number; failedIndex?: number; testId?: string }) {
  return (
    <div className="@container mb-4 flex items-center" data-testid={testId}>
      {steps.map((label, i) => {
        const failed = i === failedIndex
        const done = !failed && i < currentIndex
        const current = !failed && i === currentIndex
        return (
          <div key={label} className={clsx('flex items-center', i < steps.length - 1 && 'flex-1')}>
            <span className="relative flex size-5 shrink-0 items-center justify-center">
              {current && <span className="absolute inline-flex size-full animate-ping rounded-full bg-accent/40" />}
              <span
                className={clsx(
                  'relative flex size-5 items-center justify-center rounded-full border text-[10px] font-medium',
                  failed
                    ? 'border-bad/50 bg-bad/15 text-bad'
                    : done
                      ? 'border-ok/50 bg-ok/15 text-ok'
                      : current
                        ? 'border-accent bg-accent-soft text-accent'
                        : 'border-nb-800 text-nb-600',
                )}
              >
                {failed ? <X size={ICON_MD} /> : done ? <Check size={ICON_MD} /> : i + 1}
              </span>
            </span>
            {/* Where the row is narrow (a phone, or the column an agent's row opens into) only the current step keeps its words on screen (the others stay for a screen reader): the labels do not fit in a row. */}
            <span aria-current={current ? 'step' : undefined} className={clsx('ml-1.5 whitespace-nowrap text-[11px]', !current && '@max-md:sr-only', failed ? 'text-bad' : done ? 'text-nb-400' : current ? 'text-nb-200' : 'text-nb-600')}>{label}</span>
            {i < steps.length - 1 && <span className={clsx('mx-2 h-px flex-1', done ? 'bg-ok/30' : 'bg-nb-850')} />}
          </div>
        )
      })}
    </div>
  )
}

/** One numbered step of the "run this" screen: the number in a ring, a title, then whatever the step needs. */
export function RunStep({ n, title, children, testId }: { n: number; title: string; children?: ReactNode; testId?: string }) {
  return (
    <li className="flex gap-3" data-testid={testId}>
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full border border-nb-800 bg-nb-930 text-xs font-medium text-nb-300" aria-hidden>{n}</span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-nb-200">{title}</div>
        <div className="text-xs text-nb-500">{children}</div>
      </div>
    </li>
  )
}

/** A tiny inline trend line for a short numeric series (see api.dependencySeries) - not a chart: no
 *  axes, no ticks, no hover, just the shape, meant to sit inline in a detail row the way a Pill does.
 *  An `undefined` entry breaks the line rather than being interpolated across or dropped, so a gap in
 *  the underlying samples (no measured RTT at some snapshot) reads as a real gap, never a smoothed-over
 *  guess. Renders nothing when fewer than two points actually have a value - a single dot says nothing
 *  about a trend. */
export function Sparkline({
  values,
  width = 72,
  height = 20,
  className,
  title,
}: {
  values: (number | undefined)[]
  width?: number
  height?: number
  className?: string
  title?: string
}) {
  const defined = values.filter((v): v is number => v !== undefined)
  if (defined.length < 2 || values.length < 2) return null
  const min = Math.min(...defined)
  const max = Math.max(...defined)
  const span = max - min || 1
  const dx = width / (values.length - 1)
  const toY = (v: number) => height - ((v - min) / span) * (height - 2) - 1
  // Consecutive defined samples join into one segment; an undefined sample ends the current segment
  // instead of being bridged over, so the drawn line never implies a value that was never measured.
  const segments: string[] = []
  let current: string[] = []
  values.forEach((v, i) => {
    if (v === undefined) {
      if (current.length > 1) segments.push(current.join(' '))
      current = []
      return
    }
    current.push(`${i * dx},${toY(v)}`)
  })
  if (current.length > 1) segments.push(current.join(' '))
  return (
    <span title={title}>
      <svg width={width} height={height} className={clsx('shrink-0 overflow-visible text-nb-400', className)}>
        {segments.map((pts, i) => (
          <polyline key={i} points={pts} fill="none" stroke="currentColor" strokeWidth={1.4} strokeLinecap="round" strokeLinejoin="round" />
        ))}
      </svg>
    </span>
  )
}
