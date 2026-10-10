/**
 * Projects the topology model onto a React Flow graph.
 *
 * One model, several planes: `buildGraph` is the only place that knows how a
 * "view" turns entities into boxes and edges, so adding a new plane (network,
 * data-flow, cost…) means adding a branch here + optionally a node component.
 */
import { callerIfaceSpeedMbps, count, placeLabel } from './present'
import { buildPodsView, type PodsView } from './pods'
import { MarkerType, Position, type Edge, type Node } from '@xyflow/react'
import { deepEqual } from './discovered'
import { isObserved } from './observed'
import { buildNetworks, networkSentence, type Network } from './networks'
import { clusterMeshLine, connectionVerdict, inMesh, meshName, proxyWords, type MeshVerdict } from './mesh'
import { clusterLoad, lossBand, nodeLoad, pathQuality, peakLoad, type ClusterLoad, type NodeLoad, type PathQuality } from './metrics'
import { alertOfLoad, alertOfPods, alertOfStatus, PRESSURE_WARN, statusSentence, worstAlert, type Alert, type Detail } from './detail'
import {
  DEVICE_KINDS,
  TIER_ORDER,
  type Cluster,
  type ClusterLink,
  type Dependency,
  type DependencyStats,
  type Device,
  type DeviceKind,
  type ExternalEndpoint,
  type GroupBy,
  type MachineKind,
  type MachineNode,
  type Path,
  type Service,
  type ServiceKind,
  type Site,
  type Status,
  type Tier,
  type Topology,
  type ViewKind,
} from './types'

/* ---------- node data ---------- */
export type GroupData = {
  kind: 'group'
  /** Cluster id, tier name, or (device / external groups) site id · 'all' · 'none'. */
  entityId: string
  groupBy: GroupBy
  /** Set for groups that are not clusters or tiers. */
  extra?: 'devices' | 'external'
  title: string
  subtitle: string
  /** Distribution of the cluster, for its logo. */
  distribution?: string
  /** ISO country code of the site, for its flag. */
  country?: string
  tier: Tier
  status: Status
  stats: string
  empty: string
  /** How loaded a cluster is, from what its nodes report. Only cluster groups have it. */
  load?: ClusterLoad
  /** Where the cluster or site is ("Frankfurt, Germany"): what Calm keeps under the name, in place of the distribution and counts. */
  place?: string
  /** Set when the box itself is not fine (its own status, a resource under pressure, a node or service down). A card's
   *  problem is its own: the box does not repeat it. Calm tints the box and says what is wrong in `note`. */
  alert?: Alert
  /** What is wrong with the box, in a few words ("1/2 nodes ready", "Memory 92% requested"); set with `alert`. */
  note?: string
  /** Service mesh overlay: what mesh the cluster runs, e.g. "Istio 1.22 · sidecar · mTLS permissive". */
  mesh?: { label: string; tone: 'good' | 'warn' | 'bad'; title: string }
  /** Set on a real cluster box (groupBy 'cluster' only) when one of its approved agents has at least one
   *  telemetry signal actually running - a "local operator" is a property of that agent, not a separate
   *  node on the canvas (see nodes.tsx's antenna badge). */
  localTelemetry?: { layers: string[] }
  /** CNI and ingress controller the agent detected running in the cluster (Cluster.cni/.ingress -
   *  interpret.go's detectAddons), when either is known. Surfaced as a small badge on a real cluster box
   *  (see nodes.tsx) instead of only the inspector's text row, so it's visible without a click - see this
   *  session's own note on why a full separate networking view isn't warranted yet (no routing-rule data
   *  behind it to actually draw), but the already-detected name is cheap to show here. */
  networking?: { cni?: string; ingress?: string }
  /** Calm: the networks this box sits on (a shared subnet, an overlay), drawn as quiet chips in its header rather than as lines.
   *  `text` is the sentence to read ("Shares subnet 10.30.0.0/16 with polaris-edge"); `members` are the boxes (React Flow ids, this one
   *  included) on the network, which light together when it is hovered or one of them is hovered or selected. */
  networks?: { id: string; kind: 'overlay' | 'subnet'; via: string; text: string; members: string[] }[]
}

export type CardData = {
  kind: 'service' | 'machine' | 'device' | 'external'
  entityId: string
  title: string
  subtitle: string
  meta: string
  status: Status
  tier: Tier
  clusterName: string
  machineKind?: MachineKind
  deviceKind?: DeviceKind
  /** Controller kind for a `service` card: Deployment, StatefulSet, DaemonSet or Job. */
  serviceKind?: ServiceKind
  /** Units a device group stands for (used for the group total). */
  units?: number
  control?: boolean
  /** Services drawn inside a machine card (infrastructure view overlay). */
  chips?: { id: string; name: string }[]
  /** Placement advice: where this service would be better off (a cluster name), when the engine has an opinion. */
  hint?: string
  /** Replicas wanted and ready, for a service that is not fully up. */
  notReady?: string
  /** Service mesh overlay: how the mesh treats this service. `tone` picks the chip's colour. */
  mesh?: { label: string; tone: 'in' | 'control' | 'out'; title: string }
  /** Chain layout only, on a service whose name another service shares: its cluster, drawn under the title
   *  even when zoomed far out (the subtitle that names it is hidden then). */
  clusterTag?: string
  /** Machine cards only, node-probe-only facts worth a glance without opening the Inspector: whether the
   *  machine can run without mains power, and the fastest physical uplink the probe saw (the max of
   *  MachineNode.networkInterfaces' speedMbps, not any one interface in particular - which one is fastest
   *  is a detail the Inspector's own per-interface list already covers). */
  hardware?: { hasBattery?: boolean; nicMbps?: number }
  /** Set when the card is not fine (status, pods not ready, a machine under pressure): Calm draws it in the state colour with
   *  `note` under its name, and the layout gives it that line; a card without one is as small as its name. */
  alert?: Alert
  /** What is wrong, in a few words ("1 crash-looping · 1 not ready", "Memory 92% requested"); set with `alert`. */
  note?: string
  /** Machine cards only: how much of the machine's CPU, memory and pod slots is already promised. */
  load?: NodeLoad
  /** `service` cards only: the pod rail, the "27/30 ready" summary and the popover's rows (see lib/pods.ts).
   *  Absent when no per-pod facts were collected (older agent tier, or no pods up): the card then keeps just
   *  the replica count and, if some are not ready, the "x/y ready" chip. */
  pods?: PodsView
}

export type GroupNode = Node<GroupData, 'boundary'>
export type CardNode = Node<CardData, 'card'>

export type NamespaceData = {
  kind: 'namespace'
  /** Unique across the graph: the cluster's group key plus the namespace. */
  entityId: string
  clusterId: string
  namespace: string
  /** How many cards sit inside, for the header. */
  count: number
  status: Status
  tier: Tier
}
export type NamespaceNode = Node<NamespaceData, 'namespace'>

export type TopoNode = GroupNode | CardNode | NamespaceNode

export type EdgeData = {
  crossGroup: boolean
  aggregated: boolean
  from: string
  to: string
  sources?: string[]
  confidence?: string
  /** Seen in traffic, not only declared. */
  observed?: boolean
  /** Seen once, but not for a long time. */
  stale?: boolean
  /** 0..1, how much traffic (log scale); drives the line width and the speed of the moving dashes. */
  weight?: number
  /** Measured round trip and loss between the two clusters, when the ends are in different ones. */
  quality?: PathQuality
  /** Service mesh overlay: what the mesh does to this connection, inferred from configuration. */
  mesh?: MeshVerdict
  /** Only set on a cross-cluster dependency: 'direct' when the target is reached over a flat/mesh-federated
   *  network route, 'gateway' when the call has to go out through the target's own external exposure
   *  (ingress, node port or load balancer) to reach it at all. See makeEdge's own doc for why this is
   *  meaningless for a same-cluster call. */
  route?: 'direct' | 'gateway'
  /** Calm: which of up to five tracks (-2..2) of a gutter this bundle line runs on, so two lines that share a gutter are two lines and not one. */
  track?: number
  /** Pixels to shift this line's source end sideways (perpendicular to the caller->callee line). */
  sourceOffset?: number
  /** Pixels to shift this line's target end sideways (perpendicular to the caller->callee line). Independent from sourceOffset, so a line can fan out at a busy node while still landing cleanly at a quiet one. */
  targetOffset?: number
  /** Rolling-window traffic numbers for the hover card (EdgeHoverCard) - the same Dependency.stats the
   *  Inspector already shows once you click the line, surfaced a click earlier. Unset for a group<->group
   *  telemetry edge (regional operators), which isn't a traffic measurement. */
  stats?: DependencyStats
  /** How traffic was observed - conntrack can't see bytes, so a conntrack-only edge's bytesPerSec (if any)
   *  is not trustworthy the way an eBPF one is; the hover card applies the same "eBPF, or a real positive
   *  number" gate the Inspector already uses for this exact reason. */
  via?: 'ebpf' | 'conntrack'
  /** The caller's physical network interface for this dependency's traffic (Dependency.iface) - eBPF only,
   *  same as on Dependency itself. Surfaced on the hover card so it doesn't take a click to see. */
  iface?: string
  /** `iface`'s own rated speed (present.ts's callerIfaceSpeedMbps), when the calling service's node(s)
   *  unambiguously report one for an interface of that name - the same capacity the Inspector's node view
   *  already shows per-interface, paired here with this one dependency's own throughput instead of a
   *  node's total. Undefined whenever that's ambiguous or unknown, never guessed. */
  ifaceSpeedMbps?: number
  /** Cumulative TCP segments retransmitted over the edge's life (Dependency.retransmits) - eBPF only, 0 on
   *  a conntrack-only edge means "not measured", not "no loss". The hover card pairs this with
   *  `stats.retransmitsPerMin` the same way the Inspector already does. */
  retransmits?: number
  /** Latest smoothed TCP round-trip sample (Dependency.rttMs) - a gauge, not this edge's Network path
   *  measurement (that's `quality.rttMs`, a different, cluster-to-cluster figure). */
  rttMs?: number
  /** Latest TCP RTT mean-deviation sample (Dependency.jitterMs) - a gauge, sampled alongside rttMs. */
  jitterMs?: number
  /** Most recent SYN->ESTABLISHED handshake time (Dependency.handshakeMs) - a gauge, a per-connection
   *  fact rather than a per-byte one, so shown once per dependency rather than as a rate. */
  handshakeMs?: number
  /** Cumulative connection attempts that never reached ESTABLISHED (Dependency.failedAttempts) - eBPF
   *  only, same "0 means not measured" rule as retransmits. Pairs with `stats.failedAttemptsPerMin`; can
   *  be non-zero even when connections/bytesPerSec are entirely absent, which is exactly the "this
   *  dependency is never actually reachable" case worth surfacing. */
  failedAttempts?: number
  /** The SNI hostname seen in this edge's TLS traffic (Dependency.sniHost) - eBPF only, a gauge. */
  sniHost?: string
  /** Distinct domain names seen resolved toward this edge's destination (Dependency.dnsQueryNames) -
   *  eBPF only, newest-first, accumulates rather than being overwritten. */
  dnsQueryNames?: string[]
  /** How long a DNS response took to arrive after its matching query (Dependency.dnsRttMs) - eBPF only,
   *  a gauge, only ever set on the pod<->resolver edge itself. */
  dnsRttMs?: number
  /** A seen link that is losing connection attempts (a link that was only declared is never a problem: nothing was measured on it). Calm gives
   *  a bundle that holds one the state colour and a label saying how bad the path is, at rest; the calls themselves wait for the focus. */
  problem?: boolean
  /** A bundle's own count of the dependencies it stands for. */
  count?: number
  /** Calm, application view: 'bundle' is the one line between two boxes that stands for every dependency across them; 'detail' is one of those
   *  dependencies, drawn only for the focus. Unset for a line that crosses nothing. */
  role?: 'bundle' | 'detail'
  /** The nodes whose hover or selection lights the bundle (and rings the cards at its ends): both boxes and every call's two ends. */
  focusIds?: string[]
  /** Aggregated (group<->group) edges only: how many of the bundled dependencies were actually seen in
   *  traffic, out of the total the label already counts - the hover card's "(N seen in traffic)" aside. */
  activeCount?: number
  /** Aggregated (group<->group) edges only: how many of the bundled dependencies use each protocol
   *  (Dependency.protocol, e.g. "HTTP", "gRPC", "tcp:5432"'s own "tcp") - a single dependency's own edge
   *  never needs this, since its one label already names its one protocol exactly. Unset (rather than a
   *  one-entry map) when every bundled dependency happens to share the same protocol, so the hover card's
   *  existing single-count line is left alone in the common case. */
  protocols?: Record<string, number>
  /** Set only on a cluster<->cluster ClusterLink edge (never alongside a dependency's own fields above) -
   *  a confirmed network-level relationship, independent of any traffic or declared dependency between the
   *  two clusters. `via` names the specific evidence (a tunnel's name/kind, or the shared subnet prefix) -
   *  see ClusterLink's own doc for exactly what this is, and is not, built from. `redundancy` is how many
   *  independently corroborating node pairs back it (more than 1 means more than one path, not a single
   *  point of failure). `flowsObserved`/`avgRttMs`/`avgLossPct` roll up the live dependency flows actually
   *  matched onto this link's confirmed tunnel interface(s) - see ClusterLink's own doc; undefined/0 means
   *  no matching flow was observed yet, not that none exists, and only ever set for an "overlay" link.
   *  `encryption` classifies that same tunnel's driver as "encrypted" or "plaintext" - an inference from
   *  the driver type alone, never a measured fact; also only set for an "overlay" link. */
  clusterLink?: { kind: ClusterLink['kind']; via: string; redundancy: number; fromNode?: string; toNode?: string; fromAddress?: string; toAddress?: string; flowsObserved?: number; avgRttMs?: number; avgLossPct?: number; avgRtoRetransmitsPerMin?: number; avgMssBytes?: number; encryption?: ClusterLink['encryption'] }
  /** Set only on a dependency edge (never alongside clusterLink above) whose own Dependency.tunnelLink
   *  matched a confirmed overlay ClusterLink - see Dependency.tunnelLink's own doc in types.ts. The same
   *  evidence shape as clusterLink's via/redundancy/encryption, minus the aggregate flow/RTT/loss fields,
   *  which stay exclusive to the standalone ClusterLink edge (an aggregate across every dependency
   *  crossing it, not a fact about this one dependency alone). */
  tunnelLink?: { fromCluster: string; toCluster: string; via: string; redundancy: number; encryption?: ClusterLink['encryption'] }
}
export type TopoEdge = Edge<EdgeData>

