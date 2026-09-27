import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import { LabelsEditor } from '@/components/ui/primitives'

describe('LabelsEditor', () => {
  test('renders one key/value row per entry already there', () => {
    render(<LabelsEditor value={{ env: 'prod', team: 'ml' }} onChange={() => {}} />)
    const keys = screen.getAllByLabelText('Label key').map((el) => (el as HTMLInputElement).value)
    const values = screen.getAllByLabelText('Label value').map((el) => (el as HTMLInputElement).value)
    expect(keys).toEqual(['env', 'team'])
    expect(values).toEqual(['prod', 'ml'])
  })

  test('typing a new pair\'s key and value reports the full record, not just the one row', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<LabelsEditor value={{ env: 'prod' }} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: /add label/i }))
    const [, secondKey] = screen.getAllByLabelText('Label key')
    const [, secondValue] = screen.getAllByLabelText('Label value')
    await user.type(secondKey, 'tier')
    await user.type(secondValue, 'backend')
    expect(onChange).toHaveBeenLastCalledWith({ env: 'prod', tier: 'backend' })
  })

  test('removing a row drops it from the reported record', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<LabelsEditor value={{ env: 'prod', team: 'ml' }} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: /remove env/i }))
    expect(onChange).toHaveBeenLastCalledWith({ team: 'ml' })
    expect(screen.getAllByLabelText('Label key')).toHaveLength(1)
  })

  test('a row with an emptied key is left out of the record (it is not a real pair yet)', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<LabelsEditor value={{}} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: /add label/i }))
    await user.type(screen.getByLabelText('Label value'), 'prod')
    expect(onChange).toHaveBeenLastCalledWith({})
  })
})
