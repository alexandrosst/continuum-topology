import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { StatusDot } from '@/components/ui/primitives'
import { shownStatus } from '@/lib/detail'
import { STATUS_COLOR } from '@/lib/types'

describe('shownStatus: one state, one colour, one word', () => {
  test('a thing that reports well and has nothing wrong inside is healthy', () => {
    expect(shownStatus('healthy')).toEqual({ color: STATUS_COLOR.healthy, word: 'healthy' })
  })

  test('a thing that reports well but has something wrong inside is not green', () => {
    expect(shownStatus('healthy', 'warn')).toEqual({ color: STATUS_COLOR.degraded, word: 'needs a look' })
    expect(shownStatus('healthy', 'bad')).toEqual({ color: STATUS_COLOR.offline, word: 'broken' })
    expect(shownStatus('unknown', 'bad').word).toBe('broken')
  })

  test('what a thing reports about itself keeps its own words', () => {
    expect(shownStatus('offline', 'warn')).toEqual({ color: STATUS_COLOR.offline, word: 'offline' })
    expect(shownStatus('degraded', 'bad')).toEqual({ color: STATUS_COLOR.degraded, word: 'degraded' })
  })

  test('the Inspector dot says the same as the card', () => {
    render(<StatusDot status="healthy" alert="bad" withLabel />)
    expect(screen.getByText('broken')).toBeTruthy()
    expect(screen.queryByText('healthy')).toBeNull()
  })
})
