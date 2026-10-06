import clsx from 'clsx'
import { MoreHorizontal } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { buttonClass } from '@/components/ui/buttonClass'
import { ICON_MD, MenuPanel } from '@/components/ui/primitives'

export interface RowMenuItem {
  key: string
  label: ReactNode
  onSelect: () => void
  danger?: boolean
  testId?: string
}

const PANEL_HEIGHT = 44 + 36 * 6

/**
 * A row's actions in a MenuPanel. A table scrolls sideways inside its own box, which would cut a menu that hangs from a cell, so the
 * panel is drawn on the page itself, at the button. It closes on a choice, a click outside, Escape and any scroll (a menu that stays
 * while what it belongs to moves away points at nothing).
 */
export default function RowMenu({ ariaLabel, items, children, testId }: { ariaLabel: string; items: RowMenuItem[]; children?: ReactNode; testId?: string }) {
  const [at, setAt] = useState<{ top: number; left: number } | null>(null)
  const button = useRef<HTMLButtonElement>(null)
  const open = at !== null
  const close = () => setAt(null)
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        setAt(null)
        button.current?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    window.addEventListener('scroll', close, true)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', close, true)
    }
  }, [open])
  const toggle = () => {
    if (open) return close()
    const r = button.current?.getBoundingClientRect()
    if (!r) return
    // Anchored at the button's right edge; pulled up when the panel would run off the bottom of the window.
    setAt({ top: Math.max(8, Math.min(r.top, window.innerHeight - PANEL_HEIGHT)), left: r.right })
  }
  return (
    <>
      <button type="button" ref={button} className={buttonClass(children ? 'secondary' : 'ghost', 'sm')} aria-label={ariaLabel} aria-haspopup="menu" aria-expanded={open} onClick={toggle} data-testid={testId}>
        {children ?? <MoreHorizontal size={ICON_MD} aria-hidden />}
      </button>
      {open && createPortal(
        <div className="fixed z-[41] size-0" style={{ top: at.top, left: at.left }}>
          <MenuPanel open onClose={close} role="menu" aria-label={ariaLabel} className="w-60 overflow-hidden p-1">
            {items.map((it) => (
              <button
                key={it.key}
                type="button"
                role="menuitem"
                className={clsx('block w-full rounded-md px-3 py-2 text-left text-sm hover:bg-nb-940 focus-visible:bg-nb-940', it.danger ? 'text-bad' : 'text-nb-300')}
                data-testid={it.testId}
                onClick={() => {
                  close()
                  // Back on the row's own button first: a dialog the choice opens remembers what had focus and returns it there when it
                  // closes, and this item is gone by then.
                  button.current?.focus()
                  it.onSelect()
                }}
              >
                {it.label}
              </button>
            ))}
          </MenuPanel>
        </div>,
        document.body,
      )}
    </>
  )
}
