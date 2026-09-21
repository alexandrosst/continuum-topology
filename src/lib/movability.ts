// "Can this service move, and where to?" A verdict a person can argue with, built only from facts the model
// already holds: volumes, placement constraints, disruption budgets, devices it is bound to, and the policy
// (sensitivity, trust zone, data residency) of where it runs. Nothing here moves anything; it is what the
// orchestrator (and the person approving it) needs to see first.
import { Anchor, MoveRight, TriangleAlert, type LucideIcon } from 'lucide-react'
import { assess, type Advice, type Check, type Fix, type FactRow, type Level, type Verdict } from './advice'
import { adjustPools, defaultCtx, fixForPool, judgeCapacity, poolsOf, type CapCtx, type Place, type Pools } from './capacity'
import { observation, targetStatus, type Tone, type TargetStatus } from './provenance'
import type { Agent, Cluster, Device, Dependency, MachineNode, Service, Site, TrustZone } from './types'

export type MoveVerdict = 'free' | 'careful' | 'pinned'
export type ReasonSeverity = 'blocker' | 'caution' | 'info'

export interface MoveReason {
  severity: ReasonSeverity
  code: string
  text: string
}

export interface Movability {
  verdict: MoveVerdict
  reasons: MoveReason[]
}

export interface MoveTarget {
  cluster: Cluster
  site?: Site
  /**
   * True only when everything was checked and holds: the verdict is "fits". A target that could not be checked is not
   * a fit (see `verdict`), and one that was ruled out is not either.
   */
  fits: boolean
  /** Three-valued: fits, can't tell (something is not known or too uncertain), or does not fit. */
  verdict: Verdict
  /** How far to trust the verdict: the weakest fact that decides it. */
  confidence: Level
  /** The full answer: every check and dimension, the reasons, what would fix a "can't tell" and what would change the verdict. */
  advice: Advice
  /** The facts the answer used, with source, class and age. */
  facts: FactRow[]
  /** What rules this cluster out (verdict "does not fit"). */
  blockers: string[]
  /** The same, without the capacity ones: what stays true however full the cluster is. */
  policyBlockers: string[]
  /** What could not be checked or is too uncertain to certify (verdict "can't tell"). */
  unknown: string[]
  /** What a person can do to turn a "can't tell" into an answer. */
  fixes: Fix[]
  /** Everything except capacity: node selectors, data residency, trust zone, kind. Capacity is judged per world, from `pools`. */
  checks: Check[]
  /** Free capacity of the nodes that could host the workload, before any what-if moves. */
  pools: Pools
  place: Place
  /**
   * Set when the cluster is not a target at all: it is not live (stale, disconnected, revoked, gone). Only live
   * targets are ever offered; this says why one was not. A live cluster whose capacity is unknown is not here: it is a
   * "can't tell", with the fix.
   */
  excluded?: Pick<TargetStatus, 'state' | 'reason'>
}

export interface MoveModel {
  clusters: Cluster[]
  nodes: MachineNode[]
  services: Service[]
  devices: Device[]
  dependencies: Dependency[]
  sites: Site[]
  /** Whether the agent reads volumes for this service's cluster. When it does not, "no volumes" proves nothing. */
  storageKnown?: boolean
  /** Agents, to say which access tier would make a missing figure known. */
  agents?: Agent[]
  /** When the facts are judged and how old they may be; defaults to now and the server's default window. */
  cap?: CapCtx
}

const alive = <T extends { deletedAt?: string }>(xs: T[]) => xs.filter((x) => !x.deletedAt)

const ZONE_RANK: Record<TrustZone, number> = { public: 0, private: 1, restricted: 2 }

const isHostnameKey = (k: string) => k === 'kubernetes.io/hostname'
const isArchKey = (k: string) => k === 'kubernetes.io/arch' || k === 'beta.kubernetes.io/arch'
const isOsKey = (k: string) => k === 'kubernetes.io/os' || k === 'beta.kubernetes.io/os'
const looksLikeAccelerator = (k: string, v: string) => /gpu|nvidia|accelerator|tpu|npu|coral|jetson/i.test(`${k}=${v}`)

