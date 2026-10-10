import { fireEvent, render, screen } from '@testing-library/react'
import { Download, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { describe, expect, test, vi } from 'vitest'
import OverflowMenu, { type OverflowItem } from '@/components/topology/OverflowMenu'

function Harness({ items }: { items: OverflowItem[] }) {
  const [open, setOpen] = useState(false)
  return <OverflowMenu open={open} onToggle={() => setOpen((v) => !v)} onClose={() => setOpen(false)} items={items} label="More canvas actions" testId="more" />
}

const items = (over: Partial<OverflowItem> = {}): OverflowItem[] => [
  { key: 'reset', label: 'Reset layout', icon: RotateCcw, onSelect: () => {}, testId: 'reset' },
  { key: 'png', label: 'Export PNG', icon: Download, onSelect: () => {}, testId: 'png', ...over },
]

describe('OverflowMenu', () => {
  test('the button has a name and says it opens a menu; the menu and its choices are a real menu', () => {
    render(<Harness items={items()} />)
    const button = screen.getByRole('button', { name: 'More canvas actions' })
    expect(button).toHaveAttribute('aria-haspopup', 'menu')
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('menu')).toBeNull()
    fireEvent.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('menu', { name: 'More canvas actions' })).toBeInTheDocument()
    expect(screen.getAllByRole('menuitem').map((m) => m.textContent?.trim())).toEqual(['Reset layout', 'Export PNG'])
  })

  test('opens on the first choice; the arrow keys, Home and End move between choices and wrap', () => {
    render(<Harness items={items()} />)
    fireEvent.click(screen.getByTestId('more'))
    const [first, second] = screen.getAllByRole('menuitem')
    expect(first).toHaveFocus()
    fireEvent.keyDown(first, { key: 'ArrowDown' })
    expect(second).toHaveFocus()
    fireEvent.keyDown(second, { key: 'ArrowDown' })
    expect(first).toHaveFocus()
    fireEvent.keyDown(first, { key: 'ArrowUp' })
    expect(second).toHaveFocus()
    fireEvent.keyDown(second, { key: 'Home' })
    expect(first).toHaveFocus()
    fireEvent.keyDown(first, { key: 'End' })
    expect(second).toHaveFocus()
  })

  test('a choice that cannot be made is skipped by the arrow keys and cannot be clicked; the others are unaffected', () => {
    const onSelect = vi.fn()
    render(<Harness items={items({ disabled: true, onSelect })} />)
    fireEvent.click(screen.getByTestId('more'))
    expect(screen.getByTestId('png')).toBeDisabled()
    const first = screen.getByTestId('reset')
    expect(first).toHaveFocus()
    fireEvent.keyDown(first, { key: 'ArrowDown' })
    expect(first).toHaveFocus()
    fireEvent.click(screen.getByTestId('png'))
    expect(onSelect).not.toHaveBeenCalled()
  })

  test('Escape closes the menu and returns to the button; a choice closes it, returns there and runs', () => {
    const onSelect = vi.fn()
    render(<Harness items={items({ onSelect })} />)
    const button = screen.getByTestId('more')
    fireEvent.click(button)
    fireEvent.keyDown(screen.getByTestId('reset'), { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(button).toHaveFocus()
    fireEvent.click(button)
    fireEvent.click(screen.getByTestId('png'))
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).toBeNull()
    expect(button).toHaveFocus()
  })
})
