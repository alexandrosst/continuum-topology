import { useEffect, useMemo, useRef, useState } from 'react'
import { useEffectiveModel } from '@/store/effectiveModel'
import { useObserved } from '@/store/observed'
import { usePolicy } from '@/store/placement'
import { usePaths, useTopology } from '@/store/topology'
import { recommend } from './engine'
import { keepHints, placementHints } from './hints'
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

const NONE = new Map<string, string>()

/**
 * Where the plan would move services, for the canvas markers, worked out once the browser is idle: the plan is
 * the one heavy step of a state poll (seconds on a large estate), and the edges must not wait behind it. The
 * markers follow the same plan `usePlan` gives, a moment later.
 */
export function usePlanHints(): Map<string, string> {
  const world = useWorld()
  const policy = usePolicy((s) => s.policy)
  const [hints, setHints] = useState(NONE)
  const last = useRef(NONE)
  useEffect(() => {
    const run = () => setHints((last.current = keepHints(last.current, placementHints(recommend(world, policy), world))))
    if (typeof requestIdleCallback !== 'function') {
      const id = setTimeout(run, 50)
      return () => clearTimeout(id)
    }
    const id = requestIdleCallback(run, { timeout: 2000 })
    return () => cancelIdleCallback(id)
  }, [world, policy])
  return hints
}