/** Labels a node can be matched on, including the ones Kubernetes derives from fields. */
const nodeLabels = (n: MachineNode): Record<string, string> => ({
  ...(n.arch ? { 'kubernetes.io/arch': n.arch, 'beta.kubernetes.io/arch': n.arch } : {}),
  ...(n.zone ? { 'topology.kubernetes.io/zone': n.zone } : {}),
  ...n.labels,
})

const siteOf = (c: Cluster | undefined, sites: Site[]) => sites.find((s) => s.id === c?.siteId)

export function movability(w: Service, m: MoveModel): Movability {
  const reasons: MoveReason[] = []
  const add = (severity: ReasonSeverity, code: string, text: string) => reasons.push({ severity, code, text })
  const nodes = alive(m.nodes)
  const cluster = m.clusters.find((c) => c.id === w.clusterId)
  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name ?? id

  // --- what kind of workload it is
  if (w.kind === 'DaemonSet') add('blocker', 'daemonset', 'A DaemonSet runs on every node by design. It does not move: it follows the nodes.')
  if (w.kind === 'Job') add('info', 'job', 'A job runs to completion. Run it again elsewhere instead of moving it.')

  // --- data
  const vols = w.volumes ?? []
  const pinned = vols.filter((v) => v.pinnedNodeIds && v.pinnedNodeIds.length > 0)
  if (pinned.length > 0) {
    const where = [...new Set(pinned.flatMap((v) => v.pinnedNodeIds ?? []).map(nodeName))]
    add('blocker', 'local-volume', `Data sits on a local volume on ${where.join(', ')}. It cannot follow the pod without being copied.`)
  }
  const shared = vols.filter((v) => !v.pinnedNodeIds || v.pinnedNodeIds.length === 0)
  if (shared.length > 0) {
    const gb = shared.reduce((a, v) => a + v.sizeGb, 0)
    add('caution', 'volume', `${shared.length} persistent volume${shared.length === 1 ? '' : 's'} (${gb} GB): the data has to be copied or replicated to the new place first.`)
  }
  if (vols.length === 0 && w.kind === 'StatefulSet') add('caution', 'stateful', 'A StatefulSet usually keeps state, but no volumes were reported. Check before moving it.')
  if (vols.length === 0 && m.storageKnown === false) add('info', 'storage-unknown', 'Storage is not read for this cluster, so volumes may exist that are not shown here.')

  // --- placement constraints it declares
  for (const [k, v] of Object.entries(w.nodeSelector ?? {})) {
    if (isHostnameKey(k)) add('blocker', 'hostname', `Pinned to the machine ${v} by a node selector.`)
    else if (isArchKey(k)) add('caution', 'arch', `Built for ${v}: only ${v} machines can run it.`)
    else if (isOsKey(k)) add('caution', 'os', `Needs ${v} nodes.`)
    else if (looksLikeAccelerator(k, v)) add('caution', 'accelerator', `Needs an accelerator (${k}=${v}).`)
    else add('caution', 'selector', `Only runs on nodes labelled ${k}=${v}.`)
  }
  if ((w.tolerations ?? []).length > 0) add('info', 'tolerations', `Tolerates ${w.tolerations!.join(', ')}: the new place needs matching taints to keep the same placement.`)

  // --- devices it is bound to
  const mine = new Set(w.nodeIds)
  const devs = alive(m.devices)
  const deviceEnds = new Set(
    m.dependencies.flatMap((d) => (d.from === w.id && d.toKind === 'device' ? [d.to] : d.to === w.id && d.fromKind === 'device' ? [d.from] : [])),
  )
  const attached = devs.filter((d) => d.gatewayNodeId && mine.has(d.gatewayNodeId))
  if (attached.length > 0) add('blocker', 'attached-device', `${attached.map((d) => d.name).join(', ')} ${attached.length === 1 ? 'is' : 'are'} plugged into ${[...new Set(attached.map((d) => nodeName(d.gatewayNodeId!)))].join(', ')}, where it runs.`)
  const talkers = devs.filter((d) => deviceEnds.has(d.id) && !attached.includes(d))
  if (talkers.length > 0) {
    const sites = [...new Set(talkers.map((d) => m.sites.find((s) => s.id === d.siteId)?.name).filter(Boolean))]
    add('caution', 'devices', `Talks to ${talkers.map((d) => d.name).join(', ')}${sites.length ? ` at ${sites.join(', ')}` : ''}. Moving far away adds latency to every message.`)
  }

  // --- how it is operated
  if (w.disruption && w.disruption.allowed === 0) add('caution', 'pdb', 'Its disruption budget allows no evictions right now, so its pods cannot be drained.')
  if (w.replicas <= 1 && !w.autoscaler && w.kind !== 'Job' && w.kind !== 'DaemonSet') add('caution', 'single', 'One replica only: moving it means a gap in service.')
  if (w.autoscaler) add('info', 'autoscaled', `Autoscaled between ${w.autoscaler.min} and ${w.autoscaler.max} replicas: it can be started in the new place before the old one is scaled down.`)
  if ((w.exposure === 'ingress' || w.exposure === 'load-balancer') && (w.hosts ?? []).length > 0) {
    add('caution', 'exposed', `Reachable at ${w.hosts!.slice(0, 2).join(', ')}${w.hosts!.length > 2 ? '…' : ''}: DNS and the ingress have to follow it.`)
  }
  const mates = alive(m.services).filter((s) => s.id !== w.id && s.applicationId && s.applicationId === w.applicationId && s.clusterId === w.clusterId)
  if (mates.length > 0) add('info', 'application', `Part of an application with ${mates.length} other service${mates.length === 1 ? '' : 's'} here (${mates.slice(0, 3).map((s) => s.name).join(', ')}${mates.length > 3 ? '…' : ''}). Moving it alone splits them across a network.`)

  // --- policy
  const site = siteOf(cluster, m.sites)
  const residency = cluster?.dataResidency ?? site?.dataResidency
  if (w.sensitivity && w.sensitivity !== 'public' && residency) add('caution', 'residency', `${w.sensitivity === 'confidential' ? 'Confidential' : 'Internal'} data that has to stay in ${residency}.`)
  const zone = cluster?.trustZone ?? site?.trustZone
  if (zone === 'restricted') add('caution', 'trust', 'Runs in a restricted zone: it can only go to another restricted place.')

  const verdict: MoveVerdict = reasons.some((r) => r.severity === 'blocker') ? 'pinned' : reasons.some((r) => r.severity === 'caution') ? 'careful' : 'free'
  // worst first, so the sentence that matters is the first one
  const order: Record<ReasonSeverity, number> = { blocker: 0, caution: 1, info: 2 }
  reasons.sort((a, b) => order[a.severity] - order[b.severity])
  return { verdict, reasons }
}

