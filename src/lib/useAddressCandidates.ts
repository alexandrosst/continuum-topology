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

/** How often the list is read again while it is waiting for something a person does outside this page (running a command against the
 *  cluster): the Service getting a node port or an address, an agent being connected. */
const WAITING_POLL_MS = 4000

/** Whether the answer is one that changes by itself once someone acts elsewhere, so it is worth asking again without being told to. */
function awaitsAnAction(r: AddressCandidates): boolean {
  return !!r.expose || (!!r.serverCluster && r.serverCluster.state !== 'known') || r.candidates.every((x) => x.kind === 'recorded')
}

/** The addresses Ikhnos found for a regional operator from what its cluster's agent reports. `refresh` is bumped to ask again. `null` until the answer
 *  arrives, and for good when the lookup fails: a missing list is a convenience lost, never an error to show.
 *
 *  While the answer says it is waiting for something done outside this page (a command to run, a cluster to connect) it asks again every few
 *  seconds, and at once when the person comes back to this window, so the list fills in without closing and reopening the dialog. */
export function useAddressCandidates(operatorId: string | undefined, refresh = 0): AddressCandidates | null {
  const conn = useServer((s) => s.conn)
  const [found, setFound] = useState<{ id: string; r: AddressCandidates } | null>(null)
  useEffect(() => {
    if (!operatorId) return
    let live = true
    let timer: ReturnType<typeof setTimeout> | undefined
    let waiting = false
    const load = async () => {
      clearTimeout(timer)
      const c = conn()
      if (!c) return
      try {
        const r = await api.operatorAddressCandidates(c, operatorId)
        if (!live) return
        setFound({ id: operatorId, r })
        waiting = awaitsAnAction(r)
      } catch {
        // Typing an address still works without the list.
      }
      if (live && waiting) timer = setTimeout(() => void load(), WAITING_POLL_MS)
    }
    const onReturn = () => {
      if (live && waiting && document.visibilityState !== 'hidden') void load()
    }
    void load()
    window.addEventListener('focus', onReturn)
    document.addEventListener('visibilitychange', onReturn)
    return () => {
      live = false
      clearTimeout(timer)
      window.removeEventListener('focus', onReturn)
      document.removeEventListener('visibilitychange', onReturn)
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
