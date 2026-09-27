import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import ColumnPicker from '@/components/ColumnPicker'
import type { ColumnDef } from '@/lib/columns'

const COLUMNS: ColumnDef[] = [
  { key: 'cluster', label: 'Cluster' },
  { key: 'ip', label: 'IP' },
]

describe('ColumnPicker', () => {
  test('shows no hidden-count badge when every column is visible', () => {
    render(<ColumnPicker columns={COLUMNS} isVisible={() => true} onToggle={() => {}} />)
    expect(screen.queryByTestId('columns-hidden-count')).not.toBeInTheDocument()
  })

  test('counts hidden columns in the badge', () => {
    render(<ColumnPicker columns={COLUMNS} isVisible={(k) => k !== 'ip'} onToggle={() => {}} />)
    expect(screen.getByTestId('columns-hidden-count')).toHaveTextContent('1 hidden')
  })

  test('opening the picker lists one checkbox per column, checked to match isVisible', async () => {
    const user = userEvent.setup()
    render(<ColumnPicker columns={COLUMNS} isVisible={(k) => k !== 'ip'} onToggle={() => {}} />)
    await user.click(screen.getByTestId('columns-button'))
    expect(screen.getByTestId('column-cluster')).toBeChecked()
    expect(screen.getByTestId('column-ip')).not.toBeChecked()
  })

  test('clicking a column\'s checkbox reports that column\'s key, not its checked state', async () => {
    const user = userEvent.setup()
    const onToggle = vi.fn()
    render(<ColumnPicker columns={COLUMNS} isVisible={() => true} onToggle={onToggle} />)
    await user.click(screen.getByTestId('columns-button'))
    await user.click(screen.getByTestId('column-ip'))
    expect(onToggle).toHaveBeenCalledWith('ip')
  })
})