export interface GraphOptions {
  /** 'calm' (the default) sizes a card that is fine to its name alone; 'full' gives every card the room for everything. */
  detail?: Detail
  view: ViewKind
  groupBy: GroupBy
  servicesOnNodes: boolean
  links: boolean
  /** Application view: also draw devices (IoT) and external endpoints that dependencies point at. */
  devices?: boolean
  /** Also draw traffic that is machinery (DNS lookups, system namespaces); off by default. */
  noise?: boolean
  /** Measured paths, to put round trip and loss on links between clusters. */
  paths?: Path[]
  /** Show the service mesh: its control plane, which services are in it, and what it does to each connection. */
  mesh?: boolean
  /** Service id → name of the cluster the placement engine would move it to. */
  hints?: Map<string, string>
  /** Application view, grouped by cluster: draw a sub-box per namespace inside each cluster's box. */
  namespaces?: boolean
  /** Application view: lay services out as a left-to-right dependency chain instead of grouping them into
   * cluster/tier boxes. One flat ranking across every cluster - cross-cluster calls are already first-class
   * (see the `crossCluster` flag on Dependency), so a chain that also respected cluster boundaries would
   * fight the very ordering this is for. Each card still names its cluster in the subtitle. */
  chain?: boolean
  /** Cluster id → its active local-operator summary (which layers are actually live, and which agent to
   *  open when the badge is clicked). Computed by the caller from `agents`/`installedTelemetry`, not part
   *  of the core Topology model - purely a canvas annotation, the same role `hints` plays for placement. */
  localOperators?: Map<string, { layers: string[]; agentId: string }>
  /** Confirmed overlay/subnet relationships between cluster pairs - derived server-side fresh on every
   *  poll (see ClusterLink's own doc), passed in the same way `paths` is rather than living on Topology
   *  itself, since neither is ever part of the stored workspace. */
  clusterLinks?: ClusterLink[]
}

export const groupId = (key: string) => `g:${key}`
export const cardId = (id: string) => `c:${id}`

/* ---------- layout constants ---------- */
// PAD/HEADER (and their NS_ twins below) are exported alongside the layout functions that use them:
// they're also the exact margins a card's own drag `extent` reuses (see buildGraph's two card-push sites
// below), so a test asserting on that extent needs the real constants, not a copy that could quietly drift.
export const PAD = 20
// The box header (title, subtitle/country/mesh line, and - when a cluster has load - a fourth load-meter row)
// runs up to about 72px tall at normal zoom; HEADER is the y where the first row of children starts, so it needs
// real breathing room past that, not just enough to avoid overlap.
export const HEADER = 96
// The header's own subtitle/load-meter row (LoadRow, Load.tsx) is a `flex flex-wrap` line, and it can carry
// up to five items at once: CPU/Mem/Pods mini-bars plus, when a cluster is unhealthy, "N/M nodes ready" and
// "N services not fully up" warning text. An earlier pass here only accounted for two items (CPU/Mem) and
// set this floor to 352px - comfortably wide enough for that case, but a real cluster reporting all three
// bars *and* both warning strings still wrapped to three lines at 352px, overflowing HEADER's 96px budget
// by over 10px and landing back on top of the card below (confirmed by forcing that exact worst-case content
// through a live, real-browser render and bisecting the width where it drops from three wrapped lines to
// two: measured to need at least ~430px). Rather than chase every future combination of LoadRow content
// with ever-finer width tuning, or widen HEADER itself (which would waste space in the far more common
// case where everything fits on one line), the floor is set well past that measured two-line breakpoint -
// with real margin, same reasoning as before, so a slightly longer subtitle or a different font stack
// doesn't reopen the gap. This intentionally does not chase full single-line fitment for the five-item
// worst case (that needs ~650px, wide enough to look absurd on a single-node box) - two wrapped lines fits
// inside HEADER's budget with room to spare, which is enough.
export const MIN_GROUP_HEADER_WIDTH = 460
const GAP_X = 72
const GAP_Y = 44
const GROUP_GAP_X = 64
const ROW_GAP = 150
/** Calm rows sit closer: the lines between boxes run in the gap, which only needs room for a lane and a label. */
const CALM_ROW_GAP = 72
// Widened from 244/248 per the UI/UX pass: several common service/device names ("inference-regional",
// "stream-aggregator", "Vibration sensor") were truncating hard even with visible slack around the card -
// the icon, status dot, and optional meta/hint column on the right all eat into the title's real estate
// before a single letter of a long name gets drawn. Sized (via a real measurement against the sample data,
// not a guess) so a normal compound service name like "stream-aggregator" renders in full; a genuinely long
// device name ("Temperature sensors" and beyond) can still truncate; there's no fixed width that fits every
// arbitrary name, which is exactly why the native `title=` tooltip (added in an earlier pass) exists as the
// fallback rather than chasing zero truncation by growing every card to accommodate the longest outlier.
export const APP_CARD = { w: 300, h: 68 }
/* Calm lays everything on one 8px grid: every card is the same size, whatever it says (its name, and under it the one line that says what is
   wrong when something is), every box has the same header slot, and boxes that share a row share a width and a height. */
/** The one card of the Calm canvas: a 32px icon tile between two 8px margins, and wide enough for a name like "event-aggregator" at the size the zoomed-out canvas draws it. A problem's line fits inside it, so a card never changes height. */
export const CALM_CARD = { w: 304, h: 48 }
export const CALM_CARD_H = CALM_CARD.h
/** A box's header: the same anatomy as a card (tile, name, one line, status), then 24px of air before the first row of cards, room for the note line of a box that has one. */
export const CALM_HEADER = 72
const CALM_PAD = 16
const CALM_GAP_X = 40
const CALM_GAP_Y = 24
/** A Calm row wraps rather than grow wider than this. */
const CALM_ROW_MAX = 1440
const CALM_BOX_GAP = 64
/** Two columns of cards in a box at most: a box with more cards grows taller, so every box can share one width and the gaps between boxes line up from row to row. */
const CALM_COLS = 2
/** A box is never narrower than this, so its header has room for a name, a network chip and a status mark however few cards it holds. */
const CALM_MIN_W = 416
// A card that lists the services on a machine (a choice made in Options) keeps them in view: 10px of padding, then the chip rows.
const CALM_BLOCK = 10
const CALM_CHIP_ROW = 24
// Exported so a test can assert a card's own height reserves room for whichever extra badge row(s) its data ends up rendering.
export const MACHINE_CARD = { w: 288, h: 84 }
const CHIP_ROW = 22
// A namespace sub-box nests one level inside a cluster box: a little padding and a short header for its
// name, then the same card grid a cluster box would use on its own.
export const NS_PAD = 14
export const NS_HEADER = 32
const NS_GAP_Y = 22

export interface Box {
  x: number
  y: number
  w: number
  h: number
}

interface Item {
  id: string // react-flow id
  data: CardData
  w: number
  h: number
  /** Set for service cards (application view): which namespace to nest it under when that option is on. */
  namespace?: string
}

/** Rows 0-2 are the cluster tiers; devices sit below the far edge, external endpoints below that - the row order mirrors
 * "how far this is from the workload itself". */
const DEVICE_ROW = 3
const EXTERNAL_ROW = 4

interface GroupAcc {
  key: string
  row: number
  tier: Tier
  cluster?: Cluster
  /** Device / external groups: what to show in the header. */
  extra?: { kind: 'devices' | 'external'; entityId: string; title: string; subtitle: string; place?: string; country?: string; status?: Status }
  items: Item[]
}

interface PlacedChild {
  item: Item
  x: number
  y: number
  h: number
}

interface NsBox {
  namespace: string
  x: number
  y: number
  w: number
  h: number
  children: PlacedChild[]
}

interface Placed {
  g: GroupAcc
  w: number
  h: number
  /** Where the first row of cards starts: below the box's own header. */
  header: number
  children: PlacedChild[]
  /** The width of what is inside, before the box is widened to match its row. */
  inner: number
  /** Set instead of (never alongside) `children` when this group nests its items under namespace sub-boxes. */
  nsBoxes?: NsBox[]
}

/** The room cards are laid out in: Full's wide gaps and four columns, or Calm's grid. */
interface Grid { pad: number; gapX: number; gapY: number; maxCols: number }
const FULL_GRID: Grid = { pad: PAD, gapX: GAP_X, gapY: GAP_Y, maxCols: 4 }
const CALM_GRID: Grid = { pad: CALM_PAD, gapX: CALM_GAP_X, gapY: CALM_GAP_Y, maxCols: CALM_COLS }

/** One row of cards, left to right, wrapping at `cols`: shared by a cluster box and a namespace sub-box. */
function packItems(items: Item[], headerY: number, pad = PAD, stretch = true, grid: Grid = FULL_GRID): { w: number; h: number; children: PlacedChild[] } {
  const n = items.length
  const cols = Math.max(1, Math.min(grid.maxCols, Math.ceil(Math.sqrt(n))))
  const cw = items[0]?.w ?? 0
  const children: PlacedChild[] = []
  let y = headerY
  for (let i = 0; i < n; i += cols) {
    const slice = items.slice(i, i + cols)
    const rowH = Math.max(...slice.map((s) => s.h))
    // Full: every card in a row is as tall as the tallest, so a row reads as one band; the card keeps its header at the top
    // (see Card), so a shorter card's status dot and title stay level with its neighbours'. Calm keeps each card its own height,
    // top-aligned: a fine card next to one with a problem is not stretched into an empty box.
    slice.forEach((item, j) => children.push({ item, x: pad + j * (cw + grid.gapX), y, h: stretch ? rowH : item.h }))
    y += rowH + grid.gapY
  }
  const usedCols = Math.min(cols, n)
  const w = n === 0 ? 0 : pad * 2 + usedCols * cw + (usedCols - 1) * grid.gapX
  const h = n === 0 ? headerY : y - grid.gapY + pad
  return { w, h, children }
}

