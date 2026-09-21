import type { Level } from '../advice'
import { movability } from '../movability'
import { observation, targetStatus } from '../provenance'
import type { Service } from '../types'
import { evaluate, isMovableKind, recommend, whatIf, type WhatIf } from './engine'
import type { Move, Policy } from './types'
import { clusterOfService, clusterStatus, freeCapacity, moveModel, rtt, siteOfCluster, storageKnown, type World } from './world'

/**
 * Deciders. Something that looks at the estate and proposes where services should run. The built-in ones run in
 * the browser; an external one is any HTTP service an administrator configured, reached through the server.
 * Whatever proposes a move, the same checks apply afterwards: a proposal is only kept when the hard constraints
 * allow it, and every kept proposal is scored by the same function, so deciders can be compared fairly.
 * Nothing here applies a move. A decider recommends; a person decides.
 */

export const DECISION_SCHEMA = 1

/** What a decider is told. Everything in it is already shown in the dashboard; nothing more leaves the server. */
export interface DecisionInput {
  schema: typeof DECISION_SCHEMA
  question: 'placement'
  generatedAt: string
  policy: Policy
  clusters: {
    id: string
    name: string
    tier: string
    site?: string
    country?: string
    residency?: string
    trustZone?: string
    freeCpu?: number
    freeMemGb?: number
    /** Added in schema 1, additively: live | disconnected | stale | revoked, absent for a cluster nobody observes. */
    state?: string
    /** Whether it may be proposed as a target. False for anything not live, or whose capacity is unknown. */
    eligible?: boolean
    /** Why it may not ("stale for 2 h", "agent revoked 3 h ago", "capacity unknown"). */
    stateReason?: string
  }[]
  /**
   * Clusters left out as targets, with why. Nothing in `candidates` is ever one of them. Added additively.
   * `kind` says whether it is out because it is not live (`not-live`) or because nothing can be certified there (`capacity-unknown`).
   */
  excluded?: { cluster: string; name: string; state: string; reason: string; kind: 'not-live' | 'capacity-unknown' }[]
  /** Best known round trip between clusters, in milliseconds, and how it was obtained. */
  links: { from: string; to: string; rttMs: number; basis: string }[]
  services: {
    id: string
    name: string
    namespace: string
    cluster: string
    kind: string
    replicas: number
    cpuRequestM?: number
    memRequestMi?: number
    volumesGb: number
    sensitivity?: string
    /** free | careful | pinned */
    mobility: string
    /** The state of the cluster it runs in, when an agent observes it. Added additively. */
    clusterState?: string
    /** The only clusters it may be proposed for: every hard constraint is certified to hold there (verdict "fits"). */
    candidates: string[]
    /** Clusters where it cannot be told whether it fits (something is unknown or too uncertain). Not candidates. Added additively. */
    undecided?: string[]
    /** Why it is pinned or needs care. */
    constraints: string[]
  }[]
  /** What talks to what. `to` is a service id, a device id or an external address. */
  flows: { from: string; to: string; toKind: 'service' | 'device' | 'external'; bytesPerSec?: number; connectionsPerMin?: number; observed: boolean }[]
}

export interface ProposedMove {
  serviceId: string
  to: string
  reason?: string
}

/** What a decider must answer with. Unknown fields are ignored. */
export interface DecisionOutput {
  schema?: number
  recommendations: ProposedMove[]
}

