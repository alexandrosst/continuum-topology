import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import FilterMenu from '@/components/topology/FilterMenu'

describe('FilterMenu · platform', () => {
  const base = { open: true, onOpenChange: () => {}, filter: { clusters: [], apps: [], kinds: [] }, clusters: [], applications: [], onChange: () => {} }

  test('Problems only appears only while the platform layer is on, and counts as a filter', () => {
    const { rerender } = render(<FilterMenu {...base} />)
    expect(screen.queryByTestId('filter-platform-problems')).toBeNull()
    const onChange = vi.fn()
    rerender(<FilterMenu {...base} platform={{ problemsOnly: false, onChange }} />)
    fireEvent.click(screen.getByTestId('filter-platform-problems'))
    expect(onChange).toHaveBeenCalledWith(true)
    rerender(<FilterMenu {...base} platform={{ problemsOnly: true, onChange }} />)
    expect(screen.getByTestId('filter-count').textContent).toBe('1')
  })
})
