import clsx from 'clsx'
import { Cable, ChevronDown, ChevronUp, Network, Pencil, X } from 'lucide-react'
import { useEffect, useMemo, useState, type ComponentProps, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { CheckLine } from '@/components/discovery/AgentParts'
import EntityHistory from '@/components/EntityHistory'
import { EvidenceSection, WeakValues } from '@/components/EvidenceSection'
import MobilityPanel from '@/components/MobilityPanel'
import PlacementHint from '@/components/PlacementHint'
import { LoadMeters } from '@/components/topology/Load'
import { LinkRow, Section } from '@/components/topology/InspectorParts'
import PlatformPanel, { PlatformSubtitle } from '@/components/topology/PlatformPanel'
import { DistroIcon, Flag, Place, ProviderIcon, WithIcon } from '@/components/ui/brand'
import { Button, CompletenessBadge, ConnectivityStatusBadge, DetailRow, ICON_MD, ICON_SM, Input, IpAddress, Pill, Provenance as ProvenanceStrip, Select, Sparkline, SourceBadge, StatusDot, TierBadge, TunnelEvidence } from '@/components/ui/primitives'
import { completeness } from '@/lib/completeness'
import { CONFIDENCE_TONE, dependencyProvenance, observation } from '@/lib/provenance'
import { hasOverrides } from '@/lib/effective'
import { exitIps } from '@/lib/geo'
import { ageLabel, autoscalerRange, callerIfaceSpeedMbps, disruptionLabel, formatMemory, GEO_UNLOCATABLE_HELP, GEO_UNLOCATABLE_LABEL, ipInCidr, linkUtilizationPct, podsLabel, podsPercent, recentlyScaledPods, volumeSize } from '@/lib/present'
import { usePlacementSuggestions } from '@/lib/usePlacement'
import { useHistoryView } from '@/store/history'
import { useConn, useServer } from '@/store/server'
import { useRawTopology, useTopology } from '@/store/topology'
import { bytesPerSec, bytesTotal, isObserved, trafficSummary } from '@/lib/observed'
import { clusterLoad, lossBand, nodeLoad, pathQuality, rttLabel } from '@/lib/metrics'
import { useClusterLinks, useClusterPairConnectivity, usePaths } from '@/store/topology'
import { useCanvasFocus } from '@/store/canvasFocus'
import { buildNetworks as networksOf, networkWord } from '@/lib/networks'
import { connectionVerdict, meshName, MTLS_WORDS, proxyWords, VERDICT_COLOR } from '@/lib/mesh'
import type { PlatformModel } from '@/lib/platformLayer'
import { CONNECTIVITY, DEVICE_KINDS, TIERS, type Agent, type Dependency, type Evidence, type ExternalEndpoint, type ExternalKind, type OverrideMeta, type Provenance, type Resources, type Tier } from '@/lib/types'
import { api } from '@/lib/api'
import type { DependencySeriesPoint } from '@/lib/history'

export type Selection = { kind: 'cluster' | 'tier' | 'node' | 'service' | 'device' | 'site' | 'external' | 'dependency' | 'platform'; id: string } | null

// A thin wrapper around the shared DetailRow (primitives.tsx) - keeps every one of this file's ~50+
// existing Row(...) call sites unchanged (same props, same roomy non-dense sizing) while the actual
// label/value layout lives in one place shared with EdgeHoverCard's compact `dense` rows.
function Row({ label, children, wrap, badge, copy }: { label: string; children: ReactNode; wrap?: boolean; badge?: ReactNode; copy?: string }) {
  return (
    <DetailRow label={label} wrap={wrap} badge={badge} copy={copy}>
      {children}
    </DetailRow>
  )
}

/** A row that only renders when there is something to show. */
function Maybe({ label, children, copy }: { label: string; children: ReactNode; copy?: string }) {
  if (children === undefined || children === null || children === false || children === '') return null
  return <Row label={label} copy={copy}>{children}</Row>
}

/** The RTT/loss/throughput trend behind a dependency's current numbers (see api.dependencySeries) -
 *  three tiny inline Sparklines, each silently rendering nothing if that particular signal never had
 *  two measured points in the window (a conntrack-only dependency, for instance, has throughput
 *  history but no rttMs/lossPct at all). Fetched once per dependency id rather than kept in the
 *  topology store: this is 24h of recorded history, a different lifetime from the live polled model
 *  everything else in this file reads. */
function DependencyTrend({ dependencyId }: { dependencyId: string }) {
  const conn = useConn()
  // Keyed on the dependency id it was fetched for, not just "the latest points": a stale response for
  // a dependency the selection has since moved away from must never render under the new one, and
  // starting a fetch sets no state of its own (the state update lives only in the promise
  // continuation below) - so switching dependencies shows nothing until its own fetch resolves,
  // rather than another dependency's trend flashing briefly in between.
  const [state, setState] = useState<{ id: string; points: DependencySeriesPoint[] } | null>(null)
  useEffect(() => {
    let live = true
    api.dependencySeries(conn, dependencyId, 24).then((pts) => {
      if (live) setState({ id: dependencyId, points: pts })
    }).catch(() => {
      if (live) setState({ id: dependencyId, points: [] })
    })
    return () => {
      live = false
    }
  }, [conn, dependencyId])
  if (!state || state.id !== dependencyId) return null
  const points = state.points
  const rtt = points.map((p) => p.rttMs)
  const loss = points.map((p) => p.lossPct)
  const bps = points.map((p) => p.bytesPerSec)
  const hasTrend = (s: (number | undefined)[]) => s.filter((v) => v !== undefined).length >= 2
  if (!hasTrend(rtt) && !hasTrend(loss) && !hasTrend(bps)) return null
  return (
    <Row label="Trend (24h)">
      <span className="flex items-center gap-3">
        {hasTrend(rtt) && <Sparkline values={rtt} title="Round trip, last 24h" />}
        {hasTrend(loss) && <Sparkline values={loss} title="Loss%, last 24h" className="text-bad" />}
        {hasTrend(bps) && <Sparkline values={bps} title="Throughput, last 24h" className="text-ok" />}
      </span>
    </Row>
  )
}

/** Why discovery believes a value. Shown so a guess never looks like a fact. */
/**
 * A small "confirmed" tag for a field a person set by hand over a guessed or unknown value - the positive
 * counterpart to the "Not sure" caveat below (and to weakAttributes' evidence chips): those disappear once a
 * field is overridden, but disappearing is easy to miss, especially when there was nothing to notice in the
 * first place (a probe-based guess never showed a caveat at all - see the Type row). This makes the edit visible
 * right where the value is, instead of only in the Evidence section further down (where it does show up, as
 * "declared by a person" with the old guess kept beneath it) or the History page's audit entry.
 *
 * `meta` names who confirmed it and when, when that is known (see Provenance.overrideMeta); older overrides made
 * before this existed have no `meta`, and fall back to the generic wording rather than guessing an author.
 */
function Confirmed({ meta }: { meta?: OverrideMeta }) {
  const title = meta ? `${meta.by} set this by hand ${ageLabel(meta.at)} ago; it will not be overwritten by rediscovery.` : 'You set this by hand; it will not be overwritten by rediscovery.'
  return (
    <span className="ml-1.5 inline-block align-middle whitespace-nowrap rounded-full border border-ok/30 bg-ok/10 px-1.5 py-px text-[10px] font-medium text-ok" title={title}>
      {meta ? `confirmed by ${meta.by}` : 'you confirmed this'}
    </span>
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

/** The shared Provenance strip's props for one attribute's own detection evidence (a device's "kind", a
 *  node's "Type", an external endpoint's identity) - the signal discovery saw and how sure it is, nothing
 *  else. Replaces this file's old Why, which drew the same two facts by hand once per entity kind. */
function fieldProvenance(ev?: Evidence): ComponentProps<typeof ProvenanceStrip> | undefined {
  if (!ev) return undefined
  return { source: ev.signal, sourceTitle: ev.detail, confidence: { tone: CONFIDENCE_TONE[ev.confidence], label: ev.confidence } }
}

/** The shared Provenance strip's props for a whole discovered/declared record (cluster, node, service,
 *  device): the origin words this file's old Origin showed, the aggregate "Edited" badge SourceBadge
 *  already carries for a person's override, confidence from the record's own observation state
 *  (live/stale/disconnected/revoked/gone - the only "how sure" a whole record carries; a single value's own
 *  guess/unknown is WeakValues' job and stays separate from this), and when it was last seen. */
function recordProvenance(e: Provenance): ComponentProps<typeof ProvenanceStrip> {
  const obs = observation(e)
  return {
    source: e.source === 'manual' ? 'Entered manually' : e.source === 'discovered' ? 'Detected' : 'Imported',
    sourceBadge: <SourceBadge source="manual" overridden={hasOverrides(e)} />,
    confidence: obs ? { tone: obs.tone, label: obs.label, title: obs.reason } : e.stale ? { tone: 'warn', label: 'stale' } : undefined,
    freshness: e.lastSeen ? { label: 'Seen', at: e.lastSeen } : undefined,
  }
}

/** An external endpoint's own Provenance strip: unlike a device (whose identity evidence and record origin
 *  sit in separate sections and so get two strips, see the 'device' branch below), an external endpoint's
 *  Why and Origin used to sit right next to each other in the same "Identity" section - two strips stacked
 *  there would just repeat the "Provenance" label - so this folds them into one, preferring the sharper
 *  per-field evidence (what actually named this endpoint) over the generic record-origin wording wherever
 *  both exist, and keeps the record's own freshness either way. The "found in traffic, not declared
 *  anywhere" caveat an external-only record used to hardcode into its own freshness sentence rides along as
 *  `freshness.note` instead - the same mechanism any future caller can reuse, not another bespoke string. */
function externalProvenance(e: ExternalEndpoint): ComponentProps<typeof ProvenanceStrip> {
  const record = recordProvenance(e)
  const field = fieldProvenance(e.evidence?.identity)
  return {
    ...record,
    ...field,
    freshness: e.source === 'discovered' && e.lastSeen ? { label: 'Seen', at: e.lastSeen, note: 'found in traffic, not declared anywhere' } : record.freshness,
  }
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
        <span className="text-nb-500" title="Turn it on with the install command's “traffic observer” option, or: helm upgrade … --reset-then-reuse-values --set flowObserver.enabled=true">
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
          <span className="text-warn">No collector reporting (last heard {ago(o.lastReport)})</span>
        ) : (
          <span title={o.collectors.map((c) => `${c.node}: ${c.method}${c.bytesKnown ? '' : ', bytes not measured'}`).join('\n')}>
            {parts.join(', ')} · {watched.size} of {nodeNames.length || watched.size} nodes
          </span>
        )}
      </Row>
      {missing.length > 0 && o.collectors.length > 0 && (
        <Row label="Not observed" wrap><span className="text-warn" title="No collector has reported from these nodes, so traffic to or from their pods is missing.">{missing.slice(0, 3).join(', ')}{missing.length > 3 ? ` +${missing.length - 3}` : ''}</span></Row>
      )}
      {o.lost > 0 && (
        <Row label="Dropped" wrap><span className="text-warn" title="Connections a collector could not attribute or count, for example ones that were already open when it started.">{o.lost.toLocaleString()} observations</span></Row>
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
  // The server fills this in itself when the address matches a small, bundled table of known public
  // ranges (see backend/internal/netid) - shown only while the name still matches that guess, so it
  // gets out of the way the moment a person names the endpoint themselves.
  const detected = endpoint.source !== 'manual' && endpoint.name && endpoint.evidence?.identity
  return (
    <div className="border-t border-nb-850 px-5 py-4">
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-nb-500">What is it?</div>
      {detected && (
        <p className="mb-2 text-xs text-nb-500" title={endpoint.evidence?.identity?.detail}>
          Detected automatically: {endpoint.evidence?.identity?.signal ?? `matched ${endpoint.name}`}.
        </p>
      )}
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

const res = (r?: Resources) => (r ? `${r.cpu} vCPU · ${r.memoryGb} GB${r.diskGb !== undefined ? ` · ${formatMemory(r.diskGb)} disk` : ''}` : undefined)
const list = (a?: string[]) => (a && a.length ? a.join(', ') : undefined)

/** Kubernetes-style key=value pairs (a node selector, a device's labels) as individual chips instead of
 *  one long comma-joined line that only ever scrolled sideways: each pair keeps its own wrap boundary,
 *  and the key reads dimmer than the value so a long list still scans at a glance. Renders nothing when
 *  there is nothing to show, the same as Maybe. */
function KeyValueChips({ label, pairs }: { label: string; pairs?: Record<string, string> }) {
  const entries = pairs ? Object.entries(pairs) : []
  if (entries.length === 0) return null
  return (
    <div className="flex items-baseline justify-between gap-4 py-1.5 text-sm">
      <span className="shrink-0 text-nb-500">{label}</span>
      <div className="flex min-w-0 flex-wrap justify-end gap-1">
        {entries.map(([k, v]) => (
          <Pill key={k} className="max-w-full gap-0.5 font-mono text-[11px] leading-none">
            <span className="truncate text-nb-500">{k}</span>
            <span className="text-nb-600">=</span>
            <span className="truncate text-nb-300">{v}</span>
          </Pill>
        ))}
      </div>
    </div>
  )
}
/** A handful of short items (accelerators, disks, network interfaces, taints, conditions) as individually
 *  wrapping chips instead of one long comma-joined line that only ever scrolled sideways - the same fix as
 *  KeyValueChips, for plain values instead of key=value pairs. Renders nothing when there is nothing to show,
 *  the same as Maybe.
 *
 *  Each chip still truncates on its own (`max-w-full truncate`) when a single item is wider than the sidebar
 *  itself - a verbose one like an interface's "name (kind, speed, MTU, utilization)" summary routinely is,
 *  even sitting alone on its own wrapped line with nothing competing for width. Unlike Row's long single
 *  values, there's no good way to make one chip among several horizontally scroll on its own without either
 *  scrolling the whole wrapped group (breaking the ones that already fit) or growing it past the chip shape
 *  entirely - so instead every chip gets its own `title`, the same hover-for-the-full-value pattern already
 *  used throughout this sidebar wherever something can't just be made to fit (geo reasons, evidence detail,
 *  mesh state, and more). `title` on the outer group (below) is separate and unrelated: a general explanation
 *  of the whole field (see the "CIDR overlap" call site), not a stand-in for any one chip's own full text. */
function Chips({ label, items, tone, title }: { label: string; items?: string[]; tone?: 'warn'; title?: string }) {
  if (!items || items.length === 0) return null
  return (
    <div className="flex items-baseline justify-between gap-4 py-1.5 text-sm">
      <span className="shrink-0 text-nb-500">{label}</span>
      <div className="flex min-w-0 flex-wrap justify-end gap-1" title={title}>
        {items.map((it, i) => (
          <Pill key={i} title={it} className={clsx('max-w-full truncate text-[11px] leading-none', tone === 'warn' ? 'text-warn' : 'text-nb-300')}>
            {it}
          </Pill>
        ))}
      </div>
    </div>
  )
}

const connLabel = (c?: string) => CONNECTIVITY.find((x) => x.value === c)?.label

/** Busiest first, machinery (DNS, system) last: the applications' own dependencies are what people look for. */
const busiest = (a: Dependency, b: Dependency) =>
  Number(!!a.noise) - Number(!!b.noise) || (b.stats?.bytesPerSec ?? 0) - (a.stats?.bytesPerSec ?? 0) || (b.stats?.connectionsPerMin ?? 0) - (a.stats?.connectionsPerMin ?? 0)

export default function Inspector({
  selection,
  platform,
  onSelect,
  onEdit,
  onClose,
  localOperators,
  onConfigureTelemetry,
}: {
  selection: Selection
  /** The platform layer on the canvas, for a selection of one of its parts. */
  platform?: PlatformModel
  /** Cluster id -> the agent running local telemetry there, which the canvas no longer shows a control for. */
  localOperators?: ReadonlyMap<string, { layers: string[]; agentId: string }>
  onConfigureTelemetry?: (agentId: string) => void
  onSelect: (s: Selection) => void
  onEdit: (s: NonNullable<Selection>) => void
  onClose: () => void
}) {
  const { clusters, nodes, namespaces, services, devices, dependencies, applications, sites, siteLinks, externalEndpoints, agents } = useTopology()
  const placement = usePlacementSuggestions().byCluster
  const inPast = useHistoryView((s) => s.at !== null)
  // On a narrow screen the details are a sheet over the canvas; "peek" shrinks it to its header so the selected thing stays in view.
  const [peek, setPeek] = useState(false)
  const measured = usePaths()
  const clusterPairConnectivity = useClusterPairConnectivity()
  const clusterLinks = useClusterLinks()
  const setNetwork = useCanvasFocus((s) => s.setNetwork)
  const publicIpFallbackOn = useServer((s) => s.info?.geoip?.publicIpFallback)
  // Id -> node, built once per `nodes` change instead of fresh on every render just to resolve the one or
  // two nodes a selected dependency's caller-interface lookup actually needs (see callerIfaceSpeedMbps below).
  const nodeById = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes])
  // Grouping every dependency by its `from`/`to` end, and sorting each group, is a full scan of the
  // topology's dependencies - worth doing once per `dependencies` change rather than redoing on every
  // render regardless of whether the selection (or anything else) actually changed. depSections below
  // then becomes a plain O(1) lookup into this index.
  const depIndex = useMemo(() => {
    const calls = new Map<string, Dependency[]>()
    const calledBy = new Map<string, Dependency[]>()
    for (const d of dependencies) {
      if (!calls.has(d.from)) calls.set(d.from, [])
      calls.get(d.from)!.push(d)
      if (!calledBy.has(d.to)) calledBy.set(d.to, [])
      calledBy.get(d.to)!.push(d)
    }
    for (const list of calls.values()) list.sort(busiest)
    for (const list of calledBy.values()) list.sort(busiest)
    return { calls, calledBy }
  }, [dependencies])
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
    // Same words dependencyProvenance gives the Inspector's own dependency page and the shared Provenance
    // strip, so this compact inline summary never drifts into a fourth wording of the same fact.
    const { source, via } = dependencyProvenance(d)
    const bits = [
      d.port ? `${d.protocol}:${d.port}` : d.protocol,
      seen ? `seen${via ? ` (${via})` : ''}${others.length ? ` + ${others.join('+')}` : ''}` : source,
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
  const depSections = (id: string) => ({ calls: depIndex.calls.get(id) ?? [], calledBy: depIndex.calledBy.get(id) ?? [] })

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
    const clusterSite = sites.find((x) => x.id === c.siteId)
    title = c.name
    subtitle = (
      <span className="flex flex-wrap items-center gap-2">
        <StatusDot status={c.status} withLabel />
        <TierBadge tier={c.tier} />
      </span>
    )
    body = (
      <>
        <Section title="Identity">
          <Row label="Distribution" badge={!!c.overrides?.distribution && <Confirmed meta={c.overrideMeta?.distribution} />}>
            <WithIcon icon={<DistroIcon distribution={c.distribution} size={ICON_SM} />}>{c.distribution} {c.version}</WithIcon>
          </Row>
          <Row label="Provider" badge={!!c.overrides?.provider && <Confirmed meta={c.overrideMeta?.provider} />}>
            {c.provider ? <WithIcon icon={<ProviderIcon provider={c.provider} size={ICON_SM} />}>{c.provider}</WithIcon> : '—'}
          </Row>
          <Maybe label="Age">{c.createdAt ? `${ageLabel(c.createdAt)} (${new Date(c.createdAt).toLocaleDateString()})` : undefined}</Maybe>
          <Maybe label="Trust zone · residency">{[c.trustZone, c.dataResidency].filter(Boolean).join(' · ')}</Maybe>
          {!c.trustZone && !clusterSite?.trustZone && (
            <Row label="Trust zone" wrap>
              <span className="text-warn">
                Not set: placement can't tell whether a workload here is allowed to move to a more sensitive zone, or leave for a less trusted one. Set it here, or on its site.
              </span>
            </Row>
          )}
        </Section>
        <Section title="Location & network">
          <Row label="Location"><Place site={sites.find((x) => x.id === c.siteId)} fallback={c.region} /></Row>
          {!sites.some((x) => x.id === c.siteId) && <PlacementHint suggestion={placement.get(c.id)} egressIp={c.egressIp} />}
          <Maybe label="Site">{siteName(c.siteId)}</Maybe>
          <Maybe label="Region label">{c.region}</Maybe>
          <Maybe label="CNI · Ingress">{[c.cni, c.ingress].filter(Boolean).join(' · ')}</Maybe>
          <Maybe label="Pod CIDR" copy={c.podCidr}><span className="font-mono text-xs">{c.podCidr}</span></Maybe>
          <Chips label="CIDR overlap" items={overlap.map((o) => o.name)} tone="warn" title="Overlapping pod CIDRs can break direct cross-cluster routing." />
          <Maybe label="Service CIDR" copy={c.serviceCidr}><span className="font-mono text-xs">{c.serviceCidr}</span></Maybe>
          <Chips label="Storage" items={c.storageClasses} />
          {c.apiEndpoint && <Row label="API endpoint" copy={c.apiEndpoint}><IpAddress ip={c.apiEndpoint} inline /></Row>}
          {c.apiEndpoint && ipInCidr(c.apiEndpoint, c.serviceCidr) && (
            <p className="-mt-1 text-xs text-nb-500">
              This is the cluster's own API service address (inside its Service CIDR) - it works for the agent and other in-cluster clients, but is not reachable from outside the cluster.
            </p>
          )}
          {c.egressIp && <Row label="Exit IP" copy={c.egressIp}><IpAddress ip={c.egressIp} inline /></Row>}
          {agent?.connectingGeo && (
            <div className="mt-2 rounded-md border border-nb-850 bg-nb-930/40 px-2.5 py-2 text-xs">
              <div className="mb-1 text-nb-500">GeoIP says</div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span
                  className="inline-flex items-center gap-2 text-nb-300"
                  title={
                    agent.connectingGeo.estimated
                      ? "This cluster's own connecting address is private, so this is where this server's own internet connection appears to be instead - accurate if they share a network, off if they don't."
                      : "Where a GeoIP database places the address this cluster connects from. A hint only: a VPN, a mobile network or a cloud provider's egress can put it far from the cluster."
                  }
                >
                  <Flag code={agent.connectingGeo.country} />
                  <span>
                    {[agent.connectingGeo.city, agent.connectingGeo.countryName || agent.connectingGeo.country].filter(Boolean).join(', ')}
                    {agent.connectingGeo.accuracyKm ? <span className="text-nb-500"> ±{agent.connectingGeo.accuracyKm} km</span> : null}
                    {agent.connectingGeo.estimated && <span className="ml-1 text-nb-500">(estimated)</span>}
                  </span>
                </span>
                {agent.connectingGeo.asOrg && (
                  <span
                    className="text-nb-500"
                    title="Which network this address belongs to, from a separate ASN database - independent of the city/country guess, and unaffected by a VPN or cloud egress moving it."
                  >
                    · {agent.connectingGeo.asOrg}
                    {agent.connectingGeo.asn ? ` (AS${agent.connectingGeo.asn})` : ''}
                  </span>
                )}
              </div>
            </div>
          )}
          {!agent?.connectingGeo && agent?.connectingGeoReason && (
            <div className="mt-2 rounded-md border border-nb-850 bg-nb-930/40 px-2.5 py-2 text-xs">
              <div className="mb-1 text-nb-500">GeoIP says</div>
              <span className="text-nb-400" title={GEO_UNLOCATABLE_HELP[agent.connectingGeoReason]}>
                No location — {GEO_UNLOCATABLE_LABEL[agent.connectingGeoReason].toLowerCase()}
                {(agent.connectingGeoReason === 'cgnat' || agent.connectingGeoReason === 'private') && !publicIpFallbackOn &&
                  ' (an administrator can turn on estimating this from the server\'s own address in the deployment settings)'}
              </span>
            </div>
          )}
        </Section>
        <Section title="Load">
          {ns.length > 0 || ws.length > 0 ? <LoadMeters load={clusterLoad(c, ns, ws)} /> : <p className="text-sm text-nb-500">Nothing reported yet.</p>}
        </Section>
        {(() => {
          // The same networks the canvas chips name, in words, with who else is on them; hovering one lights those clusters on the canvas.
          const mine = networksOf(clusterLinks).filter((n) => n.members.some((m) => m.id === c.id))
          return mine.length > 0 && (
            <Section title={`Networks (${mine.length})`}>
              {mine.map((n) => (
                <div key={n.id} className="mb-2.5 last:mb-0" data-testid="inspector-network" onPointerEnter={() => setNetwork(n.id)} onPointerLeave={() => setNetwork(null)}>
                  <div className="flex items-center gap-1.5 px-2 text-sm text-nb-300">
                    {n.kind === 'overlay' ? <Cable size={ICON_SM} className="shrink-0 text-info" aria-hidden="true" /> : <Network size={ICON_SM} className="shrink-0 text-info" aria-hidden="true" />}
                    <span>{networkWord(n.kind)}</span>
                    <span className="truncate text-nb-500" title={n.via}>{n.via}</span>
                  </div>
                  {n.members.filter((m) => m.id !== c.id).map((m) => (
                    <LinkRow key={m.id} label={m.name} sub={n.kind === 'overlay' ? 'same overlay' : 'same subnet'} onClick={() => onSelect({ kind: 'cluster', id: m.id })} />
                  ))}
                </div>
              ))}
            </Section>
          )
        })()}
        {(() => {
          // Every cluster pair c has SOME relationship with (a confirmed ClusterLink, or observed
          // cross-cluster traffic) - a strict superset of the old "Cluster links" section, which only
          // ever showed the two cases a ClusterLink can positively confirm. See
          // ClusterPairConnectivity's own doc for what each status means and is backed by.
          const pairs = clusterPairConnectivity.filter((p) => p.fromCluster === c.id || p.toCluster === c.id)
          return pairs.length > 0 && (
            <Section title={`Cluster connectivity (${pairs.length})`}>
              {pairs.map((p) => {
                const otherId = p.fromCluster === c.id ? p.toCluster : p.fromCluster
                const otherName = p.fromCluster === c.id ? p.toName : p.fromName
                // fieldProvenance(ev?) already returns undefined for an undefined ev - computed once
                // here, rather than spread straight off a conditional, so a "tunnel"/"subnet" row (whose
                // evidence lives entirely on `links` below, not on this field) never tries to render a
                // Provenance strip with nothing behind it.
                const fp = p.evidence ? fieldProvenance(p.evidence) : undefined
                return (
                  <div key={`${p.fromCluster}:${p.toCluster}`} className="mb-2.5 last:mb-0">
                    <LinkRow
                      label={otherName}
                      onClick={() => onSelect({ kind: 'cluster', id: otherId })}
                    />
                    <div className="flex items-center gap-2 px-2 pb-1">
                      <ConnectivityStatusBadge status={p.status} />
                      {!!p.dependencyFlows && (
                        <span
                          className="text-xs text-nb-500"
                          title="Observed cross-cluster dependency flows connecting exactly this pair, regardless of which interface they used"
                        >
                          {p.dependencyFlows} dependency flow{p.dependencyFlows === 1 ? '' : 's'}
                        </span>
                      )}
                    </div>
                    {/* "tunnel"/"subnet": the same TunnelEvidence block EdgeHoverCard renders for this
                        exact link's canvas edge, so a click here never shows less than a hover already
                        did - one block per link when both an overlay and a subnet link corroborate the
                        same pair (independent facts, see ClusterLink's own doc). "unexplained"/
                        "unknown": the Provenance strip every other guessed/uncertain field already uses,
                        carrying the same signal/confidence/detail this evidence was built from. */}
                    <div className="px-2">
                      {p.links && p.links.length > 0 ? (
                        p.links.map((l, i) => {
                          const thisNode = l.fromCluster === c.id ? l.fromNode : l.toNode
                          const otherNode = l.fromCluster === c.id ? l.toNode : l.fromNode
                          const thisAddress = l.fromCluster === c.id ? l.fromAddress : l.toAddress
                          const otherAddress = l.fromCluster === c.id ? l.toAddress : l.fromAddress
                          return (
                            <div key={l.kind} className={i > 0 ? 'mt-1.5 border-t border-nb-850 pt-1.5' : undefined}>
                              {p.links!.length > 1 && (
                                <div className="text-[11px] uppercase tracking-wide text-nb-600">{l.kind === 'overlay' ? 'Overlay' : 'Same subnet'}</div>
                              )}
                              <TunnelEvidence
                                via={l.via}
                                encryption={l.encryption}
                                redundancy={l.redundancy}
                                nodeA={thisNode}
                                nodeB={otherNode}
                                addressA={thisAddress}
                                addressB={otherAddress}
                                flowsObserved={l.flowsObserved}
                                avgRttMs={l.avgRttMs}
                                avgLossPct={l.avgLossPct}
                                avgRtoRetransmitsPerMin={l.avgRtoRetransmitsPerMin}
                                avgMssBytes={l.avgMssBytes}
                              />
                            </div>
                          )
                        })
                      ) : fp ? (
                        <ProvenanceStrip {...fp} />
                      ) : null}
                    </div>
                  </div>
                )
              })}
              <p className="mt-1.5 text-xs text-nb-500">
                Whether each related cluster is actually reachable, and how - confirmed from both sides' own routing/address data where possible, never a guess from naming or a declared exposure setting. "Unexplained" means traffic crosses with nothing here to explain how; "unknown" means not enough was collected from one or both sides to say either way.
              </p>
            </Section>
          )
        })()}
        <Section title="Access & discovery">
          <Row label="Discovered"><CompletenessBadge c={completeness(c, nodes, services, dependencies)} /></Row>
          {agent && (
            <Row label="Agent">
              <Link to="/discovery" className="text-accent hover:underline">{agent.name}</Link> <span className="text-nb-500">tier {agent.accessTier}</span>
            </Row>
          )}
          {localOperators?.get(c.id) && (
            <Row label="Local telemetry" wrap>
              <span className="text-nb-300">Running{localOperators.get(c.id)!.layers.length ? ` (${localOperators.get(c.id)!.layers.join(', ')})` : ''}</span>{' '}
              {onConfigureTelemetry && (
                <button type="button" className="text-accent hover:underline" onClick={() => onConfigureTelemetry(localOperators.get(c.id)!.agentId)} data-testid="configure-local-telemetry">
                  Configure
                </button>
              )}
            </Row>
          )}
          {agent && <ObserverRow agent={agent} nodeNames={ns.map((n) => n.name)} />}
          {agent?.scope && (
            <Row label="Namespaces read" wrap>
              <span title={agent.scope.description}>{agent.scope.inScope} of {agent.scope.namespaces}</span>
            </Row>
          )}
          {agent?.link && (
            <Row label="Connection" wrap>
              <span title="Everything this agent has sent since the server itself last started - not this connection's own lifetime, and not reset by a reconnect.">
                {bytesTotal(agent.link.bytes)} sent
                {agent.link.connectedSince && <> · connected {ago(agent.link.connectedSince)}</>}
              </span>
              <span className="ml-1 text-nb-500">
                ({[
                  agent.link.syncs ? `${agent.link.syncs.toLocaleString()} sync${agent.link.syncs === 1 ? '' : 's'}` : '',
                  agent.link.flows ? `${agent.link.flows.toLocaleString()} flow batch${agent.link.flows === 1 ? '' : 'es'}` : '',
                  agent.link.measurements ? `${agent.link.measurements.toLocaleString()} measurement${agent.link.measurements === 1 ? '' : 's'}` : '',
                  agent.link.heartbeats ? `${agent.link.heartbeats.toLocaleString()} heartbeat${agent.link.heartbeats === 1 ? '' : 's'}` : '',
                ].filter(Boolean).join(', ')})
              </span>
            </Row>
          )}
          {agent && <CheckLine agent={agent} />}
          <ProvenanceStrip {...recordProvenance(c)} />
          <WeakValues kind="cluster" rec={c} />
        </Section>
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
          {ns.length === 0 && <p className="text-sm text-nb-500">{c.source === 'discovered' ? 'No node has been reported for this cluster (the agent may read below the Infrastructure access level).' : 'No nodes declared.'}</p>}
        </Section>
        <Section title={`Services (${ws.length})`}>
          <Maybe label="Pending pods">
            {c.pendingPodCount ? (
              <span className="text-warn" title="Pods waiting to be scheduled onto a node right now, across every namespace this agent can see. Stuck if this doesn't drop - often insufficient capacity or a constraint (taint, affinity, resource request) nothing on offer can satisfy.">
                {c.pendingPodCount}
              </span>
            ) : undefined}
          </Maybe>
          {ws.map((w) => <LinkRow key={w.id} label={w.name} sub={w.namespace} onClick={() => onSelect({ kind: 'service', id: w.id })} />)}
          {ws.length === 0 && <p className="text-sm text-nb-500">{c.source === 'discovered' ? 'No service has been reported for this cluster (the agent may read below the Services access level).' : 'No services declared.'}</p>}
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
    // Link utilization per interface: outbound eBPF-measured traffic from workloads scheduled on this node,
    // summed by the caller's own physical interface (Dependency.iface) and matched against that interface's
    // rated speed. Conntrack-only edges have no bytes to trust (the same via==='ebpf' gate used everywhere
    // else this data appears), and a dependency with no traffic since the stale window doesn't count either.
    const ifaceBytesPerSec = new Map<string, number>()
    for (const d of dependencies) {
      if (d.via !== 'ebpf' || d.stale || !d.iface || d.stats?.bytesPerSec === undefined) continue
      if (d.fromKind !== 'service' || !ws.some((w) => w.id === d.from)) continue
      ifaceBytesPerSec.set(d.iface, (ifaceBytesPerSec.get(d.iface) ?? 0) + d.stats.bytesPerSec)
    }
    title = n.name
    subtitle = (
      <span className="flex flex-wrap items-center gap-2">
        <StatusDot status={n.status} withLabel />
        <Pill>{n.role === 'control-plane' ? 'Control plane' : 'Worker'}</Pill>
      </span>
    )
    body = (
      <>
        <Section title="Identity">
          <Row label="Cluster">
            <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'cluster', id: n.clusterId })}>{clusterName(n.clusterId)}</button>
          </Row>
          <Row label="Type" badge={!!n.overrides?.kind && <Confirmed meta={n.overrideMeta?.kind} />}>
            {n.kind === 'vm' ? 'VM' : n.kind === 'bare-metal' ? 'Bare metal' : 'Edge device'}
          </Row>
          {!n.overrides?.kind && n.source === 'discovered' && (n.probed ? n.evidence?.kind?.confidence === 'medium' : n.evidence?.kind?.confidence === 'low') && (
            <ProvenanceStrip
              source={n.probed ? 'node probe' : 'Kubernetes API'}
              confidence={{
                tone: CONFIDENCE_TONE[n.evidence!.kind!.confidence],
                label: n.evidence!.kind!.confidence,
                title: n.probed
                  ? 'The node probe saw no hypervisor flag or firmware name for this - it inferred the type from the chassis or battery instead, which can be wrong. Set it here if it is.'
                  : 'This type is a guess from the Kubernetes API. Turn on the node probe when connecting the cluster to have the machine tell for itself, or set it here.',
              }}
            />
          )}
          <Maybe label="Virtualization">{n.virtualization}</Maybe>
          <Maybe label="Hardware">{n.hardwareModel}</Maybe>
          <Maybe label="Instance">{[n.instanceType, n.zone].filter(Boolean).join(' · ')}</Maybe>
          <Row label="OS">{n.os}</Row>
          <Maybe label="Architecture">{n.arch}</Maybe>
          <Maybe label="Kernel">{n.kernel}</Maybe>
          <Maybe label="Runtime">{n.runtime}</Maybe>
          <Maybe label="Age">{n.createdAt ? `${ageLabel(n.createdAt)} (${new Date(n.createdAt).toLocaleDateString()})` : undefined}</Maybe>
        </Section>
        <Section title="Capacity">
          <Row label="IP" copy={n.ip}><IpAddress ip={n.ip} inline /></Row>
          <Row label="Capacity">{n.cpu} vCPU · {n.memoryGb} GB{n.diskGb !== undefined ? ` · ${formatMemory(n.diskGb)} disk` : ''}</Row>
          {(() => {
            const load = nodeLoad(n)
            return load && <div className="my-2"><LoadMeters load={load} /></div>
          })()}
          <Maybe label="Allocatable">{res(n.allocatable)}</Maybe>
          <Maybe label="Requested">{res(n.requested)}</Maybe>
          <Maybe label="Pods">
            {n.podCount !== undefined ? (
              <span className={(podsPercent(n.podCount, n.podCapacity) ?? 0) >= 90 ? 'text-bad' : undefined}>
                {podsLabel(n.podCount, n.podCapacity)}{n.podCapacity ? ` (max ${n.podCapacity})` : ''}
              </span>
            ) : n.podCapacity ? `max ${n.podCapacity}` : undefined}
          </Maybe>
          <Chips label="Accelerators" items={n.accelerators?.map((a) => `${a.count}× ${a.vendor} ${a.model}`)} />
          <Maybe label="CPU model">{[n.cpuModel, n.cpuThreads ? `${n.cpuThreads} threads` : undefined].filter(Boolean).join(' · ')}</Maybe>
          <Maybe label="Pressure">
            {n.cpuPressurePct === undefined && n.memoryPressurePct === undefined && n.ioPressurePct === undefined
              ? undefined
              : [
                  n.cpuPressurePct !== undefined ? `CPU ${n.cpuPressurePct.toFixed(1)}%` : undefined,
                  n.memoryPressurePct !== undefined ? `Mem ${n.memoryPressurePct.toFixed(1)}%` : undefined,
                  n.ioPressurePct !== undefined ? `IO ${n.ioPressurePct.toFixed(1)}%` : undefined,
                ]
                  .filter(Boolean)
                  .map((s, i) => (
                    <span
                      key={i}
                      className={i > 0 ? 'ml-2' : undefined}
                      title="cgroup v2 PSI: the share of the last 60 seconds this machine had at least one task actually stalled waiting on this resource, not just using it - a direct, measured bottleneck signal, not inferred from a raw usage percentage"
                    >
                      {s}
                    </span>
                  ))}
          </Maybe>
          <Maybe label="OOM kills">
            {n.oomKillCount === undefined ? undefined : (
              <span
                className={n.oomKillCount > 0 ? 'text-bad' : undefined}
                title="Cumulative count from cgroup v2's own per-cgroup OOM-kill accounting, summed across every cgroup on this node - node-level only, no per-pod attribution. Worth checking against this node's own pods' flow activity for the same window."
              >
                {n.oomKillCount} total
              </span>
            )}
          </Maybe>
          <Maybe label="Link saturation">
            {n.linkSaturation === undefined || n.linkSaturation.length === 0
              ? undefined
              : n.linkSaturation.map((ls, i) => (
                  <span
                    key={ls.iface}
                    className={[i > 0 ? 'ml-2' : undefined, ls.saturationPct !== undefined && ls.saturationPct >= 90 ? 'text-bad' : undefined].filter(Boolean).join(' ') || undefined}
                    title={`${ls.iface}: ${(ls.throughputBps / 1_000_000).toFixed(1)} Mbps of traffic this window${ls.saturationPct === undefined ? " (the interface's own rated speed could not be read, so a saturation share is not known)" : ' of its own rated speed'}`}
                  >
                    {ls.iface} {ls.saturationPct !== undefined ? `${ls.saturationPct.toFixed(0)}%` : 'unknown%'}
                  </span>
                ))}
          </Maybe>
          <Maybe label="SNAT exhaustion">
            {n.snatExhaustion === undefined || n.snatExhaustion === 0 ? undefined : (
              <span
                className="text-bad"
                title="Connect() attempts on this node that failed with EADDRNOTAVAIL (ran out of ephemeral ports / SNAT mappings) since its flow collector's eBPF program was loaded - a running total, not a per-window count. A connection that fails this way never shows up as a Dependency or a retransmit, so a node can look healthy by every other signal here while this climbs; most relevant on a gateway proxying lots of short-lived outbound connections."
              >
                {n.snatExhaustion} failed connects
              </span>
            )}
          </Maybe>
          <Maybe label="Thermal throttling">
            {n.thermalTripCount === undefined || n.thermalTripCount === 0 ? undefined : (
              <span
                className="text-bad"
                title="How many times the kernel's thermal:thermal_zone_trip tracepoint has fired since the flow collector's eBPF program attached - the firmware itself judged a sensor past its limit and started shedding load, normally by capping or downclocking the CPU below whatever capacity this node still advertises. Capacity derating from heat, never a power/energy reading: not the same signal an energy exporter such as Kepler would give."
              >
                {n.thermalTripCount} trip{n.thermalTripCount === 1 ? '' : 's'}
              </span>
            )}
          </Maybe>
          <Chips label="Disks" items={n.disks?.map((d) => `${d.model || d.type || 'disk'}${d.sizeBytes ? ` (${formatMemory(d.sizeBytes / 1024 ** 3)})` : ''}`)} />
        </Section>
        <Section title="Network & health">
          <Maybe label="Uplink">{connLabel(n.connectivity)}</Maybe>
          <Chips
            label="Interfaces"
            items={n.networkInterfaces?.map((i) => {
              const bps = ifaceBytesPerSec.get(i.name)
              const util = bps !== undefined && i.speedMbps ? linkUtilizationPct(bps, i.speedMbps) : undefined
              return `${i.name} (${[i.kind, i.speedMbps ? `${i.speedMbps} Mbps` : undefined, i.mtu ? `MTU ${i.mtu}` : undefined, util !== undefined ? `${util}% used` : undefined].filter(Boolean).join(', ')})`
            })}
          />
          <Chips
            label="Tunnels"
            items={n.tunnels?.map((t) => `${t.name} (${[t.kind, t.up === undefined ? undefined : (t.up ? 'up' : 'down'), t.mtu ? `MTU ${t.mtu}` : undefined].filter(Boolean).join(', ')}${t.confirmed ? `, confirmed ↔ ${t.confirmed}` : ''})`)}
          />
          {n.hasBattery && <Row label="Power">Has a battery: can run without mains power</Row>}
          <Chips label="Taints" items={n.taints} />
          <Chips label="Conditions" items={n.conditions} tone="warn" />
        </Section>
        <Section title="Discovery">
          <ProvenanceStrip {...recordProvenance(n)} />
          <WeakValues kind="node" rec={n} />
        </Section>
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
    subtitle = (
      <span className="flex flex-wrap items-center gap-2">
        <StatusDot status={w.status} withLabel />
        <Pill>{w.kind}</Pill>
      </span>
    )
    body = (
      <>
        <Section title="Identity">
          <Row label="Cluster">
            <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'cluster', id: w.clusterId })}>{clusterName(w.clusterId)}</button>
          </Row>
          <Row label="Application">{appName(w.applicationId) ?? '—'}</Row>
          <Row label="Namespace">{w.namespace}</Row>
          <Row label="Image" copy={w.image || undefined}><span className="font-mono text-xs">{w.image || '—'}</span></Row>
          <Maybe label="Digest" copy={w.imageDigest}><span className="font-mono text-xs">{w.imageDigest?.slice(0, 19)}</span></Maybe>
          <Maybe label="Managed by">{w.managedBy}</Maybe>
          <Maybe label="Age">{w.createdAt ? `${ageLabel(w.createdAt)} (${new Date(w.createdAt).toLocaleDateString()})` : undefined}</Maybe>
        </Section>
        <Section title="Resources & scaling">
          <Row label="Replicas">{ready}</Row>
          <Maybe label="Restarts">{w.restarts ? String(w.restarts) : undefined}</Maybe>
          <Maybe label="OOM kills">
            {w.oomKills ? (
              <span className="text-warn" title="Containers killed by the kernel because they asked for more memory than their limit allowed. Raise the memory limit, or find the leak.">
                {w.oomKills}
              </span>
            ) : undefined}
          </Maybe>
          <Maybe label="Requests">{rq}</Maybe>
          <Maybe label="Limits">{lim}</Maybe>
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
        </Section>
        <Section title="Networking">
          <Maybe label="Exposure">{[w.exposure, list(w.hosts)].filter(Boolean).join(' · ')}</Maybe>
          <Chips label="Ports" items={w.ports?.map(String)} />
          <KeyValueChips label="Node selector" pairs={w.nodeSelector} />
          <Chips label="Tolerations" items={w.tolerations} />
          <Maybe label="Sensitivity">{w.sensitivity}</Maybe>
        </Section>
        <Section title="Discovery">
          <ProvenanceStrip {...recordProvenance(w)} />
          <WeakValues kind="service" rec={w} />
        </Section>
        {w.mesh && (
          <Section title="Service mesh">
            <Row label="Mesh">{meshName(w.mesh.mesh)}</Row>
            <Row label="Proxy" wrap>{proxyWords(w.mesh).replace(`${meshName(w.mesh.mesh)} · `, '')}</Row>
            <Chips label="Kept out of proxy" items={w.mesh.excludedPorts} />
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
                  <div className="mt-0.5 text-xs text-warn" title="A local volume: this service cannot be moved without moving or copying the data.">
                    Data only on{' '}
                    {v.pinnedNodeIds.map((id, i) => {
                      const pn = nodes.find((x) => x.id === id)
                      return (
                        <span key={id}>
                          {i > 0 && ', '}
                          {pn ? <button className="underline hover:text-warn" onClick={() => onSelect({ kind: 'node', id })}>{pn.name}</button> : id}
                        </span>
                      )
                    })}
                  </div>
                )}
              </div>
            ))}
          </Section>
        )}
        {w.pods && w.pods.length > 0 && (() => {
          const recent = recentlyScaledPods(w.pods)
          // Not-ready first, then the scaling event (if any), then everything else - the same priority
          // order the canvas dot strip uses to decide which pods survive its own display cap, so whichever
          // 1-2 pods actually need a look are at the top of what could otherwise be a long, scrolled list.
          const sorted = [...w.pods].sort((a, b) => Number(!!a.ready) - Number(!!b.ready) || Number(recent.has(b.name)) - Number(recent.has(a.name)))
          return (
            <Section title={`Pods (${w.pods.length})`}>
              {sorted.map((p) => {
                const pn = nodes.find((x) => x.id === p.nodeId)
                const notReady = !p.ready
                const isRecent = recent.has(p.name)
                return (
                  <div key={p.name} className="py-1.5 text-sm">
                    <div className="flex items-baseline justify-between gap-3">
                      <span className="truncate text-nb-300" title={p.name}>{p.name}</span>
                      <span className={notReady ? 'shrink-0 text-xs text-warn' : 'shrink-0 text-xs text-nb-500'}>
                        {p.phase}
                        {notReady ? ' · not ready' : ''}
                      </span>
                    </div>
                    <div className="text-xs text-nb-500">
                      {pn ? (
                        <button className="underline hover:text-nb-300" onClick={() => onSelect({ kind: 'node', id: p.nodeId! })}>{pn.name}</button>
                      ) : (
                        p.nodeId || 'not scheduled'
                      )}
                      {p.createdAt && ` · ${ageLabel(p.createdAt)} old`}
                      {p.restarts ? ` · ${p.restarts} restart${p.restarts === 1 ? '' : 's'}` : ''}
                      {isRecent && <span className="text-info"> · recently added (scaling)</span>}
                    </div>
                  </div>
                )
              })}
            </Section>
          )
        })()}
        <Section title="Can it move?">
          <MobilityPanel service={w} onSelectCluster={(id) => onSelect({ kind: 'cluster', id })} />
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
    subtitle = (
      <span className="flex flex-wrap items-center gap-2">
        <StatusDot status={d.status} withLabel />
        <Pill>{DEVICE_KINDS.find((k) => k.value === d.kind)?.label ?? d.kind}{d.count > 1 ? ` ×${d.count}` : ''}</Pill>
      </span>
    )
    body = (
      <>
        <Section title="Identity">
          {fieldProvenance(d.evidence?.kind) && <ProvenanceStrip {...fieldProvenance(d.evidence?.kind)!} />}
          <Row label="Application">{appName(d.applicationId) ?? '—'}</Row>
          <Row label="Site">{d.siteId ? (
            <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'site', id: d.siteId! })}>{siteName(d.siteId)}</button>
          ) : '—'}</Row>
          <Maybe label="Hardware">{d.hardwareModel}</Maybe>
          <Maybe label="Firmware">{d.firmware}</Maybe>
          <KeyValueChips label="Labels" pairs={d.labels} />
        </Section>
        <Section title="Connectivity">
          <Row label="Units">{d.count}</Row>
          <Row label="Protocol">{d.protocol || '—'}</Row>
          <Maybe label="Connectivity">{connLabel(d.connectivity)}</Maybe>
          {gw && (
            <Row label="Attached to">
              <button className="text-accent hover:underline" onClick={() => onSelect({ kind: 'node', id: gw.id })}>{gw.name}</button>
            </Row>
          )}
        </Section>
        <Section title="Discovery">
          <ProvenanceStrip {...recordProvenance(d)} />
        </Section>
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
  } else if (selection.kind === 'platform') {
    const part = platform?.entities.find((x) => x.id === selection.id)
    if (!part) return null
    title = part.name
    editable = false
    subtitle = <PlatformSubtitle part={part} />
    body = <PlatformPanel part={part} platform={platform!} onSelect={onSelect} />
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
          <Section title="Location">
            <Row label="Location"><Place site={s} /></Row>
            <Row label="Coordinates" copy={`${s.lat}, ${s.lng}`}><span className="font-mono text-xs">{s.lat.toFixed(2)}, {s.lng.toFixed(2)}</span></Row>
            {exitIps(cs).length > 0 && (
              <Row label="Exit IP"><span className="flex flex-col gap-1">{exitIps(cs).map((ip) => <IpAddress key={ip} ip={ip} inline />)}</span></Row>
            )}
            <Maybe label="Trust zone · residency">{[s.trustZone, s.dataResidency].filter(Boolean).join(' · ')}</Maybe>
            {!s.trustZone && cs.some((c) => !c.trustZone) && (
              <Row label="Trust zone" wrap>
                <span className="text-warn">
                  Not set: {cs.filter((c) => !c.trustZone).length === cs.length ? 'none of its clusters have one either' : 'some of its clusters have their own, but the rest fall back to this'}. Placement can't tell whether workloads there are allowed to move to a more sensitive zone, or leave for a less trusted one. <Link to="/sites" className="text-accent hover:underline">Set it</Link>.
                </span>
              </Row>
            )}
          </Section>
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
              return <LinkRow key={l.id} label={siteName(other) ?? other} sub={`${bits}${l.source === 'declared' ? ' (declared)' : ''}`} onClick={sites.some((x) => x.id === other) ? () => onSelect({ kind: 'site', id: other }) : undefined} />
            })}
          </Section>
        )}
      </>
    )
  } else if (selection.kind === 'external') {
    const e = externalEndpoints.find((x) => x.id === selection.id)
    if (!e) return null
    const { calls, calledBy } = depSections(e.id)
    title = e.name ?? e.host
    subtitle = <Pill>{e.kind}</Pill>
    editable = false
    body = (
      <>
        <Section title="Identity">
          {e.name && <Row label="Address" copy={e.host}><span className="font-mono text-xs">{e.host}</span></Row>}
          <Maybe label="Port">{e.port ? String(e.port) : undefined}</Maybe>
          <Maybe label="Likely">{e.service ? `${e.service} (guessed from the port)` : undefined}</Maybe>
          <ProvenanceStrip {...externalProvenance(e)} />
          {(e.ips?.length ?? 0) > 1 && (
            <Chips
              label={`Addresses (${e.ips!.length})`}
              items={e.ips}
              title="Every individual address observed that resolved to this one entity - merged here since they're all the same provider's own infrastructure, not separate destinations."
            />
          )}
        </Section>
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
    // The caller's own interface capacity, right next to its throughput below - same resolution and same
    // "undefined rather than a guess" rule as the node view's own per-interface utilization above.
    const ifaceSpeedMbps = callerIfaceSpeedMbps(d.iface, fromSvc?.nodeIds, nodeById)
    const dprov = dependencyProvenance(d)
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
            <Maybe label="Likely">{d.service ? `${d.service} (guessed from the port)` : undefined}</Maybe>
            <ProvenanceStrip
              source={dprov.source}
              sourceBadge={dprov.via && <Pill className="text-[11px] text-nb-400">{dprov.via}</Pill>}
              confidence={{ tone: CONFIDENCE_TONE[d.confidence], label: d.confidence }}
            />
            <Maybe label="Interface">{d.iface}</Maybe>
            <Maybe label="First seen">{ago(d.firstSeen)}</Maybe>
            <Maybe label="Last seen">{ago(d.lastSeen)}</Maybe>
            {d.noise && <Row label="Kind of traffic">{d.noise === 'dns' ? 'DNS (machinery)' : 'System (machinery)'}</Row>}
          </div>
        </Section>
        <Section title="Traffic">
          {seen && s ? (
            <>
              <Maybe label="Throughput">
                {s.bytesPerSec !== undefined && (d.via === 'ebpf' || s.bytesPerSec > 0) ? (
                  <span>
                    {bytesPerSec(s.bytesPerSec)}
                    {ifaceSpeedMbps !== undefined && linkUtilizationPct(s.bytesPerSec, ifaceSpeedMbps) !== undefined && (
                      <span className="text-nb-500" title={`${d.iface}'s own negotiated link speed - the share of it this dependency's own throughput is using, not a cap enforced here.`}>
                        {' '}({linkUtilizationPct(s.bytesPerSec, ifaceSpeedMbps)}% of {d.iface}'s {ifaceSpeedMbps} Mbps)
                      </span>
                    )}
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Connections">{s.connectionsPerMin !== undefined ? `${Math.round(s.connectionsPerMin * 10) / 10} per minute` : undefined}</Maybe>
              <Maybe label="Requests">{s.reqPerSec !== undefined ? `${s.reqPerSec} per second` : undefined}</Maybe>
              <Maybe label="Errors">{s.errorRate !== undefined ? `${(s.errorRate * 100).toFixed(s.errorRate < 0.1 ? 1 : 0)}%` : undefined}</Maybe>
              <Maybe label="p95 latency">{s.p95Ms !== undefined ? `${s.p95Ms} ms` : undefined}</Maybe>
              <Maybe label="TCP round trip">
                {d.rttMs !== undefined ? (
                  <span title="Smoothed RTT sampled from the kernel's own TCP stack for this dependency's traffic - not an active probe, and not the same measurement as Network path's Round trip below.">
                    {rttLabel(d.rttMs)}
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Jitter">
                {d.via === 'ebpf' && d.jitterMs !== undefined ? (
                  <span title="The kernel's own RTT mean-deviation, sampled alongside the round trip above.">{rttLabel(d.jitterMs)}</span>
                ) : undefined}
              </Maybe>
              <Maybe label="Connection setup">
                {d.via === 'ebpf' && d.handshakeMs !== undefined ? (
                  <span title="Time from SYN to ESTABLISHED on this dependency's most recent handshake - a per-connection fact, most telling on a cross-cluster/WAN edge.">{rttLabel(d.handshakeMs)}</span>
                ) : undefined}
              </Maybe>
              <Maybe label={s.lossPct !== undefined ? 'Loss' : 'Retransmits'}>
                {d.via === 'ebpf' ? (
                  s.lossPct !== undefined ? (
                    <span title={`${d.retransmits ?? 0} retransmitted of the segments sent so far; ${Math.round((s.retransmitsPerMin ?? 0) * 10) / 10} per minute`}>
                      {`${s.lossPct < 10 ? s.lossPct.toFixed(1) : Math.round(s.lossPct)}%`}
                    </span>
                  ) : (
                    `${Math.round((s.retransmitsPerMin ?? 0) * 10) / 10} per minute${d.retransmits ? ` (${d.retransmits} total)` : ''}`
                  )
                ) : undefined}
              </Maybe>
              <Maybe label="RTO retransmits">
                {d.via === 'ebpf' && d.rtoRetransmits ? (
                  <span title="The subset of the retransmits above that the RTO timer itself fired for - no ACK at all came back within a full round-trip-plus-backoff, as opposed to a fast retransmit recovering from ordinary reordering without ever stalling the connection. This is the real leading sign of a degrading link.">
                    {`${Math.round((s.rtoRetransmitsPerMin ?? 0) * 10) / 10} per minute (${d.rtoRetransmits} total)`}
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Failed attempts">
                {d.via === 'ebpf' ? (
                  <span title="Connection attempts to/from this dependency that never reached ESTABLISHED - refused, timed out, reset, or unreachable - from the kernel's own socket state, not inferred from timing.">
                    {`${Math.round((s.failedAttemptsPerMin ?? 0) * 10) / 10} per minute${d.failedAttempts ? ` (${d.failedAttempts} total)` : ''}`}
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Window">{s.windowSec ? `${Math.round(s.windowSec / 60) || '<1'} min` : undefined}</Maybe>
              <Maybe label="Buffer drops">
                {d.via === 'ebpf' && d.bufferDrops ? (
                  <span title="Receive-side buffer drops, from the kernel's own per-socket counter - the receiving application not draining its socket fast enough, not the network losing a packet in transit.">
                    {d.bufferDrops} total
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Congestion window">
                {d.via === 'ebpf' && d.cwndSegments !== undefined ? (
                  <span title="The kernel's own current congestion window, in segments - read alongside the pacing rate below to say what's currently bounding this connection's send rate.">
                    {d.cwndSegments} segments
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Pacing rate">
                {d.via === 'ebpf' && d.pacingBps !== undefined ? <span>{bytesPerSec(d.pacingBps)}</span> : undefined}
              </Maybe>
              <Maybe label="Receive window">
                {d.via === 'ebpf' && d.rcvWndBytes !== undefined ? (
                  <span
                    className={d.rcvWndBytes === 0 ? 'text-bad' : undefined}
                    title="The receive window this node is currently advertising to the peer - 0 means this side has told the peer to stop sending because its own receive buffer is not draining fast enough (the application not reading, not the network losing anything)."
                  >
                    {d.rcvWndBytes === 0 ? 'stalled (0)' : bytesTotal(d.rcvWndBytes)}
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Peer window">
                {d.via === 'ebpf' && d.sndWndBytes !== undefined ? (
                  <span
                    className={d.sndWndBytes === 0 ? 'text-bad' : undefined}
                    title="The peer's last-advertised receive window to this node - 0 means the peer stalled this connection, which looks identical to a congested path from this node's own counters above unless this is read too."
                  >
                    {d.sndWndBytes === 0 ? 'stalled (0)' : bytesTotal(d.sndWndBytes)}
                  </span>
                ) : undefined}
              </Maybe>
              <Maybe label="Send buffer">
                {d.via === 'ebpf' && d.wmemQueuedBytes !== undefined && d.sndbufBytes ? (
                  <span
                    className={d.wmemQueuedBytes / d.sndbufBytes >= 0.9 ? 'text-bad' : undefined}
                    title="Bytes queued in this edge's own local send/write queue against its current ceiling (SO_SNDBUF) - near-full means this socket's local send buffer is saturated: the application not writing fast enough to notice, or itself backpressured by a congested path it cannot drain into."
                  >
                    {`${bytesTotal(d.wmemQueuedBytes)} / ${bytesTotal(d.sndbufBytes)} (${Math.round((d.wmemQueuedBytes / d.sndbufBytes) * 100)}% full)`}
                  </span>
                ) : undefined}
              </Maybe>
              <DependencyTrend dependencyId={d.id} />
              {d.stale && <p className="mt-1 text-xs text-warn">No traffic since {ago(d.lastSeen)}.</p>}
            </>
          ) : (
            <p className="text-sm text-nb-500">{seen ? 'Seen, but no rates were reported.' : 'No traffic has been seen for this; it comes from what was declared. Turn on the traffic observer in Discovery to check it.'}</p>
          )}
          <Maybe label="TLS server name">
            {d.sniHost ? (
              <span title="The hostname this dependency's TLS ClientHello named (SNI), read before the handshake encrypts anything - only eBPF sees this.">{d.sniHost}</span>
            ) : undefined}
          </Maybe>
          <Chips
            label="DNS queries"
            items={d.dnsQueryNames}
            title="Distinct domain names resolved toward this dependency's destination - only eBPF sees this."
          />
          <Maybe label="DNS response">
            {d.via === 'ebpf' && d.dnsRttMs !== undefined ? (
              <span title="Time from query to matching response, correlated by transaction ID - UDP only, only eBPF sees this.">{rttLabel(d.dnsRttMs)}</span>
            ) : undefined}
          </Maybe>
        </Section>
        {(verdict || d.meshBypass || d.tlsHandshake) && (
          <Section title="Service mesh">
            {d.meshBypass && (
              <>
                <Row label="Observed"><span style={{ color: VERDICT_COLOR.bypassed }}>bypasses proxy</span></Row>
                <p className="text-xs text-nb-400">
                  Traffic was seen leaving {f?.label ?? 'the caller'} directly, without passing through its mesh proxy - not inferred from configuration, but read off the wire (eBPF).
                </p>
              </>
            )}
            {verdict && !d.meshBypass && (
              <>
                <Row label="Encryption"><span style={{ color: VERDICT_COLOR[verdict.state] }}>{verdict.short}</span></Row>
                <p className="text-xs text-nb-400">{verdict.detail}</p>
                <p className="mt-1.5 text-xs text-nb-500">Inferred from configuration (proxies, ports kept out of them, and the mesh's mutual-TLS policy). Nothing here was measured on the wire.</p>
              </>
            )}
            {d.tlsHandshake && (
              <>
                <Row label="TLS handshake">
                  <span
                    style={{ color: d.tlsHandshake === 'failed' ? VERDICT_COLOR.bypassed : undefined }}
                    title="Read off the wire (eBPF): did this edge's own TLS handshake visibly fail (a plaintext alert record) or complete (sustained application data, no alert) - never a certificate identity or validity check, which this cannot see at all."
                  >
                    {d.tlsHandshake === 'failed' ? 'visibly failed' : 'completed'}
                  </span>
                </Row>
                <p className="text-xs text-nb-500">
                  Only tells whether the handshake itself visibly broke on the wire - not certificate identity, not certificate validity, and not whether this traffic went through a mesh proxy at all (see "bypasses proxy"/"Encryption" above for that).
                </p>
              </>
            )}
          </Section>
        )}
        {d.tunnelLink && (
          // Mirrors what EdgeHoverCard already shows on a hover of this exact dependency's edge (see
          // EdgeData.tunnelLink / Dependency.tunnelLink's own doc) - this used to be the one place a
          // click showed nothing a hover already had. Never carries nodeA/nodeB, addressA/addressB or
          // the flow rollup (those are ClusterLink-only, see the aggregate on the cluster's own "Cluster
          // links" section instead), so TunnelEvidence renders only Via/Encryption/Redundancy here.
          <Section title="Cluster link">
            <TunnelEvidence via={d.tunnelLink.via} encryption={d.tunnelLink.encryption} redundancy={d.tunnelLink.redundancy} />
            {/* This dependency's own measured MSS, not the cluster-wide average TunnelEvidence's
                avgMssBytes prop carries (that one is ClusterLink-only, shown in the cluster's own
                "Cluster links" section above) - the specific number for this specific edge, which is
                exactly what crossing a confirmed tunnel makes worth looking at (see Dependency.mssBytes'
                own doc). */}
            {!!d.mssBytes && (
              <Row label="Effective MSS">
                <span title="This edge's own current effective segment size (tcp_sock.mss_cache) - shrunk below the plain interface MTU by this tunnel's own encapsulation overhead (VXLAN/WireGuard/GRE headers)">
                  {d.mssBytes}B
                </span>
              </Row>
            )}
            <p className="mt-1.5 text-xs text-nb-500">
              This dependency's own traffic crosses a confirmed overlay tunnel between {clusterName(d.tunnelLink.fromCluster)} and {clusterName(d.tunnelLink.toCluster)}.
            </p>
          </Section>
        )}
        {across && (
          <Section title="Network path">
            {q ? (
              <>
                <Row label="Round trip"><span title="TCP connect time, median">{rttLabel(q.rttMs)}</span></Row>
                <Row label="Loss"><span className={lossBand(q.lossPct) === 'hot' ? 'text-bad' : lossBand(q.lossPct) === 'warn' ? 'text-warn' : undefined}>{q.lossPct.toFixed(q.lossPct < 10 ? 1 : 0)}% of connection attempts failed</span></Row>
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
    // modal-pop (index.css): the same fade/scale ease-in every other panel in this app already uses on
    // open, rather than snapping into place - this component mounts fresh each time selection goes from
    // null to something (the early return above), so this only ever plays on that actual open, not on
    // every click to a *different* entity while already open (same DOM node, just new content then).
    //
    // overscroll-contain: without it, a wheel/trackpad scroll that bottoms out this panel's own
    // overflow-y-auto keeps going - scroll chaining - into whatever sits behind/beneath it: the React
    // Flow canvas, which treats that leftover scroll delta as a pan/zoom gesture. The visible symptom was
    // the whole canvas appearing to "jump" the moment the sidebar finished scrolling, since nothing told
    // the browser this panel's own scroll boundary was the end of the gesture, not a handoff to whatever's
    // underneath it.
    <aside className={clsx('modal-pop fixed inset-x-0 bottom-0 z-30 flex flex-col overflow-y-auto overscroll-contain rounded-t-xl border-t border-nb-850 bg-nb-920 shadow-2xl transition-[max-height] duration-200 motion-reduce:transition-none lg:static lg:max-h-none lg:w-80 lg:min-h-0 lg:shrink-0 lg:rounded-none lg:border-l lg:border-t-0 lg:shadow-none', peek ? 'max-h-[30vh]' : 'max-h-[65vh]')}>
      <button
        type="button"
        onClick={() => setPeek((v) => !v)}
        aria-expanded={!peek}
        aria-label={peek ? 'Show all details' : 'Show only the header, to see the canvas'}
        className="sticky top-0 z-10 flex shrink-0 justify-center rounded-t-xl bg-nb-920 py-1.5 text-nb-500 hover:text-nb-300 lg:hidden"
        data-testid="inspector-peek"
      >
        {peek ? <ChevronUp size={ICON_MD} aria-hidden="true" /> : <ChevronDown size={ICON_MD} aria-hidden="true" />}
      </button>
      <div className="flex items-start justify-between gap-3 px-5 pb-4 pt-1 lg:pt-4">
        <div className="min-w-0">
          <h2 className="truncate text-base font-medium text-nb-300">{title}</h2>
          <div className="mt-1.5">{subtitle}</div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {editable && !inPast && (
            <Button variant="ghost" size="sm" onClick={() => onEdit(selection)} aria-label="Edit">
              <Pencil size={ICON_SM} /> Edit
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close inspector">
            <X size={ICON_MD} />
          </Button>
        </div>
      </div>
      {body}
      {(selection.kind === 'cluster' || selection.kind === 'node' || selection.kind === 'service') && <EvidenceSection key={`ev:${selection.kind}:${selection.id}`} kind={selection.kind} id={selection.id} />}
      {selection.kind !== 'platform' && <EntityHistory key={`${selection.kind}:${selection.id}`} kind={selection.kind} id={selection.id} />}
    </aside>
  )
}
