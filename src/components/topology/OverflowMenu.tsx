import type { LucideIcon } from 'lucide-react'
import { MoreHorizontal } from 'lucide-react'
import { useEffect, useRef, type KeyboardEvent } from 'react'
import { buttonClass } from '@/components/ui/buttonClass'
import { ICON_SM, MenuPanel } from '@/components/ui/primitives'

export interface OverflowItem {
  key: string
  label: string
  icon: LucideIcon
  onSelect: () => void
  /** Shown but not choosable; `title` says why. */
  disabled?: boolean
  title?: string
  testId?: string
}

/**
 * The toolbar's "..." menu: the actions a person reaches for now and then (reset the layout, save a picture, pick from the canvas)
 * rather than every visit. A MenuPanel like the other toolbar popovers, and a real menu for the keyboard and screen readers: it opens
 * on the first choice, the arrow keys, Home and End move between choices, Escape closes it and returns to the button, and a choice
 * closes it. The page owns whether it is open (one toolbar popover at a time).
 */
export default function OverflowMenu({ open, onToggle, onClose, items, label = 'More actions', testId }: { open: boolean; onToggle: () => void; onClose: () => void; items: OverflowItem[]; label?: string; testId?: string }) {
  const trigger = useRef<HTMLButtonElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const choices = () => [...(panel.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? [])]
  useEffect(() => {
    if (open) choices()[0]?.focus()
  }, [open])
  const close = () => {
    onClose()
    trigger.current?.focus()
  }
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      return close()
    }
    const all = choices()
    const at = all.indexOf(document.activeElement as HTMLButtonElement)
    const to = e.key === 'ArrowDown' ? (at + 1) % all.length : e.key === 'ArrowUp' ? (at <= 0 ? all.length - 1 : at - 1) : e.key === 'Home' ? 0 : e.key === 'End' ? all.length - 1 : -1
    if (to < 0 || all.length === 0) return
    e.preventDefault()
    all[to].focus()
  }
  return (
    <div className="relative">
      <button
        type="button"
        ref={trigger}
        className={buttonClass('secondary', 'md', 'px-2.5')}
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={onToggle}
        data-testid={testId}
      >
        <MoreHorizontal size={ICON_SM} aria-hidden />
      </button>
      <MenuPanel open={open} onClose={onClose} role="menu" aria-label={label} className="w-56 overflow-hidden p-1" onKeyDown={onKeyDown} ref={panel}>
        {items.map(({ key, label: text, icon: Icon, onSelect, disabled, title, testId: id }) => (
          <button
            key={key}
            type="button"
            role="menuitem"
            disabled={disabled}
            title={title}
            data-testid={id}
            onClick={() => {
              onClose()
              trigger.current?.focus()
              onSelect()
            }}
            className="flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm text-nb-300 hover:bg-nb-940 focus-visible:bg-nb-940 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent"
          >
            <Icon size={ICON_SM} className="text-nb-500" aria-hidden /> {text}
          </button>
        ))}
      </MenuPanel>
    </div>
  )
}
