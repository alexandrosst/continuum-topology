import { create } from 'zustand'

/**
 * Requests the server rejected because too many wrong approval codes were typed. Once rejected, the request
 * leaves the pending list, so without this the person who mistyped would see the card vanish with no
 * explanation. The Discovery page keeps showing the card, with what happened, until it is dismissed.
 */
interface Locks {
  locked: Record<string, true>
  lock: (id: string) => void
  dismiss: (id: string) => void
}

export const useApprovalLocks = create<Locks>((set) => ({
  locked: {},
  lock: (id) => set((s) => ({ locked: { ...s.locked, [id]: true } })),
  dismiss: (id) =>
    set((s) => {
      const { [id]: _gone, ...rest } = s.locked
      return { locked: rest }
    }),
}))
