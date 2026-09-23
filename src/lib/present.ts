// Pure helpers that decide how model values are shown. Kept out of the components so they can be tested.
import type { Resources, ServiceVolume, Site } from './types'

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
