// Pure helpers that decide how model values are shown. Kept out of the components so they can be tested.
import type { GeoUnlocatableReason, Resources, ServiceVolume, Site } from './types'

/* ---------- places ---------- */

/** "Greece" for "GR"; the code itself when the runtime does not know it. */
export function countryName(code?: string): string {
  const c = (code ?? '').trim().toUpperCase()
  if (!c) return ''
  try {
    const n = new Intl.DisplayNames(['en'], { type: 'region' }).of(c)
    return !n || /^unknown region$/i.test(n) ? c : n
  } catch {
    return c
  }
}

/* ---------- application groupings ---------- */

/** How a grouping's origin reads in a sentence: `Application.origin` and a `Suggestion`'s are the same vocabulary. */
export const ORIGIN_LABEL: Record<string, string> = {
  explicit: 'continuum.io/application label',
  argo: 'Argo CD app',
  helm: 'Helm release',
  'part-of': 'part-of label',
  namespace: 'namespace',
}

/** The words for a grouping origin, or the raw value itself for one this build does not yet name. */
export const originLabel = (origin: string): string => ORIGIN_LABEL[origin] ?? origin

/** "Athens, Greece"; just the country or just the city when that is all that is known. */
export function placeLabel(s?: Pick<Site, 'city' | 'country'>): string {
  if (!s) return ''
  return [s.city?.trim(), countryName(s.country)].filter(Boolean).join(', ')
}

/* ---------- kubernetes distributions and providers ---------- */

export type DistroKey = 'eks' | 'gke' | 'aks' | 'k3s' | 'rke2' | 'openshift' | 'microk8s' | 'talos' | 'k0s' | 'kind' | 'kubeadm' | 'kubernetes'

/** The common Kubernetes distributions, offered as a picklist the same way PROVIDER_OPTIONS is: value
 * is the compact form clusters already store (matching seed data's "EKS"/"k3s"/"kubeadm"), label spells
 * out the vendor for anyone picking from the list. Deliberately leaves out the generic "kubernetes"
 * DistroKey - it is what an unrecognised value already falls back to, not a choice of its own. */
export const DISTRIBUTION_OPTIONS: { value: string; label: string }[] = [
  { value: 'EKS', label: 'EKS (Amazon)' },
  { value: 'GKE', label: 'GKE (Google)' },
  { value: 'AKS', label: 'AKS (Azure)' },
  { value: 'k3s', label: 'k3s' },
  { value: 'RKE2', label: 'RKE2' },
  { value: 'OpenShift', label: 'OpenShift' },
  { value: 'MicroK8s', label: 'MicroK8s' },
  { value: 'Talos', label: 'Talos' },
  { value: 'k0s', label: 'k0s' },
  { value: 'kind', label: 'kind / minikube' },
  { value: 'kubeadm', label: 'kubeadm' },
]

/** Which known distribution a free-text value refers to. Anything unrecognised is plain Kubernetes. */
export function distroKey(distribution?: string): DistroKey {
  const s = (distribution ?? '').toLowerCase().replace(/[\s_-]+/g, '')
  if (!s) return 'kubernetes'
  if (/^eks|amazon|aws/.test(s)) return 'eks'
  if (/^gke|google/.test(s)) return 'gke'
  if (/^aks|azure/.test(s)) return 'aks'
  if (/k3s/.test(s)) return 'k3s'
  if (/rke2|rancher|rke/.test(s)) return 'rke2'
  if (/openshift|okd/.test(s)) return 'openshift'
  if (/microk8s/.test(s)) return 'microk8s'
  if (/talos/.test(s)) return 'talos'
  if (/k0s/.test(s)) return 'k0s'
  if (/^kind|minikube/.test(s)) return 'kind'
  if (/kubeadm/.test(s)) return 'kubeadm'
  return 'kubernetes'
}

export type ProviderKey =
  | 'aws' | 'azure' | 'gcp' | 'hetzner' | 'digitalocean' | 'ovh' | 'scaleway' | 'vultr' | 'alibaba' | 'openstack' | 'vmware' | 'proxmox' | 'on-prem' | 'edge' | 'unknown'

