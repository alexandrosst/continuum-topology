import { levelOf, sensitivityText, weakerLevel, worse, type Advice, type Change, type Class, type FactRow, type Fix, type Level } from '../advice'
import { judgeTarget, movability, moveTargets, type MoveReason, type MoveTarget, type MoveVerdict } from '../movability'
import { bytesPerSec as bytesPerSecShort } from '../observed'
import { observation } from '../provenance'
import type { Service } from '../types'
import type { EdgeEvidence, EvidenceInput, Evaluation, Move, Plan, Policy, Recommendation, RttBasis, Skipped, WouldChange } from './types'
import { clusterOfService, freeCapacity, locName, locOf, moveModel, rtt, serviceNeed, siteIdOf, siteOfCluster, storageKnown, totalCapacity, withMoves, type World } from './world'

/** Traffic at or above this counts as "constantly used"; below it, an edge counts for proportionally less. */
const FULL_ACTIVITY_BPS = 50 * 1024
const MIN_ACTIVITY = 0.05
/** A dependency nobody measured still means the two talk. */
const DECLARED_ACTIVITY = 0.3
const MB = 1024 * 1024

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n))
const round1 = (n: number) => Math.round(n * 10) / 10

/** Services the engine may move. Jobs run to completion and DaemonSets follow their nodes. */
export const isMovableKind = (s: Service) => s.kind === 'Deployment' || s.kind === 'StatefulSet'

interface Peer {
  dependencyId: string
  kind: 'service' | 'device' | 'external'
  id: string
  bps?: number
  activity: number
  trafficKnown: boolean
}

function peersOf(w: World, s: Service): Peer[] {
  const out: Peer[] = []
  for (const d of w.deps) {
    let kind: Peer['kind'] | undefined
    let id = ''
    if (d.from === s.id && d.fromKind === 'service') [kind, id] = [d.toKind, d.to]
    else if (d.to === s.id && d.toKind === 'service') [kind, id] = [d.fromKind, d.from]
    if (!kind || id === s.id) continue
    const bps = d.stats?.bytesPerSec
    const cpm = d.stats?.connectionsPerMin
    let activity = DECLARED_ACTIVITY
    if (bps !== undefined) activity = clamp(bps / FULL_ACTIVITY_BPS, MIN_ACTIVITY, 1)
    else if (cpm !== undefined) activity = clamp(cpm / 30, MIN_ACTIVITY, 1)
    if (d.stale) activity = MIN_ACTIVITY
    out.push({ dependencyId: d.id, kind, id, bps, activity, trafficKnown: bps !== undefined })
  }
  return out
}

function peerName(w: World, p: Peer): string {
  if (p.kind === 'service') return w.byService.get(p.id)?.name ?? p.id
  if (p.kind === 'device') return w.byDevice.get(p.id)?.name ?? p.id
  return w.byExternal.get(p.id)?.name ?? w.byExternal.get(p.id)?.host ?? p.id
}

/** The cost of one dependency with the service running in `cluster`. */
function edgeAt(w: World, P: Policy, cluster: string, p: Peer): EdgeEvidence {
  const loc = locOf(w, p.kind, p.id)
  const r = rtt(w, cluster, loc)
  const ms = Number.isFinite(r.ms) ? r.ms : P.fallbackMs
  const mySite = siteOfCluster(w, cluster)?.id
  const peerSite = siteIdOf(w, loc)
  let cross = false
  if (loc.kind === 'cluster') cross = loc.id !== cluster && !(mySite && peerSite && mySite === peerSite)
  else if (loc.kind === 'site') cross = !(mySite && mySite === loc.id)
  const latency = P.latency * p.activity * ms
  const traffic = cross && p.bps ? P.traffic * (p.bps / MB) : 0
  return {
    dependencyId: p.dependencyId,
    peerName: peerName(w, p),
    peerKind: p.kind,
    peerWhere: locName(w, loc),
    bytesPerSec: p.bps,
    activity: p.activity,
    trafficKnown: p.trafficKnown,
    rtt: { ms: Number.isFinite(r.ms) ? r.ms : NaN, basis: r.basis },
    crossSite: cross,
    cost: latency + traffic,
  }
}