type Match = 'yes' | 'no' | 'maybe'

/** Whether a node matches a selector. An architecture the node never reported is "maybe": absence is not a no. */
function matches(n: MachineNode, k: string, v: string): Match {
  const l = nodeLabels(n)
  if (l[k] === v) return 'yes'
  if (isArchKey(k) && !n.arch && l[k] === undefined) return 'maybe'
  return 'no'
}

const VERDICT_RANK: Record<Verdict, number> = { fits: 0, cantTell: 1, doesNotFit: 2 }

/**
 * The answer for one workload at one target: the checks that do not depend on load, and capacity from the pools, which
 * a what-if can shift by moving workloads in and out. Unknown capacity, and capacity too uncertain to certify, is
 * "can't tell"; only a definite shortfall (or a violated constraint) is "does not fit".
 */
export function judgeTarget(
  t: Pick<MoveTarget, 'checks' | 'pools' | 'place' | 'cluster' | 'excluded'>,
  s: Service,
  ctx: CapCtx,
  moves: { s: Pick<Service, 'replicas' | 'cpuRequestM' | 'memRequestMi'>; dir: 'in' | 'out' }[] = [],
): { advice: Advice; facts: FactRow[] } {
  const pools = moves.length > 0 ? adjustPools(t.pools, moves) : t.pools
  const state = observation(t.cluster)?.kind ?? 'declared'
  const { dims, facts } = judgeCapacity(s, pools, t.place, ctx, state)
  const checks = [...t.checks]
  if (t.excluded) checks.unshift({ name: 'live', verdict: 'doesNotFit', class: 'reported', reason: t.excluded.reason ?? 'not a live target' })
  return { advice: assess(dims, checks), facts }
}

