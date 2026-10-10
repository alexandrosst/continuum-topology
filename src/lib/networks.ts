import type { ClusterLink } from '@/lib/types'

/**
 * A network is a fact, not a connection: every cluster that sits on one subnet, or is joined by one overlay, belongs to it - however many
 * pairs the server happened to confirm. Pair links that name the same evidence (the same prefix, the same tunnel) are the one network.
 */
export interface Network {
  id: string
  kind: ClusterLink['kind']
  /** The evidence: a shared prefix ("10.30.0.0/16") or a tunnel ("wg0 (wireguard)"). */
  via: string
  members: { id: string; name: string }[]
}

export const networkWord = (kind: Network['kind']) => (kind === 'overlay' ? 'Overlay' : 'Same subnet')

export function buildNetworks(links: readonly ClusterLink[] | undefined): Network[] {
  const by = new Map<string, Network>()
  for (const l of links ?? []) {
    const id = `${l.kind}:${l.via}`
    const n = by.get(id) ?? { id, kind: l.kind, via: l.via, members: [] }
    for (const m of [{ id: l.fromCluster, name: l.fromName }, { id: l.toCluster, name: l.toName }]) if (!n.members.some((x) => x.id === m.id)) n.members.push(m)
    by.set(id, n)
  }
  return [...by.values()]
}

/** "Shares subnet 10.30.0.0/16 with polaris-edge" - the one sentence the chip, its tooltip and the Inspector all say. */
export function networkSentence(n: Network, clusterId: string): string {
  const others = n.members.filter((m) => m.id !== clusterId).map((m) => m.name)
  return `${n.kind === 'overlay' ? 'On overlay' : 'Shares subnet'} ${n.via}${others.length ? ` with ${others.join(', ')}` : ''}`
}