/** How sure each way of knowing a round trip is. A same-cluster hop and a declared link are taken at their word. */
const RTT_CLASS: Record<RttBasis, Class> = { 'same-cluster': 'reported', measured: 'measured', declared: 'reported', 'same-site': 'inferred', estimated: 'guess', unknown: 'unknown' }

/** The class of one connection's figures: the round trip and how busy the link is, the weaker of the two. */
const edgeClass = (e: EdgeEvidence): Class => worse(RTT_CLASS[e.rtt.basis], e.trafficKnown ? 'measured' : 'guess')

/** An edge carries the answer when it accounts for at least this share of what is being judged. */
const DECISIVE_SHARE = 0.1

function edgeInputs(e: EdgeEvidence): EvidenceInput[] {
  const rttLabel = `round trip to ${e.peerName} (${basisText(e.rtt.basis)})`
  const busyLabel = e.trafficKnown ? `how busy the link to ${e.peerName} is (measured)` : `how busy the link to ${e.peerName} is (not measured: assumed)`
  return [
    { label: rttLabel, class: RTT_CLASS[e.rtt.basis] },
    { label: busyLabel, class: e.trafficKnown ? 'measured' : 'guess' },
  ]
}

/** The weakest class among the connections that carry the cost of running somewhere, with the inputs that decide it. */
function edgesEvidence(edges: EdgeEvidence[]): { class: Class; inputs: EvidenceInput[] } {
  const total = edges.reduce((a, e) => a + e.cost, 0)
  const inputs: EvidenceInput[] = []
  let cls: Class = 'measured'
  if (total > 0) {
    for (const e of edges) {
      if (e.cost < DECISIVE_SHARE * total) continue
      cls = worse(cls, edgeClass(e))
      inputs.push(...edgeInputs(e))
    }
  }
  return { class: cls, inputs }
}

/** What a fit rests on, as inputs: the room and the need in each dimension that decides the verdict. */
function fitInputs(ev: { verdict: Evaluation['verdict']; advice?: MoveAdvice }): EvidenceInput[] {
  const a = ev.advice
  if (!a) return []
  const out: EvidenceInput[] = []
  for (const d of a.dims) {
    if (d.verdict !== a.verdict) continue
    out.push({ label: `free ${d.dimension.label} (${d.pool.class}${d.pool.aged ? ', not recently confirmed' : ''})`, class: d.class })
  }
  for (const c of a.checks) if (c.verdict === a.verdict && c.verdict !== 'fits') out.push({ label: c.reason, class: c.class })
  return out
}

type MoveAdvice = Advice

function levelFor(cls: Class, verdict: Evaluation['verdict']): Level {
  const l = levelOf(cls)
  return verdict === 'cantTell' ? weakerLevel(l, 'low') : l
}

/** The service as placement should judge it: where it runs in this world. */
const placed = (w: World, s: Service): Service => {
  const c = clusterOfService(w, s.id)
  return c && c !== s.clusterId ? { ...s, clusterId: c } : s
}

function targetsFor(w: World, s: Service): Map<string, MoveTarget> {
  const svc = placed(w, s)
  return new Map(moveTargets(svc, moveModel(w, storageKnown(w, svc.clusterId))).map((t) => [t.cluster.id, t]))
}

/** How full the cluster's CPU would be, counting what this world moved in and out. */
function utilization(w: World, clusterId: string, extraCpu: number): number | undefined {
  const total = totalCapacity(w, clusterId)
  if (!total || total.cpu <= 0) return undefined
  const free = freeCapacity(w, clusterId)
  return clamp((total.cpu - free.cpu + extraCpu) / total.cpu, 0, 2)
}

/** The workloads this world moved into and out of a cluster, for what that does to the room there. */
function movesAt(w: World, clusterId: string): { s: Service; dir: 'in' | 'out' }[] {
  const out: { s: Service; dir: 'in' | 'out' }[] = []
  for (const [id, mv] of w.moved) {
    const m = w.byService.get(id)
    if (!m) continue
    if (mv.to === clusterId) out.push({ s: m, dir: 'in' })
    if (mv.from === clusterId) out.push({ s: m, dir: 'out' })
  }
  return out
}

