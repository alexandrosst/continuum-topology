import clsx from 'clsx'
import { useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'

export interface TabItem<T extends string | boolean> {
  id: T
  label: ReactNode
  icon?: ReactNode
  /** A small number beside the label (how many there are in it). */
  count?: number | string
  testId?: string
  /** The tab's tooltip. */
  title?: string
}

/** The class of the active/idle tab of the underline style; exported so a row of links (FUSION's sections) can look the same as a row of buttons. */
export const tabClass = (active: boolean): string => clsx('relative flex items-center gap-1.5 whitespace-nowrap px-3 py-2 text-sm transition-colors duration-[var(--dur-fast)] focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent', active ? 'text-nb-300' : 'text-nb-400 hover:text-nb-300')

/**
 * The one tab control. `underline` is for views of one page (the accent line slides to the tab you picked); `segmented` is the small
 * switch for two or three ways of looking at the same thing (List / Map). Both are a real tablist: arrow keys, Home and End move
 * between the tabs, only the open tab is in the tab order, and each tab names the panel it opens (`TabPanel`, with the same `idBase`).
 */
export function Tabs<T extends string | boolean>({ items, value, onChange, label, idBase, variant = 'underline', className }: { items: TabItem<T>[]; value: T; onChange: (id: T) => void; label: string; idBase?: string; variant?: 'underline' | 'segmented'; className?: string }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const listRef = useRef<HTMLDivElement>(null)
  const [bar, setBar] = useState<{ left: number; width: number } | null>(null)
  const at = Math.max(0, items.findIndex((i) => i.id === value))

  // The sliding line: measured from the open tab, re-measured when the tab or the labels change.
  useLayoutEffect(() => {
    if (variant !== 'underline') return
    const el = refs.current[at]
    if (!el || el.offsetWidth === 0) return
    setBar({ left: el.offsetLeft, width: el.offsetWidth })
  }, [at, variant, items])

  const move = (e: KeyboardEvent, from: number) => {
    const last = items.length - 1
    const to = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? (from === last ? 0 : from + 1) : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? (from === 0 ? last : from - 1) : e.key === 'Home' ? 0 : e.key === 'End' ? last : -1
    if (to < 0) return
    e.preventDefault()
    refs.current[to]?.focus()
    onChange(items[to].id)
  }

  return (
    <div
      ref={listRef}
      role="tablist"
      aria-label={label}
      className={clsx(variant === 'underline' ? 'relative flex flex-wrap border-b border-nb-850' : 'flex w-fit overflow-hidden rounded-md border border-nb-850', className)}
    >
      {items.map((t, i) => {
        const active = i === at
        return (
          <button
            key={String(t.id)}
            ref={(el) => { refs.current[i] = el }}
            type="button"
            role="tab"
            id={idBase ? `${idBase}-tab-${String(t.id)}` : undefined}
            aria-controls={idBase ? `${idBase}-panel-${String(t.id)}` : undefined}
            aria-selected={active}
            tabIndex={active ? 0 : -1}
            title={t.title}
            onClick={() => onChange(t.id)}
            onKeyDown={(e) => move(e, i)}
            data-testid={t.testId}
            className={variant === 'underline' ? tabClass(active) : clsx('flex items-center gap-1.5 px-3 py-1.5 text-sm transition-colors duration-[var(--dur-fast)]', active ? 'bg-nb-940 text-nb-300' : 'text-nb-400 hover:text-nb-300')}
          >
            {t.icon}
            {t.label}
            {t.count !== undefined && <span className="rounded-full bg-nb-930 px-1.5 text-[11px] leading-4 tabular-nums text-nb-500">{t.count}</span>}
          </button>
        )
      })}
      {variant === 'underline' && bar && (
        <span aria-hidden className="tab-bar pointer-events-none absolute -bottom-px h-0.5 rounded-full bg-accent" style={{ left: 0, width: bar.width, transform: `translateX(${bar.left}px)` }} />
      )}
    </div>
  )
}

/** The panel a tab opens. Only the open one is in the page's tree. */
export function TabPanel({ idBase, id, active, children, className }: { idBase: string; id: string; active: boolean; children: ReactNode; className?: string }) {
  if (!active) return null
  return (
    <div role="tabpanel" id={`${idBase}-panel-${id}`} aria-labelledby={`${idBase}-tab-${id}`} className={clsx('tab-panel', className)}>
      {children}
    </div>
  )
}
