import { useMemo, useState } from 'react'
import { buildPlatform, onlyProblems, type PlatformModel } from '@/lib/platformLayer'
import type { Agent } from '@/lib/types'
import { useOperators, useTelemetryIntents } from '@/lib/useOperators'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** The ages written on the canvas are minutes and seconds old by the time they are read: this is how often they are worked out again. */
const AGE_TICK_MS = 30_000

/**
 * The platform layer of the canvas (see lib/platformLayer.ts): what the app already loads - the agents and what they report, the
 * telemetry intents, the operators - joined into one model. Nothing is asked of the server while the layer is off. The operators are
 * administrators' to list and the intents editors', so for anyone else the layer still shows the agents and what they run.
 *
 * The answer keeps its identity while nothing in it changed, so a poll that found the same thing does not lay the canvas out again.
 */
export function usePlatformLayer(opts: { enabled: boolean; clusters: { id: string; name: string }[]; agents: Agent[]; scoped: boolean; problemsOnly: boolean }): PlatformModel | undefined {
  const { enabled, clusters, agents, scoped, problemsOnly } = opts
  const admin = useServer((s) => s.isAdmin())
  const editor = useServer((s) => s.canEdit())
  const rawAgents = useServer((s) => s.state?.agents)
  const { operators } = useOperators(enabled && admin)
  const { intents } = useTelemetryIntents(enabled && editor)
  const [now, setNow] = useState(() => Date.now())
  useVisiblePolling(() => setNow(Date.now()), enabled ? AGE_TICK_MS : null)
  const fresh = useMemo(() => {
    if (!enabled) return undefined
    const model = buildPlatform({ clusters, agents, rawAgents, intents, operators, scoped }, now)
    return problemsOnly ? onlyProblems(model) : model
  }, [enabled, clusters, agents, rawAgents, intents, operators, scoped, problemsOnly, now])
  // Same content, same object: only a change in what is drawn lays the canvas out again.
  const key = fresh ? JSON.stringify(fresh) : ''
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => fresh, [key])
}
