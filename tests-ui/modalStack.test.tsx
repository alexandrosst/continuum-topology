import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import { Button, Modal } from '@/components/ui/primitives'

function Stacked() {
  const [outer, setOuter] = useState(true)
  const [inner, setInner] = useState(false)
  return (
    <>
      <Modal open={outer} onClose={() => setOuter(false)} title="Outer">
        <Button onClick={() => setInner(true)}>Open inner</Button>
      </Modal>
      <Modal open={inner} onClose={() => setInner(false)} title="Inner">inner body</Modal>
    </>
  )
}

describe('Modal over Modal', () => {
  test('Escape closes only the dialog on top, so the work underneath is not taken with it', async () => {
    const user = userEvent.setup()
    render(<Stacked />)
    await user.click(screen.getByRole('button', { name: 'Open inner' }))
    expect(screen.getByRole('dialog', { name: 'Inner' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Inner' })).not.toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: 'Outer' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Outer' })).not.toBeInTheDocument()
  })
})

function StackedToggle() {
  const [inner, setInner] = useState(false)
  const [busy, setBusy] = useState(false)
  return (
    <>
      <Modal open onClose={() => undefined} title="Outer" dismissible={!busy}>
        <Button onClick={() => setInner(true)}>Open inner</Button>
        <Button onClick={() => setBusy((b) => !b)}>Toggle busy</Button>
      </Modal>
      <Modal open={inner} onClose={() => setInner(false)} title="Inner">inner body</Modal>
    </>
  )
}

describe('Modal stack order', () => {
  test('an outer dialog that re-renders with a new dismissible does not climb back over the one opened above it', async () => {
    const user = userEvent.setup()
    render(<StackedToggle />)
    await user.click(screen.getByRole('button', { name: 'Open inner' }))
    // The outer dialog is re-rendered underneath (a busy flag flips) while the inner one is open.
    await user.click(screen.getByRole('button', { name: 'Toggle busy' }), { pointerEventsCheck: 0 })
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Inner' })).not.toBeInTheDocument()
  })
})
