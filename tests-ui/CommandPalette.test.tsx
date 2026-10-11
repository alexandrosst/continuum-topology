import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, test } from 'vitest'
import CommandPalette from '@/components/CommandPalette'

function Harness() {
  const [open, setOpen] = useState(false)
  return (
    <MemoryRouter>
      <button onClick={() => setOpen(true)}>Open search</button>
      <button>Behind</button>
      <CommandPalette open={open} onClose={() => setOpen(false)} />
    </MemoryRouter>
  )
}

describe('CommandPalette as a dialog', () => {
  test('focus moves in, Tab stays inside, Escape closes from anywhere, focus returns to the opener', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    const opener = screen.getByRole('button', { name: 'Open search' })
    await user.click(opener)
    const input = screen.getByRole('combobox')
    expect(input).toHaveFocus()

    await user.tab()
    expect(input).toHaveFocus()
    await user.tab({ shift: true })
    expect(input).toHaveFocus()

    // Escape must work with focus somewhere other than the input (a click on the dialog's own text, say).
    ;(document.activeElement as HTMLElement).blur()
    expect(document.body).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
  })
})
