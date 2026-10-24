import clsx from 'clsx'
import { Check, Plug, X } from 'lucide-react'
import { useState } from 'react'
import { when } from '@/features/discovery/components/AgentParts'

import type { useConnectFlow } from '@/features/discovery/components/ConnectFlow'
import GettingStarted, { useGettingStarted } from '@/components/GettingStarted'
import { GroupingPicker } from '@/components/GroupingPicker'
import { GoneRecords, ObservedClusters } from '@/components/Observations'
import { Button, EmptyState, ICON_SM, Pill } from '@/components/ui/primitives'
import type { GroupingAlternative, Suggestion } from '@/lib/types'
import { usePlacementSuggestions } from '@/lib/usePlacement'
import { useTopology } from '@/store/topology'

/**
 * A `create-application` suggestion only shows the label that won. Some services carry more than one
 * of the labels discovery understands (an Argo app that is also part of a bigger Helm release, say),
 * and the one that wins by precedence is not always the one a person wants grouped by. This lets them
 * regroup onto a runner-up instead of dismissing the suggestion and typing a name from scratch - and
 * the same choice stays available from the application itself afterwards (see ApplicationForm).
 */
function AlternativeLabelPicker({ suggestion, alternatives }: { suggestion: Suggestion; alternatives: GroupingAlternative[] }) {
  const applyAlternative = useTopology((s) => s.applyAlternative)
  return <GroupingPicker label="Use a different label instead" alternatives={alternatives} onPick={(alt) => applyAlternative(suggestion.id, alt)} />
}

/** What the agents found that needs a decision (the inbox of suggestions), and what they have seen and lost: the second tab of the Agents page. */
export default function DiscoveryPanel({ connect, part }: { connect: ReturnType<typeof useConnectFlow>; part: 'inbox' | 'seen' }) {
  const { agents, suggestions, decideSuggestion, decideDerived } = useTopology()
  const placement = usePlacementSuggestions().suggestions
  const started = useGettingStarted('discovery')
  const canAdminister = connect.canStart
  const derivedIds = new Set(placement.map((s) => s.id))
  const open = [...suggestions.filter((s) => s.status === 'open'), ...placement]
  // A decided suggestion eases out instead of cutting to the next row: the row plays its exit animation
  // first (row-exit, in index.css), and only then is the decision actually applied, which is what removes
  // it from `open`. ROW_EXIT_MS must match that animation's duration.
  const ROW_EXIT_MS = 200
  const [exiting, setExiting] = useState<Set<string>>(new Set())
  const decide = (s: (typeof open)[number], d: 'accepted' | 'dismissed') => {
    setExiting((prev) => new Set(prev).add(s.id))
    setTimeout(() => (derivedIds.has(s.id) ? decideDerived(s, d) : decideSuggestion(s.id, d)), ROW_EXIT_MS)
  }
  const agentName = (id?: string) => agents.find((a) => a.id === id)?.name
  const showStarted = started && !started.dismissed
  const approved = agents.filter((a) => a.status === 'approved')

  return (
    <>
      {part === 'inbox' ? (
        <>
      {showStarted && started && (
        <div className="mb-8">
          <GettingStarted checklist={started.checklist} onConnect={connect.start} onDismiss={started.dismiss} />
        </div>
      )}

      <p className="mb-3 max-w-3xl text-sm text-nb-500">What the agents found that needs your decision. Nothing here changes your topology until you accept it; once you decide, the outcome shows up in History and who approved it shows up in Activity.</p>
      {open.length === 0 ? (
        <div className="mb-8">
          {approved.length === 0 ? (
            <EmptyState
              title="Nothing found yet"
              description="No cluster is connected yet, so no agent has reported anything. What agents find (new devices, outside endpoints, suggested groupings) is listed here for you to accept or dismiss."
              action={!showStarted && canAdminister ? <Button variant="primary" onClick={connect.start}><Plug size={ICON_SM} /> Connect a cluster</Button> : undefined}
            />
          ) : (
            <EmptyState title="Nothing to review" description="The connected agents have no new devices, outside endpoints or groupings to suggest at the moment. Anything they find later is listed here." />
          )}
        </div>
      ) : (
        <div className="mb-8 space-y-2">
          {open.map((s) => (
            <div
              key={s.id}
              className={clsx(
                'flex flex-wrap items-start justify-between gap-x-6 gap-y-3 rounded-xl border border-nb-850 bg-nb-925 px-5 py-4',
                exiting.has(s.id) && 'row-exit',
              )}
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-nb-300">{s.title}</span>
                  <Pill>{s.kind === 'infrastructure' ? 'possible new cluster' : s.kind.replace('-', ' ')}</Pill>
                </div>
                <p className="mt-1 max-w-2xl text-sm text-nb-500">{s.detail}</p>
                <p className="mt-1 text-xs text-nb-600">
                  {agentName(s.agentId) ?? (derivedIds.has(s.id) ? 'worked out from place tables' : s.kind === 'infrastructure' ? 'worked out from observed traffic' : 'unknown agent')}{s.createdAt ? ` · ${when(s.createdAt)}` : ''}
                  {!s.apply && ' · informational: acknowledging only records the decision'}
                </p>
                {s.apply?.type === 'create-application' && !!s.apply.alternatives?.length && (
                  <AlternativeLabelPicker suggestion={s} alternatives={s.apply.alternatives} />
                )}
              </div>
              <div className="flex shrink-0 gap-2">
                <Button size="sm" onClick={() => decide(s, 'dismissed')} aria-label={`Dismiss ${s.title}`}>
                  <X size={ICON_SM} /> Dismiss
                </Button>
                {s.apply?.type === 'connect-cluster' ? (
                  canAdminister && (
                    <Button
                      size="sm"
                      variant="primary"
                      onClick={() => {
                        decide(s, 'accepted')
                        connect.start()
                      }}
                      aria-label={`Connect ${s.title}`}
                      data-testid="connect-suspicion"
                    >
                      <Plug size={ICON_SM} /> Connect it
                    </Button>
                  )
                ) : (
                  <Button size="sm" variant="primary" onClick={() => decide(s, 'accepted')} aria-label={`${s.apply ? 'Accept' : 'Acknowledge'} ${s.title}`}>
                    <Check size={ICON_SM} /> {s.apply ? 'Accept' : 'Acknowledge'}
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

        </>
      ) : (
        <>
      <ObservedClusters />
      <GoneRecords className="mt-8" />

        </>
      )}
    </>
  )
}
