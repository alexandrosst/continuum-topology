import { inMesh, meshName, mtlsFor, MTLS_WORDS } from './mesh'
import { observation, type ObsInfo } from './provenance'
import type { Agent, AgentScope, Cluster, Namespace, Service, Source } from './types'

/**
 * The namespaces of every cluster, one row each, with what an agent's scope says about them.
 *
 * A namespace an agent was told to leave out is never reported, and its name never leaves the cluster: there is
 * nothing to list. What the server can say is how MANY were left out, and this file words that as counts only.
 */

export type NamespaceMesh = {
  /** "Istio · mTLS strict", "Istio · opted out". */
  label: string
  /** How many of the namespace's workloads have a mesh proxy handling their traffic. */
  covered: number
  total: number
  tone: 'ok' | 'warn' | 'muted'
  detail: string
}

export interface NamespaceRow {
  kind: 'namespace'
  key: string
  clusterId: string
  clusterName: string
  name: string
  /** Every record of the namespace (Deployment, StatefulSet, DaemonSet, Job) is what the Services page calls a service. */
  services: number
  /** "3 Deployments · 1 StatefulSet". */
  kinds: string
  /** Workloads a Kubernetes Service or ingress reaches from outside the cluster (node port, load balancer, ingress). */
  exposed: number
  mesh?: NamespaceMesh
  /** The observation state of the namespace's own record. Absent for one a person declared. */
  observation?: ObsInfo
  source: Source | 'services'
  /** In scope: an agent reported it. */
  scope: 'in' | 'declared'
}

export interface ExcludedRow {
  kind: 'excluded'
  key: string
  clusterId: string
  clusterName: string
  /** How many namespaces the agent's scope left out. Never their names. */
  count: number
  /** How the scope was set, as the agent described it ("namespaces shop, payments", "label team=a"). */
  description: string
}

export type Row = NamespaceRow | ExcludedRow

type ScopeAgent = Pick<Agent, 'status' | 'scope' | 'kubernetesVersion'>

export type ClusterScope =
  /** No agent, or a hand-made / sample one that never reported a scope: nothing is left out that we know of. */
  | { kind: 'none' }
  /** The agent reads every namespace it can. */
  | { kind: 'all'; namespaces: number }
  /** The agent was told to read some of them. */
  | { kind: 'narrowed'; namespaces: number; inScope: number; excluded: number; description: string }

/** What one agent's scope says, from the counts it reports. */
export function scopeOf(agent: ScopeAgent | undefined): ClusterScope {
  if (!agent || agent.status !== 'approved') return { kind: 'none' }
  const sc: AgentScope | undefined = agent.scope
  // Hand-made and sample agents never reported a scope; a real one that sends none is not narrowing anything.
  if (!sc) return agent.kubernetesVersion ? { kind: 'all', namespaces: 0 } : { kind: 'none' }
  const excluded = Math.max(0, sc.namespaces - sc.inScope)
  if (excluded === 0 && !sc.description) return { kind: 'all', namespaces: sc.namespaces }
  return { kind: 'narrowed', namespaces: sc.namespaces, inScope: sc.inScope, excluded, description: sc.description }
}

/** "3 namespaces are left out by this agent's scope; their names never leave the cluster." */
export function excludedWords(count: number): string {
  return `${count} ${count === 1 ? 'namespace is' : 'namespaces are'} left out by this agent’s scope. Their names never leave the cluster, so they cannot be listed.`
}

/** "In scope" or, for a cluster nobody observes, "declared". */
export function scopeLabel(row: Pick<NamespaceRow, 'scope'>): string {
  return row.scope === 'in' ? 'in scope' : 'declared'
}

const KIND_WORD: Record<string, string> = { Deployment: 'Deployment', StatefulSet: 'StatefulSet', DaemonSet: 'DaemonSet', Job: 'Job' }
const OUTSIDE = new Set(['node-port', 'load-balancer', 'ingress'])

function kindsLine(list: Service[]): string {
  const by = new Map<string, number>()
  for (const s of list) by.set(s.kind, (by.get(s.kind) ?? 0) + 1)
  return [...by].map(([k, n]) => `${n} ${KIND_WORD[k] ?? k}${n === 1 ? '' : 's'}`).join(' · ')
}

/**
 * What the mesh does in this namespace, from the cluster's mesh, the namespace's own asks and its workloads' proxies.
 * A namespace no workload of which is in the mesh, and that neither asks for it nor holds its control plane, says nothing.
 */
