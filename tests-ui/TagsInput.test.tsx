import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test, vi } from 'vitest'
import { TagsInput } from '@/components/ui/primitives'

/** TagsInput calls onChange with the next array on every edit; a real caller feeds that back in as `value`,
 *  so the wrapper does too - otherwise typing a second tag would always start from the initial value. */
function Wrapper({ initial = [] as string[], onChange }: { initial?: string[]; onChange?: (v: string[]) => void }) {
  const [value, setValue] = useState(initial)
  return <TagsInput value={value} onChange={(v) => { setValue(v); onChange?.(v) }} placeholder="namespace" />
}

describe('TagsInput', () => {
  test('shows each existing entry as its own chip', () => {
    render(<Wrapper initial={['shop', 'payments']} />)
    expect(screen.getByText('shop')).toBeInTheDocument()
    expect(screen.getByText('payments')).toBeInTheDocument()
  })

  test('a trailing space commits the typed word as a new tag and clears the draft', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Wrapper onChange={onChange} />)
    await user.type(screen.getByRole('textbox'), 'shop ')
    expect(onChange).toHaveBeenLastCalledWith(['shop'])
    expect(screen.getByRole('textbox')).toHaveValue('')
  })

  test('typing "a, b c," produces three separate tags, matching how the old comma/space text field parsed', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Wrapper onChange={onChange} />)
    await user.type(screen.getByRole('textbox'), 'a, b c,')
    expect(onChange).toHaveBeenLastCalledWith(['a', 'b', 'c'])
  })

  test('Enter commits whatever is typed even with no trailing separator', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Wrapper onChange={onChange} />)
    await user.type(screen.getByRole('textbox'), 'hr-data{Enter}')
    expect(onChange).toHaveBeenLastCalledWith(['hr-data'])
  })

  test('clicking a chip\'s remove button drops just that tag', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Wrapper initial={['shop', 'payments']} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: 'Remove shop' }))
    expect(onChange).toHaveBeenLastCalledWith(['payments'])
  })

  test('Backspace on an empty draft removes the last tag, the same shortcut most chip inputs use', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Wrapper initial={['shop', 'payments']} onChange={onChange} />)
    screen.getByRole('textbox').focus()
    await user.keyboard('{Backspace}')
    expect(onChange).toHaveBeenLastCalledWith(['shop'])
  })

  test('the placeholder shows when there are no tags yet', () => {
    render(<Wrapper initial={[]} />)
    expect(screen.getByRole('textbox')).toHaveAttribute('placeholder', 'namespace')
  })

  test('the placeholder is gone once there is at least one tag', () => {
    render(<Wrapper initial={['shop']} />)
    expect(screen.getByRole('textbox')).not.toHaveAttribute('placeholder')
  })
})
