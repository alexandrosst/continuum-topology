/**
 * Free capacity and what a workload asks for, as facts with a class and an age, and the fit of one against the other.
 * The rules live in advice.ts (the browser's copy of backend/internal/advice); this file turns the model's records
 * into the facts those rules judge, and says what would make an unknown known.
 */
import { assess, CPU, fit, GIB, MEMORY, newPool, adjustPool, type Advice, type CapNode, type Check, type DimResult, type Dimension, type Fact, type FactRow, type Fix, type Need, type Pool } from './advice'
import type { EffectiveModel } from './provenance'
import type { Agent, MachineNode, Service } from './types'

/** The staleness window the server uses by default (4 heartbeats of 30 s); the model states the real one. */
export const DEFAULT_WINDOW_MS = 120_000

/** When the facts are judged, and how old a fact may be before it counts as one class worse. */
export interface CapCtx {
  now: number
  windowMs: number
  /** When the server last confirmed a node's figures (ms), from the effective model. Absent: the figures have no age. */
  observedAt?: (nodeId: string) => number | undefined
}

export const defaultCtx = (now = Date.now()): CapCtx => ({ now, windowMs: DEFAULT_WINDOW_MS })

/**
 * The context for judging facts against the server's effective model: its own staleness window, and when it last
 * confirmed each node's figures. Without a model, the default window and no ages (a figure with no age is not aged).
 */
export function capCtxOf(model: EffectiveModel | undefined, now: number): CapCtx {
  if (!model) return defaultCtx(now)
  const observed = new Map<string, number>()
  for (const e of model.entities ?? []) {
    if (e.kind !== 'node') continue
    const at = Date.parse(e.attributes.cpuAllocatable?.observedAt ?? e.attributes.memoryAllocatable?.observedAt ?? e.lastObservedAt ?? '')
    if (Number.isFinite(at)) observed.set(e.id, at)
  }
  return { now, windowMs: model.observation.staleAfterSeconds * 1000, observedAt: (id) => observed.get(id) }
}

const unknownFact: Fact = { value: null, class: 'unknown' }

/**
 * What one node offers and what is already asked of it. A figure that is missing is unknown, not zero: a node whose
 * pods are not read has no "requested" (the agent's access tier is below 2), which is not the same as nothing being
 * requested. A record a person typed is taken at their word (reported) and has no age.
 */
export function capNodes(nodes: MachineNode[], ctx: CapCtx): { cpu: CapNode[]; memory: CapNode[] } {
  const cpu: CapNode[] = []
  const memory: CapNode[] = []
  for (const n of nodes) {
    const observedAt = n.source === 'discovered' ? ctx.observedAt?.(n.id) : undefined
    const fact = (v: number | undefined, scale = 1): Fact => (v === undefined || v === null ? unknownFact : { value: v * scale, class: 'reported', observedAt })
    const why = n.source === 'discovered' ? 'pods are not read at this access tier' : 'nothing says what is already requested there'
    cpu.push({ id: n.id, name: n.name, allocatable: fact(n.allocatable?.cpu), requested: fact(n.requested?.cpu), requestedWhy: why })
    memory.push({ id: n.id, name: n.name, allocatable: fact(n.allocatable?.memoryGb, GIB), requested: fact(n.requested?.memoryGb, GIB), requestedWhy: why })
  }
  return { cpu, memory }
}

/** What all replicas of a workload ask for, in cores and bytes. No request set is unknown: it is not "asks for nothing". */
export function needOf(s: Pick<Service, 'replicas' | 'cpuRequestM' | 'memRequestMi'>): { cpu: Need; memory: Need } {
  const r = Math.max(1, s.replicas)
  const cpu: Need = s.cpuRequestM && s.cpuRequestM > 0 ? { value: (s.cpuRequestM * r) / 1000, class: 'reported' } : { value: null, class: 'unknown' }
  const memory: Need = s.memRequestMi && s.memRequestMi > 0 ? { value: s.memRequestMi * r * 1024 * 1024, class: 'reported' } : { value: null, class: 'unknown' }
  return { cpu, memory }
}

export interface Pools {
  cpu: Pool
  memory: Pool
}

export function poolsOf(nodes: MachineNode[], ctx: CapCtx): Pools {
  const c = capNodes(nodes, ctx)
  return { cpu: newPool(c.cpu, ctx.now, ctx.windowMs), memory: newPool(c.memory, ctx.now, ctx.windowMs) }
}

/** The pools after workloads moved in and out (a moved-in workload with no request removes the floor). */
export function adjustPools(p: Pools, moves: { s: Pick<Service, 'replicas' | 'cpuRequestM' | 'memRequestMi'>; dir: 'in' | 'out' }[]): Pools {
  let { cpu, memory } = p
  for (const m of moves) {
    const n = needOf(m.s)
    const sign = m.dir === 'in' ? -1 : 1
    if (m.dir === 'in') {
      cpu = adjustPool(cpu, n.cpu.value === null || n.cpu.value === undefined ? null : sign * n.cpu.value)
      memory = adjustPool(memory, n.memory.value === null || n.memory.value === undefined ? null : sign * n.memory.value)
    } else {
      // what leaves was counted in "requested" only if it had a request; without one there is nothing to give back
      if (n.cpu.value) cpu = adjustPool(cpu, n.cpu.value)
      if (n.memory.value) memory = adjustPool(memory, n.memory.value)
    }
  }
  return { cpu, memory }
}