export function buildDecisionInput(w: World, P: Policy, now = new Date()): DecisionInput {
  const clusters = w.clusters.map((c) => {
    const site = siteOfCluster(w, c.id)
    const free = freeCapacity(w, c.id)
    const status = targetStatus(c, free.known)
    return {
      state: observation(c)?.kind,
      eligible: status.eligible,
      stateReason: status.eligible ? undefined : status.reason,
      id: c.id,
      name: c.name,
      tier: c.tier,
      site: site?.name,
      country: site?.country,
      residency: c.dataResidency ?? site?.dataResidency,
      trustZone: c.trustZone ?? site?.trustZone,
      // only when every node reported it: a sum over some nodes is not "what the cluster has free"
      freeCpu: free.exact ? Math.round(free.cpu * 10) / 10 : undefined,
      freeMemGb: free.exact ? Math.round(free.memGb * 10) / 10 : undefined,
    }
  })
  const links: DecisionInput['links'] = []
  for (const a of w.clusters)
    for (const b of w.clusters) {
      if (a.id >= b.id) continue
      const r = rtt(w, a.id, { kind: 'cluster', id: b.id })
      if (Number.isFinite(r.ms)) links.push({ from: a.id, to: b.id, rttMs: r.ms, basis: r.basis })
    }
  const services: DecisionInput['services'] = []
  for (const s of w.services) {
    const home = clusterOfService(w, s.id) ?? s.clusterId
    const svc = home === s.clusterId ? s : { ...s, clusterId: home }
    const mv = movability(svc, moveModel(w, storageKnown(w, home)))
    const movable = isMovableKind(s) && mv.verdict !== 'pinned'
    const verdicts = movable ? w.clusters.filter((c) => c.id !== home).map((c) => ({ id: c.id, verdict: evaluate(w, s, c.id, P).verdict })) : []
    const candidates = verdicts.filter((v) => v.verdict === 'fits').map((v) => v.id)
    const undecided = verdicts.filter((v) => v.verdict === 'cantTell').map((v) => v.id)
    services.push({
      id: s.id,
      name: s.name,
      namespace: s.namespace,
      cluster: home,
      clusterState: observation(w.byCluster.get(home))?.kind,
      kind: s.kind,
      replicas: s.replicas,
      cpuRequestM: s.cpuRequestM,
      memRequestMi: s.memRequestMi,
      volumesGb: (s.volumes ?? []).reduce((a, v) => a + v.sizeGb, 0),
      sensitivity: s.sensitivity,
      mobility: isMovableKind(s) ? mv.verdict : 'pinned',
      candidates,
      undecided,
      constraints: mv.reasons.filter((r) => r.severity !== 'info').map((r) => r.text),
    })
  }
  const flows = w.deps
    .filter((d) => d.fromKind === 'service' || d.toKind === 'service')
    .map((d) => ({
      from: d.from,
      to: d.to,
      toKind: d.toKind,
      bytesPerSec: d.stats?.bytesPerSec,
      connectionsPerMin: d.stats?.connectionsPerMin,
      observed: d.sources.includes('observed'),
    }))
  const excluded = clusters
    .filter((c) => c.eligible === false)
    .map((c) => ({ cluster: c.id, name: c.name, state: c.state ?? 'unknown', reason: c.stateReason ?? '', kind: clusterStatus(w, c.id).capacityUnknown ? ('capacity-unknown' as const) : ('not-live' as const) }))
  return { schema: DECISION_SCHEMA, question: 'placement', generatedAt: now.toISOString(), policy: P, clusters, excluded, links, services, flows }
}

export interface DeciderResult {
  deciderId: string
  name: string
  kind: 'builtin' | 'external'
  ok: boolean
  error?: string
  /** Proposals that passed the checks. */
  moves: (ProposedMove & { benefit: number; confidence: Level })[]
  /** Proposals that were refused, and why. */
  rejected: (ProposedMove & { why: string })[]
  /** What applying all kept proposals together would do. */
  outcome?: WhatIf
  tookMs: number
}

export interface Decider {
  id: string
  name: string
  description: string
  kind: 'builtin' | 'external'
  propose: (input: DecisionInput, w: World, P: Policy) => Promise<ProposedMove[]>
}

/** The rule-based baseline: the engine's own recommendations. */
export const baselineDecider: Decider = {
  id: 'baseline',
  name: 'Baseline (weighted cost)',
  description: 'Minimises round-trip time and cross-site traffic, with a cost for copying data and for filling a cluster. Explains every move.',
  kind: 'builtin',
  propose: async (_i, w, P) => recommend(w, P).recommendations.map((r) => ({ serviceId: r.serviceId, to: r.to, reason: r.reasons[0] })),
}

