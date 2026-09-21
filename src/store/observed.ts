import { create } from 'zustand'
import type { EffectiveModel } from '@/lib/provenance'
import type { Dependency, ExternalEndpoint, Path, Tombstone } from '@/lib/types'

/**
 * What agents saw on the wire and measured, as the server last reported it. It is derived data that changes
 * every window, so it lives here and is laid over the model when the UI reads it; it is never written into
 * the workspace a person edits and exports.
 *
 * Also here, for the same reason: the records that disappeared (tombstones, kept by the server for a while), and
 * the effective model (declared and observed combined, with the provenance of every attribute) once something asked for it.
 */
interface ObservedStore {
  dependencies: Dependency[]
  externalEndpoints: ExternalEndpoint[]
  paths: Path[]
  tombstones: Tombstone[]
  model?: EffectiveModel
  /** Server clock minus this browser's clock, in ms, from the last state poll: facts are aged on the server's time, not this machine's. */
  skewMs: number
  set: (d: Dependency[], e: ExternalEndpoint[], paths?: Path[], tombstones?: Tombstone[]) => void
  setModel: (m: EffectiveModel | undefined) => void
  setSkew: (ms: number) => void
  clear: () => void
}

export const useObserved = create<ObservedStore>((set) => ({
  dependencies: [],
  externalEndpoints: [],
  paths: [],
  tombstones: [],
  model: undefined,
  skewMs: 0,
  set: (dependencies, externalEndpoints, paths = [], tombstones = []) => set({ dependencies, externalEndpoints, paths, tombstones }),
  setModel: (model) => set({ model }),
  setSkew: (skewMs) => set({ skewMs }),
  clear: () => set({ dependencies: [], externalEndpoints: [], paths: [], tombstones: [], model: undefined }),
}))
