import { create } from 'zustand'
import { api, type Conn } from '@/lib/api'
import type { HistoryPoint, SnapshotTopology } from '@/lib/history'

/**
 * "The estate as it was": while a recorded moment is chosen, every page reads that moment instead of now.
 * It lives in memory only (nothing about it is saved or shared), and while it is active the topology store
 * refuses edits, so a change can never be made to a picture of the past by mistake.
 */
interface HistoryView {
  /** The recorded moment being shown (RFC 3339), or null for the live view. */
  at: string | null
  snapshot: SnapshotTopology | null
  loading: boolean
  error?: string
  /** Recorded moments, oldest first (for stepping between them). */
  points: HistoryPoint[]
  /** When an edit was last refused because a past moment is shown, so the banner can say why nothing happened. */
  refusedAt: number
  loadPoints: (c: Conn) => Promise<void>
  /** Show the recording at or before `at`. Resolves to the moment actually shown. */
  view: (c: Conn, at: string) => Promise<string | null>
  /** Back to now. */
  live: () => void
}

let ticket = 0

export const useHistoryView = create<HistoryView>((set) => ({
  at: null,
  snapshot: null,
  loading: false,
  points: [],
  refusedAt: 0,

  loadPoints: async (c) => {
    try {
      set({ points: (await api.history(c)).points })
    } catch {
      /* stepping is a convenience; the banner works without it */
    }
  },

  view: async (c, at) => {
    const mine = ++ticket
    set({ loading: true, error: undefined })
    try {
      const s = await api.snapshot(c, at)
      if (mine !== ticket) return null // a newer request replaced this one
      set({ at: s.at, snapshot: s.topology, loading: false })
      return s.at
    } catch (e) {
      if (mine !== ticket) return null
      set({ loading: false, error: e instanceof Error ? e.message : 'The recording could not be read.' })
      return null
    }
  },

  live: () => {
    ticket++
    set({ at: null, snapshot: null, loading: false, error: undefined })
  },
}))

/** True while a recorded moment is shown. Read outside React (the topology store uses it to refuse edits). */
export const viewingThePast = () => useHistoryView.getState().snapshot !== null

/** An edit was attempted while a past moment is shown; nothing was changed. */
export const refuseEdit = () => useHistoryView.setState({ refusedAt: Date.now() })
