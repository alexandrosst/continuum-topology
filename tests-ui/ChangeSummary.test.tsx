import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import ChangeSummary from '@/components/telemetry/ChangeSummary'

describe('ChangeSummary', () => {
  test('lists what the command changes, one line each', () => {
    render(<ChangeSummary changes={['Turns on Traces', 'Sends to a instead of b']} installed testId="cs" />)
    expect(screen.getByTestId('cs')).toHaveTextContent('This command will change:')
    expect(screen.getByTestId('cs-list').querySelectorAll('li')).toHaveLength(2)
  })

  test('on an install, no change says so; on a fresh install with nothing to compare it says nothing', () => {
    const { rerender } = render(<ChangeSummary changes={[]} installed testId="cs" />)
    expect(screen.getByTestId('cs')).toHaveTextContent('changes nothing the install is known to have')
    rerender(<ChangeSummary changes={[]} installed={false} testId="cs" />)
    expect(screen.queryByTestId('cs')).not.toBeInTheDocument()
  })
})
