import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { HealthWord } from '@/components/telemetry/ArrivalCheck'
import { StatusChip } from '@/components/fusion/StatusChip'
import StateChip from '@/components/operators/StateChip'
import { SHOWN_WORD } from '@/lib/fusionStatus'
import { HEALTH_WORD } from '@/lib/status'

/**
 * The words a person reads for how something is doing. Four screens each carried their own table of them; this pins what each says, in the
 * places it is drawn, so that bringing them into one table changes none of them.
 */

describe('the words for a status', () => {
  test('the one table: healthy, needs attention, not working, setting up, unknown', () => {
    expect(HEALTH_WORD).toEqual({ healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', setup: 'Setting up', unknown: 'Unknown' })
  })

  test('FUSION, which also has a part that is coming up and one that is switched off', () => {
    expect(SHOWN_WORD).toEqual({ healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', unknown: 'Unknown', starting: 'Starting', off: 'Off' })
  })
})

describe('the words as they are drawn', () => {
  test.each([
    ['healthy', 'Healthy'],
    ['attention', 'Needs attention'],
    ['down', 'Not working'],
    ['setup', 'Setting up'],
    ['unknown', 'Unknown'],
  ] as const)('the operators chip for %s says %s', (state, word) => {
    const { container, unmount } = render(<StateChip state={state} />)
    expect(container.querySelector('[data-state]')).toHaveAttribute('data-state', state)
    expect(container).toHaveTextContent(word)
    unmount()
  })

  test('the operators chip with a count says how many are in that state', () => {
    render(<StateChip state="attention" count={3} />)
    expect(screen.getByText('3 needs attention')).toBeInTheDocument()
  })

  test.each([
    ['healthy', 'Healthy'],
    ['attention', 'Needs attention'],
    ['down', 'Not working'],
    ['unknown', 'Unknown'],
    ['starting', 'Starting'],
    ['off', 'Off'],
  ] as const)('the FUSION chip for %s says %s', (status, word) => {
    const { container, unmount } = render(<StatusChip status={status} />)
    expect(container.querySelector('[data-status]')).toHaveAttribute('data-status', status)
    expect(container).toHaveTextContent(word)
    unmount()
  })

  test.each([
    ['healthy', 'Healthy'],
    ['attention', 'Needs attention'],
    ['down', 'Not working'],
    ['unknown', 'Unknown'],
  ] as const)('the word beside the arrival check for %s says %s', (health, word) => {
    const { container, unmount } = render(<HealthWord health={health} />)
    expect(container.querySelector('[data-health]')).toHaveAttribute('data-health', health)
    expect(container).toHaveTextContent(word)
    unmount()
  })
})
