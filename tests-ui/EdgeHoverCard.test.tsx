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

describe('EdgeHoverCard · DNS response latency', () => {
  test('DNS response row renders its own ms value, distinct from DNS queries', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', dnsQueryNames: ['a.example.com'], dnsRttMs: 47.5 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.getByText('DNS response')).toBeTruthy()
    expect(screen.getByText('48 ms')).toBeTruthy()
  })

  test('DNS response row does not require dnsQueryNames to be present', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', dnsRttMs: 8 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.getByText('DNS response')).toBeTruthy()
    expect(screen.getByText('8.0 ms')).toBeTruthy()
  })

  test('DNS response row does not render on a conntrack-only edge, even with the field present', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'conntrack', dnsRttMs: 10 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.queryByText('DNS response')).toBeNull()
  })
})

describe('EdgeHoverCard · protocol mix', () => {
  test('an aggregated edge with more than one protocol shows a breakdown, busiest first', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ aggregated: true, protocols: { gRPC: 1, HTTP: 9, Kafka: 4 } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="eu"
        toName="us"
      />,
    )
    expect(screen.getByText('Protocols')).toBeTruthy()
    expect(screen.getByText('HTTP ×9, Kafka ×4, gRPC ×1')).toBeTruthy()
  })

  test('no Protocols row when the bundle is entirely one protocol (EdgeData.protocols unset)', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ aggregated: true, activeCount: 3 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="eu"
        toName="us"
      />,
    )
    expect(screen.queryByText('Protocols')).toBeNull()
  })

  test('a single (non-aggregated) dependency edge never shows a Protocols row', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ via: 'ebpf', rttMs: 5 })}
        pos={pos}
        host={document.createElement('div')}
        fromName="web"
        toName="checkout"
      />,
    )
    expect(screen.queryByText('Protocols')).toBeNull()
  })
})

describe('EdgeHoverCard · cluster-link flow rollup', () => {
  test('shows flow count, avg RTT and avg loss together when all three are present', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({
          clusterLink: { kind: 'overlay', via: 'wg0 (wireguard)', redundancy: 1, flowsObserved: 3, avgRttMs: 42, avgLossPct: 0.2 },
        })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    expect(screen.getByText('Flows observed')).toBeTruthy()
    expect(screen.getByText('Flows observed').nextElementSibling).toHaveTextContent('3 flows · 42 ms avg RTT · 0.2% avg loss')
  })

  test('singular "flow" when exactly one is observed, and the row omits a measurement with no sample yet', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({
          clusterLink: { kind: 'overlay', via: 'wg0 (wireguard)', redundancy: 1, flowsObserved: 1, avgLossPct: 0 },
        })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    // avgRttMs is undefined (no measured sample yet) - it must not render as "0 ms", it must be absent.
    expect(screen.getByText('Flows observed').nextElementSibling).toHaveTextContent('1 flow · 0.0% avg loss')
  })

  test('no row at all when nothing has been matched onto this link yet', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ clusterLink: { kind: 'overlay', via: 'wg0 (wireguard)', redundancy: 1 } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    expect(screen.queryByText('Flows observed')).toBeNull()
  })

  test('the row just reads flowsObserved rather than re-deriving kind === "overlay" itself - the backend is the one place that only ever sets it for an overlay link', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ clusterLink: { kind: 'subnet', via: '10.20.30.0/24', redundancy: 1, flowsObserved: 2 } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    expect(screen.getByText('Flows observed')).toBeTruthy()
  })
})

describe('EdgeHoverCard · cluster-link encryption posture', () => {
  test('an encrypted (WireGuard/IPsec) tunnel reads as encrypted, with no warning styling', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ clusterLink: { kind: 'overlay', via: 'wg0 (wireguard)', redundancy: 1, encryption: 'encrypted' } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    const dd = screen.getByText('encrypted by design')
    expect(dd.className).not.toContain('text-warn')
  })

  test('a plaintext tunnel (e.g. GRE) is called out, with warning styling', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ clusterLink: { kind: 'overlay', via: 'gre0 (gre)', redundancy: 1, encryption: 'plaintext' } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    const dd = screen.getByText('no encryption of its own')
    expect(dd.className).toContain('text-warn')
  })

  test('no Encryption row on a subnet link, which has no tunnel driver to classify', () => {
    render(
      <EdgeHoverCard
        edge={makeEdge({ clusterLink: { kind: 'subnet', via: '10.20.30.0/24', redundancy: 1 } })}
        pos={pos}
        host={document.createElement('div')}
        fromName="edge-a"
        toName="cloud"
      />,
    )
    expect(screen.queryByText('Encryption')).toBeNull()
  })
})
