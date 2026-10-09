import clsx from 'clsx'
import { AlertTriangle, Radio } from 'lucide-react'
import { useState } from 'react'
import { Button, EmptyState, ICON_SM, TierBadge } from '@/components/ui/primitives'
import { extrasOf } from '@/lib/consent'
import { clusterTelemetryLine } from '@/lib/telemetrySetup'
import type { Agent, Cluster } from '@/lib/types'

/**
 * "Where from": the cluster whose telemetry is being set up, one per command (each has its own discovery agent and its own release). Only a cluster
 * with an approved agent is listed, since that is what the rest of the setup reads its state from; with none, the way to connect one.
 * An agent that is not connected can still be chosen - the command is run by hand - but the row says the check at the end will not work yet.
 */
export default function ClusterStep({
  agents,
  clusters,
  rawAgents,
  selected,
  onSelect,
  onConnect,
  testIdPrefix,
}: {
  agents: Agent[]
  clusters: Cluster[]
  /** What the server sent about each agent, for what it sends today. */
  rawAgents: readonly unknown[] | undefined
  selected: string | undefined
  onSelect: (agentId: string) => void
  onConnect: () => void
  testIdPrefix: string
}) {
  const [now] = useState(() => Date.now())
  if (agents.length === 0) {
    return (
      <EmptyState
        title="Connect a cluster first"
        description="Telemetry is set up per cluster, and there is no cluster with an approved agent yet. Connect one, then come back here."
        action={<Button variant="primary" onClick={onConnect} data-testid={`${testIdPrefix}-connect`}><Radio size={ICON_SM} /> Connect a cluster</Button>}
      />
    )
  }
  return (
    <div data-testid={`${testIdPrefix}-picker`}>
      <p className="mb-2 text-sm font-medium text-nb-300" id={`${testIdPrefix}-where-label`}>Which cluster should the data come from?</p>
      <div role="radiogroup" aria-labelledby={`${testIdPrefix}-where-label`} className="divide-y divide-nb-850 overflow-hidden rounded-xl border border-nb-850 bg-nb-925">
        {agents.map((a) => {
          const c = clusters.find((cl) => cl.id === a.clusterId)
          const on = a.id === selected
          return (
            <button
              key={a.id}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => onSelect(a.id)}
              data-testid={`${testIdPrefix}-target`}
              className={clsx('flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left transition-colors', on ? 'bg-accent-soft' : 'hover:bg-nb-930')}
            >
              <span className={clsx('flex size-4 shrink-0 items-center justify-center rounded-full border', on ? 'border-accent' : 'border-nb-700')} aria-hidden>
                {on && <span className="size-2 rounded-full bg-accent" />}
              </span>
              <span className="min-w-0 flex-1 basis-48">
                <span className="block truncate text-sm font-medium text-nb-300">{a.name}</span>
                <span className="block text-xs text-nb-500">{clusterTelemetryLine(extrasOf(rawAgents, a.id).diagnostics, now)}</span>
                {a.connected === false && (
                  <span className="mt-0.5 flex items-center gap-1 text-xs text-warn" data-testid={`${testIdPrefix}-offline`}>
                    <AlertTriangle size={ICON_SM} aria-hidden /> Its agent is not connected. You can still set this up; the check at the end needs the agent.
                  </span>
                )}
              </span>
              {c && <TierBadge tier={c.tier} />}
            </button>
          )
        })}
      </div>
    </div>
  )
}
