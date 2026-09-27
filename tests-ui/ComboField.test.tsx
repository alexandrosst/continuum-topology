import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import { ComboField } from '@/components/ui/primitives'

const OPTIONS = [
  { value: 'AWS', label: 'AWS' },
  { value: 'Azure', label: 'Azure' },
]

/** ComboField is controlled - a real usage always feeds `onChange` back into `value`, so the test wrapper does too. */
function Wrapper({ initial = '' }: { initial?: string }) {
  const [value, setValue] = useState(initial)
  return (
    <>
      <ComboField value={value} onChange={setValue} options={OPTIONS} placeholder="Pick a provider" />
      <output data-testid="value">{value}</output>
    </>
  )
}

describe('ComboField', () => {
  test('shows the dropdown, with the matching option selected, when the value is one of the options', () => {
    render(<Wrapper initial="AWS" />)
    expect(screen.getByRole('combobox')).toHaveTextContent('AWS')
  })

  test('starts in free-text mode when the value is not one of the options, so an uncommon existing value is never hidden', () => {
    render(<Wrapper initial="MyPrivateCloud" />)
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.getByPlaceholderText('Pick a provider')).toHaveValue('MyPrivateCloud')
  })

  test('picking an option from the list calls onChange with its value', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial="AWS" />)
    await user.click(screen.getByRole('combobox'))
    const listbox = screen.getByRole('listbox')
    await user.click(within(listbox).getByRole('option', { name: 'Azure' }))
    expect(screen.getByTestId('value')).toHaveTextContent('Azure')
  })

  test('picking "Other…" switches to a text field with an empty value, not the last selection', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial="AWS" />)
    await user.click(screen.getByRole('combobox'))
    await user.click(screen.getByRole('option', { name: 'Other…' }))
    const input = screen.getByPlaceholderText('Pick a provider')
    expect(input).toHaveValue('')
    await user.type(input, 'Rackspace')
    expect(screen.getByTestId('value')).toHaveTextContent('Rackspace')
  })

  test('"Pick from list" switches back to the dropdown with a cleared value, not the typed text', async () => {
    const user = userEvent.setup()
    render(<Wrapper initial="Rackspace" />)
    await user.click(screen.getByRole('button', { name: 'Pick from list' }))
    expect(screen.getByTestId('value')).toHaveTextContent('')
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })
})