/** A deliberately simple rival: put each service in the cluster of the peer it exchanges the most data with. */
export const talkerDecider: Decider = {
  id: 'heaviest-talker',
  name: 'Follow the heaviest talker',
  description: 'Moves each service next to the peer it exchanges the most traffic with, if the constraints allow. Ignores latency and cost.',
  kind: 'builtin',
  propose: async (_i, w) => {
    const out: ProposedMove[] = []
    for (const s of w.services) {
      if (!isMovableKind(s)) continue
      const home = clusterOfService(w, s.id) ?? s.clusterId
      let best: { cluster: string; bps: number; name: string } | undefined
      for (const d of w.deps) {
        const peer = d.from === s.id && d.fromKind === 'service' ? { kind: d.toKind, id: d.to } : d.to === s.id && d.toKind === 'service' ? { kind: d.fromKind, id: d.from } : undefined
        if (!peer || peer.kind !== 'service') continue
        const c = clusterOfService(w, peer.id)
        const bps = d.stats?.bytesPerSec ?? 0
        if (c && (!best || bps > best.bps)) best = { cluster: c, bps, name: w.byService.get(peer.id)?.name ?? peer.id }
      }
      if (best && best.cluster !== home && best.bps > 0) out.push({ serviceId: s.id, to: best.cluster, reason: `Its heaviest peer, ${best.name}, runs there.` })
    }
    return out
  },
}

export const BUILTIN_DECIDERS: Decider[] = [baselineDecider, talkerDecider]

/** Keep only what a decider may propose: known services and clusters, movable services, and clusters where the constraints hold. */
export function vet(w: World, P: Policy, proposals: ProposedMove[]): { kept: DeciderResult['moves']; rejected: DeciderResult['rejected'] } {
  const kept: DeciderResult['moves'] = []
  const rejected: DeciderResult['rejected'] = []
  const seen = new Set<string>()
  for (const p of proposals) {
    const s = typeof p?.serviceId === 'string' ? w.byService.get(p.serviceId) : undefined
    const reason = typeof p?.reason === 'string' ? p.reason.slice(0, 400) : undefined
    const base = { serviceId: String(p?.serviceId ?? ''), to: String(p?.to ?? ''), reason }
    if (!s) {
      rejected.push({ ...base, why: 'There is no such service.' })
      continue
    }
    if (!w.byCluster.has(base.to)) {
      rejected.push({ ...base, why: 'There is no such cluster.' })
      continue
    }
    if (seen.has(s.id)) {
      rejected.push({ ...base, why: 'A second proposal for the same service.' })
      continue
    }
    seen.add(s.id)
    const home = clusterOfService(w, s.id) ?? s.clusterId
    if (base.to === home) {
      rejected.push({ ...base, why: 'It already runs there.' })
      continue
    }
    if (!isMovableKind(s)) {
      rejected.push({ ...base, why: `A ${s.kind} does not move.` })
      continue
    }
    const mv = movability(home === s.clusterId ? s : { ...s, clusterId: home }, moveModel(w, storageKnown(w, home)))
    if (mv.verdict === 'pinned') {
      rejected.push({ ...base, why: `It is pinned: ${mv.reasons.find((r) => r.severity === 'blocker')?.text ?? 'something ties it here'}` })
      continue
    }
    const before = evaluate(w, s, home, P)
    const after = evaluate(w, s, base.to, P)
    if (after.verdict === 'doesNotFit') {
      rejected.push({ ...base, why: `It cannot run there: ${after.blockers.join('; ')}.` })
      continue
    }
    if (after.verdict === 'cantTell') {
      // a proposal is kept only when it can be certified; "not sure" is not "fits"
      rejected.push({ ...base, why: `It cannot be told whether it fits there: ${after.unchecked.join('; ')}.` })
      continue
    }
    kept.push({ ...base, benefit: Math.round((before.cost - after.cost - after.migrationCost) * 10) / 10, confidence: after.confidence })
  }
  return { kept, rejected }
}

