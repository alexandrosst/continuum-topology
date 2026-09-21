import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { DEFAULT_POLICY, type Policy } from '@/lib/placement/types'

/**
 * How this browser weighs the costs in the placement advice. It is a viewer's own setting (it changes the advice
 * they read, nothing else), kept in this browser only.
 */
interface PolicyStore {
  policy: Policy
  set: (p: Partial<Policy>) => void
  reset: () => void
}

const clean = (p: Partial<Policy> | undefined): Policy => {
  const out = { ...DEFAULT_POLICY }
  for (const k of Object.keys(DEFAULT_POLICY) as (keyof Policy)[]) {
    const v = p?.[k]
    if (typeof v === 'number' && Number.isFinite(v) && v >= 0) out[k] = v
  }
  return out
}

export const usePolicy = create<PolicyStore>()(
  persist(
    (set) => ({
      policy: DEFAULT_POLICY,
      set: (p) => set((s) => ({ policy: clean({ ...s.policy, ...p }) })),
      reset: () => set({ policy: DEFAULT_POLICY }),
    }),
    { name: 'continuum-placement/policy', version: 1, partialize: (s) => ({ policy: s.policy }), merge: (saved, cur) => ({ ...cur, policy: clean((saved as { policy?: Partial<Policy> } | undefined)?.policy) }) },
  ),
)
