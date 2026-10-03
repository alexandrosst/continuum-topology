import { create } from 'zustand'
import { deepEqual } from '@/lib/discovered'
import type { EffectiveModel } from '@/lib/provenance'
import type { ClusterLink, ClusterPairConnectivity, Dependency, ExternalEndpoint, Path, Tombstone } from '@/lib/types'

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
  /** Confirmed overlay/subnet relationships between cluster pairs - same "derived every window, never
   *  stored in the workspace" story as the three above, and also not available for a past/historic view
   *  (see ClusterLink's own doc for why: it is a live correlation, not a recorded fact). */
  clusterLinks: ClusterLink[]
  /** The broader cluster-pair connectivity picture (tunnel/subnet/unexplained/unknown) - same "derived
   *  every window, never stored, not available for a past view" story as clusterLinks above. */
  clusterPairConnectivity: ClusterPairConnectivity[]
  tombstones: Tombstone[]
  model?: EffectiveModel
  /** Server clock minus this browser's clock, in ms, from the last state poll: facts are aged on the server's time, not this machine's. */
  skewMs: number
  set: (d: Dependency[], e: ExternalEndpoint[], paths?: Path[], tombstones?: Tombstone[], clusterLinks?: ClusterLink[], clusterPairConnectivity?: ClusterPairConnectivity[]) => void
  setModel: (m: EffectiveModel | undefined) => void
  setSkew: (ms: number) => void
  clear: () => void
}

export const useObserved = create<ObservedStore>((set) => ({
  dependencies: [],
  externalEndpoints: [],
  paths: [],
  clusterLinks: [],
  clusterPairConnectivity: [],
  tombstones: [],
  model: undefined,
  skewMs: 0,
  // A plain `set({ dependencies, ... })` here handed every poll's freshly-parsed JSON arrays straight
  // through, even when their content was byte-for-byte the same as last time - state.generatedAt (and
  // therefore this call) changes on the server's clock every poll regardless of whether anything observed
  // actually changed (see hub.go). That fresh identity fed straight into useTopology's `liveDeps`/`liveExt`
  // and, through usePaths(), into TopologyPage's `graph` memo - forcing a full canvas re-layout on every
  // single poll tick, the exact bug class fixed elsewhere for local operators (TopologyPage.tsx) and the
  // effective model (effectiveModel.ts). deepEqual (see lib/discovered.ts's own doc comment on it) hands
  // back the *same* array a poll that changed nothing about that particular field, so a memo keyed on it
  // only sees a new identity when something in it actually did change.
  set: (dependencies, externalEndpoints, paths = [], tombstones = [], clusterLinks = [], clusterPairConnectivity = []) =>
    set((s) => ({
      dependencies: deepEqual(dependencies, s.dependencies) ? s.dependencies : dependencies,
      externalEndpoints: deepEqual(externalEndpoints, s.externalEndpoints) ? s.externalEndpoints : externalEndpoints,
      paths: deepEqual(paths, s.paths) ? s.paths : paths,
      tombstones: deepEqual(tombstones, s.tombstones) ? s.tombstones : tombstones,
      clusterLinks: deepEqual(clusterLinks, s.clusterLinks) ? s.clusterLinks : clusterLinks,
      clusterPairConnectivity: deepEqual(clusterPairConnectivity, s.clusterPairConnectivity) ? s.clusterPairConnectivity : clusterPairConnectivity,
    })),
  setModel: (model) => set({ model }),
  setSkew: (skewMs) => set({ skewMs }),
  clear: () => set({ dependencies: [], externalEndpoints: [], paths: [], clusterLinks: [], clusterPairConnectivity: [], tombstones: [], model: undefined }),
}))