/** What running `s` in `clusterId` costs, and whether the hard constraints allow it: fits, does not fit, or cannot be told. */
export function evaluate(w: World, s: Service, clusterId: string, P: Policy, targets?: Map<string, MoveTarget>): Evaluation {
  const current = clusterOfService(w, s.id) ?? s.clusterId
  const edges = peersOf(w, s).map((p) => edgeAt(w, P, clusterId, p))
  const latencyCost = edges.reduce((a, e) => a + P.latency * e.activity * (Number.isFinite(e.rtt.ms) ? e.rtt.ms : P.fallbackMs), 0)
  const trafficCost = edges.reduce((a, e) => a + e.cost, 0) - latencyCost
  const need = serviceNeed(s)
  const util = utilization(w, clusterId, current === clusterId ? 0 : need.cpu)
  const headroomCost = util === undefined ? 0 : P.headroom * clamp((util - 0.8) / 0.2, 0, 1)
  const act = edges.reduce((a, e) => a + e.activity, 0)
  const weightedRttMs = act > 0 ? edges.reduce((a, e) => a + e.activity * (Number.isFinite(e.rtt.ms) ? e.rtt.ms : P.fallbackMs), 0) / act : 0

  let verdict: Evaluation['verdict'] = 'fits'
  const blockers: string[] = []
  const unchecked: string[] = []
  let fixes: Fix[] = []
  let facts: Evaluation['facts'] = []
  let wouldChange: Change[] = []
  let advice: MoveAdvice | undefined
  let fitClass: Class = 'reported'
  if (clusterId !== current) {
    const t = (targets ?? targetsFor(w, s)).get(clusterId)
    if (!t) {
      verdict = 'doesNotFit'
      blockers.push('this cluster is not known')
    } else {
      // capacity is judged here, not in the target: it depends on what this world has already moved in and out
      const j = judgeTarget(t, s, w.cap, movesAt(w, clusterId))
      advice = j.advice
      verdict = advice.verdict
      facts = j.facts.concat(t.facts.filter((f) => f.attribute === 'dataResidency' || f.attribute === 'trustZone'))
      blockers.push(...advice.checks.filter((c) => c.verdict === 'doesNotFit').map((c) => c.reason), ...advice.dims.filter((d) => d.verdict === 'doesNotFit').map((d) => d.reason))
      unchecked.push(...advice.checks.filter((c) => c.verdict === 'cantTell').map((c) => c.reason), ...advice.dims.filter((d) => d.verdict === 'cantTell').map((d) => d.reason))
      fixes = advice.fixes
      wouldChange = advice.changes
      fitClass = advice.class
    }
  }
  const ee = edgesEvidence(edges)
  const confidenceClass = worse(ee.class, fitClass)
  const gb = (s.volumes ?? []).reduce((a, v) => a + v.sizeGb, 0)
  return {
    serviceId: s.id,
    clusterId,
    cost: round1(latencyCost + trafficCost + headroomCost),
    latencyCost: round1(latencyCost),
    trafficCost: round1(trafficCost),
    headroomCost: round1(headroomCost),
    migrationCost: clusterId === current ? 0 : round1(P.migration * gb),
    weightedRttMs: round1(weightedRttMs),
    crossSiteBps: edges.filter((e) => e.crossSite).reduce((a, e) => a + (e.bytesPerSec ?? 0), 0),
    edges,
    unknownPeers: edges.filter((e) => e.rtt.basis === 'unknown').length,
    confidence: levelFor(confidenceClass, verdict),
    confidenceClass,
    fitClass,
    verdict,
    fits: verdict === 'fits',
    blockers,
    unchecked,
    fixes,
    facts,
    wouldChange,
    advice,
    inputs: [...ee.inputs, ...fitInputs({ verdict, advice })],
    utilAfter: util,
  }
}

const BASIS_TEXT: Record<RttBasis, string> = {
  'same-cluster': 'same cluster',
  measured: 'measured',
  declared: 'declared',
  'same-site': 'same site',
  estimated: 'estimated from distance',
  unknown: 'unknown',
}
export const basisText = (b: RttBasis) => BASIS_TEXT[b]

const ms = (n: number) => (Number.isFinite(n) ? (n < 10 ? n.toFixed(1) : Math.round(n).toString()) : '?')

