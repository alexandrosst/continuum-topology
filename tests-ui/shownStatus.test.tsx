import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { StatusMark } from '@/components/topology/StatusMark'
import { StatusDot } from '@/components/ui/primitives'
import { shownStatus } from '@/lib/detail'
import { STATUS_COLOR } from '@/lib/types'

describe('shownStatus: one state, one colour, one word', () => {
  test('a thing that reports well and has nothing wrong inside is healthy', () => {
    expect(shownStatus('healthy')).toEqual({ state: 'healthy', color: STATUS_COLOR.healthy, word: 'Healthy' })
  })

  test('a thing that reports well but has something wrong inside is not green', () => {
    expect(shownStatus('healthy', 'warn')).toEqual({ state: 'attention', color: STATUS_COLOR.degraded, word: 'Needs attention' })
    expect(shownStatus('healthy', 'bad')).toEqual({ state: 'down', color: STATUS_COLOR.offline, word: 'Not working' })
    expect(shownStatus('unknown', 'bad').word).toBe('Not working')
  })

  test('what a thing reports about itself is one of the four words, never its own', () => {
    expect(shownStatus('offline', 'warn')).toEqual({ state: 'down', color: STATUS_COLOR.offline, word: 'Not working' })
    expect(shownStatus('degraded', 'bad')).toEqual({ state: 'attention', color: STATUS_COLOR.degraded, word: 'Needs attention' })
    expect(shownStatus('unknown').word).toBe('Unknown')
  })

  test('the Inspector dot says the same as the card', () => {
    render(<StatusDot status="healthy" alert="bad" withLabel />)
    expect(screen.getByText('Not working')).toBeTruthy()
    expect(screen.queryByText('Healthy')).toBeNull()
  })
})

describe('StatusMark: a shape and a word, never the colour alone', () => {
  test.each([
    ['healthy', 'Healthy'],
    ['attention', 'Needs attention'],
    ['down', 'Not working'],
    ['unknown', 'Unknown'],
  ] as const)('%s is announced as "%s" and carries a glyph of its own', (state, word) => {
    const { container } = render(<StatusMark state={state} />)
    const mark = screen.getByRole('img', { name: word })
    expect(mark).toHaveAttribute('data-status', state)
    // the shape differs by state: an svg for the two that need attention, a plain dot or ring for the others
    expect(container.querySelectorAll('svg').length).toBe(state === 'attention' || state === 'down' ? 1 : 0)
  })
})