/**
 * Buckets a cluster's cards by namespace (in the order they already come in - callers sort services by
 * namespace first, so this does not need to sort again) and stacks the sub-boxes vertically. Every sub-box
 * gets the same width, the widest one's, so the stack reads as one aligned column rather than a jumble.
 */
function layoutNamespaces(items: Item[], stretch: boolean, grid: Grid = FULL_GRID): { w: number; h: number; boxes: NsBox[] } {
  const byNs = new Map<string, Item[]>()
  for (const it of items) {
    const key = it.namespace || 'no namespace'
    if (!byNs.has(key)) byNs.set(key, [])
    byNs.get(key)!.push(it)
  }
  const laidOut = [...byNs.entries()].map(([namespace, its]) => ({ namespace, its, ...packItems(its, NS_HEADER, NS_PAD, stretch, grid) }))
  const w = Math.max(0, ...laidOut.map((b) => b.w))
  let y = 0
  const boxes: NsBox[] = laidOut.map((b) => {
    const box: NsBox = { namespace: b.namespace, x: 0, y, w, h: b.h, children: b.children }
    y += b.h + NS_GAP_Y
    return box
  })
  return { w, h: Math.max(0, y - NS_GAP_Y), boxes }
}

/** The services of a cluster that are drawn as cards, so the cluster's "not fully up" count and its "N services"
 *  count are about the same set (the mesh's own workloads are hidden without the overlay; the Infrastructure
 *  view draws no service cards, so it keeps the whole cluster). */
function shownServices(clusterId: string, items: Item[], view: GraphOptions['view'], byCluster: Map<string, Service[]>): Service[] {
  const all = byCluster.get(clusterId) ?? []
  if (view !== 'application') return all
  const drawn = new Set(items.map((i) => i.data.entityId))
  return all.filter((s) => drawn.has(s.id))
}

const worstStatus = (ss: Status[]): Status => {
  for (const s of ['offline', 'degraded', 'unknown', 'healthy'] as const) if (ss.includes(s)) return s
  return 'unknown'
}

