import { createContext } from 'react'
import type { ClusterLink } from './types'

/** One network a cluster sits on (a shared subnet), or one tunnel it is joined through, with the other clusters on it. */
export interface ClusterNet {
  kind: ClusterLink['kind']
  /** The shared subnet prefix ("10.30.0.0/16"), or the tunnel's name and kind ("wg0 (wireguard)"). */
  via: string
  peers: { id: string; name: string }[]
}

/**
 * What each cluster shares with the others, from the confirmed cluster links. Said on the cluster's header
 * and in the Inspector instead of drawn as a line: that two clusters share a subnet or a tunnel is a fact,
 * not a status. A link is undirected, so both ends get the other as a peer. A tunnel is only ever paired with
 * the peers its own links name (a name like "wg0" is commonly reused across unrelated tunnels). Subnets come first.
 */
export function clusterNets(links: ClusterLink[]): Map<string, ClusterNet[]> {
  const nets = new Map<string, ClusterNet[]>()
  const add = (id: string, kind: ClusterNet['kind'], via: string, peer: { id: string; name: string }) => {
    const mine = nets.get(id) ?? []
    let net = mine.find((n) => n.kind === kind && n.via === via)
    if (!net) mine.push((net = { kind, via, peers: [] }))
    if (!net.peers.some((p) => p.id === peer.id)) net.peers.push(peer)
    nets.set(id, mine)
  }
  for (const l of links) {
    add(l.fromCluster, l.kind, l.via, { id: l.toCluster, name: l.toName })
    add(l.toCluster, l.kind, l.via, { id: l.fromCluster, name: l.fromName })
  }
  for (const mine of nets.values()) mine.sort((a, b) => Number(a.kind === 'overlay') - Number(b.kind === 'overlay'))
  return nets
}

const names = (n: ClusterNet) => n.peers.map((p) => p.name).join(', ')

/** The chip text as [what, qualifier]: the prefix and "· shared with 2" for a network, the tunnel's own label
 *  ("wg0 (wireguard)") alone for a tunnel. The qualifier is the part a narrow chip gives up first. */
export const netLabel = (n: ClusterNet): [string, string?] => (n.kind === 'subnet' ? [n.via, `· shared with ${n.peers.length}`] : [n.via])

/** The sentence behind a chip or an Inspector line: the network or tunnel by name, and who is on it. */
export const netSentence = (n: ClusterNet) => `${n.kind === 'subnet' ? 'Network' : 'Tunnel'} ${n.via} — ${n.kind === 'subnet' ? 'shared with' : 'to'} ${names(n)}`

/** The clusters to ring when the given networks are hovered, focused or belong to the selected cluster. */
export const peerIds = (nets: ClusterNet[] | undefined): ReadonlySet<string> => new Set(nets?.flatMap((n) => n.peers.map((p) => p.id)))

/** Which cluster boxes carry the thin peer ring: the clusters on the network or tunnel chip being hovered or focused,
 *  or else those sharing one with the selected cluster. `show` is how a chip says which (null: back to the selection's). */
export const PeerRingContext = createContext<{ ring: ReadonlySet<string>; show: (ids: ReadonlySet<string> | null) => void }>({ ring: new Set(), show: () => {} })