function reasonsFor(cur: Evaluation, tgt: Evaluation, toName: string): string[] {
  const out: string[] = []
  const drop = cur.crossSiteBps - tgt.crossSiteBps
  if (drop > 1024) {
    const gone = tgt.edges.filter((e) => !e.crossSite).map((e) => e.peerName)
    const was = cur.edges.filter((e) => e.crossSite && e.bytesPerSec).map((e) => e.peerName)
    const names = [...new Set(was.filter((n) => gone.includes(n)))].slice(0, 3)
    out.push(`${bytesPerSecShort(drop)} of traffic that crosses between sites now stays local${names.length ? ` (with ${names.join(', ')})` : ''}.`)
  } else if (drop < -1024) {
    out.push(`It adds ${bytesPerSecShort(-drop)} of traffic between sites.`)
  }
  if (cur.weightedRttMs - tgt.weightedRttMs >= 1) out.push(`The round trip to what it talks to, weighted by how busy each link is, falls from ${ms(cur.weightedRttMs)} ms to ${ms(tgt.weightedRttMs)} ms at ${toName}.`)
  const curBy = new Map(cur.edges.map((e) => [e.dependencyId, e]))
  const best = tgt.edges
    .map((e) => ({ e, was: curBy.get(e.dependencyId) }))
    .filter((x) => x.was && x.was.cost - x.e.cost > 0.5)
    .sort((a, b) => b.was!.cost - b.e.cost - (a.was!.cost - a.e.cost))
    .slice(0, 3)
  for (const { e, was } of best) {
    out.push(`${e.peerName}: round trip ${ms(was!.rtt.ms)} ms (${basisText(was!.rtt.basis)}) → ${ms(e.rtt.ms)} ms (${basisText(e.rtt.basis)}).`)
  }
  return out
}

/** More than this many moves in one round is a re-architecture, not a recommendation. */
const MAX_MOVES = 40

interface Candidate {
  service: Service
  verdict: MoveVerdict
  reasons: MoveReason[]
  /** Hard constraints do not depend on where other services run, so they are worked out once. */
  targets: Map<string, MoveTarget>
}

/**
 * What a recommendation rests on. The connections that carry the gain decide the ranking (an edge counts when it
 * accounts for at least a tenth of the total change, judged on whichever side of the move it costs more, because
 * that is where an error hurts), and so does the room at the target. The weakest of them is the confidence; nothing
 * is averaged, so one guessed input among several good ones shows.
 */
function evidenceOfMove(cur: Evaluation, tgt: Evaluation): { class: Class; inputs: EvidenceInput[]; facts: FactRow[] } {
  const was = new Map(cur.edges.map((e) => [e.dependencyId, e]))
  const deltas = tgt.edges.map((e) => ({ e, d: Math.abs((was.get(e.dependencyId)?.cost ?? 0) - e.cost), side: (was.get(e.dependencyId)?.cost ?? 0) > e.cost ? was.get(e.dependencyId)! : e }))
  for (const e of cur.edges) if (!tgt.edges.some((x) => x.dependencyId === e.dependencyId)) deltas.push({ e, d: e.cost, side: e })
  const sum = deltas.reduce((a, x) => a + x.d, 0)
  const inputs: EvidenceInput[] = []
  const facts: FactRow[] = []
  let cls: Class = 'measured'
  if (sum > 0) {
    for (const x of deltas) {
      if (x.d < DECISIVE_SHARE * sum) continue
      cls = worse(cls, edgeClass(x.side))
      inputs.push(...edgeInputs(x.side))
      facts.push(...edgeFacts(x.side))
    }
  }
  return { class: cls, inputs, facts }
}

const RTT_SOURCE: Record<RttBasis, string> = { 'same-cluster': 'inferred', measured: 'measured', declared: 'declared', 'same-site': 'inferred', estimated: 'inferred', unknown: 'inferred' }

/** A connection as facts: the round trip and how busy the link is, each with how it is known. */
function edgeFacts(e: EdgeEvidence): FactRow[] {
  return [
    {
      attribute: 'roundTripMs',
      entity: e.dependencyId,
      entityName: `${e.peerName} (${e.peerWhere})`,
      value: Number.isFinite(e.rtt.ms) ? e.rtt.ms : null,
      unit: 'ms',
      source: RTT_SOURCE[e.rtt.basis],
      confidence: RTT_CLASS[e.rtt.basis],
      evidence: e.rtt.basis === 'unknown' ? 'no measurement, declared link or distance to go on; the cost assumes the fallback round trip' : basisText(e.rtt.basis),
    },
    {
      attribute: 'bytesPerSec',
      entity: e.dependencyId,
      entityName: e.peerName,
      value: e.bytesPerSec ?? null,
      unit: 'bytes/s',
      source: e.trafficKnown ? 'measured' : 'declared',
      confidence: e.trafficKnown ? 'measured' : 'guess',
      evidence: e.trafficKnown ? undefined : 'the dependency is declared and no traffic was counted, so how busy it is is assumed',
    },
  ]
}

