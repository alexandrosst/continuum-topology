import { useMemo, useState } from 'react'
import { buildPlatform, type PlatformModel } from '@/lib/platformLayer'
import type { Agent } from '@/lib/types'
import { useOperators, useTelemetryIntents } from '@/lib/useOperators'
import { useVisiblePolling } from '@/lib/usePolling'
import { useServer } from '@/store/server'

/** The ages are minutes and seconds old by the time they are read: this is how often they are worked out again. */
const AGE_TICK_MS = 30_000

/**
 * The platform of the Telemetry tab (see lib/platformLayer.ts): what the app already loads - the agents and what they report, the
 * telemetry intents, the operators - joined into one model. Used only by the tab, so nothing is asked of the server anywhere else.
 * The operators are administrators' to list and the intents editors', so for anyone else it still shows the agents and what they run.
 * Undefined until what this person may read has arrived, so the lanes are drawn once rather than filled in piece by piece.
 */
export function usePlatformLayer(clusters: { id: string; name: string }[], agents: Agent[]): PlatformModel | undefined {
  const admin = useServer((s) => s.isAdmin())
  const editor = useServer((s) => s.canEdit())
  const rawAgents = useServer((s) => s.state?.agents)
  const ops = useOperators(admin)
  const ints = useTelemetryIntents(editor)
  const [now, setNow] = useState(() => Date.now())
  useVisiblePolling(() => setNow(Date.now()), AGE_TICK_MS)
  const ready = ops.loaded && ints.loaded
  return useMemo(
    () => (ready ? buildPlatform({ clusters, agents, rawAgents, intents: ints.intents, operators: ops.operators, showFusionOff: admin }, now) : undefined),
    [ready, clusters, agents, rawAgents, ints.intents, ops.operators, admin, now],
  )
}
