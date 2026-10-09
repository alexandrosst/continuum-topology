/**
 * The platform layer of the topology: the telemetry pipeline drawn next to the clusters it serves. Each cluster has its Discovery
 * agent and, when telemetry is installed, its Local operator; Regional operators, the Central operator and FUSION sit outside the
 * clusters, joined local -> regional -> central -> FUSION.
 *
 * Everything here is derived from what the app already loads (approved agents and their reports, telemetry intents, the operators
 * the server lists - the central one included, whose health is FUSION's own state), and nothing is invented: a time that is not
 * known is left out, a state that cannot be known is "Unknown". The result is plain data: graph.ts places it, the canvas draws it,
 * the Inspector describes it.
 */
import { extrasOf } from './consent'
import { exportReach, exportRows } from './exportHealth'
import { CENTRAL_OPERATOR_ID } from './fusionStatus'
import { TELEMETRY_SIGNALS, type Modality } from './install'
import { ago } from './observed'
import type { Agent, OperatorDestination, RegionalOperator, TelemetryIntent } from './types'

/** The four states, the same words everywhere a platform part is named. */
export type PlatformStatus = 'healthy' | 'attention' | 'down' | 'unknown'
export const PLATFORM_STATUS_WORD: Record<PlatformStatus, string> = { healthy: 'Healthy', attention: 'Needs attention', down: 'Not working', unknown: 'Unknown' }
const RANK: Record<PlatformStatus, number> = { healthy: 0, unknown: 1, attention: 2, down: 3 }
const worst = (...s: PlatformStatus[]): PlatformStatus => s.reduce((a, b) => (RANK[b] > RANK[a] ? b : a), 'healthy')

export type PlatformKind = 'agent' | 'local' | 'regional' | 'central' | 'fusion'

export interface PlatformEntity {
  /** 'agent:<agent id>', 'local:<agent id>', 'regional:<operator id>', 'central' or 'fusion'. */
  id: string
  kind: PlatformKind
  /** Node title: 'Discovery agent', 'Local operator', the operator's own name, 'Central operator', 'FUSION'. */
  name: string
  /** The line under the title: its version, what it collects, its role. */
  detail: string
  /** The cluster it runs in (agents and local operators). */
  clusterId?: string
  clusterName?: string
  status: PlatformStatus
  /** One sentence: what is wrong, or that nothing is. */
  sentence: string
  /** What to do, for a state that needs it. */
  todo?: string
  version?: string
  /** When data last arrived (local operators: from its collector counters; FUSION: from FUSION itself). */
  lastDataAt?: string
  /** An operator's heartbeat, which says it is alive, not that data arrived. */
  lastHeartbeatAt?: string
  /** What a local operator collects, by signal type. */
  collecting?: Modality[]
  /** A part that is not turned on (FUSION, when it is off): nothing to report, so it is Unknown, and drawn quietly. */
  off?: boolean
  /** The agent behind an agent or local operator row. */
  agentId?: string
  /** Where it sends: the platform parts it is joined to, and, for a destination that is no platform part, its address. */
  sendsTo: { id: string; name: string }[]
  elsewhere?: string
}

export interface PlatformEdge {
  id: string
  from: string
  to: string
  status: PlatformStatus
  /** Last data on this hop, as "2 s" / "14 min"; absent when the hop has no time of its own. */
  age?: string
  lastDataAt?: string
}

export interface PlatformModel {
  entities: PlatformEntity[]
  edges: PlatformEdge[]
}

export interface PlatformInput {
  /** The clusters on the canvas. */
  clusters: { id: string; name: string }[]
  agents: Agent[]
  rawAgents: readonly unknown[] | undefined
  intents: TelemetryIntent[]
  /** The organisation's operators, the central one included. Empty for someone who may not list them. */
  operators: RegionalOperator[]
  /** Draw FUSION as "Not turned on" when it is off. Only for someone who may list the operators: for anyone else it is not known. */
  showFusionOff?: boolean
}

/** Two missed reports: the same line the Agents page draws between "connected" and "late". */
const LATE_AFTER_MS = 75_000
const MODALITIES: Modality[] = ['metrics', 'logs', 'traces']
const MODALITY_OF = new Map(TELEMETRY_SIGNALS.map((x) => [x.id, x.modality]))
export const MODALITY_WORD: Record<Modality, string> = { metrics: 'Metrics', logs: 'Logs', traces: 'Traces' }