/** The point at which a move stops paying for itself, on the connection that carries most of its gain. */
function moveWouldChange(w: World, P: Policy, cur: Evaluation, tgt: Evaluation, net: number, toName: string): WouldChange[] {
  const out: WouldChange[] = []
  for (const c of tgt.wouldChange) {
    out.push({ text: c.text, effect: 'verdict', attribute: c.attribute, unit: c.unit, direction: c.direction, threshold: c.threshold, current: c.current })
  }
  const required = Math.max(P.minAbsolute, P.minBenefit * cur.cost)
  const slack = net - required
  const was = new Map(cur.edges.map((e) => [e.dependencyId, e]))
  const top = [...tgt.edges]
    .filter((e) => Number.isFinite(e.rtt.ms) && e.activity > 0 && (was.get(e.dependencyId)?.cost ?? 0) - e.cost > 0)
    .sort((a, b) => (was.get(b.dependencyId)!.cost - b.cost) - (was.get(a.dependencyId)!.cost - a.cost))[0]
  if (top && P.latency > 0 && slack > 0) {
    const ms = top.rtt.ms + slack / (P.latency * top.activity)
    out.push({
      text: `if the round trip between ${toName} and ${top.peerName} is more than ${fmtMs(ms)} ms (it is ${fmtMs(top.rtt.ms)} ms, ${basisText(top.rtt.basis)}) the move no longer pays for itself`,
      effect: 'recommendation',
      attribute: `roundTripMs:${top.dependencyId}`,
      unit: 'ms',
      direction: 'atLeast',
      threshold: ms,
      current: top.rtt.ms,
    })
  }
  void w
  return out
}

const fmtMs = (n: number) => (n < 10 ? n.toFixed(1) : Math.round(n).toString())

/** The best move for one candidate in the world as it is now, if it clears the thresholds. */
function assess(w: World, P: Policy, c: Candidate): Recommendation | undefined {
  const s = c.service
  const home = clusterOfService(w, s.id) ?? s.clusterId
  const cur = evaluate(w, s, home, P, c.targets)
  // A place that certainly fits is preferred to one where nobody can tell; that one is only recommended (hedged) when nothing
  // certain is worth moving to. Either way a place that certainly does not fit is never offered.
  const options = w.clusters.filter((k) => k.id !== home).map((k) => evaluate(w, s, k.id, P, c.targets)).filter((e) => e.verdict !== 'doesNotFit')
  const byCost = (a: Evaluation, b: Evaluation) => a.cost + a.migrationCost - (b.cost + b.migrationCost) || a.clusterId.localeCompare(b.clusterId)
  options.sort(byCost)
  const worthIt = (e: Evaluation) => round1(cur.cost - e.cost - e.migrationCost) >= Math.max(P.minAbsolute, P.minBenefit * cur.cost)
  const best = options.find((e) => e.verdict === 'fits' && worthIt(e)) ?? options.find((e) => e.verdict === 'cantTell' && worthIt(e))
  if (!best) return undefined
  const benefit = round1(cur.cost - best.cost)
  const net = round1(benefit - best.migrationCost)
  const toName = w.byCluster.get(best.clusterId)?.name ?? best.clusterId
  const caveats = c.reasons.filter((r) => r.severity !== 'info').map((r) => r.text)
  const gb = (s.volumes ?? []).reduce((a, v) => a + v.sizeGb, 0)
  if (best.migrationCost > 0) caveats.unshift(`Persistent data (${gb} GB) has to be copied first.`)
  if (best.unknownPeers > 0) caveats.push(`${best.unknownPeers} of what it talks to could not be placed, so part of this is an assumption (${P.fallbackMs} ms).`)
  for (const u of best.unchecked) caveats.push(`Not checked at ${toName}: ${u}.`)
  if (cur.edges.every((e) => !e.trafficKnown)) caveats.push('No traffic was measured for it; the dependencies are declared, so how busy they are is a guess.')
  const ev = evidenceOfMove(cur, best)
  // what decides it is the connections that carry the gain and the room at the target; the weaker of the two
  const confidenceClass = worse(ev.class, best.fitClass)
  return {
    serviceId: s.id,
    serviceName: s.name,
    from: home,
    to: best.clusterId,
    current: cur,
    target: best,
    benefit,
    net,
    verdict: c.verdict,
    fit: best.verdict,
    confidence: levelFor(confidenceClass, best.verdict),
    confidenceClass,
    inputs: [...ev.inputs, ...fitInputs({ verdict: best.verdict, advice: best.advice })],
    facts: [...ev.facts, ...best.facts],
    wouldChange: moveWouldChange(w, P, cur, best, net, toName),
    fixes: best.fixes,
    reasons: reasonsFor(cur, best, toName),
    caveats,
    alternatives: options.filter((o) => o.clusterId !== best.clusterId).slice(0, 3),
  }
}