export function buildGraph(topology: Topology, o: GraphOptions): { nodes: TopoNode[]; edges: TopoEdge[] } {
  // Machinery traffic (DNS, kube-system) would bury the applications' own edges; it is opt-in.
  const t: Topology = o.noise ? topology : { ...topology, dependencies: topology.dependencies.filter((d) => !d.noise) }
  const calm = o.detail === 'calm'
  if (o.view === 'application' && o.chain) return buildChainGraph(t, o)
  const clusterById = new Map(t.clusters.map((c) => [c.id, c]))
  const serviceById = new Map(t.services.map((w) => [w.id, w]))
  const siteById = new Map(t.sites.map((s) => [s.id, s]))
  const nodeById = new Map(t.nodes.map((n) => [n.id, n]))

  // Per-cluster/per-node indices, built once (O(nodes+services)) instead of the per-group `.filter()` over
  // the FULL nodes/services arrays this used to do below (O(groups * (nodes+services)) - noticeable once a
  // deployment has more than a handful of clusters, since every group re-scans everyone else's nodes too).
  const nodesByCluster = new Map<string, MachineNode[]>()
  for (const n of t.nodes) {
    const arr = nodesByCluster.get(n.clusterId)
    if (arr) arr.push(n)
    else nodesByCluster.set(n.clusterId, [n])
  }
  const servicesByCluster = new Map<string, Service[]>()
  const servicesByNodeId = new Map<string, { id: string; name: string }[]>()
  for (const s of t.services) {
    const arr = servicesByCluster.get(s.clusterId)
    if (arr) arr.push(s)
    else servicesByCluster.set(s.clusterId, [s])
    for (const nid of s.nodeIds) {
      const chips = servicesByNodeId.get(nid)
      const chip = { id: s.id, name: s.name }
      if (chips) chips.push(chip)
      else servicesByNodeId.set(nid, [chip])
    }
  }
  const clusterCountByTier = new Map<Tier, number>()
  for (const c of t.clusters) clusterCountByTier.set(c.tier, (clusterCountByTier.get(c.tier) ?? 0) + 1)

  /* 1. Build the items (cards) of the chosen plane, keyed by their group. */
  const groups = new Map<string, GroupAcc>()
  const ensureGroup = (c: Cluster) => {
    const key = o.groupBy === 'cluster' ? c.id : c.tier
    if (!groups.has(key)) groups.set(key, { key, row: TIER_ORDER[c.tier], tier: c.tier, cluster: o.groupBy === 'cluster' ? c : undefined, items: [] })
    return groups.get(key)!
  }
  // Empty clusters still get a (placeholder) group so they are visible.
  t.clusters.forEach(ensureGroup)

  const groupKeyOfCluster = (cid: string) => {
    const c = clusterById.get(cid)
    return c ? (o.groupBy === 'cluster' ? c.id : c.tier) : undefined
  }
  /** Which group each card ended up in, by entity id (ids are unique across entity kinds). */
  const groupOfEntity = new Map<string, string>()

  if (o.view === 'application') {
    const sorted = [...t.services].sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name))
    for (const w of sorted) {
      const c = clusterById.get(w.clusterId)
      if (!c) continue
      // The mesh's own workloads (istiod, ztunnel, gateways) are machinery: shown only with the mesh overlay.
      if (w.mesh?.controlPlane && !o.mesh) continue
      const g = ensureGroup(c)
      g.items.push(serviceItem(w, c, o.groupBy === 'tier', o.hints?.get(w.id), o.mesh, nodeById, serviceById, calm))
      groupOfEntity.set(w.id, g.key)
    }

    if (o.devices) {
      const siteById = new Map(t.sites.map((s) => [s.id, s]))
      const perSite = o.groupBy === 'cluster'
      for (const dv of [...t.devices].sort((a, b) => a.name.localeCompare(b.name))) {
        const s = dv.siteId ? siteById.get(dv.siteId) : undefined
        const entityId = perSite ? (s?.id ?? 'none') : 'all'
        const key = `dev:${entityId}`
        if (!groups.has(key)) {
          groups.set(key, {
            key,
            row: DEVICE_ROW,
            tier: 'far-edge',
            extra: {
              kind: 'devices',
              entityId,
              title: perSite ? (s?.name ?? 'Unplaced devices') : 'Devices',
              subtitle: perSite ? (s ? [s.kind.replace('-', ' '), placeLabel(s)].filter(Boolean).join(' · ') : 'No site assigned') : '',
              place: perSite ? placeLabel(s) : '',
              country: perSite ? s?.country : undefined,
            },
            items: [],
          })
        }
        groups.get(key)!.items.push(deviceItem(dv, s, !perSite, calm))
        groupOfEntity.set(dv.id, key)
      }

      // External endpoints only appear when something actually calls them (or is called by them).
      const wanted = new Set(t.dependencies.flatMap((d) => [d.fromKind === 'external' ? d.from : '', d.toKind === 'external' ? d.to : '']).filter(Boolean))
      const ext = t.externalEndpoints.filter((e) => wanted.has(e.id)).sort((a, b) => a.host.localeCompare(b.host))
      if (ext.length) {
        const key = 'ext:all'
        groups.set(key, { key, row: EXTERNAL_ROW, tier: 'cloud', extra: { kind: 'external', entityId: 'all', title: 'External', subtitle: 'Outside every onboarded cluster' }, items: [] })
        for (const e of ext) {
          groups.get(key)!.items.push(externalItem(e, calm))
          groupOfEntity.set(e.id, key)
        }
      }
    }
  } else {
    const sorted = [...t.nodes].sort((a, b) => Number(b.role === 'control-plane') - Number(a.role === 'control-plane') || a.name.localeCompare(b.name))
    for (const n of sorted) {
      const c = clusterById.get(n.clusterId)
      if (!c) continue
      const chips = o.servicesOnNodes ? (servicesByNodeId.get(n.id) ?? []) : undefined
      ensureGroup(c).items.push(machineItem(n, c, chips, o.groupBy === 'tier', calm))
    }
  }

  // What each cluster box says about itself: its load, whether it is fine and, when it is not, in what words. A card's own problem is the card's.
  const infoOf = new Map<string, { load?: ClusterLoad; alert?: Alert; note?: string }>()
  for (const g of groups.values()) {
    const cl = g.cluster
    if (!cl) continue
    const load = clusterLoad(cl, nodesByCluster.get(cl.id) ?? [], shownServices(cl.id, g.items, o.view, servicesByCluster))
    const alert = clusterAlert(cl, load)
    const parts = [load.nodes > load.ready && `${load.ready}/${load.nodes} nodes ready`, load.unready > 0 && `${count(load.unready, 'service')} not fully up`, pressureNote(load)]
    infoOf.set(g.key, { load, alert, note: alert ? parts.filter(Boolean).join(' · ') || STATUS_WORD[cl.status] || undefined : undefined })
  }

  // Calm: network membership is a fact about a box, not a line between two. Boxes that share a subnet or an overlay know each other.
  const netsOf = new Map<string, Network[]>()
  if (calm) {
    for (const n of buildNetworks(o.clusterLinks)) {
      for (const m of n.members) {
        const k = groupKeyOfCluster(m.id)
        if (k && groups.has(k) && !netsOf.get(k)?.includes(n)) netsOf.set(k, [...(netsOf.get(k) ?? []), n])
      }
    }
  }
  const drawnMembers = (n: Network) => [...new Set(n.members.map((m) => groupKeyOfCluster(m.id)))].filter((k): k is string => !!k && groups.has(k)).map(groupId)

  /* 2. Lay out: tiers are rows (cloud on top → far edge at the bottom), groups sit side by side. */
  const rows = new Map<number, GroupAcc[]>()
  ;[...groups.values()]
    .sort((a, b) => a.row - b.row || (a.cluster?.name ?? a.extra?.title ?? '').localeCompare(b.cluster?.name ?? b.extra?.title ?? ''))
    .forEach((g) => {
      rows.set(g.row, [...(rows.get(g.row) ?? []), g])
    })

  // Namespace sub-boxes only make sense for a real cluster box in the application view: a tier box already
  // mixes several clusters together, and infrastructure cards (nodes) have no namespace.
  const nsEnabled = o.namespaces && o.view === 'application' && o.groupBy === 'cluster'

  // Calm: one header slot for every box (a tile, a name and one line), whatever the box has to say. Full keeps its taller one.
  const headerOf = () => (calm ? CALM_HEADER : HEADER)
  const pad = calm ? CALM_PAD : PAD
  const gapX = calm ? CALM_BOX_GAP : GROUP_GAP_X
  const layoutCards = (g: GroupAcc, header: number): Placed => {
    const n = g.items.length
    if (nsEnabled && g.cluster && n > 0) {
      const { w: nsW, h: nsH, boxes } = layoutNamespaces(g.items, !calm, calm ? CALM_GRID : FULL_GRID)
      const inner = pad * 2 + nsW
      return { g, w: Math.max(inner, 248, calm ? CALM_MIN_W : MIN_GROUP_HEADER_WIDTH), inner, h: header + nsH + pad, header, children: [], nsBoxes: boxes }
    }
    const { w, h, children } = packItems(g.items, header, pad, !calm, calm ? CALM_GRID : FULL_GRID)
    return { g, w: Math.max(w || 248, 248, calm ? CALM_MIN_W : MIN_GROUP_HEADER_WIDTH), inner: w, h: n === 0 ? header + 52 : h, header, children }
  }
  /** Calm: boxes that share a row share a width and a height, and what is inside sits in the middle of the box, so a row is one band. */
  const settle = (row: Placed[]): Placed[] => {
    if (!calm) return row
    const w = Math.max(...row.map((p) => p.w))
    const h = Math.max(...row.map((p) => p.h))
    return row.map((p) => {
      const dx = Math.round((w - p.inner) / 8) * 4
      return { ...p, w, h, children: p.children.map((c) => ({ ...c, x: c.x + dx })), nsBoxes: p.nsBoxes?.map((b) => ({ ...b, x: b.x + dx })) }
    })
  }
  /** Calm: a tier with more boxes than fit side by side wraps onto another line instead of running off the screen. */
  const wrap = (row: Placed[]): Placed[][] => {
    if (!calm) return [row]
    const lines: Placed[][] = [[]]
    let used = 0
    for (const p of row) {
      const cur = lines[lines.length - 1]
      if (cur.length && used + gapX + p.w > CALM_ROW_MAX) {
        lines.push([p])
        used = p.w
      } else {
        cur.push(p)
        used += (cur.length > 1 ? gapX : 0) + p.w
      }
    }
    return lines
  }
  // One header slot per row: boxes side by side start their cards at the same height.
  const laid = [...rows.entries()].sort((a, b) => a[0] - b[0]).map(([, gs]) => gs.map((g) => layoutCards(g, headerOf())))
  // Calm: every box is as wide as the widest, so the gaps between boxes are in the same place on every row, and a line can run down one.
  const colW = calm ? Math.max(CALM_MIN_W, ...laid.flat().map((p) => p.w)) : 0
  const placedRows = laid.flatMap((r) => wrap(calm ? r.map((p) => ({ ...p, w: colW })) : r).map(settle))
  const rowWidths = placedRows.map((r) => r.reduce((s, p) => s + p.w, 0) + (r.length - 1) * gapX)
  const maxW = Math.max(0, ...rowWidths)

  const nodes: TopoNode[] = []
  const abs = new Map<string, Box>() // absolute boxes by react-flow id (for edge routing)
  let y = 0
  placedRows.forEach((row, ri) => {
    // Calm rows start at the same left edge, so the boxes are columns; Full centres each row.
    let x = calm ? 0 : (maxW - rowWidths[ri]) / 2
    for (const p of row) {
      const { g } = p
      const gid = groupId(g.key)
      const cl = g.cluster
      const groupStatus = g.extra?.status ?? worstStatus(cl ? [cl.status, ...g.items.map((i) => i.data.status)] : g.items.map((i) => i.data.status))
      const clustersInTier = clusterCountByTier.get(g.tier) ?? 0
      const ex = g.extra
      const units = g.items.reduce((s, i) => s + (i.data.units ?? 1), 0)
      const siteCount = new Set(g.items.map((i) => i.data.clusterName).filter(Boolean)).size
      const info = infoOf.get(g.key)
      nodes.push({
        id: gid,
        type: 'boundary',
        position: { x, y },
        style: { width: p.w, height: p.h },
        zIndex: 0,
        draggable: true,
        data: {
          kind: 'group',
          entityId: ex ? ex.entityId : g.key,
          groupBy: o.groupBy,
          extra: ex?.kind,
          title: ex ? ex.title : cl ? cl.name : tierLabel(g.tier),
          subtitle: ex
            ? ex.subtitle || (ex.kind === 'devices' ? `${siteCount} site${siteCount === 1 ? '' : 's'}` : '')
            : cl
              ? [cl.distribution, placeLabel(siteById.get(cl.siteId ?? '')) || cl.region].filter(Boolean).join(' · ')
              : `${clustersInTier} cluster${clustersInTier === 1 ? '' : 's'}`,
          distribution: cl?.distribution,
          country: ex ? ex.country : cl ? siteById.get(cl.siteId ?? '')?.country : undefined,
          tier: g.tier,
          status: groupStatus,
          place: ex ? ex.place : cl ? placeLabel(siteById.get(cl.siteId ?? '')) || cl.region : '',
          load: info?.load,
          alert: info?.alert,
          note: info?.note,
          mesh: o.mesh && o.view === 'application' && cl?.mesh ? groupMesh(cl.mesh) : undefined,
          localTelemetry: cl && o.groupBy === 'cluster' ? o.localOperators?.get(cl.id) : undefined,
          networking: cl && o.groupBy === 'cluster' && (cl.cni || cl.ingress) ? { cni: cl.cni, ingress: cl.ingress } : undefined,
          networks: netsOf.get(g.key)?.map((n) => ({ id: n.id, kind: n.kind, via: n.via, text: networkSentence(n, g.cluster?.id ?? ''), members: drawnMembers(n) })),
          stats:
            ex?.kind === 'devices'
              ? count(units, 'device')
              : ex
                ? count(g.items.length, 'endpoint')
                : count(g.items.length, o.view === 'application' ? 'service' : 'node'),
          empty: o.view === 'application' ? 'No services' : 'No nodes',
        },
      })
      abs.set(gid, { x, y, w: p.w, h: p.h })
      if (p.nsBoxes) {
        for (const nb of p.nsBoxes) {
          const nsId = `ns:${g.key}:${nb.namespace}`
          nodes.push({
            id: nsId,
            type: 'namespace',
            parentId: gid,
            extent: 'parent',
            draggable: false,
            position: { x: pad + nb.x, y: p.header + nb.y },
            style: { width: nb.w, height: nb.h },
            zIndex: 5,
            data: {
              kind: 'namespace',
              entityId: nsId,
              clusterId: cl!.id,
              namespace: nb.namespace,
              count: nb.children.length,
              status: worstStatus(nb.children.map((c) => c.item.data.status)),
              tier: g.tier,
            },
          })
          for (const c of nb.children) {
            nodes.push({
              id: c.item.id,
              type: 'card',
              parentId: nsId,
              // A plain 'parent' extent only keeps a card within the namespace box's full [0,w]x[0,h] -
              // right up against its own edges and the "NN namespace" header text at (0,0). This instead
              // reuses the exact margins packItems already laid the card out with in the first place (NS_PAD
              // on the sides/bottom, NS_HEADER on top, reserved for the namespace box's own header row), so
              // dragging a card can never put it somewhere the initial layout itself would never have.
              extent: [[NS_PAD, NS_HEADER], [nb.w - NS_PAD, nb.h - NS_PAD]],
              position: { x: c.x, y: c.y },
              style: { width: c.item.w, height: c.h },
              zIndex: 10,
              data: c.item.data,
            })
            abs.set(c.item.id, { x: x + pad + nb.x + c.x, y: y + p.header + nb.y + c.y, w: c.item.w, h: c.h })
          }
        }
      } else {
        for (const c of p.children) {
          nodes.push({
            id: c.item.id,
            type: 'card',
            parentId: gid,
            // Same reasoning as the namespace case above: keep a dragged card within the same PAD/HEADER
            // margin packItems already used to lay it out, not the group box's own bare edges - otherwise a
            // drag can park a card flush against the box's left/right/bottom border, or up under the
            // cluster's own header (title, subtitle, load meter).
            extent: [[pad, p.header], [p.w - pad, p.h - pad]],
            position: { x: c.x, y: c.y },
            style: { width: c.item.w, height: c.h },
            zIndex: 10,
            data: c.item.data,
          })
          abs.set(c.item.id, { x: x + c.x, y: y + c.y, w: c.item.w, h: c.h })
        }
      }
      x += p.w + gapX
    }
    y += Math.max(...row.map((p) => p.h)) + (calm ? CALM_ROW_GAP : ROW_GAP)
  })

  /* 3. Edges. */
  const edges: TopoEdge[] = []
  const depById = new Map(t.dependencies.map((d) => [d.id, d]))
  if (o.view === 'application') {
    for (const d of t.dependencies) {
      if (d.from === d.to) continue // a service calling itself has no distinct "other end" to draw a line to
      const s = cardId(d.from)
      const tg = cardId(d.to)
      if (!abs.has(s) || !abs.has(tg)) continue // an end is not drawn (e.g. devices hidden)
      const cross = groupOfEntity.get(d.from) !== groupOfEntity.get(d.to)
      const fc = serviceById.get(d.from)?.clusterId
      const tc = serviceById.get(d.to)?.clusterId
      const quality = o.paths && fc && tc && fc !== tc ? pathQuality(o.paths, fc, tc) : undefined
      const verdict = o.mesh ? connectionVerdict(d, serviceById.get(d.from), serviceById.get(d.to), fc ? clusterById.get(fc) : undefined, t.namespaces) : undefined
      const route = crossClusterRoute(d, serviceById.get(d.to))
      // Only resolvable for a service caller - external/device sources have no nodeIds of their own to
      // check an interface's rated speed against.
      const ifaceSpeedMbps = d.fromKind === 'service' ? callerIfaceSpeedMbps(d.iface, serviceById.get(d.from)?.nodeIds, nodeById) : undefined
      edges.push(makeEdge(d.id, s, tg, abs, {
        // The line says what it is; what the mesh does to it is the colour (see the legend) and the inspector's words.
        label: edgeLabel(d),
        mesh: verdict,
        quality,
        cross: cross || !!d.crossCluster,
        aggregated: false,
        from: d.from,
        to: d.to,
        sources: d.sources,
        confidence: d.confidence,
        observed: isObserved(d),
        stale: d.stale,
        weight: weightOf(d),
        stats: d.stats,
        via: d.via,
        iface: d.iface,
        ifaceSpeedMbps,
        retransmits: d.retransmits,
        rttMs: d.rttMs,
        jitterMs: d.jitterMs,
        handshakeMs: d.handshakeMs,
        failedAttempts: d.failedAttempts,
        sniHost: d.sniHost,
        dnsQueryNames: d.dnsQueryNames,
        dnsRttMs: d.dnsRttMs,
        route,
        tunnelLink: d.tunnelLink,
        problem: isObserved(d) && !d.stale && !!quality && lossBand(quality.lossPct) !== 'ok',
      }))
    }
    if (calm) bundleCrossEdges(edges, abs, groupOfEntity, depById)
  } else if (o.links) {
    // Aggregate service dependencies into group ↔ group links.
    const agg = new Map<string, { a: string; b: string; count: number; active: number; bytesPerSec: number; protocols: Map<string, number>; quality?: PathQuality }>()
    for (const d of t.dependencies) {
      const from = serviceById.get(d.from)
      const to = serviceById.get(d.to)
      if (!from || !to) continue
      const a = groupKeyOfCluster(from.clusterId)
      const b = groupKeyOfCluster(to.clusterId)
      if (!a || !b || a === b) continue
      const [k1, k2] = a < b ? [a, b] : [b, a]
      const key = `${k1}|${k2}`
      const cur = agg.get(key) ?? { a: k1, b: k2, count: 0, active: 0, bytesPerSec: 0, protocols: new Map<string, number>() }
      cur.count++
      if (isObserved(d) && !d.stale) cur.active++
      cur.bytesPerSec += d.stats?.bytesPerSec ?? 0
      cur.protocols.set(d.protocol, (cur.protocols.get(d.protocol) ?? 0) + 1)
      if (o.paths && from.clusterId !== to.clusterId) cur.quality = lossier(cur.quality, pathQuality(o.paths, from.clusterId, to.clusterId))
      agg.set(key, cur)
    }
    for (const { a, b, count, active, bytesPerSec, protocols, quality } of agg.values()) {
      if (!abs.has(groupId(a)) || !abs.has(groupId(b))) continue
      edges.push(makeEdge(`agg:${a}|${b}`, groupId(a), groupId(b), abs, {
        label: `${count} ${count === 1 ? 'dependency' : 'dependencies'}`,
        cross: true,
        aggregated: true,
        from: a,
        to: b,
        activeCount: active,
        count,
        quality,
        problem: active > 0 && !!quality && lossBand(quality.lossPct) !== 'ok',
        protocols: protocols.size > 1 ? Object.fromEntries(protocols) : undefined,
        // Animate this bundle exactly when it actually contains real, live traffic (active > 0) - the same
        // "motion means real traffic, not just cross-cluster" rule makeEdge's own doc applies to a single
        // dependency edge. Without this, an aggregated edge never sets `observed` at all, so makeEdge's
        // `d.observed && !d.stale` check silently never animates it even when every bundled dependency is
        // actively observed.
        observed: active > 0,
        // A bundled total, not a per-dependency measurement - see EdgeData.via's own comment for why a
        // conntrack-only dependency's contribution here may understate the real total.
        stats: bytesPerSec > 0 ? { bytesPerSec } : undefined,
      }))
    }
  }

  // Cluster links: a confirmed overlay/subnet relationship, drawn directly between the two clusters' own
  // group boxes (never a third box, unlike regional operators above - the relationship IS the two
  // clusters themselves), independent of whether anything actually calls between the two clusters - it
  // is a network-level fact, not a traffic one. Two clusters that collapsed into the very same group
  // (both in the same tier, when groupBy is 'tier') have no distinct "other end" to draw a line to.
  //
  // Suppressed, though, when exactly one already-drawn dependency edge's own tunnelLink (see
  // EdgeData.tunnelLink) names this exact overlay link: that one edge already shows everything this line
  // would (via/redundancy/encryption), so drawing both would just look like a duplicate relationship
  // between the same two boxes. Zero matching dependency edges (nothing is using the tunnel right now, or
  // the detailed per-dependency edges aren't even drawn in this view/groupBy) still draws this line on its
  // own - that is the one place a quiet tunnel stays visible at all. More than one matching edge also
  // keeps this line: no single dependency edge can stand in for the aggregate (flowsObserved/avgRttMs/
  // avgLossPct) only this line carries. A "subnet" link is never suppressed - dependencies never carry a
  // tunnelLink for one (see Dependency.tunnelLink's own doc), so there is nothing to check.
  // Calm draws none of these: membership is the box's chip and ring (see netsOf above), and a line is for traffic.
  for (const cl of calm ? [] : o.clusterLinks ?? []) {
    const a = groupKeyOfCluster(cl.fromCluster)
    const b = groupKeyOfCluster(cl.toCluster)
    if (!a || !b || a === b) continue
    const ga = groupId(a)
    const gb = groupId(b)
    if (!abs.has(ga) || !abs.has(gb)) continue
    const matchingDependencyEdges =
      cl.kind === 'overlay'
        ? edges.filter((e) => e.data?.tunnelLink?.fromCluster === cl.fromCluster && e.data?.tunnelLink?.toCluster === cl.toCluster).length
        : 0
    if (matchingDependencyEdges === 1) continue
    edges.push(makeEdge(`cl:${cl.fromCluster}:${cl.toCluster}:${cl.kind}`, ga, gb, abs, {
      label: cl.kind === 'overlay' ? 'overlay' : 'same subnet',
      cross: true,
      aggregated: false,
      groupLevel: true,
      from: a,
      to: b,
      clusterLink: { kind: cl.kind, via: cl.via, redundancy: cl.redundancy, fromNode: cl.fromNode, toNode: cl.toNode, fromAddress: cl.fromAddress, toAddress: cl.toAddress, flowsObserved: cl.flowsObserved, avgRttMs: cl.avgRttMs, avgLossPct: cl.avgLossPct, avgRtoRetransmitsPerMin: cl.avgRtoRetransmitsPerMin, avgMssBytes: cl.avgMssBytes, encryption: cl.encryption },
    }))
  }

  spreadFanned(edges, abs)
  spreadParallel(edges)
  if (calm) assignTracks(edges)
  return { nodes: nodes.map(spoken), edges }
}