/** Where a place is, for the sentences and for what would fix a missing figure. */
export interface Place {
  clusterId: string
  clusterName: string
  /** A person typed this cluster: nothing observes it, so what is missing is theirs to declare. */
  declared: boolean
  agent?: Agent
  nodeCount: number
}

/** What a person can do about a capacity figure that is not known. */
export function fixForPool(pool: Pool, p: Place, dim: Dimension): Fix | undefined {
  if (pool.nominalKnown || (pool.knownNodes > 0 && Number.isFinite(pool.hi))) return undefined
  const tier = p.agent?.accessTier
  if (p.nodeCount === 0) {
    if (p.declared) return { action: 'declare-capacity', text: `Say what ${p.clusterName}'s nodes have (allocatable CPU and memory) and what is already requested, or connect an agent that reports them`, link: '/nodes' }
    if (tier !== undefined && tier < 1) return { action: 'raise-agent-tier', text: `Raise ${p.agent!.name}'s access tier to 1 or higher so it reads the cluster's nodes and what they have free`, link: '/agents' }
    return { action: 'connect-agent', text: 'No node is reported for this cluster: check that its agent is connected and approved', link: '/agents' }
  }
  if (!Number.isFinite(pool.hi)) return { action: 'check-agent', text: 'A node reports no allocatable resources: check the agent’s permissions and version', link: '/agents' }
  if (p.declared || !p.agent) return { action: 'declare-capacity', text: `Say what is already requested on ${p.clusterName}'s nodes, or connect an agent that reports it`, link: '/nodes' }
  return { action: 'raise-agent-tier', text: `Raise ${p.agent.name}'s access tier to 2 so it reads pods and can say what is already requested${dim.name === 'cpu' ? '' : ''}`, link: '/agents' }
}

/** cpu and memory of a workload at a place: the results, and the facts used, each with class, source and age. */
export function judgeCapacity(
  s: Pick<Service, 'id' | 'name' | 'replicas' | 'cpuRequestM' | 'memRequestMi' | 'source'>,
  pools: Pools,
  place: Place,
  ctx: CapCtx,
  state?: string,
): { dims: DimResult[]; facts: FactRow[] } {
  const need = needOf(s)
  const where = `cluster ${place.clusterName}`
  const dims: DimResult[] = []
  const facts: FactRow[] = []
  for (const [dim, n, pool] of [[CPU, need.cpu, pools.cpu], [MEMORY, need.memory, pools.memory]] as [Dimension, Need, Pool][]) {
    const r = fit(dim, n, pool, where)
    if (r.verdict === 'cantTell') {
      r.fix = n.value === null || n.value === undefined ? { action: 'set-requests', text: `Set a ${dim.name} request on ${s.name} so what it needs is known` } : fixForPool(pool, place, dim)
    }
    dims.push(r)
    facts.push(freeFactRow(dim, pool, place, state, ctx))
    facts.push({
      attribute: `${dim.name}Request`,
      entity: s.id,
      entityName: s.name,
      value: n.value ?? null,
      unit: dim.unit,
      source: s.source === 'discovered' ? 'agent' : 'declared',
      confidence: n.value === null || n.value === undefined ? 'unknown' : n.class,
      evidence: n.value === null || n.value === undefined ? `no ${dim.name} request is set, so what the workload needs is not known` : `${Math.max(1, s.replicas)} replica${s.replicas > 1 ? 's' : ''} together`,
    })
  }
  return { dims, facts }
}

export function freeFactRow(dim: Dimension, pool: Pool, place: Pick<Place, 'clusterId' | 'clusterName' | 'nodeCount'>, state: string | undefined, _ctx: CapCtx): FactRow {
  return {
    attribute: `${dim.name}Free`,
    entity: place.clusterId,
    entityName: place.clusterName,
    value: pool.nominalKnown ? pool.knownFree : null,
    unit: dim.unit,
    source: 'inferred',
    confidence: pool.class,
    observedAt: pool.oldest !== undefined ? new Date(pool.oldest).toISOString() : undefined,
    state,
    evidence: pool.notes.length > 0 ? pool.notes.join('; ') : `allocatable minus requested over ${pool.nodes} node${pool.nodes === 1 ? '' : 's'}`,
    low: pool.lo,
    high: Number.isFinite(pool.hi) ? pool.hi : null,
    aged: pool.aged || undefined,
  }
}

/** Combine capacity with constraint checks into one answer. */
export const adviceOf = (dims: DimResult[], checks: Check[]): Advice => assess(dims, checks)