/**
 * The moves worth making, in the order to make them. It picks the single best move, pretends it happened, and looks
 * again: two services that would each be better off in the other's cluster must not both be told to move (they would
 * swap places and gain nothing), and a cluster that has just taken one service has less room for the next. So every
 * recommendation's numbers assume the ones above it. Nothing is applied anywhere.
 */
export function recommend(w: World, P: Policy): Plan {
  const skipped: Skipped[] = []
  const candidates: Candidate[] = []
  for (const s of w.services) {
    const home = clusterOfService(w, s.id) ?? s.clusterId
    const svc = placed(w, s)
    if (!isMovableKind(svc)) {
      skipped.push({ serviceId: s.id, serviceName: s.name, why: s.kind === 'DaemonSet' ? 'A DaemonSet follows its nodes.' : 'A job runs to completion; run it again elsewhere.' })
      continue
    }
    // What runs in a cluster that is not live is not known any more, so nothing is recommended for it.
    const homeState = observation(w.byCluster.get(home))
    if (homeState && !homeState.actionable) {
      skipped.push({ serviceId: s.id, serviceName: s.name, why: `Its cluster is ${homeState.reason ?? homeState.label}: what runs there now is not known, so nothing is recommended.` })
      continue
    }
    const mv = movability(svc, moveModel(w, storageKnown(w, home)))
    if (mv.verdict === 'pinned') {
      skipped.push({ serviceId: s.id, serviceName: s.name, why: mv.reasons.find((r) => r.severity === 'blocker')?.text ?? 'Something pins it.' })
      continue
    }
    if (peersOf(w, s).length === 0) continue // nothing is known about what it talks to: no evidence either way
    candidates.push({ service: s, verdict: mv.verdict, reasons: mv.reasons, targets: targetsFor(w, s) })
  }

  const recommendations: Recommendation[] = []
  const remaining = new Map(candidates.map((c) => [c.service.id, c]))
  let cur = w
  while (remaining.size > 0 && recommendations.length < MAX_MOVES) {
    let best: Recommendation | undefined
    for (const c of remaining.values()) {
      const r = assess(cur, P, c)
      if (r && (!best || r.net > best.net || (r.net === best.net && r.serviceName.localeCompare(best.serviceName) < 0))) best = r
    }
    if (!best) break
    recommendations.push(best)
    remaining.delete(best.serviceId)
    cur = withMoves(cur, [{ serviceId: best.serviceId, to: best.to }])
  }
  return { recommendations, stay: candidates.length - recommendations.length, skipped }
}

/* ---------- what-if ---------- */

export interface Totals {
  cost: number
  latencyCost: number
  trafficCost: number
  /** Traffic between sites, bytes per second. */
  crossSiteBps: number
  edges: number
}