/** "2 s", "14 min", "3 h", "2 d". */
export function shortAge(iso: string | undefined, now: number): string | undefined {
  if (!iso) return undefined
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  if (Number.isNaN(s)) return undefined
  return s < 60 ? `${s} s` : s < 3600 ? `${Math.round(s / 60)} min` : s < 172_800 ? `${Math.round(s / 3600)} h` : `${Math.round(s / 86_400)} d`
}

const newest = (a?: string, b?: string) => (a && b ? (Date.parse(a) >= Date.parse(b) ? a : b) : (a ?? b))

/** The platform parts an intent's destinations name: the operator it sends to, or each one per signal type; and the address of any
 *  destination that is not one of them (a backend of the organisation's own). */
function destinationsOf(intent: TelemetryIntent | undefined): { operatorIds: string[]; elsewhere?: string } {
  if (!intent) return { operatorIds: [] }
  const all: OperatorDestination[] = [intent.destination, ...Object.values(intent.routes ?? {})]
  const operatorIds = [...new Set(all.filter((d) => d.kind === 'operator' && d.targetOperatorId).map((d) => d.targetOperatorId!))]
  const own = all.filter((d) => d.kind === 'external')
  return { operatorIds, elsewhere: own[0]?.endpoint }
}

/** A Discovery agent: connected and reporting, late, disconnected. */
function agentState(a: Agent, now: number): Pick<PlatformEntity, 'status' | 'sentence' | 'todo'> {
  const last = a.lastHeartbeat ? `, last heard from ${ago(a.lastHeartbeat, now)}` : ''
  if (a.connected === undefined) return { status: 'unknown', sentence: 'Not connected to a server, so its state is not known.' }
  if (a.connected === false) return { status: 'down', sentence: `Disconnected${last}.`, todo: 'Check that the agent is running in its cluster; it reconnects by itself once it is.' }
  if (a.lastHeartbeat && now - Date.parse(a.lastHeartbeat) > LATE_AFTER_MS) return { status: 'attention', sentence: `No report for ${shortAge(a.lastHeartbeat, now)}.`, todo: 'Check that the agent is running and can reach the server.' }
  return { status: 'healthy', sentence: `Reporting${a.lastHeartbeat ? `, last heard from ${ago(a.lastHeartbeat, now)}` : ''}.` }
}

/** A Local operator is what its agent reports it sends, so it cannot say more than its agent does. */
function localState(a: Agent, rawAgents: readonly unknown[] | undefined, installed: string[], agent: PlatformEntity, now: number): Pick<PlatformEntity, 'status' | 'sentence' | 'todo' | 'lastDataAt'> {
  if (agent.status === 'down' || agent.status === 'unknown') {
    const gap = a.lastHeartbeat ? `its agent has not reported for ${shortAge(a.lastHeartbeat, now)}` : 'its agent is not connected'
    return { status: 'unknown', sentence: `Not known: ${gap}.`, todo: 'Bring the agent back first; this shows again once it reports.' }
  }
  if (installed.length === 0) return { status: 'unknown', sentence: 'Asked for, and not installed yet. No data yet.', todo: 'Run the install command from the Pipeline.' }
  const d = extrasOf(rawAgents, a.id).diagnostics
  const reach = exportReach(d)
  if (reach === 'not-reported') return { status: 'unknown', sentence: 'This agent does not report what its collector sends.' }
  if (reach === 'unreadable') return { status: 'unknown', sentence: 'The agent cannot read its collector’s counters.', todo: 'Turn on telemetry health in the install, or update the agent.' }
  const rows = exportRows(installed, d!.exportHealth!)
  const lastDataAt = rows.reduce<string | undefined>((t, r) => newest(t, r.lastSentAt), undefined)
  const has = (s: string) => rows.some((r) => r.state === s)
  if (has('failing')) return { status: 'down', sentence: 'Sending is failing.', todo: 'Check where it sends: the address, and that the receiver accepts its certificate.', lastDataAt }
  if (has('silent')) return { status: 'attention', sentence: `Quiet: no data for ${shortAge(lastDataAt, now) ?? 'a while'}.`, todo: 'Check that what it collects is still running in the cluster.', lastDataAt }
  if (has('waiting')) return has('exporting') ? { status: 'attention', sentence: 'Only some signal types are sending.', todo: 'Check the signal types that have no data yet.', lastDataAt } : { status: 'unknown', sentence: 'No data yet.', lastDataAt }
  return { status: 'healthy', sentence: `Sending, last data ${ago(lastDataAt, now)}.`, lastDataAt }
}

