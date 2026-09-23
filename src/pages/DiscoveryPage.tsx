import clsx from 'clsx'
import { ArrowRight, Check, ChevronDown, KeyRound, Plug, PlugZap, X } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { when } from '@/components/discovery/AgentParts'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import GettingStarted, { useGettingStarted } from '@/components/GettingStarted'
import { GoneRecords, ObservedClusters } from '@/components/Observations'
import { Button, EmptyState, PageHeader, Pill } from '@/components/ui/primitives'
import { originLabel } from '@/lib/present'
import type { GroupingAlternative, Suggestion } from '@/lib/types'
import { usePlacementSuggestions } from '@/lib/usePlacement'
import { useApprovalLocks } from '@/store/approvalLocks'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

/**
 * A `create-application` suggestion only shows the label that won. Some services carry more than one
 * of the labels discovery understands (an Argo app that is also part of a bigger Helm release, say),
 * and the one that wins by precedence is not always the one a person wants grouped by. This lets them
 * regroup onto a runner-up instead of dismissing the suggestion and typing a name from scratch.
 */
function AlternativeLabelPicker({ suggestion, alternatives }: { suggestion: Suggestion; alternatives: GroupingAlternative[] }) {
  const applyAlternative = useTopology((s) => s.applyAlternative)
  const [open, setOpen] = useState(false)
  return (
    <div className="mt-1.5">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-1 text-xs text-nb-500 hover:text-accent"
        aria-expanded={open}
      >
        <ChevronDown size={12} className={open ? 'rotate-180' : ''} aria-hidden />
        Use a different label instead
      </button>
      {open && (
        <div className="mt-1.5 flex flex-wrap gap-1.5" role="group" aria-label="Alternative labels">
          {alternatives.map((alt) => (
            <button
              key={`${alt.origin}-${alt.name}`}
              type="button"
              title={alt.signal}
              onClick={() => applyAlternative(suggestion.id, alt)}
              className="rounded-full border border-nb-800 bg-nb-930 px-2.5 py-1 text-xs text-nb-400 transition-colors hover:border-accent/60 hover:text-white"
            >
              {originLabel(alt.origin)}: <span className="font-medium text-nb-200">{alt.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

export default function DiscoveryPage() {
  const { agents, suggestions, decideSuggestion, decideDerived } = useTopology()
  const placement = usePlacementSuggestions().suggestions
  const server = useServer()
  // One flow, shared with the Agents page: the same wizard, the same `?connect=1` link.
  const connect = useConnectFlow()
  const started = useGettingStarted()
  const connected = server.status === 'connected'
  const canAdminister = connect.canStart
  // Approving is done on the Agents page. Here it is only pointed at: a request rejected for too many wrong codes counts too, because it still needs a look.
  const locked = useApprovalLocks((s) => s.locked)
  const waiting = agents.filter((a) => a.status === 'pending' && a.requestedAt)
  const rejectedLocked = agents.filter((a) => a.status === 'rejected' && locked[a.id])
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
  const needAttention = waiting.length + rejectedLocked.length

  return (
    <>
      <PageHeader
        title="Discovery"
        description="What the agents found that needs your decision, and what they see now. Nothing here changes your topology until you accept it."
        actions={
          canAdminister && (
            <Button variant="primary" onClick={connect.start}>
              <Plug size={16} /> Connect a cluster
            </Button>
          )
        }
      />

      {!connected && (
        <div className="mb-6 flex items-center justify-between gap-4 rounded-xl border border-nb-850 bg-nb-925 px-5 py-3" data-testid="server-status">
          <div className="flex items-center gap-3 text-sm text-nb-400">
            <PlugZap size={16} className="shrink-0 text-nb-500" />
            {server.status === 'error' ? server.error : 'Not connected to a Continuum server. Without one you can still model topologies by hand.'}
          </div>
          <Button size="sm" onClick={connect.start}>Connect to server</Button>
        </div>
      )}
      {connected && server.error && <p role="status" className="mb-4 text-sm text-amber-300">{server.error} Retrying…</p>}

      {/* Approving is the Agents page's job: this page only says that something waits there. */}
      {needAttention > 0 && (
        <div className="mb-6 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-400/30 bg-amber-400/5 px-5 py-3" role="status" data-testid="approvals-banner">
          <p className="flex items-center gap-2.5 text-sm text-amber-200">
            <KeyRound size={16} className="shrink-0" aria-hidden />
            {waiting.length > 0
              ? `${waiting.length} cluster${waiting.length === 1 ? ' is' : 's are'} waiting for approval.${canAdminister ? '' : ' An administrator has to approve it.'}`
              : `${rejectedLocked.length} request${rejectedLocked.length === 1 ? ' was' : 's were'} rejected after too many wrong codes.`}
          </p>
          <Link to="/agents#approvals" className="inline-flex items-center gap-1.5 text-sm text-accent hover:underline" data-testid="review-approvals">
            {canAdminister ? 'Review on Agents' : 'See on Agents'} <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
      )}

      {showStarted && started && (
        <div className="mb-8">
          <GettingStarted checklist={started.checklist} onConnect={connect.start} onDismiss={started.dismiss} />
        </div>
      )}

      <h2 className="mb-2 text-sm font-medium text-white">Inbox {open.length > 0 && <span className="ml-1 text-nb-500">({open.length})</span>}</h2>
      {open.length === 0 ? (
        <div className="mb-8">
          {approved.length === 0 ? (
            <EmptyState
              title="Nothing found yet"
              description="No cluster is connected yet, so no agent has reported anything. What agents find (new devices, outside endpoints, suggested groupings) is listed here for you to accept or dismiss."
              action={!showStarted && canAdminister ? <Button variant="primary" onClick={connect.start}><Plug size={16} /> Connect a cluster</Button> : undefined}
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
                  <span className="text-sm font-medium text-white">{s.title}</span>
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
                  <X size={14} /> Dismiss
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
                      <Plug size={14} /> Connect it
                    </Button>
                  )
                ) : (
                  <Button size="sm" variant="primary" onClick={() => decide(s, 'accepted')} aria-label={`${s.apply ? 'Accept' : 'Acknowledge'} ${s.title}`}>
                    <Check size={14} /> {s.apply ? 'Accept' : 'Acknowledge'}
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {agents.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-nb-850 bg-nb-925 px-5 py-4" data-testid="agents-summary">
          <p className="text-sm text-nb-300">
            {approved.length} approved · {agents.filter((a) => a.connected).length} connected now
            {waiting.length > 0 && <span className="text-amber-300"> · {waiting.length} waiting for approval</span>}
          </p>
          <Link to="/agents" className="inline-flex items-center gap-1.5 text-sm text-accent hover:underline" data-testid="see-agents">
            Agents: enrollment, approval, consent and health <ArrowRight size={14} aria-hidden />
          </Link>
        </div>
      )}

      <ObservedClusters className="mt-8" />
      <GoneRecords className="mt-8" />

      {connect.dialogs}
    </>
  )
}
