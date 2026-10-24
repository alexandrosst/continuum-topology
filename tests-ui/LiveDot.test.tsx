import { render } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { FusionDot } from '@/features/fusion/components/useFusion'
import { LiveDot, type LiveKind, Waiting } from '@/components/ui/primitives'

function dot(kind: LiveKind) {
  const { container } = render(<LiveDot kind={kind} />)
  // The pip is a ring of spans around the dot itself: the dot is the innermost.
  return [...container.querySelectorAll('span[class*="rounded-full"]')].at(-1) as HTMLElement
}

describe('LiveDot', () => {
  test('online is green, offline is red, late is amber - and the shape repeats the colour', () => {
    expect(dot('online').className).toContain('bg-ok')
    expect(dot('offline').className).toContain('bg-bad')
    expect(dot('late').className).toContain('bg-warn')
    // Hollow dots are the ones that are not alive: starting (setting up, the info blue) fades, idle is still.
    expect(dot('starting').className).toMatch(/border-info.*animate-pulse|animate-pulse.*border-info/)
    expect(dot('starting').className).not.toContain('bg-')
    expect(dot('idle').className).toContain('border-nb-500')
    expect(dot('idle').className).not.toContain('animate-pulse')
  })

  test('only the online dot sends out a ping ring', () => {
    const ping = (k: LiveKind) => render(<LiveDot kind={k} />).container.querySelectorAll('[class*="animate-ping"]').length
    expect(ping('online')).toBeGreaterThan(0)
    for (const k of ['offline', 'late', 'starting', 'idle'] as const) expect(ping(k)).toBe(0)
  })

  test('FUSION uses the same vocabulary', () => {
    const { container } = render(<FusionDot kind="running" />)
    expect(container.innerHTML).toContain('bg-ok')
    const off = render(<FusionDot kind="off" />).container
    expect(off.innerHTML).toContain('border-nb-500')
  })
})

describe('Waiting', () => {
  test('is a status with the spinner and the words', () => {
    const { getByRole } = render(<Waiting>Waiting for it</Waiting>)
    expect(getByRole('status')).toHaveTextContent('Waiting for it')
    expect(getByRole('status').querySelector('.animate-spin')).not.toBeNull()
  })
})