export function providerKey(provider?: string): ProviderKey {
  const s = (provider ?? '').toLowerCase().replace(/[\s_-]+/g, '')
  if (!s) return 'unknown'
  if (/^aws|amazon/.test(s)) return 'aws'
  if (/azure|microsoft/.test(s)) return 'azure'
  if (/^gcp|google/.test(s)) return 'gcp'
  if (/hetzner/.test(s)) return 'hetzner'
  if (/digitalocean/.test(s)) return 'digitalocean'
  if (/ovh/.test(s)) return 'ovh'
  if (/scaleway/.test(s)) return 'scaleway'
  if (/vultr/.test(s)) return 'vultr'
  if (/alibaba|aliyun/.test(s)) return 'alibaba'
  if (/openstack/.test(s)) return 'openstack'
  if (/vmware|vsphere/.test(s)) return 'vmware'
  if (/proxmox/.test(s)) return 'proxmox'
  if (/onprem|baremetal|selfhosted/.test(s)) return 'on-prem'
  if (/edge/.test(s)) return 'edge'
  return 'unknown'
}

/** The common providers, offered as a picklist (see ComboField) instead of a bare free-text field: the
 * value stored is the display label itself (matching what clusters already carry, e.g. seed data's
 * "AWS"/"On-prem"), and every one of these round-trips through providerKey() back to its own key. An
 * uncommon provider still works - it just falls through to the picker's free-text mode. Ordered roughly
 * by how often each shows up in a real fleet: the three big public clouds, then smaller/regional ones,
 * then self-hosted and on-prem infrastructure. */
export const PROVIDER_OPTIONS: { value: string; label: string }[] = [
  { value: 'AWS', label: 'AWS' },
  { value: 'Azure', label: 'Azure' },
  { value: 'Google Cloud', label: 'Google Cloud' },
  { value: 'DigitalOcean', label: 'DigitalOcean' },
  { value: 'Hetzner', label: 'Hetzner' },
  { value: 'OVHcloud', label: 'OVHcloud' },
  { value: 'Scaleway', label: 'Scaleway' },
  { value: 'Vultr', label: 'Vultr' },
  { value: 'Alibaba Cloud', label: 'Alibaba Cloud' },
  { value: 'OpenStack', label: 'OpenStack' },
  { value: 'VMware', label: 'VMware' },
  { value: 'Proxmox', label: 'Proxmox' },
  { value: 'On-prem', label: 'On-prem' },
  { value: 'Edge', label: 'Edge' },
]

/** Common node operating systems - see `MachineNode.os`. Same picklist-plus-custom pattern as
 * PROVIDER_OPTIONS: an appliance OS or something in-house still works via the picker's custom mode. */
export const OS_OPTIONS: { value: string; label: string }[] = [
  { value: 'Ubuntu 22.04', label: 'Ubuntu 22.04' },
  { value: 'Ubuntu 24.04', label: 'Ubuntu 24.04' },
  { value: 'Debian 12', label: 'Debian 12' },
  { value: 'RHEL 9', label: 'RHEL 9' },
  { value: 'Rocky Linux 9', label: 'Rocky Linux 9' },
  { value: 'Amazon Linux 2023', label: 'Amazon Linux 2023' },
  { value: 'Alpine', label: 'Alpine' },
  { value: 'Flatcar', label: 'Flatcar' },
  { value: 'Bottlerocket', label: 'Bottlerocket' },
  { value: 'Talos', label: 'Talos' },
  { value: 'Windows Server 2022', label: 'Windows Server 2022' },
]

/** Common Kubernetes CNI plugins, offered the same way PROVIDER_OPTIONS is: a picklist backed by a plain
 * free-text field, so an uncommon or in-house CNI still works via the picker's custom mode. */
export const CNI_OPTIONS: { value: string; label: string }[] = [
  { value: 'Cilium', label: 'Cilium' },
  { value: 'Calico', label: 'Calico' },
  { value: 'Flannel', label: 'Flannel' },
  { value: 'Weave Net', label: 'Weave Net' },
  { value: 'AWS VPC CNI', label: 'AWS VPC CNI' },
  { value: 'Azure CNI', label: 'Azure CNI' },
  { value: 'kube-router', label: 'kube-router' },
  { value: 'Antrea', label: 'Antrea' },
]

