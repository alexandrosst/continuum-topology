import { Pencil, X } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import EntityHistory from '@/components/EntityHistory'
import { EvidenceSection, WeakValues } from '@/components/EvidenceSection'
import MobilityPanel from '@/components/MobilityPanel'
import PlacementHint from '@/components/PlacementHint'
import ServiceAdvice from '@/components/placement/ServiceAdvice'
import { DistroIcon, Place, ProviderIcon, WithIcon } from '@/components/ui/brand'
import { Button, CompletenessBadge, Input, IpAddress, ObservationChip, Pill, Select, SourceBadge, StatusDot, TierBadge } from '@/components/ui/primitives'
import { completeness } from '@/lib/completeness'
import { observation } from '@/lib/provenance'
import { hasOverrides } from '@/lib/effective'
import { exitIps } from '@/lib/geo'
import { ageLabel, autoscalerRange, disruptionLabel, podsLabel, podsPercent, volumeSize } from '@/lib/present'
import { usePlacementSuggestions } from '@/lib/usePlacement'
import { useHistoryView } from '@/store/history'
import { useRawTopology, useTopology } from '@/store/topology'
import { bytesPerSec, isObserved, trafficSummary } from '@/lib/observed'
import { lossBand, pathQuality, rttLabel } from '@/lib/metrics'
import { usePaths } from '@/store/topology'
import { connectionVerdict, meshName, MTLS_WORDS, proxyWords, VERDICT_COLOR } from '@/lib/mesh'
import { CONNECTIVITY, DEVICE_KINDS, TIERS, type Agent, type Dependency, type Evidence, type ExternalEndpoint, type ExternalKind, type Provenance, type Resources, type Tier } from '@/lib/types'

export type Selection = { kind: 'cluster' | 'tier' | 'node' | 'service' | 'device' | 'site' | 'external' | 'dependency'; id: string } | null

function Row({ label, children, wrap }: { label: string; children: ReactNode; wrap?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1.5 text-sm">
      <span className="shrink-0 text-nb-500">{label}</span>
      <span className={wrap ? 'min-w-0 break-words text-right text-nb-300' : 'min-w-0 truncate text-right text-nb-300'}>{children}</span>
    </div>
  )
}

/** A row that only renders when there is something to show. */
function Maybe({ label, children }: { label: string; children: ReactNode }) {
  if (children === undefined || children === null || children === false || children === '') return null
  return <Row label={label}>{children}</Row>
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="border-t border-nb-850 px-5 py-4">
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">{title}</div>
      {children}
    </div>
  )
}

function LinkRow({ label, sub, onClick, stacked }: { label: string; sub?: string; onClick?: () => void; stacked?: boolean }) {
  // `stacked` puts a long sub-label (protocol, sources, rates) under the name instead of squeezing it.
  return (
    <button onClick={onClick} disabled={!onClick} className={'flex w-full rounded-md px-2 py-1.5 text-left text-sm text-nb-300 hover:bg-nb-940 disabled:hover:bg-transparent ' + (stacked ? 'flex-col gap-0.5' : 'items-center justify-between')}>
      <span className="w-full truncate">{label}</span>
      {sub && <span className={stacked ? 'w-full truncate text-xs text-nb-500' : 'ml-3 shrink-0 text-xs text-nb-500'}>{sub}</span>}
    </button>
  )
}

/** Why discovery believes a value. Shown so a guess never looks like a fact. */
function Why({ ev }: { ev?: Evidence }) {
  if (!ev) return null
  return (
    <Row label="Detected via" wrap>
      <span title={ev.detail}>
        {ev.signal} <span className={ev.confidence === 'high' ? 'text-emerald-400' : ev.confidence === 'medium' ? 'text-amber-300' : 'text-red-300'}>({ev.confidence})</span>
      </span>
    </Row>
  )
}

