/**
 * FUSION: the continuum-fusion chart (Prometheus for metrics, Loki for logs, Tempo for traces) that a regional
 * operator can save what it receives into. The server derives each store's address from the release name and
 * namespace alone; this mirrors that derivation (backend/internal/server/fusion.go's fusionRoutes, itself pinned
 * to the chart's "fusion.name" helper) so the form can show, before anything is created, exactly where each
 * signal type will go.
 */

export const FUSION_DEFAULT_RELEASE = 'fusion'
export const FUSION_DEFAULT_NAMESPACE = 'continuum-system'

const MAX_RELEASE = 50
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

/** The prefix the chart gives each Service: the release name, with "-fusion" added unless it already says so. */
export function fusionName(release: string): string {
  let n = release.includes('fusion') ? release : `${release}-fusion`
  if (n.length > MAX_RELEASE) n = n.slice(0, MAX_RELEASE)
  return n.endsWith('-') ? n.slice(0, -1) : n
}

export interface FusionStore {
  signal: 'metrics' | 'logs' | 'traces'
  store: 'Prometheus' | 'Loki' | 'Tempo'
  /** Where the operator sends this signal type, as it will appear in the operator's install command. */
  endpoint: string
}

/** The three stores, in the order metrics, logs, traces, as the operator reaches them inside the cluster. */
export function fusionStores(release: string, namespace: string): FusionStore[] {
  const n = fusionName(release || FUSION_DEFAULT_RELEASE)
  const ns = namespace || FUSION_DEFAULT_NAMESPACE
  return [
    { signal: 'metrics', store: 'Prometheus', endpoint: `${n}-prometheus.${ns}.svc:9090/api/v1/otlp` },
    { signal: 'logs', store: 'Loki', endpoint: `${n}-loki.${ns}.svc:3100/otlp` },
    { signal: 'traces', store: 'Tempo', endpoint: `${n}-tempo.${ns}.svc:4317` },
  ]
}

/** What is wrong with a FUSION release name and namespace (both optional: empty means the default). */
export function fusionProblems(release: string, namespace: string): string[] {
  const out: string[] = []
  const rel = release.trim() || FUSION_DEFAULT_RELEASE
  const ns = namespace.trim() || FUSION_DEFAULT_NAMESPACE
  if (!DNS_LABEL.test(rel) || rel.length > MAX_RELEASE) out.push(`The FUSION release name must be lowercase letters, digits and '-', at most ${MAX_RELEASE} characters`)
  if (!DNS_LABEL.test(ns) || ns.length > 63) out.push("The FUSION namespace must be lowercase letters, digits and '-', at most 63 characters")
  return out
}
