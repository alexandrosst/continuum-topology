import { render } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { Sparkline } from '@/components/ui/primitives'

describe('Sparkline', () => {
  test('renders nothing for fewer than two defined points', () => {
    expect(render(<Sparkline values={[]} />).container.querySelector('svg')).toBeNull()
    expect(render(<Sparkline values={[1]} />).container.querySelector('svg')).toBeNull()
    expect(render(<Sparkline values={[1, undefined, undefined]} />).container.querySelector('svg')).toBeNull()
  })

  test('draws one polyline segment through consecutive defined points', () => {
    const { container } = render(<Sparkline values={[1, 2, 3]} width={30} height={10} />)
    const lines = container.querySelectorAll('polyline')
    expect(lines).toHaveLength(1)
    expect(lines[0].getAttribute('points')?.split(' ')).toHaveLength(3)
  })

  test('starts a new segment after a gap instead of bridging across the missing sample', () => {
    const { container } = render(<Sparkline values={[1, undefined, 3, 4]} width={30} height={10} />)
    const lines = container.querySelectorAll('polyline')
    // Index 1 is missing, so [0] is a lone point (dropped: a segment needs >=2) and [2,3] forms the one
    // drawn segment.
    expect(lines).toHaveLength(1)
    expect(lines[0].getAttribute('points')?.split(' ')).toHaveLength(2)
  })

  test('passes the title through to the wrapping element, since <svg title> is not a valid React prop', () => {
    const { container } = render(<Sparkline values={[1, 2]} title="Round trip, last 24h" />)
    expect(container.querySelector('[title="Round trip, last 24h"]')).not.toBeNull()
  })
})