export function namespaceMesh(cluster: Cluster | undefined, ns: Namespace | undefined, services: Service[]): NamespaceMesh | undefined {
  const workloads = services.filter((s) => !s.mesh?.controlPlane)
  const covered = workloads.filter((s) => inMesh(s.mesh)).length
  const kindOf = cluster?.mesh?.kind ?? ns?.mesh ?? services.find((s) => s.mesh)?.mesh?.mesh
  if (!kindOf) return undefined
  const name = meshName(kindOf)
  if (ns?.meshOff) {
    return { label: `${name} · opted out`, covered, total: workloads.length, tone: 'muted', detail: `The namespace opts out of ${name}.` }
  }
  if (covered === 0) {
    if (services.some((s) => s.mesh?.controlPlane)) return { label: `${name} control plane`, covered, total: workloads.length, tone: 'muted', detail: `${name}’s own components run here.` }
    if (ns?.mesh) return { label: `${name} · no proxy seen`, covered, total: workloads.length, tone: 'warn', detail: `The namespace asks to join ${name}, but no workload has a proxy yet.` }
    return undefined
  }
  const mode = cluster?.mesh ? mtlsFor(cluster.mesh, ns?.name ?? services[0]?.namespace ?? '', ns) : undefined
  const unread = mode === 'unknown' || (!cluster?.mesh?.policyRead && mode === 'permissive')
  const mtls = mode ? (unread ? 'mTLS not read' : `mTLS ${mode}`) : ''
  return {
    label: [name, mtls].filter(Boolean).join(' · '),
    covered,
    total: workloads.length,
    tone: mode === 'strict' || mode === 'automatic' ? 'ok' : 'warn',
    detail: `${covered} of ${workloads.length} workloads in the mesh.${mode ? ` ${MTLS_WORDS[mode] ?? mode}.` : ''} Read from configuration, not from traffic.`,
  }
}

/**
 * One row per namespace of every cluster, clusters and namespaces alphabetical, each cluster followed by one row
 * that counts what its agent's scope left out (when it left anything out).
 */
export function buildNamespaceRows(t: { clusters: Cluster[]; namespaces: Namespace[]; services: Service[]; agents: Agent[] }): Row[] {
  const rows: Row[] = []
  const clusters = t.clusters.filter((c) => !c.deletedAt).sort((a, b) => a.name.localeCompare(b.name))
  for (const c of clusters) {
    const records = t.namespaces.filter((n) => n.clusterId === c.id && !n.deletedAt)
    const services = t.services.filter((s) => s.clusterId === c.id && !s.deletedAt)
    const names = new Map<string, Namespace | undefined>(records.map((n) => [n.name, n]))
    // A workload's namespace exists even when no record of the namespace does (a hand-made model, an agent below the tier that reads them).
    for (const s of services) if (!names.has(s.namespace)) names.set(s.namespace, undefined)
    const agent = t.agents.find((a) => a.clusterId === c.id && a.status === 'approved')
    for (const [name, rec] of [...names].sort((a, b) => a[0].localeCompare(b[0]))) {
      const mine = services.filter((s) => s.namespace === name)
      const observed = rec ? rec.source === 'discovered' : c.source === 'discovered'
      rows.push({
        kind: 'namespace',
        key: `${c.id}|${name}`,
        clusterId: c.id,
        clusterName: c.name,
        name,
        services: mine.length,
        kinds: kindsLine(mine),
        exposed: mine.filter((s) => s.exposure && OUTSIDE.has(s.exposure)).length,
        mesh: namespaceMesh(c, rec, mine),
        observation: rec ? observation(rec) : undefined,
        source: rec?.source ?? 'services',
        scope: observed ? 'in' : 'declared',
      })
    }
    const sc = scopeOf(agent)
    if (sc.kind === 'narrowed' && sc.excluded > 0) {
      rows.push({ kind: 'excluded', key: `${c.id}|excluded`, clusterId: c.id, clusterName: c.name, count: sc.excluded, description: sc.description })
    }
  }
  return rows
}

/** Does the row match what was typed in the search box? Excluded rows follow their cluster: they match its name only. */
export function rowMatches(row: Row, q: string): boolean {
  const s = q.trim().toLowerCase()
  if (!s) return true
  const hay = row.kind === 'namespace' ? [row.name, row.clusterName] : [row.clusterName]
  return hay.some((h) => h.toLowerCase().includes(s))
}
