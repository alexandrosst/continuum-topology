import { render } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { FusionDot } from '@/components/fusion/useFusion'
import { LiveDot, type LiveKind, Waiting } from '@/components/ui/primitives'

function dot(kind: LiveKind) {
  const { container } = render(<LiveDot kind={kind} />)
  return container.querySelector('span[class*="rounded-full"]') as HTMLElement
}

describe('LiveDot', () => {
  test('online is green, offline is red, late is amber - and the shape repeats the colour', () => {
    expect(dot('online').className).toContain('bg-ok')
    expect(dot('offline').className).toContain('bg-bad')
    expect(dot('late').className).toContain('bg-warn')
    // Hollow dots are the ones that are not alive: starting fades, idle is still.
    expect(dot('starting').className).toMatch(/border-warn.*animate-pulse|animate-pulse.*border-warn/)
    expect(dot('starting').className).not.toContain('bg-')
    expect(dot('idle').className).toContain('border-nb-500')
    expect(dot('idle').className).not.toContain('animate-pulse')
  })

  test('only the online dot sends out a ping ring', () => {
    const ping = (k: LiveKind) => render(<LiveDot kind={k} />).container.querySelectorAll('.animate-ping').length
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
