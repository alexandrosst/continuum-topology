// Loads the place tables in the browser, once, when something first needs them: about 1.2 MB of cities and the
// country outlines. Nothing here runs at startup, so a page that never resolves a place never downloads them.
import { useEffect, useState } from 'react'
import numeric from '@/data/country-numeric.json'
import { buildIndex, countryShapes, parseCities, type PlaceIndex } from './places'

let pending: Promise<PlaceIndex> | undefined

export function loadPlaceIndex(): Promise<PlaceIndex> {
  pending ??= Promise.all([import('@/data/cities.tsv?raw'), import('world-atlas/countries-110m.json')]).then(([cities, atlas]) =>
    buildIndex(parseCities(cities.default), countryShapes(atlas.default, numeric as Record<string, string>)),
  )
  // a failed load may be retried the next time something asks
  pending.catch(() => (pending = undefined))
  return pending
}

/** The place index, or undefined while it loads (or if it could not be loaded). */
export function usePlaceIndex(enabled = true): PlaceIndex | undefined {
  const [idx, setIdx] = useState<PlaceIndex>()
  useEffect(() => {
    if (!enabled || idx) return
    let live = true
    loadPlaceIndex().then((i) => live && setIdx(i), () => undefined)
    return () => {
      live = false
    }
  }, [enabled, idx])
  return idx
}
