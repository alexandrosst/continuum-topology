import { useEffect, useState } from 'react'
import { api } from './api'
import type { AddressCandidate, AddressCandidates } from './types'
import { useServer } from '@/store/server'

/** How each kind of candidate is named for a person. */
export const CANDIDATE_KIND: Record<AddressCandidate['kind'], string> = {
  'load-balancer': 'Load balancer',
  'node-port': 'Node port',
  'node-service-port': 'Service port on the node',
  'external-ip': 'External IP',
  recorded: 'Recorded',
}

const NETWORK_LABEL: Record<NonNullable<AddressCandidate['network']>, string> = { mesh: 'Mesh', private: 'Private network', public: 'Public' }

/** The line under a candidate: what kind it is, which network and interface it is on, and where it was found. */
export function candidateSub(x: AddressCandidate): string {
  const net = x.network ? ` · ${NETWORK_LABEL[x.network]}${x.interface ? ` (${x.interface})` : ''}` : ''
  return `${CANDIDATE_KIND[x.kind]}${net} · ${x.source}`
}

/** The addresses Ikhnos found for a regional operator from what its cluster's agent reports. `refresh` is bumped to ask again. `null` until the answer
 *  arrives, and for good when the lookup fails: a missing list is a convenience lost, never an error to show. */
export function useAddressCandidates(operatorId: string | undefined, refresh = 0): AddressCandidates | null {
  const conn = useServer((s) => s.conn)
  const [found, setFound] = useState<{ id: string; r: AddressCandidates } | null>(null)
  useEffect(() => {
    if (!operatorId) return
    let live = true
    void (async () => {
      const c = conn()
      if (!c) return
      try {
        const r = await api.operatorAddressCandidates(c, operatorId)
        if (live) setFound({ id: operatorId, r })
      } catch {
        // Typing an address still works without the list.
      }
    })()
    return () => {
      live = false
    }
  }, [conn, operatorId, refresh])
  // An answer for another operator is not this one's.
  return found && found.id === operatorId ? found.r : null
}

/** The hints worth showing as sentences: the card says why the nodes are missing when the server's cluster is not known. */
export function visibleHints(found: AddressCandidates | null): string[] {
  const sc = found?.serverCluster
  return sc && sc.state !== 'known' ? [] : found?.hints ?? []
}
