import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import FilterMenu from '@/components/topology/FilterMenu'
import type { Filter } from '@/lib/filter'

const chosen = { clusters: ['c1'], apps: [], kinds: [] } as unknown as Filter
const props = { open: false, onOpenChange: () => {}, clusters: [], applications: [], onChange: () => {} }

describe('FilterMenu button', () => {
  test('counts what is chosen, and shows no count when there is nothing to filter (disabled)', () => {
    const { rerender } = render(<FilterMenu {...props} filter={chosen} />)
    expect(screen.getByTestId('filter-count')).toHaveTextContent('1')
    rerender(<FilterMenu {...props} filter={chosen} disabled />)
    expect(screen.queryByTestId('filter-count')).toBeNull()
  })
})