/** A regional or the central operator: its heartbeat (for the central one, FUSION's state), its certificates, whether others can reach it. */
function operatorState(op: RegionalOperator, now: number): Pick<PlatformEntity, 'status' | 'sentence' | 'todo' | 'lastHeartbeatAt'> {
  const central = op.id === CENTRAL_OPERATOR_ID
  const h = op.health
  const lastHeartbeatAt = central ? undefined : h?.lastSeenAt
  const parts: Pick<PlatformEntity, 'status' | 'sentence' | 'todo'>[] = []
  if (op.certState === 'expired') parts.push({ status: 'down', sentence: 'Its certificate has expired, so nothing can send to it.', todo: 'Renew the certificate from the Pipeline.' })
  else if (op.certState === 'renewal-failing') {
    const since = op.certStateSince ? ` since ${shortAge(op.certStateSince, now)} ago` : ''
    parts.push({ status: 'attention', sentence: `Its certificate is not renewing${since}.`, todo: 'Renew it by hand from the Pipeline, and check that the server can reach the cluster.' })
  }
  if (op.addressState === 'pending') parts.push({ status: 'attention', sentence: 'Other clusters cannot reach it yet: no address is recorded.', todo: 'Record its address in the Pipeline.' })
  switch (h?.state) {
    case 'online':
      parts.push({ status: 'healthy', sentence: central ? 'Running.' : `Online, last heartbeat ${ago(h.lastSeenAt, now)}. Its certificates renew automatically.` })
      break
    case 'offline':
      parts.push({ status: 'down', sentence: `No heartbeat${h.lastSeenAt ? ` since ${ago(h.lastSeenAt, now).replace(' ago', '')} ago` : ''}.`, todo: 'Check that the operator is running in its cluster.' })
      break
    case 'starting':
      parts.push({ status: 'attention', sentence: 'Starting.', todo: 'Give it a minute; if it stays like this, open FUSION to see what is not up.' })
      break
    case 'attention':
      parts.push({ status: 'attention', sentence: 'FUSION reports a problem.', todo: 'Open FUSION to see what it reports.' })
      break
    case 'waiting':
      parts.push({ status: 'unknown', sentence: 'Waiting for its first heartbeat.' })
      break
    default:
      parts.push({ status: 'unknown', sentence: 'Its health is not reported.' })
  }
  const status = worst(...parts.map((p) => p.status))
  const main = parts.filter((p) => p.status === status).sort((a, b) => Number(!!b.todo) - Number(!!a.todo))[0]
  return { ...main, lastHeartbeatAt }
}