/**
 * Every other cluster, with what rules it out or cannot be checked for this service. Clusters that fit come first, then
 * those that cannot be told, then those ruled out; nearest tier first within each.
 */
export function moveTargets(w: Service, m: MoveModel): MoveTarget[] {
  const from = m.clusters.find((c) => c.id === w.clusterId)
  const fromSite = siteOf(from, m.sites)
  const fromResidency = from?.dataResidency ?? fromSite?.dataResidency
  const fromZone = from?.trustZone ?? fromSite?.trustZone
  const nodes = alive(m.nodes)
  const wanted = Object.entries(w.nodeSelector ?? {}).filter(([k]) => !isHostnameKey(k))
  const restricted = w.sensitivity && w.sensitivity !== 'public'
  const ctx = m.cap ?? defaultCtx()

  const out: MoveTarget[] = []
  for (const c of alive(m.clusters)) {
    if (c.id === w.clusterId) continue
    const site = siteOf(c, m.sites)
    const checks: Check[] = []
    const extraFacts: FactRow[] = []
    const ns = nodes.filter((n) => n.clusterId === c.id)
    const state = observation(c)?.kind ?? 'declared'

    if (w.kind === 'DaemonSet') checks.push({ name: 'kind', verdict: 'doesNotFit', class: 'reported', reason: 'a DaemonSet runs on every node of its own cluster' })

    // Nodes that could host it: every selector has to match on one node. A node that never reported its architecture
    // might match; it counts for nothing until it says (its capacity is unknown to this check).
    let pool = ns
    if (wanted.length > 0 && ns.length > 0) {
      const verdicts = ns.map((n) => wanted.map(([k, v]) => matches(n, k, v)))
      const definite = ns.filter((_, i) => verdicts[i].every((x) => x === 'yes'))
      const maybe = ns.filter((_, i) => !verdicts[i].includes('no') && verdicts[i].includes('maybe'))
      pool = [...definite, ...maybe.map((n) => ({ ...n, allocatable: undefined, requested: undefined }))]
      const sel = wanted.map(([k, v]) => `${k}=${v}`).join(', ')
      if (definite.length === 0 && maybe.length === 0) checks.push({ name: 'selector', verdict: 'doesNotFit', class: 'reported', reason: `no node carries ${sel}` })
      else if (definite.length === 0) {
        checks.push({
          name: 'selector',
          verdict: 'cantTell',
          class: 'unknown',
          reason: `whether ${maybe.map((n) => n.name).join(', ')} carries ${sel} is not known: the architecture is not reported`,
          fix: { action: 'check-agent', text: 'Let the agent read the nodes (access tier 1 or higher) so their architecture is known', link: '/agents' },
        })
      }
    }

    // Nothing is placed on what is not known to be there right now.
    const status = targetStatus(c, true)
    const excluded = status.eligible ? undefined : { state: status.state, reason: status.reason }

    const toResidency = c.dataResidency ?? site?.dataResidency
    if (restricted && fromResidency) {
      extraFacts.push({ attribute: 'dataResidency', entity: c.id, entityName: c.name, value: toResidency ?? null, source: 'declared', confidence: toResidency ? 'reported' : 'unknown', state, evidence: toResidency ? undefined : `this data stays in ${fromResidency}; the cluster's residency is not set` })
      if (!toResidency) {
        checks.push({
          name: 'residency',
          verdict: 'cantTell',
          class: 'unknown',
          reason: `its data residency is not set (this data stays in ${fromResidency})`,
          fix: { action: 'declare-residency', text: `Set the data residency of ${c.name} (or of its site)`, link: '/sites' },
        })
      } else if (toResidency !== fromResidency) checks.push({ name: 'residency', verdict: 'doesNotFit', class: 'reported', reason: `data must stay in ${fromResidency}, this is ${toResidency}` })
      else checks.push({ name: 'residency', verdict: 'fits', class: 'reported', reason: `data stays in ${fromResidency}` })
    }
    const toZone = c.trustZone ?? site?.trustZone
    if (fromZone) {
      extraFacts.push({ attribute: 'trustZone', entity: c.id, entityName: c.name, value: toZone ?? null, source: 'declared', confidence: toZone ? 'reported' : 'unknown', state, evidence: toZone ? undefined : `it runs in a ${fromZone} zone now; the cluster's zone is not set` })
      if (!toZone) {
        checks.push({
          name: 'trust',
          verdict: 'cantTell',
          class: 'unknown',
          reason: 'its trust zone is not set',
          fix: { action: 'declare-trust-zone', text: `Set the trust zone of ${c.name} (or of its site)`, link: '/sites' },
        })
      } else if (ZONE_RANK[toZone] < ZONE_RANK[fromZone]) checks.push({ name: 'trust', verdict: 'doesNotFit', class: 'reported', reason: `less trusted (${toZone}) than where it runs now (${fromZone})` })
      else checks.push({ name: 'trust', verdict: 'fits', class: 'reported', reason: `zone ${toZone} is at least as trusted as ${fromZone}` })
    }

    const agent = m.agents?.find((a) => a.clusterId === c.id && a.status === 'approved')
    const place: Place = { clusterId: c.id, clusterName: c.name, declared: c.source !== 'discovered', agent, nodeCount: ns.length }
    const pools = poolsOf(pool, ctx)
    const t = { checks, pools, place, cluster: c, excluded }
    const { advice, facts } = judgeTarget(t, w, ctx)
    // the fixes of the dimensions are only known here (they depend on where the missing figure comes from)
    for (const d of advice.dims) if (d.fix === undefined && d.verdict === 'cantTell') d.fix = fixForPool(d.pool, place, d.dimension)

    const doesNot = (x: { verdict: Verdict; reason: string }) => x.verdict === 'doesNotFit'
    const blockers = [...advice.checks.filter(doesNot).map((x) => x.reason), ...advice.dims.filter(doesNot).map((x) => x.reason)]
    const policyBlockers = advice.checks.filter(doesNot).map((x) => x.reason)
    const unknown = [...advice.checks.filter((x) => x.verdict === 'cantTell').map((x) => x.reason), ...advice.dims.filter((x) => x.verdict === 'cantTell').map((x) => x.reason)]
    out.push({
      cluster: c, site, fits: advice.verdict === 'fits', verdict: advice.verdict, confidence: advice.confidence, advice, facts: [...facts, ...extraFacts],
      blockers, policyBlockers, unknown, fixes: advice.fixes, checks, pools, place, excluded,
    })
  }
  const tier = { cloud: 0, edge: 1, 'far-edge': 2 } as const
  return out.sort((a, b) => VERDICT_RANK[a.verdict] - VERDICT_RANK[b.verdict] || Number(!!a.excluded) - Number(!!b.excluded) || a.unknown.length - b.unknown.length || tier[a.cluster.tier] - tier[b.cluster.tier] || a.cluster.name.localeCompare(b.cluster.name))
}

export const VERDICT_LABEL: Record<MoveVerdict, string> = { free: 'Free to move', careful: 'Move with care', pinned: 'Pinned' }

export const VERDICT_HELP: Record<MoveVerdict, string> = {
  free: 'Nothing known ties this service to where it runs.',
  careful: 'It can move, but something needs attention first (data, devices, policy or downtime).',
  pinned: 'Something makes moving it impossible or unsafe as it is. Read the reasons.',
}

/** The verdict's place in the app's one ok/warn/bad palette (`TONE_CLASS` in primitives.tsx) and the icon that
 * goes with it, kept next to the label/help above so every badge for a verdict looks and reads the same. */
export const VERDICT_TONE: Record<MoveVerdict, Tone> = { free: 'ok', careful: 'warn', pinned: 'bad' }
export const VERDICT_ICON: Record<MoveVerdict, LucideIcon> = { free: MoveRight, careful: TriangleAlert, pinned: Anchor }