/** Common device-facing protocols (sensors, gateways, controllers) - see `Device.protocol`. Same
 * picklist-plus-custom pattern as PROVIDER_OPTIONS and CNI_OPTIONS. */
export const DEVICE_PROTOCOL_OPTIONS: { value: string; label: string }[] = [
  { value: 'MQTT', label: 'MQTT' },
  { value: 'OPC UA', label: 'OPC UA' },
  { value: 'Modbus', label: 'Modbus' },
  { value: 'CoAP', label: 'CoAP' },
  { value: 'BACnet', label: 'BACnet' },
  { value: 'AMQP', label: 'AMQP' },
  { value: 'RTSP', label: 'RTSP' },
  { value: 'HTTP', label: 'HTTP' },
]

/** Common protocols for a service-to-service or device-to-service dependency - see `Dependency.protocol`.
 * Same picklist-plus-custom pattern as PROVIDER_OPTIONS. */
export const DEPENDENCY_PROTOCOL_OPTIONS: { value: string; label: string }[] = [
  { value: 'HTTP', label: 'HTTP' },
  { value: 'HTTPS', label: 'HTTPS' },
  { value: 'gRPC', label: 'gRPC' },
  { value: 'MQTT', label: 'MQTT' },
  { value: 'Kafka', label: 'Kafka' },
  { value: 'AMQP', label: 'AMQP' },
  { value: 'WebSocket', label: 'WebSocket' },
  { value: 'TCP', label: 'TCP' },
  { value: 'UDP', label: 'UDP' },
]

/** "v1.29.6+k3s1" -> "v1.29.6": the build suffix only makes a column wider. Keep the full string for tooltips. */
export const shortVersion = (v?: string): string => (v ?? '').split('+')[0]

/* ---------- resources ---------- */

/** 16 -> "16 GB", 0.5 -> "512 MB", 1536 -> "1.5 TB". */
export function formatMemory(gb: number): string {
  if (!Number.isFinite(gb) || gb <= 0) return '0 GB'
  if (gb < 1) return `${Math.round(gb * 1024)} MB`
  if (gb >= 1024) return `${trim(gb / 1024)} TB`
  return `${trim(gb)} GB`
}
const trim = (n: number) => String(Math.round(n * 10) / 10)

/** Share of an interface's own rated speed that outbound eBPF-measured traffic is currently using on it -
 *  undefined whenever there is no rated speed to compare against, so "no data" is never shown as "0% used". */
export function linkUtilizationPct(bytesPerSec: number, speedMbps: number): number | undefined {
  if (!speedMbps || speedMbps <= 0) return undefined
  const capacityBytesPerSec = (speedMbps * 1_000_000) / 8
  return Math.round((bytesPerSec / capacityBytesPerSec) * 1000) / 10
}

/** 4 -> "4", 0.5 -> "0.5". */
export const formatCpu = (cores: number): string => trim(cores)

/** How much of what a node can give is already promised to pods, 0..100, or undefined when unknown. */
export function requestedPercent(requested?: Resources, allocatable?: Resources): { cpu?: number; memory?: number } {
  const pct = (used?: number, total?: number) => (used === undefined || !total || total <= 0 ? undefined : Math.min(100, Math.round((used / total) * 100)))
  return { cpu: pct(requested?.cpu, allocatable?.cpu), memory: pct(requested?.memoryGb, allocatable?.memoryGb) }
}

/** Colour band for a utilisation bar. */
export const loadBand = (pct: number): 'ok' | 'warn' | 'hot' => (pct >= 90 ? 'hot' : pct >= 70 ? 'warn' : 'ok')

/** "2× NVIDIA A100, 1× Google Coral" for a tooltip. */
export const accelSummary = (a?: { vendor: string; model: string; count: number }[]): string =>
  (a ?? []).map((x) => `${x.count}× ${[x.vendor, x.model].filter(Boolean).join(' ')}`).join(', ')

/* ---------- age ---------- */

