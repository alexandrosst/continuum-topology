import { useEffect, useState } from 'react'
import { api } from './api'
import type { AddressCandidate, AddressCandidates } from './types'
import { useServer } from '@/store/server'

/** How each kind of candidate is named for a person. */
export const CANDIDATE_KIND: Record<AddressCandidate['kind'], string> = {
  'load-balancer': 'Load balancer',
  'node-port': 'Node port',
  'external-ip': 'External IP',
  recorded: 'Recorded',
}

/** The addresses Ikhnos found for a regional operator from what its cluster's agent reports. `null` until the answer
 *  arrives, and for good when the lookup fails: a missing list is a convenience lost, never an error to show. */
export function useAddressCandidates(operatorId: string | undefined): AddressCandidates | null {
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
  }, [conn, operatorId])
  // An answer for another operator is not this one's.
  return found && found.id === operatorId ? found.r : null
}
