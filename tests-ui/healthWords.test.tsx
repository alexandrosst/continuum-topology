import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { HealthWord } from '@/components/telemetry/ArrivalCheck'
import { StatusChip } from '@/components/fusion/StatusChip'
import StateChip from '@/components/operators/StateChip'
import { HEALTH_WORD as FUSION_WORD } from '@/lib/fusionStatus'
import { STATE_WORD } from '@/lib/operatorsView'
import { PLATFORM_STATUS_WORD } from '@/lib/platformLayer'
import { HEALTH_WORD as ARRIVAL_WORD } from '@/lib/telemetrySetup'

/**
 * The words a person reads for how something is doing. Four screens each carried their own table of them; this pins what each says, in the
 * places it is drawn, so that bringing them into one table changes none of them.
 */

describe('the words for a status', () => {
  test('the platform layer', () => {
    expect(PLATFORM_STATUS_WORD).toEqual({ healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', unknown: 'Unknown' })
  })

  test('the operators view, which also has a part that is being set up', () => {
    expect(STATE_WORD).toEqual({ healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', setup: 'Setting up', unknown: 'Unknown' })
  })

  test('the check that data arrives', () => {
    expect(ARRIVAL_WORD).toEqual({ healthy: 'Healthy', attention: 'Needs attention', broken: 'Not working', unknown: 'Unknown' })
  })

  test('FUSION, which also has a part that is coming up and one that is switched off', () => {
    expect(FUSION_WORD).toEqual({ healthy: 'Healthy', attention: 'Needs attention', broken: 'Not working', unknown: 'Unknown', starting: 'Starting', off: 'Off' })
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
    ['broken', 'Not working'],
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
    ['broken', 'Not working'],
    ['unknown', 'Unknown'],
  ] as const)('the word beside the arrival check for %s says %s', (health, word) => {
    const { container, unmount } = render(<HealthWord health={health} />)
    expect(container.querySelector('[data-health]')).toHaveAttribute('data-health', health)
    expect(container).toHaveTextContent(word)
    unmount()
  })
})
