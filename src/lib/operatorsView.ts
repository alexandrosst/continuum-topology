import { extrasOf, releaseTarget, type AgentDiagnostics } from './consent'
import { exportReach, exportRows } from './exportHealth'
import { CENTRAL_OPERATOR_ID, type FusionKind } from './fusionStatus'
import { ago } from './observed'
import type { Agent, Cluster, OperatorDestination, RegionalOperator, TelemetryIntent } from './types'

/** The four words a status is ever given, on this page and in the table, the path and the list of what needs attention. */
export type State = 'healthy' | 'attention' | 'down' | 'unknown'
export const STATE_WORD: Record<State, string> = { healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', unknown: 'Unknown' }
const SEVERITY: Record<State, number> = { down: 0, attention: 1, unknown: 2, healthy: 3 }

export type Kind = 'agent' | 'local' | 'regional' | 'central'
export const KINDS: Kind[] = ['agent', 'local', 'regional', 'central']
export const KIND_LABEL: Record<Kind, string> = { agent: 'Discovery agent', local: 'Local operator', regional: 'Regional operator', central: 'Central operator' }
export const KIND_PLURAL: Record<Kind, string> = { agent: 'Discovery agents', local: 'Local operators', regional: 'Regional operators', central: 'Central operator' }

/** What a "What to do" button opens: numbered steps (each may carry the exact command) and, when the page can do it for you, one action. */
export interface Todo {
  steps: { title: string; text?: string; command?: string }[]
  action?: { kind: 'configure' | 'address' | 'renew' | 'fusion' | 'agents'; label: string }
}

/** One component's status: a state, and for anything amber or red the sentence that says what is wrong and what to do about it. */
export interface Verdict {
  state: State
  reason?: string
  todo?: Todo
}

/** What the certificate column says. Operator certificates last 30 days and renew themselves, so only a failing renewal is a problem.
 *  `endsAt` / `until` is the day the soonest of them runs out; an agent's own certificate has no date to show. */
export type CertCell = { kind: 'none' } | { kind: 'auto'; endsAt?: string } | { kind: 'failing' | 'expired'; until: string }

export interface ComponentRow {
  id: string
  kind: Kind
  name: string
  cluster?: Cluster
  agentId?: string
  operator?: RegionalOperator
  verdict: Verdict
  /** Disconnected on purpose: still listed so it can be removed, but never counted or listed as something to fix. */
  ended?: boolean
  version?: string
  cert: CertCell
  sendsTo: string
  lastData?: string
}

const MIN = 60_000
/** Agents send a heartbeat every 30 seconds; two missed is "late". */
const LATE_AFTER_MS = 75_000
/** A telemetry request nothing has answered for this long is no longer "just run the command". */
const PENDING_AFTER_MS = 60 * MIN

/** "2 h 8 min", "14 min", "40 s": how long, for a sentence. */
export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 90) return `${s} s`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min`
  if (m < 48 * 60) return m % 60 === 0 ? `${m / 60} h` : `${Math.floor(m / 60)} h ${m % 60} min`
  const h = Math.round(m / 60)
  return h % 24 === 0 ? `${h / 24} d` : `${Math.floor(h / 24)} d ${h % 24} h`
}

/** "12 s ago" for the last-data column: the point of the column is to see it move, so seconds are kept. */
export function lastDataText(iso: string | undefined, now: number): string {
  if (!iso) return 'No data yet'
  const s = Math.round((now - Date.parse(iso)) / 1000)
  return s < 60 ? `${Math.max(0, s)} s ago` : ago(iso, now)
}

export const shortDate = (iso: string | number) => new Date(iso).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })

const newest = (dates: (string | undefined)[]): string | undefined => dates.filter((d): d is string => !!d).sort().pop()

/** The Service the regional-operator chart creates for a release named after the operator: the chart's own naming rule - the release name
 *  plus "-regional-operator", unless it already says so, cut to 63 characters. The server applies the same rule (operatorServiceName), so
 *  the two agree on what to dial and to read. */
export function operatorServiceName(id: string): string {
  const name = id.includes('regional-operator') ? id : `${id}-regional-operator`
  return name.slice(0, 63).replace(/-+$/, '')
}

/** Where a destination goes, in one short line for a table: another operator by name (FUSION's door is just "FUSION"), an endpoint, or
 *  - the central operator's own - its three stores. */
export function destinationLabel(d: OperatorDestination, operators: Pick<RegionalOperator, 'id' | 'name'>[]): string {
  if (d.kind === 'operator') return d.targetOperatorId === CENTRAL_OPERATOR_ID ? 'FUSION' : operators.find((o) => o.id === d.targetOperatorId)?.name ?? `Operator ${d.targetOperatorId ?? ''}`.trim()
  if (d.kind === 'fusion') return 'Prometheus, Loki and Tempo'
  return d.endpoint
}

/** What an operator row says about where other clusters reach it. */
export type AddressCell = { kind: 'set'; address: string; text: string } | { kind: 'pending' | 'cluster'; text: string }

/** Reachable with a recorded address; "Needs an address" while it is exposed and none is recorded (the one that wants an action);
 *  otherwise only its own cluster can send to it. `addressState` is the server's word; an older server's answer is read from the
 *  fields it does send. */
export function addressCell(op: Pick<RegionalOperator, 'address' | 'addressState' | 'reachableFromOtherClusters' | 'exposure'>): AddressCell {
  const set = op.addressState === 'set' || (op.addressState === undefined && op.reachableFromOtherClusters === true && !!op.address)
  if (set && op.address) return { kind: 'set', address: op.address, text: `Reachable at ${op.address}` }
  if (op.addressState === 'pending' || (op.addressState === undefined && (op.exposure === 'loadbalancer' || op.exposure === 'nodeport'))) return { kind: 'pending', text: 'Needs an address' }
  return { kind: 'cluster', text: 'This cluster only' }
}

/** One approved agent's local telemetry, joined: what it reports as running (`installedTelemetry`) and what was asked of it (the
 *  active TelemetryIntent). */
export interface LocalRow {
  agent: { id: string; name: string; clusterId?: string }
  cluster?: { id: string; name: string }
  /** Signals the agent reports as running. */
  installed: string[]
  reportedAt?: string
  diagnostics?: AgentDiagnostics
  intent?: TelemetryIntent
  /** Asked for, and the agent has not yet reported it running. */
  pending: boolean
  /** Reported running, and not what was asked for: the install was changed (or not yet re-run) since. */
  drifted: boolean
}

const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every((x) => b.includes(x))

/** Local operators are agents, not records of their own: every approved agent with telemetry running, plus every one that has been
 *  asked to run some and has not said so yet - which is how a command that was just run shows up before its first report. */
export function joinLocal(opts: {
  agents: { id: string; name: string; status: string; clusterId?: string }[]
  clusters: { id: string; name: string }[]
  rawAgents: readonly unknown[] | undefined
  intents: TelemetryIntent[]
}): LocalRow[] {
  const rows: LocalRow[] = []
  for (const a of opts.agents) {
    if (a.status !== 'approved') continue
    const extras = extrasOf(opts.rawAgents, a.id)
    const installed = extras.diagnostics?.installedTelemetry ?? []
    // The newest active intent for this agent: older ones were replaced.
    const intent = opts.intents.filter((i) => i.agentId === a.id && i.status === 'active').sort((x, y) => Date.parse(y.createdAt) - Date.parse(x.createdAt))[0]
    if (installed.length === 0 && !intent) continue
    rows.push({
      agent: a,
      cluster: opts.clusters.find((c) => c.id === a.clusterId),
      installed,
      reportedAt: extras.diagnostics?.reportedAt,
      diagnostics: extras.diagnostics,
      intent,
      pending: installed.length === 0,
      drifted: installed.length > 0 && !!intent && !sameSet(intent.signals.map((s) => s.id), installed),
    })
  }
  return rows
}

/** Where an intent sends, for a table cell: the operator it names (so the cell can show its health), or the endpoint. */
export function intentDestination(intent: TelemetryIntent | undefined): { operatorId?: string; label: string } | undefined {
  if (!intent) return undefined
  if (intent.routes && Object.keys(intent.routes).length > 0) return { label: 'One per signal type' }
  const d: OperatorDestination = intent.destination
  if (d.kind === 'operator' && d.targetOperatorId) return { operatorId: d.targetOperatorId, label: d.targetOperatorId }
  return { label: d.endpoint }
}

/** Every operator an intent sends to, whether it has one destination or one per signal type. */
const intentTargets = (intent: TelemetryIntent | undefined): string[] =>
  [intent?.destination, ...Object.values(intent?.routes ?? {})].flatMap((d) => (d?.kind === 'operator' && d.targetOperatorId ? [d.targetOperatorId] : []))

// ---------------------------------------------------------------- what to do, per kind of problem

const podsOf = (namespace: string) => `kubectl get pods --namespace ${namespace}`

/** Look at an agent's release in its cluster: the agent itself, and the collectors it runs for local telemetry. */
function agentSteps(a: Pick<Agent, 'namespace' | 'releaseName'> | undefined, what: string): Todo['steps'] {
  const { namespace, release } = releaseTarget({ namespace: a?.namespace, release: a?.releaseName })
  return [
    { title: `Check that ${what} is running in the cluster`, command: podsOf(namespace) },
    { title: 'Read what it says', text: 'Look for a line about reaching this server, a certificate or the destination.', command: `kubectl logs --namespace ${namespace} --selector app.kubernetes.io/instance=${release} --tail 50` },
  ]
}

function operatorSteps(op: RegionalOperator): Todo['steps'] {
  return [
    { title: 'Check that the operator is running', command: podsOf('continuum-system') },
    { title: 'Read what it says', text: 'Look for a line about reaching this server or its certificate.', command: `kubectl logs deployment/${operatorServiceName(op.id)} --namespace continuum-system --all-containers --tail 50` },
  ]
}

// ---------------------------------------------------------------- state, per kind

/** A discovery agent: only what the connection says. Anything the server cannot judge (a sample or hand-made agent) is Unknown, not down. */
export function agentVerdict(a: Agent, now: number): Verdict {
  if (a.connected === undefined) return { state: 'unknown', reason: 'Not connected live, so there is nothing to judge it by.' }
  const since = a.lastHeartbeat ? now - Date.parse(a.lastHeartbeat) : undefined
  const todo: Todo = { steps: agentSteps(a, 'the agent'), action: { kind: 'agents', label: 'Open in Agents' } }
  if (a.connected === false) return { state: 'down', reason: `Has not reported for ${since === undefined ? 'a while' : duration(since)}. What Ikhnos knows about this cluster is out of date.`, todo }
  if (since !== undefined && since > LATE_AFTER_MS) return { state: 'attention', reason: `Its heartbeat is late: the last one was ${duration(since)} ago.`, todo }
  return { state: 'healthy' }
}

/** A local operator: the collectors an agent runs. They can only be judged by what the agent last said, so with the agent offline the
 *  honest state is Unknown - never Healthy - and the reason names the agent. */
export function localVerdict(r: LocalRow, agent: Agent | undefined, destination: string, now: number): { verdict: Verdict; lastData?: string } {
  const configure: Todo['action'] = { kind: 'configure', label: 'Change what it sends' }
  if (agent?.connected === false) {
    const since = agent.lastHeartbeat ? duration(now - Date.parse(agent.lastHeartbeat)) : undefined
    return { verdict: { state: 'unknown', reason: `Its agent has not reported${since ? ` for ${since}` : ''}, so what this collector is doing is not known.` } }
  }
  if (r.pending) {
    const asked = r.intent ? now - Date.parse(r.intent.createdAt) : 0
    if (asked < PENDING_AFTER_MS) return { verdict: { state: 'unknown', reason: 'Asked to run telemetry. Nothing is reported running yet.' } }
    const todo: Todo = { steps: [{ title: 'Run the install command in the cluster', text: 'The wizard shows it again, ready to copy.' }], action: configure }
    return { verdict: { state: 'attention', reason: `Asked to run telemetry ${duration(asked)} ago, but nothing is running: the command may not have been run.`, todo } }
  }
  const lastData = newest((r.diagnostics?.exportHealth?.routes ?? []).map((x) => x.lastSentAt))
  if (r.drifted) {
    const todo: Todo = { steps: [{ title: 'Run the install command again', text: 'The wizard shows it, ready to copy.' }], action: configure }
    return { lastData, verdict: { state: 'attention', reason: 'What it runs is not what was asked: the install changed, or the new command has not been run.', todo } }
  }
  const rows = r.diagnostics?.exportHealth && exportReach(r.diagnostics) === 'ok' ? exportRows(r.installed, r.diagnostics.exportHealth) : undefined
  if (!rows) return { lastData, verdict: { state: 'unknown', reason: 'It does not report its export counters, so it is not known whether data is flowing.' } }
  const todo: Todo = { steps: [...agentSteps(agent, 'the collector'), { title: `Check that ${destination} is reachable from the cluster` }], action: configure }
  if (rows.some((x) => x.state === 'failing')) return { lastData, verdict: { state: 'down', reason: `Sending to ${destination} is failing.`, todo } }
  if (rows.some((x) => x.state === 'silent')) return { lastData, verdict: { state: 'attention', reason: lastData ? `No data has been sent for ${duration(now - Date.parse(lastData))}.` : 'No data has been sent recently.', todo } }
  if (rows.every((x) => x.state === 'waiting')) return { lastData, verdict: { state: 'unknown', reason: 'Waiting for its first data.' } }
  if (rows.some((x) => x.state === 'waiting')) return { lastData, verdict: { state: 'attention', reason: 'Some signal types have not sent anything yet.', todo } }
  return { lastData, verdict: { state: 'healthy' } }
}

/** Receiver and client certificates are the ones that renew (the CA outlives them); with neither date, the server issued nothing to show. */
export function certCell(op: Pick<RegionalOperator, 'certs' | 'certState'>): CertCell {
  const ends = [op.certs?.receiverNotAfter, op.certs?.clientNotAfter].filter((d): d is string => !!d).map((d) => Date.parse(d)).filter((t) => !Number.isNaN(t))
  if (ends.length === 0) return { kind: 'none' }
  const soonest = Math.min(...ends)
  if (op.certState === 'expired' || op.certState === 'renewal-failing') return { kind: op.certState === 'expired' ? 'expired' : 'failing', until: new Date(soonest).toISOString() }
  return { kind: 'auto', endsAt: new Date(soonest).toISOString() }
}

/** A regional or central operator. The worst thing wins and is said once: an ended one, an expired certificate, silence, a failing renewal,
 *  a missing address, and only then what its heartbeat says. */
export function operatorVerdict(op: RegionalOperator, cert: CertCell, now: number): Verdict {
  const central = op.id === CENTRAL_OPERATOR_ID
  if (op.status !== 'active') return { state: 'down', reason: `Disconnected${op.reason ? `: ${op.reason}` : ''}. It no longer counts as part of the path.` }
  const h = op.health
  const renew: Todo = {
    steps: [...operatorSteps(op), { title: 'If it keeps failing, issue new certificates', text: 'This replaces them. The commands to run where it is installed follow.' }],
    action: central ? undefined : { kind: 'renew', label: 'Renew certificates now' },
  }
  if (cert.kind === 'expired') return { state: 'down', reason: `Its certificate expired on ${shortDate(cert.until)}. Senders can no longer connect.`, todo: renew }
  if (h?.state === 'offline' && !central) {
    return { state: 'down', reason: `Offline${h.lastSeenAt ? `, last heard from ${duration(now - Date.parse(h.lastSeenAt))} ago` : ''}. Data sent to it is not arriving.`, todo: { steps: operatorSteps(op) } }
  }
  if (cert.kind === 'failing') return { state: 'attention', reason: `Certificate renewal is failing. The current one is valid until ${shortDate(cert.until)}, and data still flows.`, todo: renew }
  if (op.addressState === 'pending') {
    const todo: Todo = { steps: [{ title: 'Find the address its Service was given and record it', text: 'The next window shows the command that finds it.' }], action: { kind: 'address', label: 'Record the address' } }
    return { state: 'attention', reason: 'Clusters elsewhere cannot send to it yet: no address is recorded for it.', todo }
  }
  if (central) {
    if (h?.state === 'attention') return { state: 'attention', reason: 'FUSION reports a problem with one of its parts.', todo: { steps: [{ title: 'Open FUSION', text: 'Its overview says which part and what to do.' }], action: { kind: 'fusion', label: 'Open FUSION' } } }
    if (h?.state === 'starting') return { state: 'unknown', reason: 'FUSION is starting. This takes a minute or two.' }
    return h?.state === 'online' ? { state: 'healthy' } : { state: 'unknown', reason: 'FUSION is off, so nothing is saved there.' }
  }
  if (h?.state === 'online' && h.reporting) return { state: 'healthy' }
  if (h?.state === 'waiting') return { state: 'unknown', reason: 'Waiting for its first health report.' }
  return { state: 'unknown', reason: 'Health reporting is off, so it is not known whether it is running.' }
}

// ---------------------------------------------------------------- the rows, the path and what needs attention

export interface BuildInput {
  agents: Agent[]
  clusters: Cluster[]
  rawAgents: readonly unknown[] | undefined
  intents: TelemetryIntent[]
  /** Administrators only: the organisation's operators, the central one included. */
  operators: RegionalOperator[]
  /** What a destination can be named by (for everyone else, the read model of the destination picker). */
  known: Pick<RegionalOperator, 'id' | 'name'>[]
  now: number
}

/** Every component of the path in one list, worst first: discovery agents, local operators, regional operators and the central one. */
export function buildComponents({ agents, clusters, rawAgents, intents, operators, known, now }: BuildInput): ComponentRow[] {
  const cluster = (id?: string) => clusters.find((c) => c.id === id)
  const locals = joinLocal({ agents, clusters, rawAgents, intents }).map((r) => {
    const dest = intentDestination(r.intent)
    const sendsTo = !dest ? 'Not recorded' : dest.operatorId ? destinationLabel(r.intent!.destination, known) : dest.label
    return { r, sendsTo, ...localVerdict(r, agents.find((a) => a.id === r.agent.id), sendsTo, now) }
  })
  const rows: ComponentRow[] = agents
    .filter((a) => a.status === 'approved')
    .map((a) => ({ id: `agent:${a.id}`, kind: 'agent', name: a.name, cluster: cluster(a.clusterId), agentId: a.id, verdict: agentVerdict(a, now), version: a.version, cert: { kind: 'auto' }, sendsTo: 'This server', lastData: a.lastHeartbeat }))
  for (const l of locals) {
    // Its client certificate towards an operator renews with the agent's own; sending to an endpoint has none.
    const cert: CertCell = intentTargets(l.r.intent).length > 0 ? { kind: 'auto' } : { kind: 'none' }
    rows.push({ id: `local:${l.r.agent.id}`, kind: 'local', name: `${l.r.agent.name} collector`, cluster: cluster(l.r.agent.clusterId), agentId: l.r.agent.id, verdict: l.verdict, cert, sendsTo: l.sendsTo, lastData: l.lastData })
  }
  for (const op of operators) {
    const central = op.id === CENTRAL_OPERATOR_ID
    const cert: CertCell = op.status === 'active' ? certCell(op) : { kind: 'none' }
    // A regional operator says nothing about the data passing through it: the newest data a local operator reports sending to it is the evidence.
    const lastData = central ? op.health?.lastSeenAt : newest(locals.filter((l) => intentTargets(l.r.intent).includes(op.id)).map((l) => l.lastData))
    rows.push({ id: `${central ? 'central' : 'regional'}:${op.id}`, kind: central ? 'central' : 'regional', name: central ? 'Central operator' : op.name, operator: op, verdict: operatorVerdict(op, cert, now), ended: op.status !== 'active', cert, sendsTo: central ? 'FUSION' : destinationLabel(op.destination, operators), lastData })
  }
  return rows.sort((x, y) => SEVERITY[x.verdict.state] - SEVERITY[y.verdict.state] || KINDS.indexOf(x.kind) - KINDS.indexOf(y.kind) || x.name.localeCompare(y.name))
}

/** What needs attention: every amber or red component that has something to do about it (worst first, as the rows are). */
export const needAttention = (rows: ComponentRow[]): ComponentRow[] => rows.filter((r) => !r.ended && SEVERITY[r.verdict.state] <= SEVERITY.attention && r.verdict.todo)

/** One hop of the data path: how many of this kind there are, how many are healthy, the worst state among them and the newest data. */
export interface Hop {
  key: Kind | 'fusion'
  label: string
  total: number
  healthy: number
  state?: State
  /** How many of them are in that worst state. */
  worst: number
  lastData?: string
}

const worstState = (states: State[]): State | undefined => [...states].sort((a, b) => SEVERITY[a] - SEVERITY[b])[0]

/** The path from the discovery agents to FUSION. FUSION is not a row of the table (it has a section of its own): its hop is its own state,
 *  and is left out while it is not known or this server cannot run it at all. */
export function buildHops(rows: ComponentRow[], fusion: { kind: FusionKind; lastDataAt?: string } | undefined): Hop[] {
  const live = rows.filter((r) => !r.ended)
  const hops: Hop[] = KINDS.map((k) => {
    const of = live.filter((r) => r.kind === k)
    const state = worstState(of.map((r) => r.verdict.state))
    return { key: k, label: KIND_PLURAL[k], total: of.length, healthy: of.filter((r) => r.verdict.state === 'healthy').length, state, worst: of.filter((r) => r.verdict.state === state).length, lastData: newest(of.map((r) => r.lastData)) }
  })
  if (!fusion || fusion.kind === 'checking' || fusion.kind === 'unavailable') return hops
  const state = ({ running: 'healthy', attention: 'attention', starting: 'unknown', off: undefined } as const)[fusion.kind]
  return [...hops, { key: 'fusion', label: 'FUSION', total: state ? 1 : 0, healthy: state === 'healthy' ? 1 : 0, state, worst: state ? 1 : 0, lastData: fusion.lastDataAt }]
}
