import { useMemo } from 'react'
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
