/** How the agent port (`server.address`, what every agent dials) is currently reached, as the server chart passes it
 *  down (`--agent-exposure`, from `agent.service.type` / `agent.tlsRoute.enabled`). Purely descriptive: the server
 *  never checks this against how the port is actually reachable, it only decides what Settings → Server address
 *  suggests. Empty on a chart from before this existed. */
export type ExposureKind = 'loadbalancer' | 'nodeport' | 'gateway' | 'clusterip' | ''

export const EXPOSURE_LABEL: Record<string, string> = {
  loadbalancer: 'Cloud LoadBalancer',
  nodeport: 'NodePort',
  gateway: 'Gateway API (TLSRoute, passthrough)',
  clusterip: 'Not exposed outside the cluster',
}

export const EXPOSURE_HELP: Record<string, string> = {
  loadbalancer: "The cloud provider assigns this address. If it's still a placeholder, or the load balancer got a new one, update it below.",
  nodeport: "Agents dial a node directly at this address. If it's still a placeholder, or that node changed, update it below.",
  gateway: 'A Gateway API TLSRoute passes the agent connection through untouched, TLS and all. Changing where it points means editing the TLSRoute itself (hostnames, parentRefs) - see deploy/README.md, "Exposing the server".',
  clusterip: 'Not reachable from outside the cluster yet. See deploy/README.md, "Exposing the server", for the three supported ways to change that.',
}

const UNKNOWN_HELP = 'This install predates the server reporting how it\'s exposed, so this server cannot say. See deploy/README.md, "Exposing the server", for the three supported ways.'

export function exposureLabel(kind: string): string {
  return EXPOSURE_LABEL[kind] ?? 'Not reported'
}

export function exposureHelp(kind: string): string {
  return EXPOSURE_HELP[kind] ?? UNKNOWN_HELP
}

/** Whether a one-line address swap is the right fix: true for LoadBalancer and NodePort, false for Gateway (a
 *  TLSRoute edit, not a single value) and for ClusterIP or unknown (nothing to swap the address of yet). */
export const exposureIsAddressSwap = (kind: string): boolean => kind === 'loadbalancer' || kind === 'nodeport'

/**
 * The exact command to point agents at a new address, once you have it. Uses this server's own release name and
 * namespace when it knows them (a chart from after --release-name/--release-namespace existed); placeholders
 * otherwise - the same `<chart>` convention the chart's own NOTES.txt already uses, since Helm itself has no way to
 * recall the chart reference an install used.
 */
export function serverAddressUpgradeCommand(info: { releaseName?: string; releaseNamespace?: string } | undefined, newAddress: string): string {
  const release = info?.releaseName || '<release>'
  const ns = info?.releaseNamespace || '<namespace>'
  return `helm upgrade ${release} <chart> -n ${ns} --reuse-values --set agent.publicAddress=${newAddress || '<address>'}`
}
