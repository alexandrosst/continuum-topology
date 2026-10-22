import { useEffect } from 'react'
import { create } from 'zustand'
import { api, atLeast, type Conn } from '@/lib/api'
import { useConn, useServer } from '@/store/server'

/**
 * How many proposals wait for a decision, for the badge on the menu. The proposals list is for editors (it carries the evidence an
 * advisor used about an agent), so for anyone else the count is zero and nothing is asked. At most `LIMIT` are counted: a menu badge
 * past a hundred is not more useful, and the Proposals page has the whole list.
 */
export const WAITING_CAP = 100
/** Read again this often while the page is visible; the Proposals page also tells the badge when it learns something newer. */
const EVERY_MS = 60_000

export const useWaitingProposals = create<{ count: number; set: (n: number) => void }>((set) => ({ count: 0, set: (count) => set({ count }) }))

/** Reads the count now. A failed read leaves the last count on the menu: a badge that vanishes on a network blip is worse than a stale one. */
export async function refreshWaitingProposals(conn: Conn): Promise<void> {
  try {
    useWaitingProposals.getState().set((await api.listProposals(conn, { status: 'pending', limit: WAITING_CAP })).length)
  } catch {
    /* keep the count as it was */
  }
}

/** Keeps `useWaitingProposals` current while an editor is connected. Mounted once, by the app shell. */
export function usePollWaitingProposals(): void {
  const status = useServer((s) => s.status)
  const role = useServer((s) => s.role)
  const conn = useConn()
  const allowed = status === 'connected' && atLeast(role, 'editor')
  useEffect(() => {
    if (!allowed) {
      useWaitingProposals.getState().set(0)
      return
    }
    const read = () => document.visibilityState === 'visible' && void refreshWaitingProposals(conn)
    read()
    const id = setInterval(read, EVERY_MS)
    document.addEventListener('visibilitychange', read)
    return () => {
      clearInterval(id)
      document.removeEventListener('visibilitychange', read)
    }
  }, [allowed, conn])
}