/** Lines that run in the gutters take turns on five tracks, in a fixed order, so the lines of one gutter are side by side and not on top of each other. */
function assignTracks(edges: TopoEdge[]) {
  edges
    .filter((e) => e.data?.aggregated)
    .sort((a, b) => a.id.localeCompare(b.id))
    .forEach((e, i) => { e.data = { ...e.data!, track: (i % 5) - 2 } })
}

const lossier = (a: PathQuality | undefined, b: PathQuality | undefined) => (!b ? a : !a || b.lossPct > a.lossPct ? b : a)

/**
 * Calm, application view: every dependency that crosses from one box to another is drawn as ONE line between the two boxes (the hover says how
 * many), so the canvas shows which boxes talk, not every service-to-service call. The calls themselves stay in the graph as 'detail' lines that
 * the focus brings forward (hovering or selecting either box or either end, see OffsetEdge). A bundle that holds a failing path is the one red line.
 */
function bundleCrossEdges(edges: TopoEdge[], abs: Map<string, Box>, groupOfEntity: Map<string, string>, depById: Map<string, Dependency>) {
  const pairs = new Map<string, { a: string; b: string; members: TopoEdge[] }>()
  for (const e of edges) {
    const ga = groupOfEntity.get(e.data!.from)
    const gb = groupOfEntity.get(e.data!.to)
    if (!ga || !gb || ga === gb) continue
    const [a, b] = ga < gb ? [ga, gb] : [gb, ga]
    const pair = pairs.get(`${a}|${b}`) ?? { a, b, members: [] }
    pair.members.push(e)
    pairs.set(`${a}|${b}`, pair)
  }
  for (const { a, b, members } of pairs.values()) {
    const protocols = new Map<string, number>()
    let active = 0
    let bytesPerSec = 0
    let quality: PathQuality | undefined
    for (const e of members) {
      e.data = { ...e.data!, role: 'detail', focusIds: [groupId(a), groupId(b), e.source, e.target] }
      if (e.data.observed && !e.data.stale) active++
      bytesPerSec += e.data.stats?.bytesPerSec ?? 0
      quality = lossier(quality, e.data.quality)
      const protocol = depById.get(e.id)?.protocol
      if (protocol) protocols.set(protocol, (protocols.get(protocol) ?? 0) + 1)
    }
    edges.push(makeEdge(`bundle:${a}|${b}`, groupId(a), groupId(b), abs, {
      label: `${members.length} ${members.length === 1 ? 'dependency' : 'dependencies'}`,
      cross: true,
      aggregated: true,
      from: a,
      to: b,
      activeCount: active,
      count: members.length,
      quality,
      problem: members.some((e) => e.data?.problem),
      role: 'bundle',
      focusIds: [groupId(a), groupId(b), ...members.flatMap((e) => [e.source, e.target])],
      protocols: protocols.size > 1 ? Object.fromEntries(protocols) : undefined,
      observed: active > 0,
      stats: bytesPerSec > 0 ? { bytesPerSec } : undefined,
    }))
  }
}

/**
 * Several different edges landing on the same side of a busy node would otherwise all converge on that
 * one handle's point. Groups edges by (node, side) independently at each end (a line can fan out at one
 * end and land cleanly at the other), and within a group orders by where each line is actually headed -
 * so the fan-out visually tracks its destination instead of crossing itself near the node. Edges that
 * share the very same other box land in the same slot here; spreadParallel (run right after) adds the
 * fine, symmetric sub-offset that keeps those particular lines apart from each other.
 */
function spreadFanned(edges: TopoEdge[], abs: Map<string, Box>) {
  const FAN_GAP = 16
  type End = { edge: TopoEdge; role: 'source' | 'target' }
  const groups = new Map<string, End[]>()
  const add = (key: string, end: End) => groups.set(key, [...(groups.get(key) ?? []), end])
  for (const e of edges) {
    if (!e.sourceHandle || !e.targetHandle) continue
    add(`${e.source}|${e.sourceHandle.split('-')[0]}`, { edge: e, role: 'source' })
    add(`${e.target}|${e.targetHandle.split('-')[0]}`, { edge: e, role: 'target' })
  }
  const otherId = (en: End) => (en.role === 'source' ? en.edge.target : en.edge.source)
  for (const [key, ends] of groups) {
    const side = key.slice(key.indexOf('|') + 1) as Side
    const axis: 'x' | 'y' = side === 'top' || side === 'bottom' ? 'x' : 'y'
    const otherCenter = (en: End) => {
      const box = abs.get(otherId(en))
      if (!box) return 0
      return axis === 'x' ? box.x + box.w / 2 : box.y + box.h / 2
    }
    const clusters = new Map<string, End[]>()
    for (const en of ends) clusters.set(otherId(en), [...(clusters.get(otherId(en)) ?? []), en])
    if (clusters.size < 2) continue // one destination on this side: nothing to fan out from another
    const ordered = [...clusters.values()].sort((a, b) => otherCenter(a[0]) - otherCenter(b[0]) || a[0].edge.id.localeCompare(b[0].edge.id))
    ordered.forEach((cluster, i) => {
      const v = (i - (ordered.length - 1) / 2) * FAN_GAP
      for (const en of cluster) {
        en.edge.type = 'offset'
        en.edge.data = { ...en.edge.data!, [en.role === 'source' ? 'sourceOffset' : 'targetOffset']: v }
      }
    })
  }
}

/** Two lines between the same pair of boxes (two ports, or one each way) would be drawn on top of each other and one would vanish. */
function spreadParallel(edges: TopoEdge[]) {
  const by = new Map<string, TopoEdge[]>()
  for (const e of edges) {
    const key = e.source < e.target ? `${e.source}|${e.target}` : `${e.target}|${e.source}`
    by.set(key, [...(by.get(key) ?? []), e])
  }
  for (const group of by.values()) {
    if (group.length < 2) continue
    group.sort((a, b) => a.id.localeCompare(b.id))
    group.forEach((e, i) => {
      // The sideways direction is taken from the canonical order of the two boxes, so a line back the other way lands on the other side.
      const sign = e.source < e.target ? 1 : -1
      const v = (i - (group.length - 1) / 2) * 14 * sign
      e.type = 'offset'
      // Additive on top of any coarse fan-out offset already set: this pass only needs to separate the
      // handful of lines that share both endpoints, not decide where that whole bundle sits on the node.
      e.data = { ...e.data!, sourceOffset: (e.data?.sourceOffset ?? 0) + v, targetOffset: (e.data?.targetOffset ?? 0) + v }
    })
  }
}


/* ---------- Application view: chain layout ---------- */

const CHAIN_COL_GAP = 96
const CHAIN_ROW_GAP = 28

/**
 * Left-to-right dependency chain: one column per rank (longest path from a source, over service-to-service
 * calls only), ordered top-to-bottom within a column by a barycenter heuristic so lines cross as little as
 * possible. Cycles (two services depending on each other) are broken with a DFS feedback-arc pass before
 * ranking - the dropped back-edge is still drawn later, just without influencing anyone's column. Devices
 * and external endpoints are not part of the ranking; each is hung one column past whichever service it's
 * paired with (its first caller or callee found), stacked below that column's services. Cards are stacked by
 * their own heights (a card with a pod row is taller than one without), so none overlaps the next.
 */