export function buildPlatform(input: PlatformInput, now: number): PlatformModel {
  const entities: PlatformEntity[] = []
  const edges: PlatformEdge[] = []
  const clusterName = new Map(input.clusters.map((c) => [c.id, c.name]))
  const intentOf = (agentId: string) => input.intents.filter((i) => i.agentId === agentId && i.status === 'active').sort((x, y) => Date.parse(y.createdAt) - Date.parse(x.createdAt))[0]
  const operators = new Map(input.operators.filter((o) => o.status === 'active').map((o) => [o.id, o]))
  const entityIdOf = (opId: string) => (opId === CENTRAL_OPERATOR_ID ? 'central' : `regional:${opId}`)

  // Inside the clusters: the agent, and what it runs.
  const senders: { local: PlatformEntity; targets: string[] }[] = []
  for (const a of input.agents) {
    if (a.status !== 'approved' || !a.clusterId || !clusterName.has(a.clusterId)) continue
    const installed = extrasOf(input.rawAgents, a.id).diagnostics?.installedTelemetry ?? []
    const intent = intentOf(a.id)
    const base = { clusterId: a.clusterId, clusterName: clusterName.get(a.clusterId), agentId: a.id }
    const agent: PlatformEntity = { ...base, id: `agent:${a.id}`, kind: 'agent', name: 'Discovery agent', sendsTo: [], detail: a.version ? `v${a.version}` : 'Agent', version: a.version, lastHeartbeatAt: a.lastHeartbeat, ...agentState(a, now) }
    entities.push(agent)
    if (installed.length === 0 && !intent) continue
    const { operatorIds, elsewhere } = destinationsOf(intent)
    const asked = new Set((intent?.signals.map((x) => x.id) ?? installed).map((id) => MODALITY_OF.get(id)))
    const collecting = MODALITIES.filter((m) => asked.has(m))
    const local: PlatformEntity = {
      ...base, id: `local:${a.id}`, kind: 'local', name: 'Local operator', sendsTo: [], detail: collecting.map((m) => MODALITY_WORD[m]).join(', ') || 'Collector', collecting, elsewhere,
      ...localState(a, input.rawAgents, installed, agent, now),
    }
    entities.push(local)
    senders.push({ local, targets: operatorIds })
  }

  // Outside: regional operators the clusters send to (or declare as their sources), then the central operator and FUSION.
  const targeted = new Set(senders.flatMap((s) => s.targets))
  const operatorOf = new Map<string, RegionalOperator>()
  for (const op of operators.values()) {
    if (op.id === CENTRAL_OPERATOR_ID) continue
    if (op.sourceClusterIds.length > 0 && !op.sourceClusterIds.some((c) => clusterName.has(c)) && !targeted.has(op.id)) continue
    entities.push({ id: entityIdOf(op.id), kind: 'regional', name: op.name, detail: 'Regional operator', sendsTo: [], ...operatorState(op, now) })
    operatorOf.set(entityIdOf(op.id), op)
  }
  const central = operators.get(CENTRAL_OPERATOR_ID)
  const fusionOn = !!central && central.health?.state !== 'off' && central.health?.state !== 'unknown'
  if (central && fusionOn) {
    const state = operatorState(central, now)
    const lastDataAt = central.health?.lastSeenAt
    entities.push({ id: 'central', kind: 'central', name: 'Central operator', detail: 'Gateway into FUSION', sendsTo: [], ...state })
    entities.push({
      id: 'fusion', kind: 'fusion', name: 'FUSION', detail: 'Metrics, logs and traces', sendsTo: [], status: state.status, lastDataAt,
      sentence: central.health?.state === 'online' ? `Running${lastDataAt ? `, last data ${ago(lastDataAt, now)}` : ', waiting for its first data'}.` : state.sentence,
      todo: state.status === 'healthy' ? undefined : (state.todo ?? 'Open FUSION to see what it reports.'),
    })
  } else if (input.showFusionOff) {
    entities.push({ id: 'fusion', kind: 'fusion', name: 'FUSION', detail: 'Not turned on', off: true, sendsTo: [], status: 'unknown', sentence: 'Not turned on.', todo: 'Turn it on in FUSION to keep what the operators collect.' })
  }

  // The hops.
  const byId = new Map(entities.map((e) => [e.id, e]))
  const join = (from: PlatformEntity, toId: string, extra: Partial<PlatformEdge> = {}) => {
    const to = byId.get(toId)
    if (!to) return
    from.sendsTo.push({ id: to.id, name: to.name })
    edges.push({ id: `${from.id}>${to.id}`, from: from.id, to: to.id, status: from.status, ...extra })
  }
  for (const { local } of senders) {
    // The agent runs the local operator: this hop carries the agent's state, and how long ago it last reported.
    const agent = byId.get(`agent:${local.agentId}`)!
    join(agent, local.id, { lastDataAt: agent.lastHeartbeatAt, age: shortAge(agent.lastHeartbeatAt, now) })
  }
  for (const { local, targets } of senders) {
    const ids = targets.map(entityIdOf).filter((id) => byId.has(id))
    // One destination: the hop carries this operator's own last-data time. Several (one per signal type): it has no single time.
    for (const id of ids) join(local, id, ids.length === 1 ? { lastDataAt: local.lastDataAt, age: shortAge(local.lastDataAt, now) } : {})
  }
  for (const [id, op] of operatorOf) {
    const next = op.destination.kind === 'operator' && op.destination.targetOperatorId ? entityIdOf(op.destination.targetOperatorId) : undefined
    if (next && byId.has(next)) join(byId.get(id)!, next)
    else if (op.destination.kind === 'external') byId.get(id)!.elsewhere = op.destination.endpoint
  }
  const fusion = byId.get('fusion')
  if (fusion && !fusion.off) join(byId.get('central')!, 'fusion', { lastDataAt: fusion.lastDataAt, age: shortAge(fusion.lastDataAt, now) })
  return { entities, edges }
}
