import { observation } from './provenance'
import type { Agent, Cluster } from './types'

/**
 * "Getting started": where a new organisation is on the way from nothing to a live cluster.
 *
 * Every step is worked out from what the server holds (its settings, its agents, the records they reported), never
 * from what somebody clicked, so the list is true after a reload, from another browser, and when a colleague did
 * the step. Pure, so the wording can be tested.
 */

export type StepState = 'todo' | 'current' | 'done'
export type StepId = 'images' | 'connect' | 'approve' | 'live' | 'observe'

export type StepAction =
  /** Settings → Installation. */
  | { kind: 'settings'; label: string }
  /** Opens the Connect a cluster wizard (`options`: at the step where traffic observation and the node probe are chosen). */
  | { kind: 'connect'; label: string; options?: boolean }
  /** The approval card of one waiting agent. */
  | { kind: 'approval'; label: string; agentId: string }
  /** The Agents page. */
  | { kind: 'agents'; label: string }

export interface ChecklistStep {
  id: StepId
  state: StepState
  title: string
  /** One line in plain words: what is true now, or what happens next. */
  line: string
  action?: StepAction
  /** Does not have to be done for the cluster to work. */
  optional?: boolean
}

export interface Checklist {
  steps: ChecklistStep[]
  /** How many of the steps are done. */
  done: number
  /** The cluster is live: the list has served its purpose and goes away. */
  finished: boolean
}

type ChecklistAgent = Pick<Agent, 'id' | 'name' | 'status' | 'clusterId' | 'connected' | 'observer'>
type ChecklistCluster = Pick<Cluster, 'id' | 'name' | 'source' | 'state' | 'stateReason' | 'stale' | 'deletedAt'>

export interface ChecklistInput {
  /** From the server's install info: an image registry is set (by this organisation or by the server's flags). */
  imagesConfigured: boolean
  /** The registry, when it is known and set. */
  imageRegistry?: string
  agents: readonly ChecklistAgent[]
  clusters: readonly ChecklistCluster[]
  /** Nodes whose facts came from a node probe. */
  probedNodes?: number
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
const names = (xs: readonly { name: string }[]) => (xs.length <= 2 ? xs.map((x) => x.name).join(' and ') : `${xs[0].name} and ${xs.length - 1} more`)

export function deriveChecklist(input: ChecklistInput): Checklist {
  const { agents } = input
  const enrolled = agents.filter((a) => a.status === 'pending' || a.status === 'approved')
  const waiting = agents.filter((a) => a.status === 'pending')
  const approved = agents.filter((a) => a.status === 'approved')
  const expired = agents.filter((a) => a.status === 'expired')
  const rejected = agents.filter((a) => a.status === 'rejected')
  // Only clusters an agent observes can be "live"; a cluster typed by hand is not evidence of anything.
  const seen = input.clusters.filter((c) => !c.deletedAt && c.source === 'discovered')
  const live = seen.filter((c) => observation(c)?.kind === 'live')
  const notLive = seen.filter((c) => observation(c)?.kind !== 'live')
  const watching = agents.some((a) => a.observer && (a.observer.collectors?.length ?? 0) > 0) || (input.probedNodes ?? 0) > 0

  const done: Record<StepId, boolean> = {
    images: input.imagesConfigured,
    connect: enrolled.length > 0,
    approve: approved.length > 0,
    live: live.length > 0,
    observe: watching,
  }

  const steps: ChecklistStep[] = [
    {
      id: 'images',
      title: 'Choose where images live',
      line: input.imagesConfigured
        ? `Install commands pull the agent from ${input.imageRegistry || 'the registry you set'}.`
        : 'Not set: install commands use the chart’s built-in image names, which works only if you published those images yourself.',
      action: { kind: 'settings', label: input.imagesConfigured ? 'Review' : 'Set a registry' },
      state: 'todo',
    },
    {
      id: 'connect',
      title: 'Connect a cluster',
      line: enrolled.length
        ? `${plural(enrolled.length, 'agent has', 'agents have')} enrolled (${names(enrolled)}).`
        : 'Create a token and run one install command inside the cluster. The agent dials out, so no kubeconfig and no inbound port.',
      action: enrolled.length ? undefined : { kind: 'connect', label: 'Connect a cluster' },
      state: 'todo',
    },
    {
      id: 'approve',
      title: 'Approve it',
      line: waiting.length
        ? `${names(waiting)} ${waiting.length === 1 ? 'is' : 'are'} waiting. Type the approval code its log prints; nothing is read before you do.`
        : approved.length
          ? `Approved: ${names(approved)}.`
          : expired.length && !enrolled.length
            ? `The request from ${names(expired)} ran out. The agent asks again by itself, with a new code.`
            : rejected.length && !enrolled.length
              ? `The last request (${names(rejected)}) was rejected. Connect the cluster again with a new token.`
              : 'You confirm each cluster once, with a code that only someone who can read its log can see.',
      action: waiting.length ? { kind: 'approval', label: 'Review approval', agentId: waiting[0].id } : undefined,
      state: 'todo',
    },
    {
      id: 'live',
      title: 'See it live',
      line: live.length
        ? `${names(live)} ${live.length === 1 ? 'is' : 'are'} reporting now.`
        : notLive.length
          ? `${names(notLive)}: ${observation(notLive[0])?.reason ?? observation(notLive[0])?.label ?? 'not reporting'}.`
          : approved.length
            ? approved.some((a) => a.connected === false)
              ? 'Approved, but the agent is not connected right now. Check it on Agents.'
              : 'Approved. Waiting for the agent’s first report of what it finds.'
            : 'The cluster appears here once its agent has been approved and has reported.',
      action: approved.length && !live.length ? { kind: 'agents', label: 'Check the agent' } : undefined,
      state: 'todo',
    },
    {
      id: 'observe',
      title: 'Watch traffic and probe nodes',
      optional: true,
      line: watching
        ? 'A traffic observer or node probe is reporting.'
        : 'Optional. Both are off until you switch them on in the install options; they are the only parts that run on every node.',
      action: watching ? undefined : { kind: 'connect', label: 'Choose options', options: true },
      state: 'todo',
    },
  ]

  // One step is "current": the first that is not done. The registry is skippable (the install works without it), so it
  // stops being the focus once a cluster has enrolled.
  const focus = steps.findIndex((s) => !done[s.id] && !(s.id === 'images' && enrolled.length > 0) && !(s.id === 'observe' && live.length > 0))
  steps.forEach((s, i) => {
    s.state = done[s.id] ? 'done' : i === focus ? 'current' : 'todo'
  })
  return { steps, done: steps.filter((s) => s.state === 'done').length, finished: done.live }
}

/* ---------- dismissal, per organisation ---------- */

const KEY = 'continuum-getting-started/dismissed'
const keyFor = (org: string) => `${KEY}/${org}`

/** Whether this organisation's list was dismissed in this browser. Storage that is blocked reads as "not dismissed". */
export function wasDismissed(org: string | undefined, storage?: Pick<Storage, 'getItem'>): boolean {
  if (!org) return false
  try {
    return (storage ?? localStorage).getItem(keyFor(org)) === '1'
  } catch {
    return false
  }
}

export function dismiss(org: string | undefined, storage?: Pick<Storage, 'setItem'>): void {
  if (!org) return
  try {
    ;(storage ?? localStorage).setItem(keyFor(org), '1')
  } catch {
    /* storage unavailable: the card simply comes back next visit */
  }
}
