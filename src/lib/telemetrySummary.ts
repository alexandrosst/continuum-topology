/**
 * One sentence for the whole Telemetry tab, and the list of places a person should look at, worked out from the platform model alone.
 * "A place" is a cluster (its agent and its local operator count once, however many of the two are wrong) or one shared operator, and
 * it is what the problems pill counts on every tab: how many places have something wrong, tinted by the worst of it.
 */
import type { Problem } from './problems'
import type { PlatformEntity, PlatformModel, PlatformStatus } from './platformLayer'

export interface TelemetrySummary {
  /** Clusters whose agent runs a local operator: the ones that are meant to send. */
  clusters: number
  /** Of those, the ones whose data is on its way: the agent and the local operator are well and nothing after them is broken. */
  flowing: number
  /** Clusters with a local operator whose state is not known (yet): not a problem, not flowing either. */
  waiting: number
  /** Places with something wrong, worst first, then top to bottom as drawn. `id` is the part to open: the one that is worst. */
  places: Problem[]
  /** FUSION is not turned on: operators keep the data, or send it to an address of their own. */
  fusionOff: boolean
}

const isBad = (s: PlatformStatus) => s === 'attention' || s === 'down'
const alertOf = (s: PlatformStatus): Problem['alert'] => (s === 'down' ? 'bad' : 'warn')

const RANK: Record<PlatformStatus, number> = { healthy: 0, unknown: 0, attention: 1, down: 2 }

export function summarize(model: PlatformModel, order: (id: string) => number = () => 0): TelemetrySummary {
  const byId = new Map(model.entities.map((e) => [e.id, e]))
  const out = new Map<string, string[]>()
  for (const h of model.edges) out.set(h.from, [...(out.get(h.from) ?? []), h.to])
  const downstream = (id: string, seen = new Set<string>()): Set<string> => {
    for (const n of out.get(id) ?? []) if (!seen.has(n)) downstream(n, seen.add(n))
    return seen
  }

  const places: (Problem & { at: number })[] = []
  let clusters = 0
  let flowing = 0
  let waiting = 0
  for (const agent of model.entities.filter((e) => e.kind === 'agent')) {
    const local = byId.get(`local:${agent.agentId}`)
    // The part to open for a cluster is the worst of its two, the agent when they are as bad (it is the cause of its local operator's silence).
    const culprit = [agent, local].filter((e): e is PlatformEntity => !!e && isBad(e.status)).sort((a, b) => RANK[b.status] - RANK[a.status])[0]
    if (culprit) places.push({ id: culprit.id, alert: alertOf(culprit.status), at: order(culprit.id) })
    if (!local) continue
    clusters++
    const broken = [...downstream(local.id)].some((id) => byId.get(id)?.status === 'down')
    if (agent.status === 'healthy' && local.status === 'healthy' && !broken) flowing++
    else if (!isBad(agent.status) && !isBad(local.status) && (agent.status === 'unknown' || local.status === 'unknown')) waiting++
  }
  for (const e of model.entities) if ((e.kind === 'regional' || e.kind === 'central' || e.kind === 'fusion') && isBad(e.status)) places.push({ id: e.id, alert: alertOf(e.status), at: order(e.id) })
  places.sort((a, b) => Number(b.alert === 'bad') - Number(a.alert === 'bad') || a.at - b.at)
  return { clusters, flowing, waiting, places: places.map(({ id, alert }) => ({ id, alert })), fusionOff: byId.get('fusion')?.off === true }
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** The headline, in the app's plain words: how many of the clusters that are meant to send are sending. Never a bare zero. */
export function headline(s: TelemetrySummary): string {
  if (s.clusters === 0) return 'No cluster sends telemetry yet'
  if (s.flowing === s.clusters) return s.clusters === 1 ? 'Telemetry is flowing from 1 cluster' : `Telemetry is flowing from all ${s.clusters} clusters`
  if (s.flowing === 0) return s.waiting === s.clusters ? 'No data yet from any cluster' : 'Telemetry is not flowing from any cluster'
  return `Telemetry is flowing from ${s.flowing} of ${s.clusters} clusters`
}

/** What is left over once the problems are counted by the pill: clusters that have simply not sent yet, and FUSION being off. */
export function aside(s: TelemetrySummary): string | undefined {
  const parts = [s.waiting > 0 && s.flowing > 0 ? `${plural(s.waiting, 'cluster has', 'clusters have')} no data yet` : '', s.fusionOff ? 'FUSION is off' : ''].filter(Boolean)
  return parts.length ? parts.join(' · ') : undefined
}
