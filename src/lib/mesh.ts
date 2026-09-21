/**
 * What a service mesh does to a connection, worked out from configuration.
 *
 * Nothing here is measured. The agent reads which workloads have a mesh proxy, which ports they keep out of it,
 * and the mesh's mutual-TLS policy; this file combines those into one statement per connection. Every result
 * says so ("inferred from configuration"): a proxy that is present and a policy that says strict make it very
 * likely the traffic is encrypted, but only the wire could prove it.
 */
import type { Cluster, ClusterMesh, Dependency, Namespace, Service, ServiceMesh } from './types'

export type MeshState =
  /** Both ends are in the mesh and the policy requires (or, for ambient and Linkerd, always applies) mutual TLS. */
  | 'encrypted'
  /** Both ends are in the mesh but the policy accepts plaintext too. */
  | 'permissive'
  /** Only one end is in the mesh: that hop is outside it. */
  | 'partial'
  /** A port on this connection is kept out of the proxy, so the mesh does not see it. */
  | 'bypassed'
  /** The mesh policy switches mutual TLS off. */
  | 'plaintext'

export interface MeshVerdict {
  state: MeshState
  /** Two or three words for the line on the graph. */
  short: string
  /** A sentence for the inspector. */
  detail: string
}

export const MESH_NAME: Record<string, string> = { istio: 'Istio', linkerd: 'Linkerd', consul: 'Consul', kuma: 'Kuma' }
export const meshName = (k?: string) => (k ? (MESH_NAME[k] ?? k) : '')

export const MTLS_WORDS: Record<string, string> = {
  strict: 'strict: plaintext is refused',
  permissive: 'permissive: plaintext is accepted',
  disabled: 'disabled',
  automatic: 'automatic: always on between meshed workloads',
  unknown: 'not known',
}

/** A service is "in the mesh" when a proxy handles its traffic (seen in its pods, or asked for) and it does not bypass it. */
export const inMesh = (m?: ServiceMesh) => !!m && !m.controlPlane && !!m.proxy && !m.bypass

/** "sidecar" and "ambient" in words for a chip. */
export function proxyWords(m: ServiceMesh): string {
  if (m.controlPlane) return `${meshName(m.mesh)} control plane`
  if (m.bypass) return `${meshName(m.mesh)} · bypassed`
  if (!m.proxy) return `${meshName(m.mesh)} · not injected`
  return `${meshName(m.mesh)} · ${m.proxy}`
}

/** A port list entry like "out:5432" or "in:8000-8100" against one port. */
function listed(entries: string[] | undefined, dir: 'in' | 'out', port: number): boolean {
  for (const e of entries ?? []) {
    const [d, spec] = e.split(':')
    if (d !== dir || !spec) continue
    const [a, b] = spec.split('-').map(Number)
    if (Number.isNaN(a)) continue
    if (b === undefined ? a === port : Number.isNaN(b) ? false : port >= a && port <= b) return true
  }
  return false
}

/** Mutual TLS mode that applies to connections into a namespace: its own if it has one, else the mesh-wide mode. */
export function mtlsFor(mesh: ClusterMesh, namespace: string, ns?: Namespace): string {
  return ns?.mtls ?? mesh.namespaceMtls?.[namespace] ?? mesh.mtls
}

/**
 * The verdict on one connection between two services, or undefined when the mesh has nothing to say about it
 * (neither end is in it, or the ends are in different clusters, where this model does not follow the mesh across).
 */
export function connectionVerdict(
  dep: Pick<Dependency, 'port'>,
  from: Service | undefined,
  to: Service | undefined,
  cluster: Cluster | undefined,
  namespaces: Namespace[],
): MeshVerdict | undefined {
  const mesh = cluster?.mesh
  if (!mesh || !from || !to || from.clusterId !== to.clusterId) return undefined
  if (from.mesh?.controlPlane || to.mesh?.controlPlane) return undefined
  const a = inMesh(from.mesh)
  const b = inMesh(to.mesh)
  if (!a && !b) return undefined
  const kind = meshName(mesh.kind)
  if (a !== b) {
    const outside = a ? to : from
    return {
      state: 'partial',
      short: 'one end outside mesh',
      detail: `Only one end is in ${kind}: ${outside.name} has no mesh proxy, so this hop is likely not covered by the mesh's mutual TLS.`,
    }
  }
  if (dep.port !== undefined) {
    if (listed(from.mesh?.excludedPorts, 'out', dep.port)) {
      return { state: 'bypassed', short: 'bypasses proxy', detail: `${from.name} keeps outbound port ${dep.port} out of its proxy, so the mesh does not handle this connection.` }
    }
    if (listed(to.mesh?.excludedPorts, 'in', dep.port)) {
      return { state: 'bypassed', short: 'bypasses proxy', detail: `${to.name} keeps inbound port ${dep.port} out of its proxy, so the mesh does not handle this connection.` }
    }
  }
  const ns = namespaces.find((n) => n.clusterId === to.clusterId && n.name === to.namespace)
  const mode = mtlsFor(mesh, to.namespace, ns)
  const ambient = from.mesh?.proxy === 'ambient' && to.mesh?.proxy === 'ambient'
  if (mode === 'disabled') {
    return { state: 'plaintext', short: 'mTLS off', detail: `Both ends are in ${kind}, but its policy switches mutual TLS off.` }
  }
  if (mode === 'strict' || mode === 'automatic' || (ambient && mode !== 'unknown')) {
    const why = mode === 'strict' ? 'the policy is strict' : ambient ? 'ambient mesh always secures traffic between its workloads through ztunnel' : `${kind} turns mutual TLS on automatically`
    return { state: 'encrypted', short: 'mTLS', detail: `Both ends are in ${kind} and ${why}.` }
  }
  return {
    state: 'permissive',
    short: mode === 'unknown' ? 'mTLS unknown' : 'mTLS permissive',
    detail:
      mode === 'unknown'
        ? `Both ends are in ${kind}, but the agent could not read its policy, so it is not known whether plaintext is accepted.`
        : `Both ends are in ${kind}. The policy is permissive: proxies encrypt between themselves, but plaintext would also be accepted.`,
  }
}

export const VERDICT_COLOR: Record<MeshState, string> = {
  encrypted: '#34d399',
  permissive: '#fbbf24',
  partial: '#fbbf24',
  bypassed: '#f87171',
  plaintext: '#f87171',
}

/** True when any cluster in the list has a mesh. */
export const anyMesh = (clusters: Cluster[]) => clusters.some((c) => !!c.mesh && !c.deletedAt)

/** One line for a cluster: "Istio 1.22 · sidecar · mTLS permissive". */
export function clusterMeshLine(m: ClusterMesh): string {
  return [meshName(m.kind) + (m.version ? ` ${m.version}` : ''), m.mode, m.kind === 'linkerd' ? 'mTLS automatic' : m.policyRead || m.mtls !== 'permissive' ? `mTLS ${m.mtls}` : 'mTLS not read'].filter(Boolean).join(' · ')
}
