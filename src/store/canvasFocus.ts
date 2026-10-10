import { create } from 'zustand'

/**
 * What the pointer or the selection is resting on, kept out of React state on purpose: a hover must never re-render the page or touch the
 * graph. Leaf nodes and edges read it through boolean selectors, so only the few whose answer changes redraw.
 */
interface CanvasFocus {
  /** The box or card under the pointer (a React Flow node id). */
  hover: string | null
  /** The selected box or card. */
  pinned: string | null
  /** A network whose chip is hovered, focused or open in the Inspector. */
  network: string | null
  setHover: (id: string | null) => void
  setPinned: (id: string | null) => void
  setNetwork: (id: string | null) => void
}

export const useCanvasFocus = create<CanvasFocus>((set) => ({
  hover: null,
  pinned: null,
  network: null,
  setHover: (hover) => set((s) => (s.hover === hover ? s : { hover })),
  setPinned: (pinned) => set((s) => (s.pinned === pinned ? s : { pinned })),
  setNetwork: (network) => set((s) => (s.network === network ? s : { network })),
}))

/** The node whose surroundings are lit: what the pointer is on, else what is selected. */
export const activeId = (s: Pick<CanvasFocus, 'hover' | 'pinned'>) => s.hover ?? s.pinned

/** Whether the hover or the selection is one of `ids` (a bundle's boxes and ends): the answer a line asks to light its calls. */
export const focusedBy = (s: Pick<CanvasFocus, 'hover' | 'pinned'>, ids: readonly string[] | undefined) => !!ids && [s.hover, s.pinned].some((a) => !!a && ids.includes(a))
