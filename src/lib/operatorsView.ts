import { extrasOf, type AgentDiagnostics } from './consent'
import type { OperatorDestination, RegionalOperator, TelemetryIntent } from './types'

const DAY = 86_400_000

/** The certificate chip of an operator row: nothing while they are fine, amber in the 60 days before they run out (the server
 *  says when that is: `certState`), red once one has. The number is read off the soonest date, so it is the one that matters. */
export function certChip(op: Pick<RegionalOperator, 'certs' | 'certState'>, now = Date.now()): { tone: 'warn' | 'bad'; text: string } | undefined {
  if (op.certState === 'expired') return { tone: 'bad', text: 'Certificate expired' }
  if (op.certState !== 'expiring') return undefined
  const dates = [op.certs?.receiverNotAfter, op.certs?.clientNotAfter, op.certs?.caNotAfter].filter((d): d is string => !!d).map((d) => Date.parse(d)).filter((t) => !Number.isNaN(t))
  if (dates.length === 0) return { tone: 'warn', text: 'Certificate expires soon' }
  const days = Math.max(0, Math.ceil((Math.min(...dates) - now) / DAY))
  return { tone: 'warn', text: `Certificate expires in ${days} ${days === 1 ? 'day' : 'days'}` }
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