function layoutChain(
  serviceIds: string[],
  serviceDeps: { from: string; to: string }[],
  leafIds: string[],
  leafNear: Map<string, string | undefined>,
  heightOf: (id: string) => number,
): Map<string, { x: number; y: number }> {
  const svc = new Set(serviceIds)
  const adj = new Map<string, Set<string>>(serviceIds.map((id) => [id, new Set<string>()]))
  const radj = new Map<string, Set<string>>(serviceIds.map((id) => [id, new Set<string>()]))
  const byFrom = new Map<string, { from: string; to: string }[]>()
  for (const e of serviceDeps) {
    if (!svc.has(e.from) || !svc.has(e.to) || e.from === e.to) continue
    byFrom.set(e.from, [...(byFrom.get(e.from) ?? []), e])
  }

  // DFS feedback-arc pass: an edge to a node still on the current path (GRAY) is a back-edge and is
  // dropped from the DAG used for ranking (it's still drawn - see the caller - just doesn't set anyone's rank).
  //
  // Written as an explicit stack rather than a recursive function: a plain recursive `visit` would put one
  // JS call-stack frame per node on the *current* DFS path, so one long, mostly-linear dependency chain
  // (plausible here - a chain layout is exactly for services that call each other in a long sequence) could
  // run deep enough to blow the stack, crashing the whole page over a graph that isn't even unusually large,
  // just unusually straight. Each frame below is a plain object on the heap instead, so depth is bounded
  // only by memory, not the engine's call-stack limit - same traversal, same dag/color result either way.
  const WHITE = 0, GRAY = 1, BLACK = 2
  const color = new Map<string, number>(serviceIds.map((id) => [id, WHITE]))
  const dag: { from: string; to: string }[] = []
  for (const start of serviceIds) {
    if (color.get(start) !== WHITE) continue
    color.set(start, GRAY)
    const stack: { id: string; edges: { from: string; to: string }[]; i: number }[] = [{ id: start, edges: byFrom.get(start) ?? [], i: 0 }]
    while (stack.length > 0) {
      const frame = stack[stack.length - 1]
      if (frame.i >= frame.edges.length) {
        color.set(frame.id, BLACK)
        stack.pop()
        continue
      }
      const e = frame.edges[frame.i]
      frame.i++
      if (color.get(e.to) === GRAY) continue
      dag.push(e)
      if (color.get(e.to) === WHITE) {
        color.set(e.to, GRAY)
        stack.push({ id: e.to, edges: byFrom.get(e.to) ?? [], i: 0 })
      }
    }
  }
  for (const e of dag) {
    adj.get(e.from)!.add(e.to)
    radj.get(e.to)!.add(e.from)
  }

  // Rank = longest path from a source. Bounded relaxation rather than a topo-sort walk: simple, and safe
  // even if a residual cycle somehow slipped through (it just stops after n rounds instead of looping).
  const rank = new Map<string, number>(serviceIds.map((id) => [id, 0]))
  for (let i = 0; i < serviceIds.length; i++) {
    let changed = false
    for (const e of dag) {
      const r = (rank.get(e.from) ?? 0) + 1
      if (r > (rank.get(e.to) ?? 0)) {
        rank.set(e.to, r)
        changed = true
      }
    }
    if (!changed) break
  }

  const byRank = new Map<number, string[]>()
  for (const id of serviceIds) {
    const r = rank.get(id)!
    byRank.set(r, [...(byRank.get(r) ?? []), id])
  }
  const maxRank = Math.max(0, ...byRank.keys())
  const order = new Map<string, number>()
  for (const ids of byRank.values()) ids.forEach((id, i) => order.set(id, i))

  // A couple of sweeps - toward already-placed callers, then toward already-placed callees - untangles
  // most of the avoidable crossings without needing a real crossing-count optimizer.
  const sweep = (neighbors: (id: string) => Set<string>) => {
    for (let r = 0; r <= maxRank; r++) {
      const ids = byRank.get(r)
      if (!ids || ids.length < 2) continue
      const bc = (id: string) => {
        const ns = [...neighbors(id)]
        return ns.length ? ns.reduce((s, n) => s + (order.get(n) ?? 0), 0) / ns.length : (order.get(id) ?? 0)
      }
      ids.sort((a, b) => bc(a) - bc(b) || a.localeCompare(b))
      ids.forEach((id, i) => order.set(id, i))
    }
  }
  sweep((id) => radj.get(id)!)
  sweep((id) => adj.get(id)!)

  // Next free y in each column: services first, then the leaves hung in the same column go underneath them.
  const pos = new Map<string, { x: number; y: number }>()
  const bottom = new Map<number, number>()
  const place = (r: number, id: string) => {
    const y = bottom.get(r) ?? 0
    pos.set(id, { x: r * (APP_CARD.w + CHAIN_COL_GAP), y })
    bottom.set(r, y + heightOf(id) + CHAIN_ROW_GAP)
  }
  for (const [r, ids] of byRank) ids.forEach((id) => place(r, id))

  const leafRank = new Map<string, number>()
  for (const id of leafIds) {
    const near = leafNear.get(id)
    leafRank.set(id, near !== undefined ? (rank.get(near) ?? 0) + 1 : maxRank + 1)
  }
  const byLeafRank = new Map<number, string[]>()
  for (const id of leafIds) {
    const r = leafRank.get(id)!
    byLeafRank.set(r, [...(byLeafRank.get(r) ?? []), id])
  }
  for (const [r, ids] of byLeafRank) {
    const nearY = (id: string) => {
      const near = leafNear.get(id)
      return near ? (pos.get(near)?.y ?? 0) : 0
    }
    ids.sort((a, b) => nearY(a) - nearY(b) || a.localeCompare(b))
    ids.forEach((id) => place(r, id))
  }

  return pos
}

/** The Application view's alternate layout: every service (across every cluster) placed in one flat
 * left-to-right dependency chain instead of nested inside cluster/tier boxes. See `chain` on GraphOptions. */
function buildChainGraph(t: Topology, o: GraphOptions): { nodes: TopoNode[]; edges: TopoEdge[] } {
  const calm = o.detail === 'calm'
  const clusterById = new Map(t.clusters.map((c) => [c.id, c]))
  const siteById = new Map(t.sites.map((s) => [s.id, s]))
  const serviceById = new Map(t.services.map((w) => [w.id, w]))
  const nodeById = new Map(t.nodes.map((n) => [n.id, n]))

  const services = t.services.filter((w) => {
    const c = clusterById.get(w.clusterId)
    return c && !(w.mesh?.controlPlane && !o.mesh)
  })
  const serviceIds = services.map((w) => w.id)

  const serviceDeps = t.dependencies.filter((d) => d.fromKind === 'service' && d.toKind === 'service')

  const wantedLeaves = new Set(
    t.dependencies.flatMap((d) => [d.fromKind !== 'service' ? d.from : '', d.toKind !== 'service' ? d.to : '']).filter(Boolean),
  )
  const deviceLeaves = o.devices ? t.devices.filter((dv) => wantedLeaves.has(dv.id)) : []
  const externalLeaves = o.devices ? t.externalEndpoints.filter((e) => wantedLeaves.has(e.id)) : []
  const leafIds = [...deviceLeaves.map((d) => d.id), ...externalLeaves.map((e) => e.id)]

  const leafNear = new Map<string, string | undefined>()
  for (const id of leafIds) {
    const dep = t.dependencies.find((d) => (d.from === id && serviceById.has(d.to)) || (d.to === id && serviceById.has(d.from)))
    leafNear.set(id, dep ? (serviceById.has(dep.from) ? dep.from : dep.to) : undefined)
  }

  // Two services of the same name in different clusters would read as one at far zoom, where the subtitle
  // (and with it the cluster) is hidden: those carry the cluster name for the far presentation.
  const names = new Map<string, number>()
  for (const w of services) names.set(w.name, (names.get(w.name) ?? 0) + 1)
  const items: Item[] = [
    ...services.map((w) => {
      const c = clusterById.get(w.clusterId)!
      const item = serviceItem(w, c, true, o.hints?.get(w.id), o.mesh, nodeById, serviceById, calm)
      return (names.get(w.name) ?? 0) > 1 ? { ...item, data: { ...item.data, clusterTag: c.name } } : item
    }),
    ...deviceLeaves.map((dv) => deviceItem(dv, dv.siteId ? siteById.get(dv.siteId) : undefined, true, calm)),
    ...externalLeaves.map((e) => externalItem(e, calm)),
  ]
  const ids = [...serviceIds, ...leafIds]
  const itemById = new Map(ids.map((id, i) => [id, items[i]]))
  const positions = layoutChain(serviceIds, serviceDeps, leafIds, leafNear, (id) => itemById.get(id)?.h ?? APP_CARD.h)

  const nodes: TopoNode[] = []
  const abs = new Map<string, Box>()
  items.forEach((item, i) => {
    const p = positions.get(ids[i]) ?? { x: 0, y: 0 }
    nodes.push({ id: item.id, type: 'card', position: { x: p.x, y: p.y }, style: { width: item.w, height: item.h }, zIndex: 10, data: item.data })
    abs.set(item.id, { x: p.x, y: p.y, w: item.w, h: item.h })
  })

  const edges: TopoEdge[] = []
  for (const d of t.dependencies) {
    if (d.from === d.to) continue // same as the grouped view: nothing distinct to draw a line to
    const s = cardId(d.from)
    const tg = cardId(d.to)
    if (!abs.has(s) || !abs.has(tg)) continue
    const fc = serviceById.get(d.from)?.clusterId
    const tc = serviceById.get(d.to)?.clusterId
    const quality = o.paths && fc && tc && fc !== tc ? pathQuality(o.paths, fc, tc) : undefined
    const verdict = o.mesh ? connectionVerdict(d, serviceById.get(d.from), serviceById.get(d.to), fc ? clusterById.get(fc) : undefined, t.namespaces) : undefined
    const route = crossClusterRoute(d, serviceById.get(d.to))
    edges.push(makeEdge(d.id, s, tg, abs, {
      label: edgeLabel(d),
      mesh: verdict,
      quality,
      // No boxes to cross here, so "crossGroup" styling is driven only by the model's own crossCluster flag.
      cross: !!d.crossCluster,
      aggregated: false,
      from: d.from,
      to: d.to,
      sources: d.sources,
      confidence: d.confidence,
      observed: isObserved(d),
      stale: d.stale,
      weight: weightOf(d),
      route,
    }))
  }

  spreadFanned(edges, abs)
  spreadParallel(edges)
  return { nodes: nodes.map(spoken), edges }
}

/* ---------- helpers ---------- */
/** A box or card says its name and its state in words to a screen reader (and on keyboard focus), the same sentence the glyph and the tint say to the eye. */
function spoken<N extends TopoNode>(n: N): N {
  const d = n.data as { title?: string; status?: Status; alert?: Alert; note?: string }
  return d.title && d.status ? { ...n, ariaLabel: statusSentence(d.title, d.status, d.alert, d.note) } : n
}


function tierLabel(t: Tier) {
  return t === 'far-edge' ? 'Far edge' : t[0].toUpperCase() + t.slice(1)
}

/** What is wrong with a cluster as a whole: it reports badly, a node or a service is not up, or it is running out of room. */
export function clusterAlert(cl: Pick<Cluster, 'status'>, load: ClusterLoad): Alert | undefined {
  return worstAlert(alertOfStatus(cl.status), alertOfLoad(peakLoad(load)), load.nodes > load.ready || load.unready > 0 ? 'warn' : undefined)
}

/** What is wrong with a service: it reports badly, its pods are not all ready (or crash-looping), or fewer replicas are ready than wanted. */
export function serviceAlert(w: Service, pods: PodsView | undefined): Alert | undefined {
  return worstAlert(alertOfStatus(w.status), alertOfPods(pods), !pods && w.readyReplicas !== undefined && w.readyReplicas < w.replicas ? 'warn' : undefined)
}

/** What is wrong with a machine: it reports badly, or it is running out of room. */
export function machineAlert(n: MachineNode): Alert | undefined {
  const load = nodeLoad(n)
  return worstAlert(alertOfStatus(n.status), alertOfLoad(load && peakLoad(load)))
}

function serviceItem(w: Service, c: Cluster, withCluster: boolean, hint: string | undefined, mesh: boolean | undefined, nodeById: Map<string, MachineNode>, serviceById: Map<string, Service>, calm: boolean): Item {
  const pods = buildPodsView(w.pods, w.name, nodeById, serviceById)
  // With per-pod facts the pod summary says it ("27/30 ready"), so the chip is only for a service without them.
  const notReady = !pods && w.readyReplicas !== undefined && w.readyReplicas < w.replicas
  // One row of small chips under the name; a second for the pod rail (capped, so it always fits its row),
  // whichever combination of the two is present (mirrors the machine card's own hint/notReady/mesh row).
  const badgeRow = !!(hint || notReady || (mesh && w.mesh))
  const podsRow = !!pods
  const extraRows = (badgeRow ? 1 : 0) + (podsRow ? 1 : 0)
  const alert = serviceAlert(w, pods)
  const note = alert ? serviceNote(w, pods, notReady) : undefined
  return {
    id: cardId(w.id),
    w: calm ? CALM_CARD.w : APP_CARD.w,
    h: calm ? CALM_CARD_H : APP_CARD.h + (extraRows === 2 ? 46 : extraRows === 1 ? 24 : 0),
    namespace: w.namespace,
    data: {
      kind: 'service',
      entityId: w.id,
      title: w.name,
      subtitle: [withCluster ? c.name : '', w.namespace, w.kind].filter(Boolean).join(' · '),
      meta: `×${w.replicas}`,
      status: w.status,
      alert,
      note,
      tier: c.tier,
      clusterName: c.name,
      serviceKind: w.kind,
      hint,
      notReady: notReady ? `${w.readyReplicas}/${w.replicas} ready` : undefined,
      mesh: mesh && w.mesh ? meshChip(w) : undefined,
      pods,
    },
  }
}