/** The network cost of the whole world: every dependency between a placed service and what it talks to, once. */
export function totals(w: World, P: Policy): Totals {
  const seen = new Set<string>()
  const t: Totals = { cost: 0, latencyCost: 0, trafficCost: 0, crossSiteBps: 0, edges: 0 }
  for (const d of w.deps) {
    if (seen.has(d.id)) continue
    seen.add(d.id)
    const anchorIsFrom = d.fromKind === 'service' && w.byService.has(d.from)
    const anchorIsTo = d.toKind === 'service' && w.byService.has(d.to)
    if (!anchorIsFrom && !anchorIsTo) continue
    const anchor = anchorIsFrom ? d.from : d.to
    const s = w.byService.get(anchor)!
    const p = peersOf(w, s).find((x) => x.dependencyId === d.id)
    if (!p) continue
    const e = edgeAt(w, P, clusterOfService(w, anchor) ?? s.clusterId, p)
    const lat = P.latency * e.activity * (Number.isFinite(e.rtt.ms) ? e.rtt.ms : P.fallbackMs)
    t.latencyCost += lat
    t.trafficCost += e.cost - lat
    t.cost += e.cost
    if (e.crossSite) t.crossSiteBps += e.bytesPerSec ?? 0
    t.edges++
  }
  t.cost = round1(t.cost)
  t.latencyCost = round1(t.latencyCost)
  t.trafficCost = round1(t.trafficCost)
  return t
}

export interface ClusterLoad {
  clusterId: string
  name: string
  /** Share of CPU requested, before and after. Undefined when capacity is unknown. */
  before?: number
  after?: number
}

export interface MoveOutcome {
  serviceId: string
  serviceName: string
  from: string
  to: string
  /** Three-valued, from the same rules as everywhere else: a move that cannot be certified is not shown as fitting. */
  verdict: Evaluation['verdict']
  fits: boolean
  confidence: Level
  blockers: string[]
  unchecked: string[]
  /** What can be done to turn "can't tell" into an answer. */
  fixes: Fix[]
  /** How far the reported free capacity could be off before the verdict changes, one line per dimension. */
  sensitivity: string[]
  facts: FactRow[]
  wouldChange: Change[]
  /** Change in the service's own steady-state cost when moved on its own from the starting placement. */
  benefit: number
  before: Evaluation
  after: Evaluation
}

export interface WhatIf {
  before: Totals
  after: Totals
  moves: MoveOutcome[]
  loads: ClusterLoad[]
  /** Things that make the scenario unsafe or unproven. */
  warnings: string[]
}

/** Compare the world as it is with the world after some moves. Nothing is applied anywhere. */
export function whatIf(w: World, P: Policy, moves: Move[]): WhatIf {
  const after = withMoves(w, moves)
  const outcomes: MoveOutcome[] = []
  const warnings: string[] = []
  // Judge each move in the order given, so a later one sees the capacity and neighbours the earlier ones left.
  let step = w
  for (const m of moves) {
    const s = w.byService.get(m.serviceId)
    if (!s) continue
    const from = clusterOfService(step, s.id) ?? s.clusterId
    const before = evaluate(step, s, from, P)
    const target = evaluate(step, s, m.to, P)
    const toName = w.byCluster.get(m.to)?.name ?? m.to
    const sensitivity = (target.advice?.dims ?? []).map(sensitivityText).filter((t) => t !== '')
    outcomes.push({
      serviceId: s.id, serviceName: s.name, from, to: m.to, verdict: target.verdict, fits: target.fits, confidence: target.confidence,
      blockers: target.blockers, unchecked: target.unchecked, fixes: target.fixes, sensitivity, facts: target.facts, wouldChange: target.wouldChange,
      benefit: round1(before.cost - target.cost), before, after: target,
    })
    if (target.verdict === 'doesNotFit') warnings.push(`${s.name} cannot run at ${toName}: ${target.blockers.join('; ')}.`)
    else if (target.verdict === 'cantTell') warnings.push(`It cannot be told whether ${s.name} fits at ${toName}: ${target.unchecked.join('; ')}.`)
    if (!isMovableKind(s)) warnings.push(`${s.name} is a ${s.kind}; it does not move.`)
    else {
      const mv = movability(placed(step, s), moveModel(step, storageKnown(step, from)))
      if (mv.verdict === 'pinned') warnings.push(`${s.name} is pinned: ${mv.reasons.find((r) => r.severity === 'blocker')?.text ?? 'something ties it here'}`)
    }
    step = withMoves(step, [m])
  }
  const touched = new Set(outcomes.flatMap((o) => [o.from, o.to]))
  const loads: ClusterLoad[] = []
  for (const id of touched) {
    const c = w.byCluster.get(id)
    if (!c) continue
    const b = utilization(w, id, 0)
    const a = utilization(after, id, 0)
    loads.push({ clusterId: id, name: c.name, before: b, after: a })
    if (a !== undefined && a > 1) warnings.push(`${c.name} would be over-committed (${Math.round(a * 100)} % of its CPU requested).`)
    else if (a !== undefined && a > 0.9) warnings.push(`${c.name} would be nearly full (${Math.round(a * 100)} % of its CPU requested).`)
  }
  return { before: totals(w, P), after: totals(after, P), moves: outcomes, loads, warnings }
}

