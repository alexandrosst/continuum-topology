import { ReactFlowProvider } from '@xyflow/react'
import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, test } from 'vitest'
import { GroupBox } from '@/components/topology/nodes'
import { clusterNets, PeerRingContext, peerIds, type ClusterNet } from '@/lib/clusterNets'
import type { GroupData } from '@/lib/graph'
import type { ClusterLink } from '@/lib/types'

const link = (over: Partial<ClusterLink>): ClusterLink => ({ fromCluster: 'a', fromName: 'alpha', toCluster: 'b', toName: 'bravo', kind: 'subnet', via: '10.30.0.0/16', redundancy: 1, ...over })

const group = (entityId: string, over: Partial<GroupData> = {}): GroupData => ({
  kind: 'group', entityId, groupBy: 'cluster', title: entityId, subtitle: '', tier: 'edge', status: 'ready', stats: '2 nodes', empty: 'No nodes', ...over,
})

// Two boxes on one canvas context: `a` carries the chips, `b` is a peer that can be ringed. The ring state is
// held the way TopologyPage holds it, so hovering a chip rings the other box.
function Canvas({ nets }: { nets: ClusterNet[] }) {
  const [chipPeers, setChipPeers] = useState<ReadonlySet<string> | null>(null)
  return (
    <ReactFlowProvider>
      <PeerRingContext.Provider value={{ ring: chipPeers ?? new Set(), show: setChipPeers }}>
        <div data-testid="box-a"><GroupBox {...({ data: group('a', { nets }), selected: false } as never)} /></div>
        <div data-testid="box-b"><GroupBox {...({ data: group('b'), selected: false } as never)} /></div>
      </PeerRingContext.Provider>
    </ReactFlowProvider>
  )
}

const ring = (id: 'a' | 'b') => (screen.getByTestId(`box-${id}`).firstElementChild as HTMLElement).className.includes('inset_0_0_0_1.5px')

describe('cluster networks', () => {
  const links = [
    link({}),
    link({ toCluster: 'c', toName: 'charlie' }),
    link({ kind: 'overlay', via: 'wg0 (wireguard)', toCluster: 'd', toName: 'delta' }),
  ]
  const nets = clusterNets(links)

  test('a network counts the OTHER clusters on it, a tunnel its own peers; subnets come first', () => {
    expect(nets.get('a')).toEqual([
      { kind: 'subnet', via: '10.30.0.0/16', peers: [{ id: 'b', name: 'bravo' }, { id: 'c', name: 'charlie' }] },
      { kind: 'overlay', via: 'wg0 (wireguard)', peers: [{ id: 'd', name: 'delta' }] },
    ])
    expect(nets.get('b')?.[0].peers).toEqual([{ id: 'a', name: 'alpha' }])
    expect(nets.get('e')).toBeUndefined()
  })

  test('peerIds is who to ring: the other clusters of the given networks, nobody for none', () => {
    expect([...peerIds(nets.get('a'))].sort()).toEqual(['b', 'c', 'd'])
    expect([...peerIds([nets.get('a')![1]])]).toEqual(['d'])
    expect(peerIds(undefined).size).toBe(0)
  })

  test('a tunnel name reused by unrelated pairs is not merged into one set of peers', () => {
    const reused = clusterNets([link({ kind: 'overlay', via: 'wg0 (wireguard)' }), link({ kind: 'overlay', via: 'wg0 (wireguard)', fromCluster: 'x', fromName: 'xray', toCluster: 'y', toName: 'yankee' })])
    expect(reused.get('a')?.[0].peers.map((p) => p.id)).toEqual(['b'])
    expect(reused.get('x')?.[0].peers.map((p) => p.id)).toEqual(['y'])
  })
})

describe('GroupBox network chips', () => {
  const nets = clusterNets([link({}), link({ toCluster: 'c', toName: 'charlie' }), link({ kind: 'overlay', via: 'wg0 (wireguard)' })]).get('a')!

  test("say 'shared with N' for the network and the tunnel's own label, each with the full sentence as its tooltip", () => {
    render(<Canvas nets={nets} />)
    const chips = screen.getAllByTestId('net-chip')
    expect(chips.map((c) => c.textContent)).toEqual(['10.30.0.0/16· shared with 2', 'wg0 (wireguard)'])
    expect(chips[0]).toHaveAttribute('title', 'Network 10.30.0.0/16 — shared with bravo, charlie')
    expect(chips[1]).toHaveAttribute('title', 'Tunnel wg0 (wireguard) — to bravo')
  })

  test('a cluster with nothing shared has no chip row, and more than two fold into a +N chip', () => {
    const { unmount } = render(<ReactFlowProvider><GroupBox {...({ data: group('a'), selected: false } as never)} /></ReactFlowProvider>)
    expect(screen.queryByTestId('net-chips')).not.toBeInTheDocument()
    unmount()
    const many = clusterNets(['10.0.0.0/8', '10.1.0.0/16', '10.2.0.0/16'].map((via) => link({ via }))).get('a')!
    render(<ReactFlowProvider><GroupBox {...({ data: group('a', { nets: many }), selected: false } as never)} /></ReactFlowProvider>)
    expect(screen.getAllByTestId('net-chip')).toHaveLength(2)
    expect(screen.getByText('+1').closest('[title]')).toHaveAttribute('title', 'Network 10.2.0.0/16 — shared with bravo')
  })

  test('hovering or focusing a chip rings its peers with a thin ring, and leaving takes it off', () => {
    render(<Canvas nets={nets} />)
    const chip = screen.getAllByTestId('net-chip')[0]
    expect(ring('b')).toBe(false)
    fireEvent.mouseEnter(chip)
    expect(ring('b')).toBe(true)
    expect(ring('a')).toBe(false)
    fireEvent.mouseLeave(chip)
    expect(ring('b')).toBe(false)
    fireEvent.focus(chip)
    expect(ring('b')).toBe(true)
    fireEvent.blur(chip)
    expect(ring('b')).toBe(false)
  })

  test('a ringed box that is selected keeps its own accent ring, and a device group is never ringed', () => {
    render(
      <ReactFlowProvider>
        <PeerRingContext.Provider value={{ ring: new Set(['s', 'd']), show: () => {} }}>
          <div data-testid="sel"><GroupBox {...({ data: group('s'), selected: true } as never)} /></div>
          <div data-testid="dev"><GroupBox {...({ data: group('d', { extra: 'devices' }), selected: false } as never)} /></div>
        </PeerRingContext.Provider>
      </ReactFlowProvider>,
    )
    const cls = (id: string) => (screen.getByTestId(id).firstElementChild as HTMLElement).className
    expect(cls('sel')).toContain('var(--color-accent)')
    expect(cls('sel')).not.toContain('1.5px')
    expect(cls('dev')).not.toContain('1.5px')
  })
})