function groupMesh(m: NonNullable<Cluster['mesh']>): NonNullable<GroupData['mesh']> {
  const tone = m.mtls === 'strict' || m.mtls === 'automatic' ? 'good' : m.mtls === 'disabled' ? 'bad' : 'warn'
  const note = m.policyRead ? '' : ` ${m.policyNote ?? 'The mesh policy could not be read.'}`
  return { label: `${meshName(m.kind)} · ${m.mode}`, tone, title: `${clusterMeshLine(m)}. Inferred from configuration.${note}` }
}

function meshChip(w: Service): NonNullable<CardData['mesh']> {
  const m = w.mesh!
  const words = proxyWords(m)
  const src = m.source === 'pods' ? 'A proxy container was seen in its pods.' : m.source === 'namespace' ? 'Only what its namespace asks for; no proxy was seen yet.' : 'What its workload asks for.'
  return { label: words, tone: m.controlPlane ? 'control' : inMesh(m) ? 'in' : 'out', title: `${words}. ${src} Inferred from configuration.` }
}

const STATUS_WORD: Record<Status, string> = { offline: 'Offline', degraded: 'Degraded', unknown: '', healthy: '' }

/** The busiest resource of a machine or cluster once it is under pressure, in words: "Memory 92% requested". */
function pressureNote(load: NodeLoad | undefined): string | undefined {
  if (!load) return undefined
  const rows: [string, number | undefined, string][] = [['CPU', load.cpuPct, 'requested'], ['Memory', load.memPct, 'requested'], ['Pods', load.podPct, 'full']]
  let top: [string, number, string] | undefined
  for (const [label, pct, what] of rows) if (pct !== undefined && (!top || pct > top[1])) top = [label, pct, what]
  return top && top[1] >= PRESSURE_WARN ? `${top[0]} ${top[1]}% ${top[2]}` : undefined
}

/** What is wrong with a service, in the words its pods would use ("1 crash-looping · 1 not ready"), else its state. */
function serviceNote(w: Service, pods: PodsView | undefined, notReady: boolean): string | undefined {
  const parts = pods ? [pods.bad > 0 && `${pods.bad} crash-looping`, pods.warn > 0 && `${pods.warn} not ready`] : [notReady && `${w.readyReplicas}/${w.replicas} ready`]
  return parts.filter(Boolean).join(' · ') || STATUS_WORD[w.status] || undefined
}

const DEVICE_LABEL = Object.fromEntries(DEVICE_KINDS.map((k) => [k.value, k.label])) as Record<DeviceKind, string>

function deviceItem(dv: Device, s: Site | undefined, withSite: boolean, calm: boolean): Item {
  const alert = alertOfStatus(dv.status)
  return {
    id: cardId(dv.id),
    w: calm ? CALM_CARD.w : APP_CARD.w,
    h: calm ? CALM_CARD_H : APP_CARD.h,
    data: {
      kind: 'device',
      entityId: dv.id,
      title: dv.name,
      subtitle: [withSite ? s?.name : '', DEVICE_LABEL[dv.kind], dv.protocol].filter(Boolean).join(' · '),
      meta: dv.count > 1 ? `×${dv.count}` : '',
      status: dv.status,
      alert,
      note: alert ? STATUS_WORD[dv.status] : undefined,
      tier: 'far-edge',
      clusterName: s?.name ?? '',
      deviceKind: dv.kind,
      units: dv.count,
    },
  }
}

function externalItem(e: ExternalEndpoint, calm: boolean): Item {
  return {
    id: cardId(e.id),
    w: calm ? CALM_CARD.w : APP_CARD.w,
    h: calm ? CALM_CARD_H : APP_CARD.h,
    data: {
      kind: 'external',
      entityId: e.id,
      title: e.name ?? e.host,
      subtitle: [e.kind, e.name ? e.host : '', e.port ? `port ${e.port}` : ''].filter(Boolean).join(' · '),
      meta: '',
      status: 'unknown',
      tier: 'cloud',
      clusterName: '',
    },
  }
}

function machineItem(n: MachineNode, c: Cluster, chips: { id: string; name: string }[] | undefined, withCluster: boolean, calm: boolean): Item {
  const load = nodeLoad(n)
  const alert = machineAlert(n)
  const note = alert ? STATUS_WORD[n.status] || pressureNote(load) : undefined
  const chipRows = chips && chips.length ? Math.ceil(chips.length / 2) : 0
  const nicMbps = n.networkInterfaces?.reduce((max, i) => (i.speedMbps !== undefined && i.speedMbps > max ? i.speedMbps : max), 0)
  const hardware = n.hasBattery || nicMbps ? { hasBattery: n.hasBattery, nicMbps: nicMbps || undefined } : undefined
  return {
    id: cardId(n.id),
    w: calm ? CALM_CARD.w : MACHINE_CARD.w,
    // Card's own badge row (Card, in nodes.tsx) renders whenever `hardware` is set - a Battery and/or NIC
    // speed pill - the same extra row serviceItem() below already reserves height for via its own hint/
    // notReady/mesh badges. This was missing here, so a machine with a fast-NIC or battery badge got no
    // headroom for it at all and the pill sat flush against (visually indistinguishable from spilling past)
    // the card's own bottom border.
    // Services-on-nodes is a choice to see them, so a card that lists them keeps the room for the list.
    h: calm
      ? CALM_CARD_H + (chips ? CALM_BLOCK + (chipRows ? chipRows * CALM_CHIP_ROW + 5 : 25) : 0)
      : MACHINE_CARD.h + (hardware ? 24 : 0) + (chips ? (chipRows ? chipRows * CHIP_ROW + 14 : 26) : 0),
    data: {
      kind: 'machine',
      entityId: n.id,
      title: n.name,
      subtitle: [withCluster ? c.name : '', n.role === 'control-plane' ? 'Control plane' : 'Worker', n.ip].filter(Boolean).join(' · '),
      meta: `${n.cpu} vCPU · ${n.memoryGb} GB`,
      status: n.status,
      alert,
      note,
      load,
      tier: c.tier,
      clusterName: c.name,
      machineKind: n.kind,
      control: n.role === 'control-plane',
      chips,
      hardware,
    },
  }
}

export type Side = 'top' | 'bottom' | 'left' | 'right'
const POS: Record<Side, Position> = { top: Position.Top, bottom: Position.Bottom, left: Position.Left, right: Position.Right }
export const SIDES = POS

/** Which side of each box a line between them should leave/enter from, purely from their relative
 * position. Also used, with each box collapsed to a single point (w=h=0), to re-derive an edge's
 * direction live from its current endpoints - see OffsetEdge.tsx, which needs the exact same heuristic
 * so a node dragged around inside its box doesn't leave the arrowhead pointing the wrong way. */
export function pickSides(a: Box, b: Box): [Side, Side] {
  const dx = b.x + b.w / 2 - (a.x + a.w / 2)
  const dy = b.y + b.h / 2 - (a.y + a.h / 2)
  if (Math.abs(dy) >= Math.abs(dx) * 0.6 && Math.abs(dy) > 8) return dy > 0 ? ['bottom', 'top'] : ['top', 'bottom']
  return dx >= 0 ? ['right', 'left'] : ['left', 'right']
}

/** Which service entity ids a canvas selection (a set of React Flow node ids, e.g. from a shift-drag
 * box-select) resolves to - the topology's "Define scope from selection" quick action (ScopeFromSelection.tsx)
 * uses this to turn a raw selection into a telemetry scope draft, silently dropping anything selected that
 * isn't a service card: a cluster/tier box, a namespace sub-box, or a machine/device/external card. Order
 * follows `nodes`, not `selectedIds`, so a scope built from the same selection is stable across re-renders. */
/** Resolves a canvas multi-selection (a mix of individually-clicked service cards and, now that box/click
 * multi-select is easy to reach, whole cluster/tier/namespace boxes selected the same way) down to the flat
 * list of service entity ids it implies - the shape ScopeFromSelection.tsx actually needs. Selecting a
 * group or namespace box directly (rather than each service inside it one at a time) pulls in every service
 * card nested under it, walking the parentId chain rather than requiring a direct parent match, since a
 * service inside a namespace sub-box is two levels below its cluster's own group box. */
export function selectedServiceIds(nodes: TopoNode[], selectedIds: string[]): string[] {
  const ids = new Set(selectedIds)
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const underSelectedAncestor = (n: TopoNode): boolean => {
    let p = n.parentId
    while (p) {
      if (ids.has(p)) return true
      p = byId.get(p)?.parentId
    }
    return false
  }
  return nodes.filter((n) => n.data.kind === 'service' && (ids.has(n.id) || underSelectedAncestor(n))).map((n) => n.data.entityId)
}

/** Folds a freshly computed layout (`next`, e.g. from a poll refresh or a toggled view option) onto
 * whatever React Flow currently has on screen (`prev`), instead of just returning `next` outright - this
 * is what lets `TopologyPage.tsx`'s node-resync effect run on every poll without fighting a manual drag or
 * resetting a node's position on every unrelated refresh. Three rules, in order of how much they cost to
 * get wrong:
 *  1. A node React Flow is actively mid-gesture with (its own `dragging` flag, set by `onNodesChange` for
 *     as long as the pointer is down) is returned completely untouched - not just position-preserved. React
 *     Flow's own drag tracking keys off the node *object* it started the gesture with; handing it a brand
 *     new object for that same id mid-drag (even one with an identical x/y) can desync that internal
 *     tracking, which is a very plausible way for a long drag - and this page polls every 2-5s - to leave
 *     the canvas, or the whole page, unresponsive to clicks until a reload. Every other node still resyncs
 *     normally; only the one actually being dragged is left alone until it's dropped.
 *  2. Otherwise, a node's on-screen position is kept as-is (not overwritten by the newly computed layout)
 *     as long as it still has the same parent - React Flow positions are parent-relative (or
 *     canvas-relative with no parent), so a stale position only still means what it used to while the
 *     parent hasn't changed. This is what lets a manual drag "stick" across the next poll instead of
 *     snapping back to `buildGraph`'s computed position.
 *  3. A changed parent (e.g. toggling namespace sub-boxes re-parents every card in a cluster from the
 *     cluster box straight to a namespace box without changing the card's id) always takes the freshly
 *     computed position - an old, differently-relative position would otherwise land the card in the wrong
 *     spot, often overlapping another card, until something forced a fresh layout.
 * `highlighted` just threads through the current highlight set (the single-click Inspector selection, plus
 * whatever React Flow's own multi-select - shift/ctrl/cmd-click or a box-drag - currently holds) so callers
 * don't need a second pass over the result to re-apply it; without this, a poll landing seconds after a
 * multi-select would silently wipe every selected node's `.selected` flag back down to just the last
 * single-click one, since a freshly computed `next` has `.selected` unset on everything. */
export function resyncNodes(prev: TopoNode[], next: TopoNode[], highlighted: ReadonlySet<string>): TopoNode[] {
  const prevById = new Map(prev.map((n) => [n.id, n]))
  return keepSameNodes(prev, next.map((n) => {
    const old = prevById.get(n.id)
    if (old?.dragging) return old
    const keepOldPosition = old && old.parentId === n.parentId
    return keepNode(old, { ...n, position: keepOldPosition ? old.position : n.position, selected: highlighted.has(n.id) })
  }))
}

/** The node already on the canvas when the fresh one sets nothing differently (React Flow's own measured size and the like are not compared), else the fresh one. */
function keepNode(old: TopoNode | undefined, fresh: TopoNode): TopoNode {
  return old && (Object.keys(fresh) as (keyof TopoNode)[]).every((k) => deepEqual(old[k], fresh[k])) ? old : fresh
}

/** `prev` itself when `next` is the same nodes in the same order: a layout that changed nothing then costs React Flow no measuring and no new node list for every edge to route around. */
const keepSameNodes = (prev: TopoNode[], next: TopoNode[]) => (next.length === prev.length && next.every((n, i) => n === prev[i]) ? prev : next)