export async function runDecider(d: Decider, w: World, P: Policy): Promise<DeciderResult> {
  const t0 = Date.now()
  try {
    const input = buildDecisionInput(w, P)
    const proposals = await d.propose(input, w, P)
    const { kept, rejected } = vet(w, P, proposals)
    const moves: Move[] = kept.map((k) => ({ serviceId: k.serviceId, to: k.to }))
    return { deciderId: d.id, name: d.name, kind: d.kind, ok: true, moves: kept, rejected, outcome: whatIf(w, P, moves), tookMs: Date.now() - t0 }
  } catch (e) {
    return { deciderId: d.id, name: d.name, kind: d.kind, ok: false, error: e instanceof Error ? e.message : String(e), moves: [], rejected: [], tookMs: Date.now() - t0 }
  }
}

/** Read a decider's answer. Anything that is not a recommendation is dropped; the shape is never trusted. */
export function parseDecisionOutput(raw: unknown): ProposedMove[] {
  const list = (raw as { recommendations?: unknown } | null)?.recommendations
  if (!Array.isArray(list)) throw new Error('The decider did not answer with a "recommendations" list.')
  if (list.length > 5000) throw new Error('The decider proposed more than 5000 moves.')
  const out: ProposedMove[] = []
  for (const r of list) {
    const x = r as { serviceId?: unknown; to?: unknown; toCluster?: unknown; reason?: unknown }
    const to = typeof x?.to === 'string' ? x.to : typeof x?.toCluster === 'string' ? x.toCluster : ''
    if (typeof x?.serviceId === 'string' && to) out.push({ serviceId: x.serviceId, to, reason: typeof x.reason === 'string' ? x.reason : undefined })
  }
  return out
}

/** An external decider, reached through the server (which holds its address). */
export function externalDecider(name: string, call: (input: DecisionInput) => Promise<{ decider: string; result: unknown }>, id = 'external'): Decider {
  return {
    id,
    name,
    description: 'Configured by an administrator. It receives the estate described above and answers with recommendations; every answer is checked against the same constraints.',
    kind: 'external',
    propose: async (input) => parseDecisionOutput((await call(input)).result),
  }
}

/* ---------- comparison ---------- */

export interface ComparisonRow {
  serviceId: string
  serviceName: string
  from: string
  /** What each decider chose for this service; undefined when it left it alone. */
  picks: Map<string, { to: string; benefit: number } | undefined>
  agree: boolean
}

export interface Comparison {
  rows: ComparisonRow[]
  /** How many moves each decider proposed and the combined change in cost it would bring (negative is better). */
  totals: { deciderId: string; name: string; moves: number; costChange: number; crossSiteChange: number; ok: boolean; error?: string }[]
}

export function compare(w: World, results: DeciderResult[]): Comparison {
  const ids = new Set<string>()
  for (const r of results) for (const m of r.moves) ids.add(m.serviceId)
  const rows: ComparisonRow[] = [...ids].map((id) => {
    const s = w.byService.get(id) as Service
    const picks = new Map<string, { to: string; benefit: number } | undefined>()
    for (const r of results) {
      const m = r.moves.find((x) => x.serviceId === id)
      picks.set(r.deciderId, m ? { to: m.to, benefit: m.benefit } : undefined)
    }
    const chosen = [...picks.values()]
    const agree = chosen.every((p) => p?.to === chosen[0]?.to)
    return { serviceId: id, serviceName: s.name, from: clusterOfService(w, id) ?? s.clusterId, picks, agree }
  })
  rows.sort((a, b) => Number(a.agree) - Number(b.agree) || a.serviceName.localeCompare(b.serviceName))
  const totals = results.map((r) => ({
    deciderId: r.deciderId,
    name: r.name,
    moves: r.moves.length,
    costChange: r.outcome ? Math.round((r.outcome.after.cost - r.outcome.before.cost) * 10) / 10 : 0,
    crossSiteChange: r.outcome ? r.outcome.after.crossSiteBps - r.outcome.before.crossSiteBps : 0,
    ok: r.ok,
    error: r.error,
  }))
  return { rows, totals }
}
