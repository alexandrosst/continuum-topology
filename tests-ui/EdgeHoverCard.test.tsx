import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import EdgeHoverCard from '@/components/topology/EdgeHoverCard'
import type { EdgeData, TopoEdge } from '@/lib/graph'

// Minimal fixture: a real DOM element stands in for `host` (jsdom gives any element a defined-but-zeroed
// getBoundingClientRect, which is all this component needs to clear its `!box` early return) and `edge`
// only needs the fields EdgeHoverCard actually reads off `.data`.
function makeEdge(data: Partial<EdgeData>): TopoEdge {
  return {
    id: 'e1',
    source: 'a',
    target: 'b',
    data: { crossGroup: false, aggregated: false, from: 'a', to: 'b', ...data } as EdgeData,
  }
}

const pos = { cx: 100, cy: 100 }

describe('EdgeHoverCard · SNI and DNS titles', () => {
  // Pins the fix: both rows are `truncate`d for layout, so the only way to recover the full value on a
  // long hostname or domain list is the title attribute - this asserts it is actually there and matches
  // the untruncated value, not just whatever the row happens to show.
  test('TLS server name row carries the full hostname as its title', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', sniHost: 'checkout.example.com' })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    const dd = screen.getByText('checkout.example.com')
    expect(dd.getAttribute('title')).toBe('checkout.example.com')
  })

  test('DNS queries row titles the full, untruncated list even when the visible text is shortened', () => {
    const names = ['a.example.com', 'b.example.com', 'c.example.com']
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', dnsQueryNames: names })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    // Visible text is truncated to the first two plus a "+N more" suffix; the title must still carry all three.
    const dd = screen.getByText('a.example.com, b.example.com +1 more')
    expect(dd.getAttribute('title')).toBe(names.join(', '))
  })

  test('neither row renders when the edge is conntrack-only, even with the fields present', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'conntrack', sniHost: 'should-not-show.example.com', dnsQueryNames: ['nope.example.com'] })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.queryByText('should-not-show.example.com')).toBeNull()
    expect(screen.queryByText(/nope\.example\.com/)).toBeNull()
  })
})

describe('EdgeHoverCard · jitter, handshake and loss %', () => {
  test('jitter and connection setup rows render their own ms values, distinct from round trip', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', rttMs: 8, jitterMs: 1.5, handshakeMs: 12 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.getByText('Jitter')).toBeTruthy()
    expect(screen.getByText('1.5 ms')).toBeTruthy()
    expect(screen.getByText('Connection setup')).toBeTruthy()
    expect(screen.getByText('12 ms')).toBeTruthy()
  })

  test('the retransmits row shows a loss % once stats.lossPct is available, instead of the raw rate', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', retransmits: 2, stats: { retransmitsPerMin: 0.5, lossPct: 2 } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.getByText('Loss')).toBeTruthy()
    expect(screen.getByText('2.0%')).toBeTruthy()
    expect(screen.queryByText('0.5/min')).toBeNull()
  })

  test('falls back to the raw retransmits/min rate when lossPct is undefined (no segs_out reported yet)', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', retransmits: 4, stats: { retransmitsPerMin: 2 } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.getByText('Retransmits')).toBeTruthy()
    expect(screen.getByText('2/min')).toBeTruthy()
  })

  test('neither jitter nor connection setup rows render on a conntrack-only edge', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'conntrack', jitterMs: 3, handshakeMs: 20 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.queryByText('Jitter')).toBeNull()
    expect(screen.queryByText('Connection setup')).toBeNull()
  })
})
