import { useEffect, useMemo, useState } from 'react'
import { useEffectiveModel } from '@/store/effectiveModel'
import { useObserved } from '@/store/observed'
import { usePolicy } from '@/store/placement'
import { usePaths, useTopology } from '@/store/topology'
import { recommend } from './engine'
import type { Plan, Policy } from './types'
import { buildWorld, type World } from './world'

/** The moment facts are judged, in the server's time (the browser's clock may differ), refreshed as time passes. */
export function useJudgedAt(): number {
  const skewMs = useObserved((s) => s.skewMs)
  const [tick, setTick] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setTick(Date.now()), 15_000)
    return () => clearInterval(id)
  }, [])
  return tick + skewMs
}

/** The estate as the UI shows it (with measured paths), ready for the placement engine. */
export function useWorld(): World {
  const t = useTopology()
  const paths = usePaths()
  const model = useEffectiveModel()
  const asOf = useJudgedAt()
  const { clusters, nodes, services, devices, dependencies, sites, siteLinks, externalEndpoints, agents } = t
  return useMemo(
    () => buildWorld({ clusters, nodes, services, devices, dependencies, sites, siteLinks, externalEndpoints, agents, paths, model, asOf }),
    [clusters, nodes, services, devices, dependencies, sites, siteLinks, externalEndpoints, agents, paths, model, asOf],
  )
}

/** Recommendations for the whole estate under this browser's policy. Never applies anything. */
export function usePlan(): { world: World; plan: Plan; policy: Policy } {
  const world = useWorld()
  const policy = usePolicy((s) => s.policy)
  const plan = useMemo(() => recommend(world, policy), [world, policy])
  return { world, plan, policy }
}