export interface Evacuation {
  moves: Move[]
  /** Services that would stop, and why nothing can take them. */
  lost: Skipped[]
  result: WhatIf
}

/** Where every movable service in a cluster would go if that cluster went away. Greedy, biggest first, capacity counted. */
export function evacuate(w: World, P: Policy, clusterId: string): Evacuation {
  const inside = w.services.filter((s) => (clusterOfService(w, s.id) ?? s.clusterId) === clusterId)
  const order = [...inside].sort((a, b) => serviceNeed(b).cpu - serviceNeed(a).cpu || a.name.localeCompare(b.name))
  const moves: Move[] = []
  const lost: Skipped[] = []
  let cur = w
  for (const s of order) {
    if (!isMovableKind(s)) {
      lost.push({ serviceId: s.id, serviceName: s.name, why: s.kind === 'DaemonSet' ? 'A DaemonSet runs on the cluster’s own nodes.' : 'A job would have to be run again.' })
      continue
    }
    const mv = movability(placed(cur, s), moveModel(cur, storageKnown(cur, clusterId)))
    const blockedBy = mv.reasons.find((r) => r.severity === 'blocker')
    const targets = targetsFor(cur, s)
    const all = cur.clusters.filter((c) => c.id !== clusterId).map((c) => evaluate(cur, s, c.id, P, targets))
    const options = all.filter((e) => e.fits)
    options.sort((a, b) => a.cost + a.migrationCost - (b.cost + b.migrationCost) || a.clusterId.localeCompare(b.clusterId))
    if (blockedBy) {
      lost.push({ serviceId: s.id, serviceName: s.name, why: blockedBy.text })
      continue
    }
    if (!options[0]) {
      // nothing certain: say whether that is because nothing has room, or because nobody can tell
      const unsure = all.filter((e) => e.verdict === 'cantTell')
      const why = unsure.length > 0
        ? `No other cluster is known to have room for it, and it cannot be told for ${unsure.map((e) => cur.byCluster.get(e.clusterId)?.name ?? e.clusterId).join(', ')}: ${[...new Set(unsure.flatMap((e) => e.unchecked))].slice(0, 2).join('; ')}.`
        : 'No other cluster can take it (policy, labels or capacity).'
      lost.push({ serviceId: s.id, serviceName: s.name, why })
      continue
    }
    moves.push({ serviceId: s.id, to: options[0].clusterId })
    cur = withMoves(cur, [moves[moves.length - 1]])
  }
  return { moves, lost, result: whatIf(w, P, moves) }
}

/* ---------- how much the advice can be trusted ---------- */

export interface Coverage {
  /** Dependencies that touch a service. */
  edges: number
  /** Of those, how many have measured traffic (bytes per second). */
  trafficMeasured: number
  /** How each dependency's round trip was obtained. */
  rtt: Record<RttBasis, number>
  /** Measured paths that can be trusted. */
  paths: number
}

/** What the recommendations are built on, so nobody mistakes an estimate for a measurement. */
export function coverage(w: World): Coverage {
  const rttBasis: Record<RttBasis, number> = { 'same-cluster': 0, measured: 0, declared: 0, 'same-site': 0, estimated: 0, unknown: 0 }
  let edges = 0
  let trafficMeasured = 0
  const seen = new Set<string>()
  for (const s of w.services) {
    const home = clusterOfService(w, s.id) ?? s.clusterId
    for (const p of peersOf(w, s)) {
      if (seen.has(p.dependencyId)) continue
      seen.add(p.dependencyId)
      edges++
      if (p.trafficKnown) trafficMeasured++
      rttBasis[rtt(w, home, locOf(w, p.kind, p.id)).basis]++
    }
  }
  return { edges, trafficMeasured, rtt: rttBasis, paths: w.paths.length }
}
