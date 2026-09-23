import { useEffect, useMemo } from 'react'
import { useTopology } from '@/store/topology'
import { derivePlacementSuggestions, type PlaceIndex, type PlacementSuggestion } from './places'
import { usePlaceIndex } from './places-data'

/**
 * Place-this-cluster suggestions for clusters that are not on the map. Worked out here from tables, never
 * stored until a person decides (so every browser sees the same ones). The tables are only downloaded when
 * some cluster actually lacks a place.
 */
export function usePlacementSuggestions(): { suggestions: PlacementSuggestion[]; byCluster: Map<string, PlacementSuggestion>; index?: PlaceIndex; loading: boolean } {
  const { clusters, sites, nodes, agents, suggestions } = useTopology()
  const needed = useMemo(
    () => clusters.some((c) => !c.deletedAt && !(c.siteId && sites.some((s) => s.id === c.siteId))),
    [clusters, sites],
  )
  const index = usePlaceIndex(needed)
  const derived = useMemo(
    () => (index && needed ? derivePlacementSuggestions(index, { clusters, sites, nodes, agents, suggestions }) : []),
    [index, needed, clusters, sites, nodes, agents, suggestions],
  )
  const byCluster = useMemo(() => new Map(derived.map((s) => [s.apply && 'clusterId' in s.apply ? s.apply.clusterId : '', s])), [derived])
  return { suggestions: derived, byCluster, index, loading: needed && !index }
}

/**
 * Puts every siteless cluster on the map on its own, at the best precision `derivePlacementSuggestions` can
 * work out (a cloud region table, a city name in a label, then GeoIP of the agent's connecting address - see
 * `placementCandidates`) - a person no longer clicks "Use" on `PlacementHint` to make it happen. Still fully
 * reversible and fully logged: `decideDerived` writes the same audit entry an accept click would, and editing
 * the site afterward (rename, move, reassign) always wins, the same as any other declared-over-observed value
 * in this app. A cluster a person already dismissed a suggestion for is never reconsidered: `derivePlacementSuggestions`
 * leaves a decided cluster out of its own output, so there is nothing here to auto-accept for it.
 */
export function useAutoPlaceClusters(): void {
  const decide = useTopology((s) => s.decideDerived)
  const { suggestions } = usePlacementSuggestions()
  useEffect(() => {
    for (const sug of suggestions) decide(sug, 'accepted')
  }, [suggestions, decide])
}