/** "3 days", "5 months", "2 years" for an ISO timestamp; '' when unknown or in the future. */
export function ageLabel(iso?: string, now: number = Date.now()): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!Number.isFinite(t) || t > now + 60_000) return ''
  const s = Math.floor((now - t) / 1000)
  const unit = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`
  if (s < 3600) return s < 60 ? 'just now' : unit(Math.floor(s / 60), 'minute')
  if (s < 86400) return unit(Math.floor(s / 3600), 'hour')
  const d = Math.floor(s / 86400)
  if (d < 60) return unit(d, 'day')
  if (d < 730) return unit(Math.round(d / 30.44), 'month')
  return unit(Math.floor(d / 365.25), 'year')
}

/* ---------- pods ---------- */

/** "12 / 110" when both are known, "12" when only the count is, '' when pods are not read. */
export function podsLabel(count?: number, capacity?: number): string {
  if (count === undefined) return ''
  return capacity ? `${count} / ${capacity}` : String(count)
}

/** 0..100 of the kubelet's pod limit already used, or undefined when unknown. */
export const podsPercent = (count?: number, capacity?: number): number | undefined =>
  count === undefined || !capacity ? undefined : Math.min(100, Math.round((count / capacity) * 100))

/* ---------- storage and scaling ---------- */

/** 0.5 -> "512 MB", 10 -> "10 GB": volume sizes use the same units as memory. */
export const volumeSize = (gb: number): string => formatMemory(gb)

/** "10 GB on local-path" for a list line. */
export const volumeLabel = (v: ServiceVolume): string => `${v.name} · ${volumeSize(v.sizeGb)}${v.storageClass ? ` · ${v.storageClass}` : ''}`

/** Total requested storage of a service in GB. */
export const totalVolumeGb = (vs?: ServiceVolume[]): number => (vs ?? []).reduce((a, v) => a + v.sizeGb, 0)

/** "2–6 replicas" for an autoscaler. */
export const autoscalerRange = (a: { min: number; max: number }): string => (a.min === a.max ? `${a.min} replicas` : `${a.min}–${a.max} replicas`)

/** "at least 50%" / "at most 1 down" for a disruption budget. */
export function disruptionLabel(d: { minAvailable?: string; maxUnavailable?: string }): string {
  if (d.minAvailable) return `at least ${d.minAvailable} up`
  if (d.maxUnavailable) return `at most ${d.maxUnavailable} down`
  return ''
}

/* ---------- addresses ---------- */

export type IpScope = 'private' | 'public' | 'shared' | 'loopback' | 'link-local' | 'reserved' | 'unknown'

const IP_SCOPE_LABEL: Record<IpScope, string> = {
  private: 'Private', public: 'Public', shared: 'Shared (CGNAT)', loopback: 'Loopback', 'link-local': 'Link-local', reserved: 'Reserved', unknown: '',
}
export const ipScopeLabel = (s: IpScope): string => IP_SCOPE_LABEL[s]

/** What a scope means in plain words, for a tooltip. */
export const IP_SCOPE_HELP: Record<IpScope, string> = {
  private: 'Private range (RFC 1918 / unique-local): only reachable from inside the network.',
  public: 'Public address: routable on the internet, so anything listening there may be reachable from outside.',
  shared: 'Carrier-grade NAT range (100.64.0.0/10), typical of cellular and some ISPs: not reachable from the internet.',
  loopback: 'Loopback: this machine only.',
  'link-local': 'Link-local: only valid on one network segment.',
  reserved: 'Reserved or multicast range.',
  unknown: '',
}

/** Why the server could not even attempt a location for a connecting address - see GeoUnlocatableReason.
 * "cgnat" and "private" both mean the address is, by construction, behind some NAT (derived from the
 * address's own range, not a live probe): a router, load balancer or carrier NAT rewrote it somewhere
 * between the cluster and this server, which is exactly why enabling geoip.publicIpService (this server's
 * own public address, used as a fallback) is the documented way to get a location anyway. */
export const GEO_UNLOCATABLE_LABEL: Record<GeoUnlocatableReason, string> = {
  cgnat: 'Behind carrier-grade NAT',
  private: 'Behind NAT (private address)',
  loopback: 'Same machine as the server',
  'link-local': 'Link-local address',
  'link-local-multicast': 'Link-local multicast address',
  multicast: 'Multicast address',
  unspecified: 'Unspecified address',
}

export const GEO_UNLOCATABLE_HELP: Record<GeoUnlocatableReason, string> = {
  cgnat: 'This address is in the carrier-grade NAT range (100.64.0.0/10): very likely an ISP sharing one public address across many subscribers. No location can be derived from it alone.',
  private: 'This is a private address (RFC 1918 / unique-local). It only got here through some NAT or proxy along the way, so no location can be derived from it alone.',
  loopback: 'This is a loopback address: the server is talking to itself, or the two share a network stack.',
  'link-local': 'Link-local addresses are only valid on one network segment and never carry a location.',
  'link-local-multicast': 'A link-local multicast address never carries a location.',
  multicast: 'A multicast address never carries a location.',
  unspecified: 'The unspecified address (0.0.0.0 / ::) never carries a location.',
}

/**
 * Classify an IPv4 or IPv6 address as private, public and so on. A hostname (or anything that is not an
 * address) is "unknown": we do not resolve names here. A trailing ":port" on an IPv4 address is ignored.
 */
export function ipScope(input?: string): IpScope {
  let s = (input ?? '').trim().toLowerCase()
  if (!s) return 'unknown'
  if (/^\d+\.\d+\.\d+\.\d+:\d+$/.test(s)) s = s.slice(0, s.lastIndexOf(':'))
  const bracket = /^\[([0-9a-f:.]+)\](:\d+)?$/.exec(s)
  if (bracket) s = bracket[1]
  s = s.replace(/%.+$/, '') // zone id
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s)
  if (v4) {
    const o = v4.slice(1).map(Number)
    if (o.some((n) => n > 255)) return 'unknown'
    const [a, b] = o
    if (a === 127) return 'loopback'
    if (a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168)) return 'private'
    if (a === 100 && b >= 64 && b <= 127) return 'shared'
    if (a === 169 && b === 254) return 'link-local'
    if (a === 0 || a >= 224) return 'reserved'
    return 'public'
  }
  if (!s.includes(':') || !/^[0-9a-f:.]+$/.test(s)) return 'unknown'
  const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/.exec(s)
  if (mapped) return ipScope(mapped[1])
  if (s === '::1') return 'loopback'
  if (s === '::') return 'reserved'
  const first = parseInt(s.split(':')[0] || '0', 16)
  if (Number.isNaN(first)) return 'unknown'
  if ((first & 0xfe00) === 0xfc00) return 'private' // fc00::/7 unique local
  if ((first & 0xffc0) === 0xfe80) return 'link-local' // fe80::/10
  if ((first & 0xff00) === 0xff00) return 'reserved' // multicast
  if ((first & 0xe000) === 0x2000) return 'public' // 2000::/3 global unicast
  return 'reserved'
}

/**
 * True when `host` (an IPv4 address, optionally with a trailing ":port") falls inside `cidr` (e.g.
 * "10.43.0.0/16"). Used to tell a cluster's own API service address - a virtual ClusterIP from its
 * Service CIDR, not a real, externally reachable host - apart from a private address that just happens
 * to belong to a real machine on someone's LAN. IPv4 only: a discovered Service CIDR is effectively
 * always IPv4 for the clusters this app talks to.
 */
export function ipInCidr(host: string, cidr?: string): boolean {
  if (!cidr) return false
  const [base, bitsStr] = cidr.split('/')
  const bits = Number(bitsStr)
  if (!base || Number.isNaN(bits) || bits < 0 || bits > 32) return false
  const toInt = (ip: string): number | undefined => {
    const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ip.trim())
    if (!m) return undefined
    const o = m.slice(1).map(Number)
    if (o.some((n) => n > 255)) return undefined
    return ((o[0] << 24) | (o[1] << 16) | (o[2] << 8) | o[3]) >>> 0
  }
  const portMatch = /^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):\d+$/.exec(host.trim())
  const hostInt = toInt(portMatch ? portMatch[1] : host)
  const baseInt = toInt(base)
  if (hostInt === undefined || baseInt === undefined) return false
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0
  return (hostInt & mask) === (baseInt & mask)
}