const ago = (iso?: string) => {
  if (!iso) return undefined
  const m = Math.round((Date.now() - new Date(iso).getTime()) / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m} min ago`
  if (m < 60 * 48) return `${Math.round(m / 60)} h ago`
  return new Date(iso).toLocaleDateString()
}

function Origin({ e }: { e: Provenance }) {
  return (
    <>
      <Row label="Origin">
        {e.source === 'manual' ? 'Entered manually' : e.source === 'discovered' ? 'Detected' : 'Imported'}
        <SourceBadge source="manual" overridden={hasOverrides(e)} />
        {observation(e) ? <ObservationChip className="ml-2" info={observation(e)} /> : e.stale && <span className="ml-2 rounded border border-amber-400/30 bg-amber-400/10 px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-amber-300">Stale</span>}
      </Row>
      <Maybe label="Last seen">{ago(e.lastSeen)}</Maybe>
    </>
  )
}

/**
 * Whether the cluster's traffic observer is running, on how many nodes and how. A quiet graph and a blind
 * one look the same on the canvas, so the inspector says which it is.
 */
function ObserverRow({ agent, nodeNames }: { agent: Agent; nodeNames: string[] }) {
  if (agent.status !== 'approved') return null
  if (agent.accessTier < 2) {
    return <Row label="Traffic" wrap><span className="text-nb-500">Needs access tier 2</span></Row>
  }
  const o = agent.observer
  if (!o) {
    return (
      <Row label="Traffic" wrap>
        <span className="text-nb-500" title="Turn it on with the install command's “traffic observer” option, or: helm upgrade … --reuse-values --set flowObserver.enabled=true">
          Observer not installed
        </span>
      </Row>
    )
  }
  const by = (m: string) => o.collectors.filter((c) => c.method === m)
  const watched = new Set(o.collectors.map((c) => c.node))
  const missing = nodeNames.filter((n) => !watched.has(n))
  const parts = [
    by('ebpf').length ? `eBPF on ${by('ebpf').length}` : '',
    by('conntrack').length ? `conntrack on ${by('conntrack').length}` : '',
  ].filter(Boolean)
  return (
    <>
      <Row label="Traffic" wrap>
        {o.collectors.length === 0 ? (
          <span className="text-amber-300">No collector reporting (last heard {ago(o.lastReport)})</span>
        ) : (
          <span title={o.collectors.map((c) => `${c.node}: ${c.method}${c.bytesKnown ? '' : ', bytes not measured'}`).join('\n')}>
            {parts.join(', ')} · {watched.size} of {nodeNames.length || watched.size} nodes
          </span>
        )}
      </Row>
      {missing.length > 0 && o.collectors.length > 0 && (
        <Row label="Not observed" wrap><span className="text-amber-300" title="No collector has reported from these nodes, so traffic to or from their pods is missing.">{missing.slice(0, 3).join(', ')}{missing.length > 3 ? ` +${missing.length - 3}` : ''}</span></Row>
      )}
      {o.lost > 0 && (
        <Row label="Dropped" wrap><span className="text-amber-300" title="Connections a collector could not attribute or count, for example ones that were already open when it started.">{o.lost.toLocaleString()} observations</span></Row>
      )}
    </>
  )
}

/** Name an address that was only ever seen in traffic ("93.184.216.34" → "Payments API"). It becomes a stored record. */
function NameEndpoint({ endpoint }: { endpoint: ExternalEndpoint }) {
  const save = useRawTopology((s) => s.upsertExternalEndpoint)
  const [name, setName] = useState(endpoint.name ?? '')
  const [kind, setKind] = useState<ExternalKind>(endpoint.kind)
  const changed = name.trim() !== (endpoint.name ?? '') || kind !== endpoint.kind
  return (
    <div className="border-t border-nb-850 px-5 py-4">
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">What is it?</div>
      <div className="space-y-2">
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name it, e.g. Payments API" aria-label="Name" maxLength={80} />
        <Select value={kind} onChange={(e) => setKind(e.target.value as ExternalKind)} aria-label="Kind">
          <option value="unknown">Unknown</option>
          <option value="saas">SaaS / public API</option>
          <option value="database">Database</option>
        </Select>
        <Button size="sm" variant="primary" disabled={!changed} onClick={() => save({ ...endpoint, source: 'manual', name: name.trim() || undefined, kind })}>
          Save
        </Button>
      </div>
      <p className="mt-2 text-xs text-nb-500">Only the address is known from traffic. Naming it keeps the name when the address is seen again.</p>
    </div>
  )
}

const res = (r?: Resources) => (r ? `${r.cpu} vCPU · ${r.memoryGb} GB` : undefined)
const kv = (o?: Record<string, string>) => (o && Object.keys(o).length ? Object.entries(o).map(([k, v]) => `${k}=${v}`).join(', ') : undefined)
const list = (a?: string[]) => (a && a.length ? a.join(', ') : undefined)
const connLabel = (c?: string) => CONNECTIVITY.find((x) => x.value === c)?.label

export default function Inspector({
  selection,
  onSelect,
  onEdit,
  onClose,
}: {
  selection: Selection
  onSelect: (s: Selection) => void
  onEdit: (s: NonNullable<Selection>) => void
  onClose: () => void
}) {
  const { clusters, nodes, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints, agents } = useTopology()
  const placement = usePlacementSuggestions().byCluster
  const inPast = useHistoryView((s) => s.at !== null)
  const measured = usePaths()
  if (!selection) return null

  const clusterName = (id: string) => clusters.find((c) => c.id === id)?.name ?? '—'
  const appName = (id?: string) => applications.find((a) => a.id === id)?.name
  const siteName = (id?: string) => sites.find((s) => s.id === id)?.name

  /** Resolve either end of a dependency, whatever kind of thing it is. */
  const end = (kind: Dependency['toKind'], id: string): { label: string; sel: NonNullable<Selection> } | null => {
    if (kind === 'service') {
      const w = services.find((x) => x.id === id)
      return w ? { label: `${w.name} · ${clusterName(w.clusterId)}`, sel: { kind: 'service', id } } : null
    }
    if (kind === 'device') {
      const d = devices.find((x) => x.id === id)
      return d ? { label: `${d.name}${d.count > 1 ? ` ×${d.count}` : ''}`, sel: { kind: 'device', id } } : null
    }
    const e = externalEndpoints.find((x) => x.id === id)
    return e ? { label: e.name ?? e.host, sel: { kind: 'external', id } } : null
  }
  const depSub = (d: Dependency) => {
    const s = d.stats
    const seen = isObserved(d)
    const others = d.sources.filter((x) => x !== 'observed')
    const how = d.via === 'ebpf' ? 'eBPF' : d.via === 'conntrack' ? 'conntrack' : ''
    const bits = [
      d.port ? `${d.protocol}:${d.port}` : d.protocol,
      seen ? `seen${how ? ` (${how})` : ''}${others.length ? ` + ${others.join('+')}` : ''}` : d.sources.join('+'),
      d.confidence === 'high' ? undefined : `${d.confidence} confidence`,
      seen && !d.stale ? trafficSummary(d) : undefined,
      seen && d.stale ? `no traffic since ${ago(d.lastSeen)}` : undefined,
      seen && !d.stale && d.via === 'conntrack' && !d.stats?.bytesPerSec ? 'bytes not measured' : undefined,
      d.crossCluster ? 'across clusters' : undefined,
      d.noise ? (d.noise === 'dns' ? 'DNS' : 'system') : undefined,
      s?.reqPerSec !== undefined ? `${s.reqPerSec}/s` : undefined,
      s?.p95Ms !== undefined ? `p95 ${s.p95Ms} ms` : undefined,
    ]
    return bits.filter(Boolean).join(' · ')
  }
  /** Busiest first, machinery (DNS, system) last: the applications' own dependencies are what people look for. */
  const busiest = (a: Dependency, b: Dependency) =>
    Number(!!a.noise) - Number(!!b.noise) || (b.stats?.bytesPerSec ?? 0) - (a.stats?.bytesPerSec ?? 0) || (b.stats?.connectionsPerMin ?? 0) - (a.stats?.connectionsPerMin ?? 0)
  const depSections = (id: string) => {
    const calls = dependencies.filter((d) => d.from === id).sort(busiest)
    const calledBy = dependencies.filter((d) => d.to === id).sort(busiest)
    return { calls, calledBy }
  }

  let title = ''
  let subtitle: ReactNode = null
  let body: ReactNode = null
  let editable = true

  if (selection.kind === 'cluster') {
    const c = clusters.find((x) => x.id === selection.id)
    if (!c) return null
    const ns = nodes.filter((n) => n.clusterId === c.id)
    const ws = services.filter((w) => w.clusterId === c.id)
    const spaces = namespaces.filter((n) => n.clusterId === c.id)
    const agent = agents.find((a) => a.clusterId === c.id)
    const overlap = c.podCidr ? clusters.filter((o) => o.id !== c.id && o.podCidr === c.podCidr) : []
    title = c.name
    subtitle = <TierBadge tier={c.tier} />
    body = (
      <>
        <div className="px-5 py-3">
          <Row label="Status"><StatusDot status={c.status} withLabel /></Row>
          <Row label="Distribution"><WithIcon icon={<DistroIcon distribution={c.distribution} size={16} />}>{c.distribution} {c.version}</WithIcon></Row>
          <Why ev={c.evidence?.distribution} />
          <Row label="Provider">{c.provider ? <WithIcon icon={<ProviderIcon provider={c.provider} size={16} />}>{c.provider}</WithIcon> : '—'}</Row>
          <Row label="Location"><Place site={sites.find((x) => x.id === c.siteId)} fallback={c.region} /></Row>
          {!sites.some((x) => x.id === c.siteId) && <PlacementHint suggestion={placement.get(c.id)} />}
          <Maybe label="Site">{siteName(c.siteId)}</Maybe>
          <Maybe label="Region label">{c.region}</Maybe>
          <Maybe label="CNI · Ingress">{[c.cni, c.ingress].filter(Boolean).join(' · ')}</Maybe>
          <Maybe label="Pod CIDR"><span className="font-mono text-xs">{c.podCidr}</span></Maybe>
          {overlap.length > 0 && (
            <Row label="CIDR overlap"><span className="text-amber-300" title="Overlapping pod CIDRs can break direct cross-cluster routing.">{overlap.map((o) => o.name).join(', ')}</span></Row>
          )}
          <Maybe label="Service CIDR"><span className="font-mono text-xs">{c.serviceCidr}</span></Maybe>
          <Maybe label="Storage">{list(c.storageClasses)}</Maybe>
          {c.apiEndpoint && <Row label="API endpoint"><IpAddress ip={c.apiEndpoint} inline /></Row>}
          {c.egressIp && <Row label="Exit IP"><IpAddress ip={c.egressIp} inline /></Row>}
          {agent?.connectingGeo && (
            <Row label="GeoIP says">
              <span title="Where a GeoIP database places the address this cluster connects from. A hint only: a VPN, a mobile network or a cloud provider's egress can put it far from the cluster.">
                {[agent.connectingGeo.city, agent.connectingGeo.countryName || agent.connectingGeo.country].filter(Boolean).join(', ')}
                {agent.connectingGeo.accuracyKm ? <span className="text-nb-500"> ±{agent.connectingGeo.accuracyKm} km</span> : null}
              </span>
            </Row>
          )}
          <Maybe label="Age">{c.createdAt ? `${ageLabel(c.createdAt)} (${new Date(c.createdAt).toLocaleDateString()})` : undefined}</Maybe>
          <Maybe label="Trust zone · residency">{[c.trustZone, c.dataResidency].filter(Boolean).join(' · ')}</Maybe>
          <Row label="Discovered"><CompletenessBadge c={completeness(c, nodes, services, dependencies)} /></Row>
          {agent && (
            <Row label="Agent">
              <Link to="/discovery" className="text-accent hover:underline">{agent.name}</Link> <span className="text-nb-500">tier {agent.accessTier}</span>
            </Row>
          )}
          {agent && <ObserverRow agent={agent} nodeNames={ns.map((n) => n.name)} />}
          {agent?.scope && (
            <Row label="Namespaces read" wrap>
              <span title={agent.scope.description}>{agent.scope.inScope} of {agent.scope.namespaces}</span>
            </Row>
          )}
          <Origin e={c} />
          <WeakValues kind="cluster" rec={c} />
        </div>
        {c.mesh && (
          <Section title="Service mesh">
            <Row label="Mesh">{meshName(c.mesh.kind)}{c.mesh.version ? ` ${c.mesh.version}` : ''}</Row>
            <Row label="Mode">{c.mesh.mode}</Row>
            <Row label="Mutual TLS" wrap>{c.mesh.policyRead || c.mesh.mtls !== 'permissive' ? (MTLS_WORDS[c.mesh.mtls] ?? c.mesh.mtls) : 'policy not read'}</Row>
            {Object.entries(c.mesh.namespaceMtls ?? {}).map(([n, m]) => (
              <Row key={n} label={`Namespace ${n}`}>{m}</Row>
            ))}
            {c.mesh.controlPlane.length > 0 && (
              <div className="mt-1">
                {c.mesh.controlPlane.map((id) => {
                  const cp = services.find((x) => x.id === id)
                  return cp ? <LinkRow key={id} label={cp.name} sub="control plane" onClick={() => onSelect({ kind: 'service', id })} /> : null
                })}
              </div>
            )}
            <p className="mt-2 text-xs text-nb-500">
              Read from labels, annotations, container names and the mesh's policy objects; it says what the mesh is configured to do, not what the traffic did.
              {c.mesh.policyNote ? ` ${c.mesh.policyNote}` : ''}
            </p>
          </Section>
        )}
        <Section title={`Nodes (${ns.length})`}>
          {ns.map((n) => <LinkRow key={n.id} label={n.name} sub={n.role === 'control-plane' ? 'control plane' : ''} onClick={() => onSelect({ kind: 'node', id: n.id })} />)}
          {ns.length === 0 && <p className="text-sm text-nb-500">{c.source === 'discovered' ? 'No node has been reported for this cluster (the agent may read below the Infrastructure level).' : 'No nodes declared.'}</p>}
        </Section>
        <Section title={`Services (${ws.length})`}>
          {ws.map((w) => <LinkRow key={w.id} label={w.name} sub={w.namespace} onClick={() => onSelect({ kind: 'service', id: w.id })} />)}
          {ws.length === 0 && <p className="text-sm text-nb-500">{c.source === 'discovered' ? 'No service has been reported for this cluster (the agent may read below the Services level).' : 'No services declared.'}</p>}
        </Section>
        {spaces.length > 0 && (
          <Section title={`Namespaces (${spaces.length})`}>
            {spaces.map((n) => <LinkRow key={n.id} label={n.name} sub={appName(n.applicationId)} />)}
          </Section>
        )}
      </>
    )
  } else if (selection.kind === 'tier') {
    const tier = selection.id as Tier
    const cs = clusters.filter((c) => c.tier === tier)
    title = TIERS.find((t) => t.value === tier)?.label ?? tier
    subtitle = <TierBadge tier={tier} />
    editable = false
    body = (
      <Section title={`Clusters (${cs.length})`}>
        {cs.map((c) => <LinkRow key={c.id} label={c.name} sub={c.region} onClick={() => onSelect({ kind: 'cluster', id: c.id })} />)}
      </Section>
    )
  } else if (selection.kind === 'node') {
    const n = nodes.find((x) => x.id === selection.id)
    if (!n) return null
    const ws = services.filter((w) => w.nodeIds.includes(n.id))
    const attached = devices.filter((d) => d.gatewayNodeId === n.id)
    title = n.name
    subtitle = <Pill>{n.role === 'control-plane' ? 'Control plane' : 'Worker'}</Pill>
    body = (
      <>
        <div className="px-5 py-3">
          <Row label="Status"><StatusDot status={n.status} withLabel /></Row>
          <Row label="Cluster">
            <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'cluster', id: n.clusterId })}>{clusterName(n.clusterId)}</button>
          </Row>
          <Row label="Type">{n.kind === 'vm' ? 'VM' : n.kind === 'bare-metal' ? 'Bare metal' : 'Edge device'}</Row>
          <Why ev={n.evidence?.kind} />
          {!n.probed && n.source === 'discovered' && !n.overrides?.kind && n.evidence?.kind?.confidence === 'low' && (
            <Row label="Not sure" wrap>
              <span className="text-nb-400">This type is a guess from the Kubernetes API. Turn on the node probe when connecting the cluster to have the machine tell for itself, or set it here.</span>
            </Row>
          )}
          <Maybe label="Virtualization">{n.virtualization}</Maybe>
          <Why ev={n.evidence?.virtualization} />
          <Maybe label="Hardware">{n.hardwareModel}</Maybe>
          {n.probed && <Why ev={n.evidence?.hardwareModel} />}
          <Maybe label="Instance">{[n.instanceType, n.zone].filter(Boolean).join(' · ')}</Maybe>
          <Row label="IP"><IpAddress ip={n.ip} inline /></Row>
          <Maybe label="Age">{n.createdAt ? `${ageLabel(n.createdAt)} (${new Date(n.createdAt).toLocaleDateString()})` : undefined}</Maybe>
          <Row label="OS">{n.os}</Row>
          <Maybe label="Architecture">{n.arch}</Maybe>
          <Maybe label="Kernel · runtime">{[n.kernel, n.runtime].filter(Boolean).join(' · ')}</Maybe>
          <Row label="Capacity">{n.cpu} vCPU · {n.memoryGb} GB</Row>
          <Maybe label="Allocatable">{res(n.allocatable)}</Maybe>
          <Maybe label="Requested">{res(n.requested)}</Maybe>
          <Maybe label="Pods">
            {n.podCount !== undefined ? (
              <span className={(podsPercent(n.podCount, n.podCapacity) ?? 0) >= 90 ? 'text-red-300' : undefined}>
                {podsLabel(n.podCount, n.podCapacity)}{n.podCapacity ? ` (max ${n.podCapacity})` : ''}
              </span>
            ) : n.podCapacity ? `max ${n.podCapacity}` : undefined}
          </Maybe>
          <Maybe label="Accelerators">{n.accelerators?.map((a) => `${a.count}× ${a.vendor} ${a.model}`).join(', ')}</Maybe>
          <Maybe label="Uplink">{connLabel(n.connectivity)}</Maybe>
          {n.connectivity && <Why ev={n.evidence?.connectivity} />}
          {n.hasBattery && <Row label="Power">Has a battery: can run without mains power</Row>}
          <Maybe label="Taints">{list(n.taints)}</Maybe>
          {n.conditions && n.conditions.length > 0 && <Row label="Conditions"><span className="text-amber-300">{n.conditions.join(', ')}</span></Row>}
          <Origin e={n} />
          <WeakValues kind="node" rec={n} />
        </div>
        <Section title={`Services on this node (${ws.length})`}>
          {ws.map((w) => <LinkRow key={w.id} label={w.name} sub={w.namespace} onClick={() => onSelect({ kind: 'service', id: w.id })} />)}
          {ws.length === 0 && <p className="text-sm text-nb-500">Nothing scheduled here.</p>}
        </Section>
        {attached.length > 0 && (
          <Section title={`Devices attached (${attached.length})`}>
            {attached.map((d) => <LinkRow key={d.id} label={d.name} sub={d.count > 1 ? `×${d.count}` : ''} onClick={() => onSelect({ kind: 'device', id: d.id })} />)}
          </Section>
        )}
      </>
    )
  } else if (selection.kind === 'service') {
    const w = services.find((x) => x.id === selection.id)
    if (!w) return null
    const { calls, calledBy } = depSections(w.id)
    const ready = w.readyReplicas !== undefined ? `${w.readyReplicas} / ${w.replicas} ready` : String(w.replicas)
    const rq = [w.cpuRequestM !== undefined ? `${w.cpuRequestM}m` : '', w.memRequestMi !== undefined ? `${w.memRequestMi} Mi` : ''].filter(Boolean).join(' · ')
    const lim = [w.cpuLimitM !== undefined ? `${w.cpuLimitM}m` : '', w.memLimitMi !== undefined ? `${w.memLimitMi} Mi` : ''].filter(Boolean).join(' · ')
    title = w.name
    subtitle = <Pill>{w.kind}</Pill>
    body = (
      <>
        <div className="px-5 py-3">
          <Row label="Status"><StatusDot status={w.status} withLabel /></Row>
          <Row label="Cluster">
            <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'cluster', id: w.clusterId })}>{clusterName(w.clusterId)}</button>
          </Row>
          <Row label="Application">{appName(w.applicationId) ?? '—'}</Row>
          <Row label="Namespace">{w.namespace}</Row>
          <Row label="Image"><span className="font-mono text-xs">{w.image || '—'}</span></Row>
          <Maybe label="Digest"><span className="font-mono text-xs">{w.imageDigest?.slice(0, 19)}</span></Maybe>
          <Row label="Replicas">{ready}</Row>
          <Maybe label="Restarts">{w.restarts ? String(w.restarts) : undefined}</Maybe>
          <Maybe label="Requests">{rq}</Maybe>
          <Maybe label="Limits">{lim}</Maybe>
          <Maybe label="Exposure">{[w.exposure, list(w.hosts)].filter(Boolean).join(' · ')}</Maybe>
          <Maybe label="Ports">{w.ports?.join(', ')}</Maybe>
          <Maybe label="Managed by">{w.managedBy}</Maybe>
          <Maybe label="Node selector">{kv(w.nodeSelector)}</Maybe>
          <Maybe label="Tolerations">{list(w.tolerations)}</Maybe>
          <Maybe label="Sensitivity">{w.sensitivity}</Maybe>
          <Maybe label="Age">{w.createdAt ? `${ageLabel(w.createdAt)} (${new Date(w.createdAt).toLocaleDateString()})` : undefined}</Maybe>
          {w.autoscaler && (
            <Row label="Autoscaling" wrap>
              {autoscalerRange(w.autoscaler)}, now {w.autoscaler.current}
              {w.autoscaler.targets && w.autoscaler.targets.length > 0 && <span className="text-nb-500"> · {w.autoscaler.targets.join(', ')}</span>}
            </Row>
          )}
          {w.disruption && (
            <Row label="Disruption budget" wrap>
              {disruptionLabel(w.disruption)} <span className="text-nb-500">· {w.disruption.allowed} may be evicted now</span>
            </Row>
          )}
          <Origin e={w} />
          <WeakValues kind="service" rec={w} />
        </div>
        {w.mesh && (
          <Section title="Service mesh">
            <Row label="Mesh">{meshName(w.mesh.mesh)}</Row>
            <Row label="Proxy" wrap>{proxyWords(w.mesh).replace(`${meshName(w.mesh.mesh)} · `, '')}</Row>
            {w.mesh.excludedPorts && w.mesh.excludedPorts.length > 0 && (
              <Row label="Kept out of proxy" wrap><span className="font-mono text-xs">{w.mesh.excludedPorts.join(', ')}</span></Row>
            )}
            <p className="mt-2 text-xs text-nb-500">
              {w.mesh.controlPlane
                ? 'This is part of the mesh itself, not one of your applications.'
                : w.mesh.source === 'pods'
                  ? 'A proxy container was seen in its pods.'
                  : w.mesh.source === 'namespace'
                    ? 'Only what its namespace asks for. No proxy container was seen, so it may not be injected yet.'
                    : 'What its workload asks for.'}{' '}
              Inferred from configuration.
            </p>
          </Section>
        )}
        {w.volumes && w.volumes.length > 0 && (
          <Section title={`Storage (${w.volumes.length})`}>
            {w.volumes.map((v) => (
              <div key={v.name} className="py-1.5 text-sm">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="truncate text-nb-300">{v.name}</span>
                  <span className="shrink-0 text-nb-300">{volumeSize(v.sizeGb)}</span>
                </div>
                <div className="text-xs text-nb-500">
                  {[v.storageClass, v.accessModes?.join(', '), v.phase].filter(Boolean).join(' · ')}
                </div>
                {v.pinnedNodeIds && v.pinnedNodeIds.length > 0 && (
                  <div className="mt-0.5 text-xs text-amber-300" title="A local volume: this service cannot be moved without moving or copying the data.">
                    Data only on{' '}
                    {v.pinnedNodeIds.map((id, i) => {
                      const pn = nodes.find((x) => x.id === id)
                      return (
                        <span key={id}>
                          {i > 0 && ', '}
                          {pn ? <button className="underline hover:text-amber-200" onClick={() => onSelect({ kind: 'node', id })}>{pn.name}</button> : id}
                        </span>
                      )
                    })}
                  </div>
                )}
              </div>
            ))}
          </Section>
        )}
        <Section title="Can it move?">
          <MobilityPanel service={w} onSelectCluster={(id) => onSelect({ kind: 'cluster', id })} />
        </Section>
        <Section title="Where should this run?">
          <ServiceAdvice serviceId={w.id} />
        </Section>
        <Section title={`Runs on (${w.nodeIds.length})`}>
          {w.nodeIds.map((id) => {
            const n = nodes.find((x) => x.id === id)
            return n ? <LinkRow key={id} label={n.name} onClick={() => onSelect({ kind: 'node', id })} /> : null
          })}
          {w.nodeIds.length === 0 && <p className="text-sm text-nb-500">Not placed on any node.</p>}
        </Section>
        <Section title={`Calls (${calls.length})`}>
          {calls.map((d) => {
            const t = end(d.toKind, d.to)
            return t ? <LinkRow key={d.id} label={t.label} sub={depSub(d)} stacked onClick={() => onSelect(t.sel)} /> : null
          })}
          {calls.length === 0 && <p className="text-sm text-nb-500">No outgoing dependencies.</p>}
        </Section>
        <Section title={`Called by (${calledBy.length})`}>
          {calledBy.map((d) => {
            const f = end(d.fromKind, d.from)
            return f ? <LinkRow key={d.id} label={f.label} sub={depSub(d)} stacked onClick={() => onSelect(f.sel)} /> : null
          })}
          {calledBy.length === 0 && <p className="text-sm text-nb-500">Nobody calls this service.</p>}
        </Section>
      </>
    )
  } else if (selection.kind === 'device') {
    const d = devices.find((x) => x.id === selection.id)
    if (!d) return null
    const { calls, calledBy } = depSections(d.id)
    const gw = nodes.find((n) => n.id === d.gatewayNodeId)
    title = d.name
    subtitle = <Pill>{DEVICE_KINDS.find((k) => k.value === d.kind)?.label ?? d.kind}{d.count > 1 ? ` ×${d.count}` : ''}</Pill>
    body = (
      <>
        <div className="px-5 py-3">
          <Row label="Status"><StatusDot status={d.status} withLabel /></Row>
          <Why ev={d.evidence?.kind} />
          <Row label="Application">{appName(d.applicationId) ?? '—'}</Row>
          <Row label="Site">{d.siteId ? (
            <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'site', id: d.siteId! })}>{siteName(d.siteId)}</button>
          ) : '—'}</Row>
          <Row label="Units">{d.count}</Row>
          <Row label="Protocol">{d.protocol || '—'}</Row>
          <Maybe label="Connectivity">{connLabel(d.connectivity)}</Maybe>
          {gw && (
            <Row label="Attached to">
              <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'node', id: gw.id })}>{gw.name}</button>
            </Row>
          )}
          <Maybe label="Hardware">{d.hardwareModel}</Maybe>
          <Maybe label="Firmware">{d.firmware}</Maybe>
          <Maybe label="Labels">{kv(d.labels)}</Maybe>
          <Origin e={d} />
        </div>
        <Section title={`Sends data to (${calls.length})`}>
          {calls.map((x) => {
            const t = end(x.toKind, x.to)
            return t ? <LinkRow key={x.id} label={t.label} sub={depSub(x)} stacked onClick={() => onSelect(t.sel)} /> : null
          })}
          {calls.length === 0 && <p className="text-sm text-nb-500">Not connected to any service.</p>}
        </Section>
        <Section title={`Receives from (${calledBy.length})`}>
          {calledBy.map((x) => {
            const f = end(x.fromKind, x.from)
            return f ? <LinkRow key={x.id} label={f.label} sub={depSub(x)} stacked onClick={() => onSelect(f.sel)} /> : null
          })}
          {calledBy.length === 0 && <p className="text-sm text-nb-500">Nothing sends commands here.</p>}
        </Section>
      </>
    )
  } else if (selection.kind === 'site') {
    const s = sites.find((x) => x.id === selection.id)
    const ds = devices.filter((d) => (s ? d.siteId === s.id : selection.id === 'none' ? !d.siteId : true))
    const cs = s ? clusters.filter((c) => c.siteId === s.id) : []
    const links = s ? siteLinks.filter((l) => l.a === s.id || l.b === s.id) : []
    title = s?.name ?? (selection.id === 'none' ? 'Unplaced devices' : 'Devices')
    subtitle = <Pill>{s ? s.kind.replace('-', ' ') : 'Device group'}</Pill>
    editable = false
    body = (
      <>
        {s && (
          <div className="px-5 py-3">
            <Row label="Location"><Place site={s} /></Row>
            <Row label="Coordinates"><span className="font-mono text-xs">{s.lat.toFixed(2)}, {s.lng.toFixed(2)}</span></Row>
            {exitIps(cs).length > 0 && (
              <Row label="Exit IP"><span className="flex flex-col gap-1">{exitIps(cs).map((ip) => <IpAddress key={ip} ip={ip} inline />)}</span></Row>
            )}
            <Maybe label="Trust zone · residency">{[s.trustZone, s.dataResidency].filter(Boolean).join(' · ')}</Maybe>
          </div>
        )}
        {cs.length > 0 && (
          <Section title={`Clusters (${cs.length})`}>
            {cs.map((c) => <LinkRow key={c.id} label={c.name} sub={c.distribution} onClick={() => onSelect({ kind: 'cluster', id: c.id })} />)}
          </Section>
        )}
        <Section title={`Devices (${ds.length})`}>
          {ds.map((d) => <LinkRow key={d.id} label={d.name} sub={d.count > 1 ? `×${d.count}` : ''} onClick={() => onSelect({ kind: 'device', id: d.id })} />)}
          {ds.length === 0 && <p className="text-sm text-nb-500">No devices here.</p>}
        </Section>
        {links.length > 0 && (
          <Section title="Links to other sites">
            {links.map((l) => {
              const other = l.a === s!.id ? l.b : l.a
              const bits = [l.rttMs !== undefined ? `${l.rttMs} ms` : '', l.lossPct !== undefined ? `${l.lossPct}% loss` : '', l.mbps !== undefined ? `${l.mbps} Mbps` : ''].filter(Boolean).join(' · ')
              return <LinkRow key={l.id} label={siteName(other) ?? other} sub={`${bits}${l.source === 'declared' ? ' (declared)' : ''}`} />
            })}
          </Section>
        )}
      </>
    )
  } else if (selection.kind === 'external') {
    const e = externalEndpoints.find((x) => x.id === selection.id)
    if (!e) return null
    const { calls, calledBy } = depSections(e.id)
    const seenOnly = e.source === 'discovered'
    title = e.name ?? e.host
    subtitle = <Pill>{e.kind}</Pill>
    editable = false
    body = (
      <>
        <div className="px-5 py-3">
          {e.name && <Row label="Address"><span className="font-mono text-xs">{e.host}</span></Row>}
          <Maybe label="Port">{e.port ? String(e.port) : undefined}</Maybe>
          {seenOnly && <Maybe label="Seen">{`${ago(e.lastSeen)} · found in traffic, not declared anywhere`}</Maybe>}
          <Why ev={e.evidence?.identity} />
          <Origin e={e} />
        </div>
        <NameEndpoint key={e.id} endpoint={e} />
        <Section title={`Called by (${calledBy.length})`}>
          {calledBy.map((d) => {
            const f = end(d.fromKind, d.from)
            return f ? <LinkRow key={d.id} label={f.label} sub={depSub(d)} stacked onClick={() => onSelect(f.sel)} /> : null
          })}
          {calledBy.length === 0 && <p className="text-sm text-nb-500">Nothing calls it.</p>}
        </Section>
        {calls.length > 0 && (
          <Section title={`Connects to (${calls.length})`}>
            {calls.map((d) => {
              const t = end(d.toKind, d.to)
              return t ? <LinkRow key={d.id} label={t.label} sub={depSub(d)} stacked onClick={() => onSelect(t.sel)} /> : null
            })}
          </Section>
        )}
      </>
    )
  } else if (selection.kind === 'dependency') {
    const d = dependencies.find((x) => x.id === selection.id)
    if (!d) return null
    const f = end(d.fromKind, d.from)
    const t = end(d.toKind, d.to)
    const clusterOf = (kind: Dependency['toKind'], id: string) => (kind === 'service' ? services.find((x) => x.id === id)?.clusterId : undefined)
    const fc = clusterOf(d.fromKind, d.from)
    const tc = clusterOf(d.toKind, d.to)
    const across = !!fc && !!tc && fc !== tc
    const q = across ? pathQuality(measured, fc, tc) : undefined
    const seen = isObserved(d)
    const fromSvc = d.fromKind === 'service' ? services.find((x) => x.id === d.from) : undefined
    const toSvc = d.toKind === 'service' ? services.find((x) => x.id === d.to) : undefined
    const verdict = connectionVerdict(d, fromSvc, toSvc, clusters.find((x) => x.id === fromSvc?.clusterId), namespaces)
    const s = d.stats
    const how = d.via === 'ebpf' ? 'eBPF' : d.via === 'conntrack' ? 'conntrack' : undefined
    title = d.label ?? (d.port ? `${d.protocol}:${d.port}` : d.protocol)
    subtitle = <Pill>{d.stale ? 'Quiet' : seen ? 'Seen in traffic' : 'Declared'}</Pill>
    editable = false
    body = (
      <>
        <Section title="Connection">
          {f && <LinkRow label={f.label} sub="calls" onClick={() => onSelect(f.sel)} />}
          {t && <LinkRow label={t.label} sub="is called" onClick={() => onSelect(t.sel)} />}
          <div className="mt-1">
            <Row label="Protocol">{d.protocol}{d.port ? `:${d.port}` : ''}</Row>
            <Row label="Found by">{d.sources.join(' + ')}{how ? ` (${how})` : ''}</Row>
            <Row label="Confidence">{d.confidence}</Row>
            <Maybe label="First seen">{ago(d.firstSeen)}</Maybe>
            <Maybe label="Last seen">{ago(d.lastSeen)}</Maybe>
            {d.noise && <Row label="Kind of traffic">{d.noise === 'dns' ? 'DNS (machinery)' : 'System (machinery)'}</Row>}
          </div>
        </Section>
        <Section title="Traffic">
          {seen && s ? (
            <>
              <Maybe label="Throughput">{s.bytesPerSec !== undefined && (d.via === 'ebpf' || s.bytesPerSec > 0) ? bytesPerSec(s.bytesPerSec) : undefined}</Maybe>
              <Maybe label="Connections">{s.connectionsPerMin !== undefined ? `${Math.round(s.connectionsPerMin * 10) / 10} per minute` : undefined}</Maybe>
              <Maybe label="Requests">{s.reqPerSec !== undefined ? `${s.reqPerSec} per second` : undefined}</Maybe>
              <Maybe label="Errors">{s.errorRate !== undefined ? `${(s.errorRate * 100).toFixed(s.errorRate < 0.1 ? 1 : 0)}%` : undefined}</Maybe>
              <Maybe label="p95 latency">{s.p95Ms !== undefined ? `${s.p95Ms} ms` : undefined}</Maybe>
              <Maybe label="Window">{s.windowSec ? `${Math.round(s.windowSec / 60) || '<1'} min` : undefined}</Maybe>
              {d.stale && <p className="mt-1 text-xs text-amber-300">No traffic since {ago(d.lastSeen)}.</p>}
            </>
          ) : (
            <p className="text-sm text-nb-500">{seen ? 'Seen, but no rates were reported.' : 'Nobody has seen this in traffic; it comes from what was declared. Turn on the traffic observer in Discovery to check it.'}</p>
          )}
        </Section>
        {verdict && (
          <Section title="Service mesh">
            <Row label="Encryption"><span style={{ color: VERDICT_COLOR[verdict.state] }}>{verdict.short}</span></Row>
            <p className="text-xs text-nb-400">{verdict.detail}</p>
            <p className="mt-1.5 text-xs text-nb-500">Inferred from configuration (proxies, ports kept out of them, and the mesh's mutual-TLS policy). Nothing here was measured on the wire.</p>
          </Section>
        )}
        {across && (
          <Section title="Network path">
            {q ? (
              <>
                <Row label="Round trip"><span title="TCP connect time, median">{rttLabel(q.rttMs)}</span></Row>
                <Row label="Loss"><span className={lossBand(q.lossPct) === 'hot' ? 'text-red-300' : lossBand(q.lossPct) === 'warn' ? 'text-amber-300' : undefined}>{q.lossPct.toFixed(q.lossPct < 10 ? 1 : 0)}% of connection attempts failed</span></Row>
                <p className="mt-1 text-xs text-nb-500">
                  Measured from {clusterName(q.reversed ? tc : fc)} to {clusterName(q.reversed ? fc : tc)}{q.reversed ? ' (the other direction; the round trip is the same, loss may differ)' : ''}.
                </p>
              </>
            ) : (
              <p className="text-sm text-nb-500">
                {clusterName(fc)} → {clusterName(tc)} has not been measured. <Link to="/sites" className="text-accent hover:underline">Add its address under Sites</Link> to time it.
              </p>
            )}
          </Section>
        )}
      </>
    )
  }

  return (
    <aside className="fixed inset-x-0 bottom-0 z-30 flex max-h-[65vh] flex-col overflow-y-auto rounded-t-xl border-t border-nb-850 bg-nb-920 shadow-2xl lg:static lg:max-h-none lg:w-80 lg:shrink-0 lg:rounded-none lg:border-l lg:border-t-0 lg:shadow-none">
      <div className="flex items-start justify-between gap-3 px-5 py-4">
        <div className="min-w-0">
          <h2 className="truncate text-base font-medium text-white">{title}</h2>
          <div className="mt-1.5">{subtitle}</div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {editable && !inPast && (
            <Button variant="ghost" size="sm" onClick={() => onEdit(selection)} aria-label="Edit">
              <Pencil size={14} /> Edit
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close inspector">
            <X size={14} />
          </Button>
        </div>
      </div>
      {body}
      {(selection.kind === 'cluster' || selection.kind === 'node' || selection.kind === 'service') && <EvidenceSection key={`ev:${selection.kind}:${selection.id}`} kind={selection.kind} id={selection.id} />}
      <EntityHistory key={`${selection.kind}:${selection.id}`} kind={selection.kind} id={selection.id} />
    </aside>
  )
}
