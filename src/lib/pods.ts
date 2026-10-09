import { ageLabel, recentlyScaledPods } from './present'
import type { MachineNode, Pod, PodPeer, Service } from './types'

/** What a pod cell is drawn as. `bad` is a crash loop (restarts, even when the pod is ready again), `warn` a
 *  pod that is not ready or still pending on a node, `unscheduled` one with no node yet (a hollow ring). */
export type PodState = 'ready' | 'warn' | 'bad' | 'unscheduled'

/** From this many restarts a pod counts as crash-looping. */
export const CRASH_RESTARTS = 3
/** Cells on the card's rail; the rest fold into "+N" (a pod that needs attention never does). */
export const RAIL_LIMIT = 10
/** Healthy pods listed per node in the popover before "+N more healthy". */
export const HEALTHY_LISTED = 4

export interface PodRow {
  id: string
  /** The part of the name that tells replicas apart ("6d9f7b8c4-5c1x"), for a list that has no room for all of it. */
  label: string
  state: PodState
  /** Young next to its siblings: a scaling event (see recentlyScaledPods). */
  recent: boolean
  restarts: number
  /** "Pending", "Not ready" ...: only for a pod that is not ready. */
  phase?: string
  /** Compact: "3d", "12m". */
  age: string
  /** The hover text: name, phase, age, restarts. */
  title: string
  traffic?: PodPeer[]
}

export interface PodNodeGroup {
  nodeId: string
  nodeName: string
  /** Pods that need a look (a colour or the "new" mark) first, worst first, then the healthy ones; both by name. */
  pods: PodRow[]
}

export interface PodsView {
  total: number
  ready: number
  /** Not ready (or no node) and crash-looping pods, for the popover's header chips. */
  warn: number
  bad: number
  /** Real nodes only: the "not scheduled" group is not one. */
  nodes: number
  /** At most RAIL_LIMIT pods in a stable order (node, then name); every pod that needs attention is in it. */
  rail: PodRow[]
  overflow: number
  groups: PodNodeGroup[]
}

export const needsAttention = (p: Pick<PodRow, 'state' | 'recent'>): boolean => p.state !== 'ready' || p.recent

export function podState(p: Pod): PodState {
  if ((p.restarts ?? 0) >= CRASH_RESTARTS) return 'bad'
  if (p.ready) return 'ready'
  return p.nodeId ? 'warn' : 'unscheduled'
}

/** "checkout-7f9c8-a1b2c" of service "checkout" is "7f9c8-a1b2c". A name that does not start that way, or
 *  would leave next to nothing ("postgres-0" -> "0"), stays whole. */
export function podLabel(name: string, service: string): string {
  const rest = name.startsWith(`${service}-`) ? name.slice(service.length + 1) : name
  return rest.length >= 3 ? rest : name
}

/** "3 days" -> "3d", "just now" -> "now": the popover's age column. */
const compactAge = (s: string) => (s === 'just now' ? 'now' : s.replace(/^(\d+) (\w+?)s?$/, (_, n, u) => n + (u === 'month' ? 'mo' : u[0])))

const SEVERITY: Record<PodState, number> = { bad: 0, warn: 1, unscheduled: 1, ready: 2 }
const byName = (a: PodRow, b: PodRow) => a.id.localeCompare(b.id)

/** Everything a service card shows about its pods, from the full pod list (the rail caps, the popover does not). */
export function buildPodsView(
  pods: Pod[] | undefined,
  service: string,
  nodeById: Map<string, MachineNode>,
  serviceById: Map<string, Service>,
  now = Date.now(),
): PodsView | undefined {
  if (!pods || pods.length === 0) return undefined
  const recent = recentlyScaledPods(pods)
  // A service-kind peer is a Service id (see PodPeer): shown by name, an external one as the address it is.
  const peerName = (t: PodPeer): PodPeer => (t.peerKind === 'service' ? { ...t, peer: serviceById.get(t.peer)?.name ?? t.peer } : t)
  const byNode = new Map<string, PodNodeGroup>()
  for (const p of pods) {
    const nodeId = p.nodeId ?? ''
    let g = byNode.get(nodeId)
    if (!g) byNode.set(nodeId, (g = { nodeId, nodeName: p.nodeId ? nodeById.get(p.nodeId)?.name ?? p.nodeId : 'not scheduled', pods: [] }))
    const state = podState(p)
    const age = ageLabel(p.createdAt, now)
    const restarts = p.restarts ?? 0
    g.pods.push({
      id: p.name,
      label: podLabel(p.name, service),
      state,
      recent: recent.has(p.name),
      restarts,
      phase: p.ready ? undefined : p.phase === 'Running' ? 'Not ready' : p.phase,
      age: compactAge(age),
      title: [p.name, p.ready ? p.phase : `${p.phase}, not ready`, age && `${age} old`, restarts > 0 && `${restarts} restart${restarts === 1 ? '' : 's'}`, recent.has(p.name) && 'recently added (scaling)'].filter(Boolean).join(' · '),
      traffic: p.traffic?.map(peerName),
    })
  }
  const groups = [...byNode.values()].sort((a, b) => a.nodeName.localeCompare(b.nodeName))
  const stable = groups.flatMap((g) => [...g.pods].sort(byName))
  for (const g of groups) g.pods.sort((a, b) => SEVERITY[a.state] - SEVERITY[b.state] || +!a.recent - +!b.recent || byName(a, b))
  // The rail: everyone who needs attention first claims a cell, healthy pods fill what is left, and the
  // result goes back to the stable order so a cell does not move when another pod changes colour.
  const keep = new Set([...stable.filter(needsAttention), ...stable.filter((r) => !needsAttention(r))].slice(0, RAIL_LIMIT))
  return {
    total: pods.length,
    ready: pods.filter((p) => p.ready).length,
    warn: stable.filter((r) => r.state === 'warn' || r.state === 'unscheduled').length,
    bad: stable.filter((r) => r.state === 'bad').length,
    nodes: groups.filter((g) => g.nodeId).length,
    rail: stable.filter((r) => keep.has(r)),
    overflow: Math.max(0, stable.length - RAIL_LIMIT),
    groups,
  }
}