/** Whether two layouts put every box in the same place at the same size: what a toggle that only adds or removes lines (DNS & system traffic) leaves alone. */
export const sameLayout = (a: TopoNode[], b: TopoNode[]) =>
  a.length === b.length && a.every((n, i) => n.id === b[i].id && n.parentId === b[i].parentId && n.position.x === b[i].position.x && n.position.y === b[i].position.y && n.style?.width === b[i].style?.width && n.style?.height === b[i].style?.height)

/**
 * Sync `.selected` on the CURRENT nodes to match `highlighted`, and nothing else - the lighter-weight
 * counterpart to `resyncNodes` above, used on every selection change rather than only on a poll. Two things
 * matter here, for the same underlying reason `resyncNodes` already avoids touching a node mid-drag:
 *
 * 1. A node React Flow is actively dragging is left completely untouched. React Flow tracks a drag gesture
 *    through its own internal state; a `.selected` write from outside that gesture can fight it - and
 *    because React Flow treats even a plain click as a (near-instant) drag-start/drag-stop pair, this isn't
 *    only a concern for an intentional drag. Racing that internal state is what produced a real "Maximum
 *    update depth exceeded" crash (React error #185): clicking between two nodes could catch one of them
 *    mid-click's own transient drag.
 * 2. When nothing actually needs to change, the SAME array (and, per node, the SAME object) is returned -
 *    not a fresh one with identical contents. `.map()` alone always allocates a new array even when every
 *    element in it is unchanged, and hoisting that from a caller's `setNodes` always triggers a fresh
 *    render; a settled selection should produce zero re-renders, not one that merely looks like a no-op.
 */
export function syncSelected(nodes: TopoNode[], highlighted: ReadonlySet<string>): TopoNode[] {
  let changed = false
  const next = nodes.map((n) => {
    if (n.dragging) return n
    const want = highlighted.has(n.id)
    if (n.selected === want) return n
    changed = true
    return { ...n, selected: want }
  })
  return changed ? next : nodes
}

/**
 * Sync a `pick-ineligible` class onto every node "Pick from canvas" mode (TopologyPage's pickMode) can't
 * actually target - a device, a namespace sub-box, a cluster/service with no connected+approved agent -
 * so index.css can keep it dimmed and show a not-allowed cursor even on hover, instead of un-dimming every
 * node the same way and only discovering after a click that it led nowhere. `eligible` is null when
 * pickMode itself is off, which clears the class from every node (same as `syncSelected`'s "nothing
 * highlighted" case). Mirrors syncSelected exactly: skip a node mid-drag, and return the SAME array when
 * nothing actually changed so a settled pick-mode toggle doesn't force an extra render.
 */
export function syncPickEligibility(nodes: TopoNode[], eligible: ReadonlySet<string> | null): TopoNode[] {
  let changed = false
  const next = nodes.map((n) => {
    if (n.dragging) return n
    const want = eligible !== null && !eligible.has(n.id) ? 'pick-ineligible' : undefined
    if ((n.className ?? undefined) === want) return n
    changed = true
    return { ...n, className: want }
  })
  return changed ? next : nodes
}

/**
 * Bring `graph.nodes` (buildGraph's latest output) onto the canvas the right way for WHY it changed.
 *
 * `resyncNodes`'s rule 2 above - keep a survivor's stale position as long as its parent hasn't changed -
 * is exactly right for an ordinary background poll (this page polls every 2-5s): nothing the person did
 * should visibly move just because the same data came back again. But it stops being right the moment the
 * person changes what the canvas is even showing - a filter that hides or reveals cards, or any of the
 * other view toggles (grouping, devices, the mesh overlay, chain layout) - because `packItems`/
 * `layoutNamespaces` (above) repack a WHOLE group's items fresh every time its composition changes, and a
 * filter is exactly that: it adds or removes siblings from a box whose OTHER members keep the SAME parent,
 * so rule 3 (a changed parent forces a fresh position) never fires for them. Left on the stale rule-2 path,
 * survivors sit at coordinates that made sense for the OLD sibling set - a gap where a filtered-out
 * neighbor used to be, an overlap where a filtered-in one now lands - and the only way to see the clean,
 * freshly-packed layout was to leave the page and come back, which starts over with no stale positions to
 * preserve in the first place.
 *
 * `explicit` is that distinction, decided by the caller (TopologyPage: whether the URL's search params -
 * which every filter and view toggle goes through - changed since the last render, as opposed to only the
 * underlying topology data refreshing). When true, every node takes buildGraph's fresh position outright -
 * the same clean result a remount already gave for free - EXCEPT rule 1 above still applies: a node React
 * Flow is actively mid-gesture with is still returned completely untouched, explicit or not. Skipping that
 * guard here would hand React Flow a brand new object for whatever id it's mid-drag with the moment an
 * explicit change and a drag land in the same instant, which is exactly the "desync React Flow's own drag
 * tracking" failure resyncNodes' rule 1 exists to prevent - see syncSelected's doc comment for what that
 * failure mode actually looks like (a real "Maximum update depth exceeded" crash, React error #185). When
 * false, this is exactly `resyncNodes`.
 */
export function applyGraphUpdate(prev: TopoNode[], next: TopoNode[], highlighted: ReadonlySet<string>, explicit: boolean): TopoNode[] {
  if (!explicit) return resyncNodes(prev, next, highlighted)
  const prevById = new Map(prev.map((n) => [n.id, n]))
  return keepSameNodes(prev, next.map((n) => {
    const old = prevById.get(n.id)
    if (old?.dragging) return old
    return keepNode(old, { ...n, selected: highlighted.has(n.id) })
  }))
}

function makeEdge(
  id: string,
  source: string,
  target: string,
  abs: Map<string, Box>,
  d: {
    label: string
    mesh?: MeshVerdict
    cross: boolean
    aggregated: boolean
    /** True for any other group↔group edge that, unlike the aggregated dependency-count lines, still
     *  wants a real arrowhead (e.g. a regional operator's source-cluster arrows) - controls only the
     *  z-index (group↔group edges sit just above the group boxes, at 5), independently of `aggregated`,
     *  which is what actually hides the marker below. */
    groupLevel?: boolean
    from: string
    to: string
    sources?: string[]
    confidence?: string
    observed?: boolean
    stale?: boolean
    weight?: number
    quality?: PathQuality
    stats?: DependencyStats
    via?: 'ebpf' | 'conntrack'
    iface?: string
    ifaceSpeedMbps?: number
    retransmits?: number
    rttMs?: number
    jitterMs?: number
    handshakeMs?: number
    failedAttempts?: number
    sniHost?: string
    dnsQueryNames?: string[]
    dnsRttMs?: number
    activeCount?: number
    problem?: boolean
    count?: number
    role?: 'bundle' | 'detail'
    focusIds?: string[]
    /** Only set on a cross-cluster dependency: whether the target is reached over a flat/mesh-federated
     *  network route, or has to go out through its own external exposure (ingress, node port or load
     *  balancer) to be reached at all - the only two ways a call from outside the target's own cluster can
     *  land on it. Meaningless for a same-cluster call, which always reaches its target's ClusterIP
     *  directly regardless of whatever else that target happens to be exposed as. */
    route?: 'direct' | 'gateway'
    /** Set only for a ClusterLink edge - see EdgeData.clusterLink's own doc. Undirected in reality (two
     *  clusters either are, or are not, joined this way), so this suppresses the arrowhead the same way
     *  `aggregated` does, independently of `groupLevel`, which still wants its own real arrowhead. */
    clusterLink?: { kind: ClusterLink['kind']; via: string; redundancy: number; fromNode?: string; toNode?: string; fromAddress?: string; toAddress?: string; flowsObserved?: number; avgRttMs?: number; avgLossPct?: number; avgRtoRetransmitsPerMin?: number; avgMssBytes?: number; encryption?: ClusterLink['encryption'] }
    /** See EdgeData.tunnelLink's own doc - mutually exclusive with clusterLink above. */
    tunnelLink?: { fromCluster: string; toCluster: string; via: string; redundancy: number; encryption?: ClusterLink['encryption'] }
    protocols?: Record<string, number>
  },
): TopoEdge {
  const [ss, ts] = pickSides(abs.get(source)!, abs.get(target)!)
  return {
    id,
    source,
    target,
    // sourceHandle/targetHandle still need to name a real handle declared on each node (AllHandles in
    // nodes.tsx renders one per side) for React Flow's own bookkeeping, so pickSides' one-shot guess still
    // picks one - but OffsetEdge (below) no longer trusts that guess's actual on-screen position: every
    // edge now renders through it, and it recomputes a live anchor point from each node's current geometry
    // every render, so a card dragged to a new relative position (including inside a chain layout) gets a
    // freshly recalculated line instead of one still leaving from wherever pickSides guessed at build time.
    sourceHandle: `${ss}-s`,
    targetHandle: `${ts}-t`,
    type: 'offset',
    label: d.label,
    animated: false,
    // Animate a moving dash for genuinely live traffic (observed and not gone stale), not merely because
    // an edge crosses a cluster boundary - d.cross drove this before, which meant a purely-declared
    // cross-cluster dependency animated (implying live data flowing when there wasn't any) while a busy,
    // actually-observed same-cluster edge sat still. Cross-cluster edges keep their own distinct visual cue
    // (the grey crossGroup stroke color in TopologyPage's edge styling) - motion now means what the
    // busyness-scaled animationDuration right next to this already assumed it meant: real traffic.
    className: d.observed && !d.stale ? 'edge-animated' : undefined,
    // React Flow adds the higher of the two end nodes' z to this. Card↔card edges must land just below
    // the cards (10) so they never steal clicks; group↔group links sit just above the group boxes (0).
    zIndex: d.aggregated || d.groupLevel ? 5 : -1,
    markerEnd: d.aggregated || d.clusterLink ? undefined : { type: MarkerType.ArrowClosed, width: 14, height: 14 },
    data: { crossGroup: d.cross, aggregated: d.aggregated, from: d.from, to: d.to, sources: d.sources, confidence: d.confidence, observed: d.observed, stale: d.stale, weight: d.weight, quality: d.quality, mesh: d.mesh, stats: d.stats, via: d.via, iface: d.iface, ifaceSpeedMbps: d.ifaceSpeedMbps, retransmits: d.retransmits, rttMs: d.rttMs, jitterMs: d.jitterMs, handshakeMs: d.handshakeMs, failedAttempts: d.failedAttempts, sniHost: d.sniHost, dnsQueryNames: d.dnsQueryNames, dnsRttMs: d.dnsRttMs, activeCount: d.activeCount, route: d.route, clusterLink: d.clusterLink, tunnelLink: d.tunnelLink, protocols: d.protocols, problem: d.problem, count: d.count, role: d.role, focusIds: d.focusIds },
  }
}

/**
 * What is written on the line: only what it is ("TCP:5432"). The numbers (traffic, round trip, loss, how it
 * was found) are in the inspector when the line is selected, so a line never carries a paragraph.
 */
function edgeLabel(d: Dependency): string {
  return d.label ?? (d.port ? `${d.protocol}:${d.port}` : d.protocol)
}

/**
 * How a cross-cluster call actually reaches its target: 'direct' over a flat/mesh-federated network route,
 * or 'gateway' when the target's own external exposure (ingress, node port or load balancer) is the only
 * way in from outside its cluster. Undefined for a same-cluster call (always its target's ClusterIP,
 * regardless of whatever else that target happens to be exposed as) or when the target isn't a known
 * service (a device or external endpoint has no comparable exposure concept).
 */
function crossClusterRoute(d: Dependency, to: Service | undefined): 'direct' | 'gateway' | undefined {
  if (!d.crossCluster || d.toKind !== 'service' || !to) return undefined
  return to.exposure && to.exposure !== 'internal' ? 'gateway' : 'direct'
}

/** How busy an edge is on a log scale, 0..1, so a chatty database link does not flatten everything else. */
function weightOf(d: Dependency): number | undefined {
  const s = d.stats
  const x = s?.bytesPerSec ? s.bytesPerSec : s?.connectionsPerMin ? s.connectionsPerMin * 200 : 0
  if (!x) return undefined
  return Math.min(1, Math.log10(1 + x) / 6)
}
