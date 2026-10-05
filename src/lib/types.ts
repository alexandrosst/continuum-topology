/**
 * Domain model (schema v3).
 *
 * Deliberately graph-shaped (entities with ids + typed relations) so it maps 1:1
 * onto a property graph later (Neo4j: (:Cluster)-[:HAS_NODE]->(:Node),
 * (:Service)-[:RUNS_ON]->(:Node), (:Service)-[:CALLS]->(:Service)).
 *
 * Two kinds of data live here:
 *  - Topology: the graph itself (clusters, nodes, services, devices, …). Goes to Neo4j.
 *  - Control:  how the graph was learned (agents, suggestions, audit). Goes to Postgres.
 */

/** Position of a cluster on the computing continuum. */
export type Tier = 'cloud' | 'edge' | 'far-edge'

/** Where an entity came from. Manual today; discovery/import tomorrow. */
export type Source = 'manual' | 'discovered' | 'imported'

export type Status = 'healthy' | 'degraded' | 'offline' | 'unknown'

/**
 * Format version of the stored workspace and of exported files.
 *  4: declared intent only. Records that agents discovered are not stored; what a person said about one of them
 *     (overrides, assignments) is kept under `refs`, keyed by the record's stable id.
 */
export const SCHEMA_VERSION = 4
/** Single-tenant today; every record carries an org id so multi-tenancy is additive later. */
export const DEFAULT_ORG = 'default'

export type Confidence = 'high' | 'medium' | 'low'

/** Why discovery believes a value: the signal it saw and how sure it is. */
export interface Evidence {
  signal: string
  confidence: Confidence
  detail?: string
}

/** Who set an override by hand, and when - shown next to the value so a confirmed field says who stands behind it. */
export interface OverrideMeta {
  by: string
  at: string
}

export interface Provenance {
  orgId: string
  source: Source
  /**
   * Stable identity assigned by the world, not by us (kube-system UID for a
   * cluster, machine-id / systemUUID for a node, cluster+namespace+kind+name for
   * a service). Rediscovery matches on this, so ids can stay opaque.
   */
  key?: string
  /** ISO timestamp of last discovery/sync (unused for manual entities). */
  lastSeen?: string
  /** ISO timestamp the reported values were detected at. */
  detectedAt?: string
  /** Agent that last reported this record, and its sync revision. */
  agentId?: string
  revision?: number
  /** Not seen for several heartbeats: kept and flagged instead of silently deleted. */
  stale?: boolean
  /** Agent reported it gone. Kept as a tombstone for history; hidden from views. */
  deletedAt?: string
  /** Per-field detection evidence, e.g. { kind: { signal: 'DMI: Amazon EC2', confidence: 'high' } }. */
  evidence?: Record<string, Evidence>
  /**
   * Two-layer values: for discovered entities the base fields hold what the
   * agent last reported and `overrides` holds what a person changed. The value
   * shown everywhere is `{ ...base, ...overrides }` (see lib/effective.ts), so
   * rediscovery never erases a human edit, and clearing an override falls back
   * to the detected value.
   */
  overrides?: Record<string, unknown>
  /**
   * Who confirmed or set each overridden field, and when - keyed the same way as `overrides`. Only present for
   * fields a person actually touched from this browser onward; older overrides (or ones restored from an import
   * that predates this) simply have no entry, and are shown as "you confirmed this" with no attribution rather
   * than guessing one.
   */
  overrideMeta?: Record<string, OverrideMeta>
  /**
   * How far a discovered record can be trusted right now, as the server computed it from the clock, the agent's
   * connection and the staleness setting. Absent on records nobody observes.
   */
  state?: ObservationState
  /** A sentence for a person: "stale for 2 h", "agent revoked 3 h ago". Empty when live. */
  stateReason?: string
}

/** live: connected and heard from recently. disconnected: stream closed, picture recent. stale: silent too long. revoked: agent revoked. */
export type ObservationState = 'live' | 'disconnected' | 'stale' | 'revoked'

/**
 * A record that disappeared from what its agent reports. Kept by the server for a retention window so it can be
 * shown as gone, with when and why, instead of silently vanishing.
 */
export interface Tombstone {
  kind: 'cluster' | 'node' | 'namespace' | 'service'
  id: string
  name: string
  clusterId?: string
  agentId?: string
  goneAt: string
  lastSeen?: string
  reason: string
  /** The record as it was last known. */
  record?: Record<string, unknown>
}

/** What a person said about a discovered record: values they overrode and assignments they made. */
export interface DeclaredRef {
  kind: 'cluster' | 'node' | 'namespace' | 'service'
  overrides?: Record<string, unknown>
  /** Who set each overridden field, and when - see Provenance.overrideMeta; carried through the same way overrides are. */
  overrideMeta?: Record<string, OverrideMeta>
  applicationId?: string
  siteId?: string
}

export type SiteKind = 'cloud-region' | 'data-center' | 'edge-site'
export type TrustZone = 'public' | 'private' | 'restricted'

/** A place on the map. Clusters and devices point at a site; sites are linked with measured RTT/loss. */
export interface Site {
  id: string
  orgId: string
  name: string
  kind: SiteKind
  lat: number
  lng: number
  /** ISO 3166-1 alpha-2 code, e.g. "GR". */
  country: string
  city?: string
  /** Policy: jurisdiction the data must stay in (e.g. "EU"), for placement decisions. */
  dataResidency?: string
  trustZone?: TrustZone
}

/** Network path between two sites. Measured by the agents, or declared by a person. */
export interface SiteLink {
  id: string
  orgId: string
  a: string
  b: string
  rttMs?: number
  lossPct?: number
  mbps?: number
  measuredAt?: string
  source: 'measured' | 'declared'
}

/**
 * A measured network path from one onboarded cluster to an address: how long a TCP connection takes to open
 * (no data is sent) and how many attempts failed. Derived by the server; never part of the stored workspace.
 */
export interface Path {
  id: string
  fromCluster: string
  fromName: string
  host: string
  port: number
  label?: string
  /** Set when the address belongs to another onboarded cluster. */
  toCluster?: string
  toName?: string
  /** observed: the cluster already talks to this address; manual: an administrator asked for it. */
  source: 'observed' | 'manual'
  rttMinMs: number
  rttP50Ms: number
  rttP95Ms: number
  /** Share of connection attempts that failed in the recent window, 0-100. */
  lossPct: number
  samples: number
  at: string
  stale?: boolean
}

export interface Application extends Provenance {
  id: string
  name: string
  description: string
  /** How the grouping was decided (explicit, argo, helm, part-of, namespace…). */
  origin: string
  confidence: Confidence
}

export type ExternalKind = 'saas' | 'database' | 'unknown'

/** Something a service calls that lives outside every onboarded cluster. */
export interface ExternalEndpoint extends Provenance {
  id: string
  host: string
  /** What a person calls it ("Stripe API"), when they have named an address that was only ever seen. */
  name?: string
  port?: number
  kind: ExternalKind
  /** The application usually found on `port` (e.g. "PostgreSQL", "Kafka") - a guess from the port number
   * alone, same caveat as Dependency.service: a workload can run anything on any port. */
  service?: string
  /** Every individual address observed that resolved to this same endpoint identity - just [host] for the
   * common case of one address, one endpoint, but more than one when several addresses collapsed into a
   * single node (several of Google's or GitHub's own edge addresses, for example, are "the same thing" and
   * share one node on the canvas). Sorted for a stable order across polls. */
  ips?: string[]
}

/**
 * A server-confirmed network-level relationship between two onboarded clusters: either joined through an
 * overlay/tunnel, or sitting on the very same flat subnet with no tunnel at all. See the backend's
 * ClusterLink doc for exactly what evidence this is (and is not) built from - real, kernel-reported
 * routing/address data from both sides, never a guess from naming or a declared external-exposure flag
 * the way Dependency.route is. Derived fresh on every poll, like Path and Dependency above it; never part
 * of the stored workspace, and (like TunnelInterface.confirmed, which this draws on) not available for a
 * past/historic view - it is a live correlation across the current topology, not a recorded fact.
 */
export interface ClusterLink {
  fromCluster: string
  fromName: string
  toCluster: string
  toName: string
  /** overlay: joined through a tunnel/overlay interface. subnet: same flat network segment, no tunnel. */
  kind: 'overlay' | 'subnet'
  /** The specific evidence: a tunnel's name and kind ("wg0 (wireguard)") for an overlay link, or the
   *  shared subnet prefix ("10.0.5.0/24") for a subnet link - never just "connected" with nothing to
   *  point at. */
  via: string
  /** How many independently corroborating node pairs back this link - always at least 1. More than 1
   *  means there is more than one path between these two clusters for this kind (e.g. a second WireGuard
   *  peering kept for failover): losing one does not necessarily cut the clusters off from each other. */
  redundancy: number
  /** The specific node on each side whose tunnel (or shared subnet) first corroborated this link - named
   *  so the evidence points at an actual machine, not only a cluster pair and a driver name. When
   *  redundancy is more than 1, these name only the first matching pair found. */
  fromNode?: string
  toNode?: string
  /** The two tunnel interfaces' own addresses that confirmed an "overlay" link (e.g. "10.8.0.1/24" and
   *  "10.8.0.2/24"). Empty for a "subnet" link, where `via` already is the complete evidence. */
  fromAddress?: string
  toAddress?: string
  /** How many live Dependency flows were actually matched onto this link's confirmed tunnel interface(s),
   *  scoped by the actual cluster PAIR both of the dependency's endpoints resolve to (one side in
   *  fromCluster, the other in toCluster) - never by interface name alone, and never by just the calling
   *  side's own cluster either, since a name like "wg0" is commonly reused across unrelated tunnels and
   *  the same cluster can sit on one side of more than one confirmed link. Only ever set for a "overlay"
   *  link; a "subnet" link has no specific interface to correlate flows against. Undefined/0 means no
   *  matching flow was observed yet, not that none exists. */
  flowsObserved?: number
  /** Average RTT (ms) across the matched flows that have a measured sample. Undefined when none of them
   *  do yet - the same "0 means not measured" convention Dependency.rttMs itself uses, never a fabricated
   *  0ms. */
  avgRttMs?: number
  /** Average loss percentage across the matched flows that have a measured sample. Undefined when none
   *  of them do yet, never a fabricated 0%. */
  avgLossPct?: number
  /** Average rtoRetransmitsPerMin (not the raw, undifferentiated retransmit rate) across the same matched
   *  flows avgRttMs/avgLossPct roll up - specifically the RTO-timer-fired subset, since this field exists
   *  to say something about the tunnel's own link quality, and a fast retransmit recovering from ordinary
   *  reordering says nothing about that. Same "0 means not measured" tolerated ambiguity as
   *  rtoRetransmitsPerMin itself (a per-minute rate, unlike avgLossPct's percentage, has no zero-
   *  denominator case that needs telling apart from a real 0). */
  avgRtoRetransmitsPerMin?: number
  /** Average effective segment size (bytes) across the same matched flows avgRttMs/avgLossPct roll up -
   *  this is where mssBytes matters most: a healthy direct path's MSS is bounded by the interface MTU
   *  alone, while this link's own encapsulation overhead (VXLAN/WireGuard/GRE headers) eats into it
   *  further, so a falling avgMssBytes over time is a real, measured sign of growing per-packet overhead
   *  on this specific tunnel. Undefined when none of the matched flows have a measured sample yet, the
   *  same "undefined means not measured" convention avgRttMs itself uses. */
  avgMssBytes?: number
  /** Only set for an "overlay" link: whether its confirmed tunnel driver encrypts traffic by design
   *  (WireGuard, or an IPsec virtual-tunnel kind) or carries none of its own (VXLAN, GRE and its
   *  variants, IP-in-IP) - see the backend's TunnelEncryptionPosture. This is an inference from the
   *  driver type alone, the same honesty line the mesh mTLS verdict already draws elsewhere: it makes
   *  encryption very likely or very unlikely, but nothing here proves what a given packet actually was.
   *  "unknown" only for a driver kind outside that function's own closed list; undefined for a "subnet"
   *  link, which has no tunnel to classify at all. */
  encryption?: 'encrypted' | 'plaintext' | 'unknown'
}

/**
 * The broader "can these two clusters actually reach each other, and how" answer for a cluster pair that
 * already has SOME relationship - an observed cross-cluster Dependency, or a ClusterLink already
 * confirmed above. ClusterLink alone only ever positively confirms two of the four honest outcomes this
 * covers; see the backend's model.ClusterPairConnectivity doc for exactly what each `status` means and
 * is backed by. Deliberately never computed for every possible pair of onboarded clusters - only pairs
 * that already relate to each other (traffic crossing them, or a confirmed link) earn a row at all, the
 * same cardinality-bounded spirit ClusterLink correlation itself already follows. Derived fresh on every
 * poll, like ClusterLink; not available for a past/historic view, for the same reason.
 */
export interface ClusterPairConnectivity {
  fromCluster: string
  fromName: string
  toCluster: string
  toName: string
  /** tunnel: an overlay ClusterLink backs this pair (see `links`). subnet: a "subnet" ClusterLink backs
   *  it and no overlay one does. unexplained: traffic crosses this pair, both sides reported enough of
   *  their own network evidence (tunnels or host subnets) for a correlation to genuinely have been
   *  attempted, and none of it matched - a real finding, not an absence. unknown: one or both clusters
   *  never reported any tunnel or host subnet at all, so there is not enough evidence to say either way -
   *  never collapsed into "unexplained", which would claim a negative never actually checked. */
  status: 'tunnel' | 'subnet' | 'unexplained' | 'unknown'
  /** This pair's own confirmed ClusterLink(s) when status is "tunnel" or "subnet" - the exact same
   *  record(s) already in Topology.clusterLinks (an overlay and a subnet link for the same pair are
   *  independent facts and can both be present). Undefined for "unexplained"/"unknown", which have no
   *  such link to point at. */
  links?: ClusterLink[]
  /** How many observed cross-cluster Dependencies connect exactly this pair right now, regardless of
   *  which interface they used - a coarser count than any one ClusterLink's own flowsObserved (which
   *  only ever counts flows matched to one confirmed tunnel's own interface names). 0 when this pair
   *  exists only because of a ClusterLink with no live traffic currently observed crossing it. */
  dependencyFlows?: number
  /** Explains an "unexplained" or "unknown" status, in the same signal/confidence/detail shape Evidence
   *  already uses everywhere else - never a new, one-off shape for this one field. Undefined for
   *  "tunnel"/"subnet", whose evidence already lives on `links`. */
  evidence?: Evidence
}

export interface Cluster extends Provenance {
  id: string
  siteId?: string
  name: string
  tier: Tier
  distribution: string // e.g. EKS, k3s, kubeadm, MicroK8s
  version: string // e.g. v1.30.2
  provider: string // e.g. AWS, On-prem, Hetzner
  region: string // free text: region / site / location
  status: Status
  labels: Record<string, string>
  /* discovered attributes (all optional) */
  apiEndpoint?: string // host only, never credentials
  cni?: string
  ingress?: string
  podCidr?: string
  serviceCidr?: string
  storageClasses?: string[]
  /** When the cluster was created (kube-system's creation time), ISO 8601. */
  createdAt?: string
  /** Public IP the cluster reaches us from (input for the GeoIP suggestion). */
  egressIp?: string
  cloudAccount?: string
  /* policy */
  trustZone?: TrustZone
  dataResidency?: string
  /** The service mesh found in the cluster, read from its configuration (never from proxy telemetry). */
  mesh?: ClusterMesh
  /** Pods waiting to be scheduled (Pending phase, no node assigned yet) across every namespace the agent
   *  can see - fewer than the whole cluster's under namespaced RBAC, same limitation serviceCidr already
   *  has. Undefined means pods were never read (below tier 2), not that none are pending. */
  pendingPodCount?: number
}

export type MeshKind = 'istio' | 'linkerd' | 'consul' | 'kuma' | string
export type MtlsMode = 'strict' | 'permissive' | 'disabled' | 'automatic' | 'unknown'

/** What the cluster's mesh is configured to do. Inferred from labels, annotations, container names and policy objects. */
export interface ClusterMesh {
  kind: MeshKind
  mode: 'sidecar' | 'ambient' | string
  version?: string
  /** Mesh-wide mutual TLS between meshed workloads. */
  mtls: MtlsMode
  namespaceMtls?: Record<string, MtlsMode>
  /** Service ids of the mesh's own control plane and gateways. */
  controlPlane: string[]
  /** False when the agent was not allowed to read the mesh's policy objects, so `mtls` is a default and not a fact. */
  policyRead: boolean
  policyNote?: string
}

/** One service's relation to the mesh. */
export interface ServiceMesh {
  mesh: MeshKind
  proxy?: 'sidecar' | 'ambient'
  bypass?: boolean
  /** "in:8080", "out:5432": ports kept out of the proxy. */
  excludedPorts?: string[]
  controlPlane?: boolean
  /** pods (a proxy was seen) | workload | namespace (only what the namespace asks for). */
  source?: 'pods' | 'workload' | 'namespace' | string
}

export type NodeRole = 'control-plane' | 'worker'
export type MachineKind = 'vm' | 'bare-metal' | 'edge-device'
export type Connectivity = 'ethernet' | 'wifi' | 'cellular' | 'satellite' | 'lora' | 'zigbee' | 'ble' | 'serial' | 'unknown'

export interface Accelerator {
  vendor: string
  model: string
  count: number
}

/** One physical uplink a node probe saw on the machine itself. Never an address - just what kind of
 * link it is and, when the driver reports it, its negotiated speed and MTU. */
export interface NetworkInterface {
  name: string
  kind: 'ethernet' | 'wifi' | 'cellular' | string
  speedMbps?: number
  mtu?: number
}

/** One physical block device a node probe saw on the machine itself: capacity and type only, never a
 * serial number, WWN or any other per-disk identifier. */
export interface Disk {
  name: string // kernel device name, e.g. "sda", "nvme0n1" - not stable across reboots
  model?: string
  sizeBytes?: number
  type?: 'hdd' | 'ssd' | 'nvme' | string
}

/** One overlay/tunnel network interface a node probe found up on the machine (WireGuard, VXLAN, Geneve,
 * GRE, IPIP/SIT, route-based IPsec/VTI/XFRM) - identified generically from the kernel's own link kind,
 * never guessed from an interface's name. See the backend's TunnelInterface proto doc for the full list
 * of covered drivers and the two documented gaps (plain tun/tap, and policy-based IPsec with no dedicated
 * link). */
export interface TunnelInterface {
  name: string
  kind: string // wireguard | vxlan | geneve | gre | gretap | ip6gre | ip6gretap | ipip | sit | vti | vti6 | xfrm
  addresses?: string[]
  routes?: string[]
  /** This tunnel's own MTU, from the same netlink link dump `kind` is read from - undefined when
   *  unreported. A tunnel with a lower MTU than the physical path underneath it is a classic overlay
   *  gotcha (larger packets silently fragment, or get dropped outright where a middlebox blocks
   *  fragmentation). */
  mtu?: number
  /** Administrative up/down state (netlink IFF_UP) - not a guarantee the tunnel is currently passing
   *  traffic, only that it has not been disabled. Deliberately not the kernel's operational-state field:
   *  several common tunnel drivers (WireGuard among them) never report anything but "unknown" there even
   *  while fully up and carrying traffic. Always present (unlike mtu, which can genuinely be unreported). */
  up: boolean
  /** The other node this tunnel was matched to, set server-side only when one of `routes`' prefixes
   *  contains an address another onboarded node is independently known by - the same "declared vs.
   *  confirmed" distinction Dependency.sources already draws elsewhere. Empty means this tunnel's other
   *  end is not visible anywhere else in the topology, not that it doesn't exist - most tunnels
   *  legitimately lead somewhere outside any onboarded cluster. */
  confirmed?: string
}

export interface Resources {
  cpu: number
  memoryGb: number
  /** Root filesystem capacity ("ephemeral-storage" in Kubernetes' own Capacity/Allocatable) - what pod
   *  ephemeral storage, images and logs actually share, not any one physical disk (MachineNode.disks below
   *  has those, from the node probe). Unset when the kubelet does not report it. */
  diskGb?: number
}

export interface MachineNode extends Provenance {
  id: string
  name: string
  clusterId: string
  /** Names this machine had before (a rename keeps the record), and what its identity is made of. */
  aliases?: string[]
  identityBasis?: 'provider-id' | 'system-uuid' | 'machine-id' | 'name'
  role: NodeRole
  kind: MachineKind
  ip: string
  os: string
  cpu: number // cores
  memoryGb: number
  /** Mirrors Resources.diskGb above: this node's own root filesystem capacity. */
  diskGb?: number
  status: Status
  labels: Record<string, string>
  /* discovered attributes (all optional) */
  arch?: string // amd64, arm64…: decides which images can run here
  kernel?: string
  runtime?: string // containerd 1.7…
  kubeletVersion?: string
  instanceType?: string
  zone?: string
  hardwareModel?: string // e.g. Raspberry Pi 5, NVIDIA Jetson Orin
  /** The hypervisor or cloud platform under a VM ("KVM/QEMU", "Amazon EC2"). Only known when the node probe reported. */
  virtualization?: string
  /** The machine can run without mains power (laptop, board with a battery). Node probe only. */
  hasBattery?: boolean
  /** A node probe reported on this machine, so type and hardware come from the machine itself. */
  probed?: boolean
  /** The CPU model name the probe read from the machine, the real host CPU regardless of any cgroup
   * limit - unlike `cpu` above, which is Kubernetes' allocatable millicore view. Node probe only. */
  cpuModel?: string
  /** Logical CPUs (hardware threads) the probe saw on the machine itself. Node probe only. */
  cpuThreads?: number
  /** Physical uplinks the probe saw, with whatever speed/MTU sysfs reported. `connectivity` above is
   * derived from these (the kinds present); this is the fuller, per-interface view. Node probe only. */
  networkInterfaces?: NetworkInterface[]
  /** Physical disks the probe saw, capacity and type only (never a serial/WWN). Node probe only. */
  disks?: Disk[]
  /** Overlay/tunnel interfaces the probe found up (WireGuard, VXLAN, GRE, IPIP/SIT, route-based IPsec,
   *  ...). Node probe only. See TunnelInterface's own comment for exactly what is, and is not, covered. */
  tunnels?: TunnelInterface[]
  /** This node's own routable network prefix(es) (e.g. "10.0.5.12/24"), taken only from whichever
   *  interface owns the machine's default route. Node probe only; used server-side, alongside `tunnels`,
   *  to find clusters joined at the network level (see ClusterLink) - never shown as a claim on its own
   *  that two addresses are related. */
  hostSubnets?: string[]
  providerId?: string
  allocatable?: Resources
  requested?: Resources
  accelerators?: Accelerator[]
  taints?: string[]
  /** Active problem conditions only (MemoryPressure, DiskPressure, NetworkUnavailable…). */
  conditions?: string[]
  connectivity?: Connectivity
  /** When the node joined the cluster, ISO 8601. */
  createdAt?: string
  /** Most pods the kubelet will run here; the real limit on small boards. */
  podCapacity?: number
  /** Pods placed on the node. Absent when the agent does not read pods: unknown, not zero. */
  podCount?: number
  /** cgroup v2 PSI pressure-stall percentages (see Documentation/accounting/psi.rst): the share of the
   *  last 60 seconds this whole machine had at least one task stalled waiting on cpu/memory/io that
   *  could otherwise have made progress - a direct, measured bottleneck signal, not inferred from a raw
   *  usage percentage the way `cpu`/`memoryGb` above are. Node probe only, like cpuModel/cpuThreads
   *  above (independent of whether the probe could also confidently classify the machine's kind -
   *  `probed` can be false while these are still set). Undefined means not read: no node probe, a
   *  cgroup v1 host (no unified hierarchy at all), or a kernel too old for PSI - never a fabricated 0%. */
  cpuPressurePct?: number
  memoryPressurePct?: number
  ioPressurePct?: number
  /** How many times cgroup v2's own per-cgroup OOM-kill accounting has fired across every cgroup on
   *  this node, summed - a live, monotonically increasing total (the backend diffs two readings a
   *  report-window apart the same way it already diffs every other cumulative counter). Node-level
   *  only: no exact timestamp or pid, no per-pod attribution - see the backend's
   *  HostProbe.oom_kill_count doc for exactly what that gap does and does not block. Undefined means
   *  not read (no node probe, a cgroup v1 host, or a kernel with cgroups disabled), never a fabricated
   *  0. Worth reading alongside this node's own pods' flow data for the same window: a kill landing
   *  there is a real, node-side explanation a flow row's retransmit growth alone could never provide. */
  oomKillCount?: number
  /** This node's current per-interface network throughput and, where the interface's own rated speed
   *  could be read, what share of it is in use right now - computed by the flow collector from this
   *  window's real flow byte counters (see the backend's continuumv1.LinkSaturation proto doc), not by
   *  the node probe. A live, traffic-driven utilization gauge: distinct from
   *  NetworkInterface.speedMbps above, which is only ever a static, unrelated sysfs reading of the
   *  interface's own rated capacity with no throughput attached. Undefined when no flow collector has
   *  reported one for this node (an older collector, or simply nothing seen on any interface yet). */
  linkSaturation?: LinkSaturation[]
  /** How many connect() attempts on this node have failed with EADDRNOTAVAIL (ephemeral port / SNAT
   *  exhaustion) since its flow collector's eBPF program was loaded - a collector-side running total
   *  (see the backend's continuumv1.FlowReport.snat_exhaustion doc for the full story, including why
   *  the kernel function behind this, inet_hash_connect, is an internal one with no ABI stability
   *  guarantee: a future kernel can simply stop this counting anything). Undefined when nothing has
   *  been reported for this node (an older collector, conntrack rather than eBPF, or a kernel where
   *  neither of the collector's two attach paths worked) - indistinguishable from a real, confirmed 0,
   *  unlike oomKillCount above, since the one value worth a person's attention here is "greater than
   *  zero", not "confirmed healthy". Most relevant on a gateway node running many short-lived outbound
   *  connections (a NAT/egress point, or anything proxying lots of small CoAP/MQTT-style flows): such a
   *  node can look perfectly healthy by every other signal here while this climbs, since a connection
   *  that failed before reaching ESTABLISHED never shows up as a Dependency or a retransmit. */
  snatExhaustion?: number
  /** This node's current tracepoint-derived CPU thermal-throttling counters (power:cpu_frequency /
   *  thermal:thermal_zone_trip - see the backend's continuumv1.ThermalThrottle doc for the full story)
   *  - computed by the flow collector's eBPF program, not the node probe, the same collector-sourced
   *  origin snatExhaustion above has. cpuFreqChangeCount is corroboration only, never itself throttling
   *  evidence (a cpufreq governor fires it constantly under completely ordinary load); thermalTripCount
   *  is the decisive signal - the backend downgrades this node's own cpuCapacity attribute's confidence
   *  once it is nonzero (see the backend's twin/model.go), since the figure Kubernetes reports may no
   *  longer be sustainable once the hardware starts shedding heat. This is capacity derating from heat,
   *  never a power/energy reading - do not confuse either field with an external power/energy exporter
   *  such as Kepler (a wholly separate, optional telemetry pipeline). Both undefined/0 when the
   *  collector's eBPF program never attached to either tracepoint on this kernel (an old kernel, a
   *  VM/board with no thermal zones, or an older collector) - indistinguishable from a real, healthy 0,
   *  the same ambiguity snatExhaustion above already leaves unresolved, since the one value worth a
   *  person's attention here is thermalTripCount being greater than zero. */
  cpuFreqChangeCount?: number
  thermalTripCount?: number
}

/** One physical network interface's current send+receive throughput on a node, and, when its rated
 *  speed could be read, what share of it is currently in use - see MachineNode.linkSaturation's own
 *  doc comment for the fuller story of where this comes from. */
export interface LinkSaturation {
  iface: string
  throughputBps: number
  /** 0-100, uncapped only below (never negative); see the backend's continuumv1.LinkSaturation.saturation_pct
   *  doc for why it is clamped at 100 but not 0, and absent (not a fabricated 0%) when the interface's
   *  own rated speed could not be read (e.g. a virtual interface, or sysfs permission denied). */
  saturationPct?: number
}

/** A namespace: the default unit for grouping services into applications. */
export interface Namespace extends Provenance {
  id: string
  clusterId: string
  name: string
  labels: Record<string, string>
  applicationId?: string
  /** The namespace asks for its workloads to join this mesh through this kind of proxy. */
  mesh?: string
  meshProxy?: string
  /** The namespace opts out of the mesh. */
  meshOff?: boolean
  /** Mutual TLS mode when it differs from the mesh-wide one. */
  mtls?: MtlsMode
}

export type ServiceKind = 'Deployment' | 'StatefulSet' | 'DaemonSet' | 'Job'
export type Exposure = 'internal' | 'node-port' | 'load-balancer' | 'ingress'
export type ManagedBy = 'helm' | 'argo' | 'flux' | 'kustomize' | 'operator' | 'kubectl' | 'unknown'
export type Sensitivity = 'public' | 'internal' | 'confidential'

/** A persistent volume claim used by a service. */
export interface ServiceVolume {
  name: string
  storageClass?: string
  sizeGb: number
  accessModes?: string[]
  phase?: string // Bound | Pending | Lost
  /** Set when the data is tied to these nodes (local volumes) and cannot follow the pod. */
  pinnedNodeIds?: string[]
}

export interface ServiceAutoscaler {
  min: number
  max: number
  current: number
  /** e.g. "cpu 80%". */
  targets?: string[]
}

export interface ServiceDisruption {
  minAvailable?: string // "1" or "50%"
  maxUnavailable?: string
  /** Pods that may be evicted right now. */
  allowed: number
}

/** One replica backing a Service right now - name, where it's running, and the handful of per-pod facts
 *  that get flattened away the moment they're summed into the service's own aggregate fields (restarts,
 *  readyReplicas): in particular `createdAt` is this pod's own age, unlike Service.createdAt (the workload
 *  object's age, which never changes on a routine scale-up) - the one fact that can actually show a recent
 *  scaling event. Deliberately as minimal as Service's own discovered fields: no pod IP ever leaves the
 *  agent (see Address's own doc comment on the same rule) - `traffic` below is no exception, since it
 *  only ever names the OTHER side of a flow (a workload or an external address, exactly what Dependency
 *  already exposes at the service level), never this pod's own address. */
export interface Pod {
  name: string
  /** Set only when the pod's own node is itself known to this topology - empty when the node was filtered
   *  out of scope or the pod is not yet scheduled, the same rule Service.nodeIds already follows. */
  nodeId?: string
  /** Pending | Running | Succeeded | Failed | Unknown - exactly as Kubernetes reports it. */
  phase: string
  ready?: boolean
  /** This pod's own restart count (every container's, summed) - Service.restarts above is already this same
   *  number summed again across every pod, which flattens one unusually-crashy replica among otherwise-
   *  healthy ones into an unremarkable average. */
  restarts?: number
  createdAt?: string
  /** This one pod's own breakdown of who it talks to, right now - unlike a Dependency edge (historically
   *  accumulated, always present once observed), this is a point-in-time snapshot from the agent's latest
   *  report: absent below access tier 2, before the agent's first flow report since this pod started, or
   *  when every one of its connections currently goes through a Service address rather than being
   *  addressed to this pod directly. Meant to be fetched/shown only once this specific pod is expanded -
   *  never drawn as permanent edges on the canvas, which is exactly why this stays a per-pod list rather
   *  than feeding the aggregate dependency graph. */
  traffic?: PodPeer[]
}

/** One line of Pod.traffic: this pod, and one workload or external address it has been talking to.
 *  Several PodPeers can share the same `peer` (one in, one out, or different ports) - each is its own
 *  observed edge, never merged across ports/protocols/directions the way a Dependency already is. */
export interface PodPeer {
  /** The other side's identity: a Service id (joinable against Service.id, same as Dependency.from/to
   *  already give for the "service" case) when peerKind is "service", or the raw address when peerKind is
   *  "external" - the backend cannot resolve that case to an id this early (see PodPeer's own Go doc
   *  comment), so it stays a bare address there. */
  peer: string
  peerKind: 'service' | 'external'
  /** This pod's own role in the flow: "out" when it is the caller, "in" when it is the one receiving. */
  direction: 'out' | 'in'
  port: number
  protocol: string
  connections: number
  bytesOut?: number
  bytesIn?: number
}

export interface Service extends Provenance {
  id: string
  applicationId?: string
  name: string
  namespace: string
  clusterId: string
  kind: ServiceKind
  image: string
  replicas: number
  /** Nodes this service is scheduled on (placement). Must belong to clusterId. */
  nodeIds: string[]
  status: Status
  labels: Record<string, string>
  /* discovered attributes (all optional) */
  readyReplicas?: number
  imageDigest?: string
  cpuRequestM?: number // millicores
  memRequestMi?: number
  cpuLimitM?: number
  memLimitMi?: number
  ports?: number[]
  exposure?: Exposure
  hosts?: string[]
  managedBy?: ManagedBy
  /** Placement constraints: what an orchestrator may and may not do with this service. */
  nodeSelector?: Record<string, string>
  tolerations?: string[]
  restarts?: number
  /** Containers, across this service's pods, whose most recently known termination reason is OOMKilled -
   *  a live snapshot of Kubernetes' own per-container LastTerminationState (which holds only the ONE most
   *  recent termination reason per container), not a cumulative historical tally: a container OOM-killed
   *  repeatedly while staying on the same pod still contributes at most 1, and that 1 reverts to 0 the
   *  moment it next fails for any other reason. Still a sharper signal than restarts above for the common
   *  case: a restart can be a crash, a deploy or a failed liveness probe, while a nonzero value here means
   *  the pod's last restart specifically was an OOM kill. */
  oomKills?: number
  /** This service's individual replicas right now - absent/empty means either no pods are up or the agent's
   *  tier doesn't collect them, same convention as every other optional discovered list here. */
  pods?: Pod[]
  /** Application discovery grouped this service into. Applied automatically once a person has accepted that application. */
  applicationHint?: string
  mesh?: ServiceMesh
  createdAt?: string
  /** Persistent storage the service mounts. Empty/absent means stateless (or the storage module is off). */
  volumes?: ServiceVolume[]
  /** Horizontal autoscaler targeting this service: replicas may change without a person. */
  autoscaler?: ServiceAutoscaler
  /** Disruption budget: how many pods an orchestrator may take down at once. */
  disruption?: ServiceDisruption
  /* policy */
  sensitivity?: Sensitivity
}

export type DeviceKind = 'sensor' | 'actuator' | 'camera' | 'plc' | 'gateway' | 'tag' | 'vehicle' | 'other'

/**
 * A non-Kubernetes thing that belongs to an application: sensors, cameras, PLCs,
 * actuators. Usually a fleet of identical units at one site, so a record has a
 * `count` instead of one row per unit.
 */
export interface Device extends Provenance {
  id: string
  name: string
  kind: DeviceKind
  /** How many identical units this record stands for. */
  count: number
  applicationId?: string
  siteId?: string
  /** Physically attached to this node (USB/CSI camera on a Jetson, serial PLC on a gateway). */
  gatewayNodeId?: string
  protocol: string // MQTT, OPC UA, Modbus, RTSP…
  connectivity: Connectivity
  hardwareModel?: string
  firmware?: string
  status: Status
  labels: Record<string, string>
}

/** declared = inferred from Kubernetes objects, observed = seen on the wire, manual = typed by a person. */
export type DependencySource = 'declared' | 'observed' | 'manual'
export type EndpointKind = 'service' | 'device' | 'external'

export interface DependencyStats {
  bytesPerSec?: number
  connectionsPerMin?: number
  /** 0 both when there is genuinely no loss and when nothing eBPF-observed has reported yet (conntrack
   * cannot see retransmits at all) - Dependency.via says which case it is. */
  retransmitsPerMin?: number
  /** The subset of retransmitsPerMin that the RTO timer itself fired for - the real leading indicator of
   * a degrading link, as opposed to a fast retransmit recovering from ordinary reordering without ever
   * stalling the connection. Same "0 means not measured, not measured-as-zero" story as
   * retransmitsPerMin, and the same eBPF-only availability. */
  rtoRetransmitsPerMin?: number
  /** Retransmitted segments as a percentage of segments sent in this window - a real loss rate, not just
   * a raw retransmit count. Undefined (never 0) whenever no segs_out has been reported for this edge yet
   * (a conntrack-only edge, or an eBPF edge too young to have sent a full segment): there is deliberately
   * no fabricated 0% in that case, since "no denominator" and "measured zero loss" are different facts. */
  lossPct?: number
  /** Same "0 means not measured" rule as retransmitsPerMin: only eBPF sees a connection attempt that
   * never reached ESTABLISHED at all, so this is unset on a conntrack-only edge regardless of how many
   * attempts actually failed. */
  failedAttemptsPerMin?: number
  reqPerSec?: number
  errorRate?: number // 0..1
  p95Ms?: number
  windowSec?: number
}

export interface Dependency {
  id: string
  orgId: string
  from: string // id of the caller
  fromKind: EndpointKind
  to: string // id of the callee
  toKind: EndpointKind
  sources: DependencySource[]
  confidence: Confidence
  firstSeen?: string
  lastSeen?: string
  protocol: string // HTTP, gRPC, MQTT, Kafka, TCP ...
  port?: number
  /** The application usually found on `port` (e.g. "PostgreSQL", "Redis") - a guess from the port number
   * alone, set only on an observed dependency (never a declared one). A workload can run anything on any
   * port, so this is descriptive, never part of identity: two dependencies differing only in this field
   * are still the same dependency. Unset means no well-known port matched, not "unknown protocol". */
  service?: string
  label?: string
  /** Rolling-window summary, not a time series. */
  stats?: DependencyStats
  /* Seen on the wire (added by the server; never stored in the workspace). */
  /** No traffic for longer than the server's stale window. */
  stale?: boolean
  /** Traffic that is machinery rather than the applications' own. */
  noise?: 'dns' | 'system'
  /** The two ends are in different onboarded clusters. */
  crossCluster?: boolean
  /** How it was observed: eBPF counts every connection and its bytes; conntrack may not know bytes. */
  via?: 'ebpf' | 'conntrack'
  /** The caller's physical network interface (e.g. "eth0", "wlan0"), read from the kernel's own route for
   * the socket - never guessed from the port or address, and only ever set by eBPF (conntrack has no way to
   * know it). Unset means not known, which matters on a multi-homed node (an edge box with both ethernet
   * and a cellular backhaul) more than a single-homed one. */
  iface?: string
  /** Cumulative TCP segments retransmitted over this edge's whole life, from the kernel's own
   * congestion-control counters - never inferred from timing, and only ever set by eBPF. Always 0 on a
   * conntrack-only edge (via !== 'ebpf'), which has no socket to read this from: there, 0 means "not
   * measured", not "no loss". */
  retransmits?: number
  /** The cumulative subset of retransmits (above) that the RTO timer itself fired for - no ACK at all
   * came back within a full round-trip-plus-backoff, as opposed to a fast retransmit that recovered
   * from ordinary packet reordering without ever stalling the connection. The real leading indicator of
   * a degrading link: retransmits alone cannot tell the two apart. Same conntrack caveat as retransmits
   * (always 0 there, meaning "not measured"). */
  rtoRetransmits?: number
  /** Cumulative connection attempts between these two ends that never reached ESTABLISHED - refused,
   * timed out, reset mid-handshake, or unreachable - summed the same way retransmits is, with the same
   * conntrack caveat (0 there means "not measured"). A dependency can have this set with connections
   * entirely unset: that is the interesting case, something the application keeps trying to reach and
   * never does. */
  failedAttempts?: number
  /** The hostname a TLS ClientHello's SNI extension named for this edge's destination, read before the
   * handshake ever encrypts anything - only ever set by eBPF, and only once a ClientHello has actually
   * been seen (most non-TLS edges, and any conntrack-only edge, simply never have this). A gauge, like
   * iface: one peer address essentially always carries one hostname, so a later report overwrites this
   * rather than accumulating. */
  sniHost?: string
  /** Distinct domain names this edge's destination has been asked to resolve, newest-first, capped at a
   * small number - only ever set by eBPF, from DNS queries sent to this peer. Unlike sniHost, one
   * resolver edge legitimately carries many different domains over its life, so this accumulates instead
   * of being overwritten. */
  dnsQueryNames?: string[]
  /** The most recently sampled smoothed round-trip time, in milliseconds, from the kernel's own TCP RTT
   * estimator. A gauge (the latest sample), not an average over the edge's life. Unset means no sample
   * yet - most often too little exchanged to measure one, or a conntrack-only edge. */
  rttMs?: number
  /** The kernel's own RTT mean-deviation (tcp_sock.mdev_us), in milliseconds - the companion jitter figure
   * to rttMs, sampled from the same place. Same gauge semantics as rttMs: the latest sample, unset means
   * no sample yet. */
  jitterMs?: number
  /** Time from SYN to ESTABLISHED for this edge's most recent TCP handshake, in milliseconds - a
   * per-connection fact (not per-byte), so most meaningful on a cross-cluster/WAN edge where handshake
   * cost is a real, visible part of the first request's latency. A gauge like rttMs/jitterMs; unset means
   * no handshake has completed since the collector started watching this edge (or it's UDP). */
  handshakeMs?: number
  /** The kernel's own current congestion window, in segments (tcp_sock.snd_cwnd) - a gauge, read
   * alongside pacingBps to say what the kernel itself currently believes this connection's send rate is
   * bounded by. Unset means no sample yet. */
  cwndSegments?: number
  /** The pacing rate TCP's own congestion control last set, bytes/sec (sock.sk_pacing_rate) - a gauge,
   * same treatment as cwndSegments. Unset means no pacer is active yet (a very young connection), not
   * "idle". */
  pacingBps?: number
  /** The kernel's own current effective segment size, in bytes (tcp_sock.mss_cache) - a gauge, same
   * treatment as cwndSegments/pacingBps above. Most informative when tunnelLink (below) is set: an
   * overlay tunnel's own encapsulation headers (VXLAN/WireGuard/GRE) eat into the path MTU, so a falling
   * mssBytes on a tunneled edge is a real, measured sign of that overhead. Always 0/unset on a
   * conntrack-only edge. */
  mssBytes?: number
  /** The receive window this node is currently advertising to the peer on this edge, in bytes
   * (tcp_sock.rcv_wnd) - a gauge, same treatment as mssBytes/cwndSegments above. 0 is also a real,
   * meaningful reading here, not just "unset": it means this side has told the peer to stop sending
   * because its own receive buffer is not draining fast enough - the node-side half of a stalled
   * connection, distinct from retransmits (above), which is the path losing packets regardless of
   * either end's buffers. Read alongside sndWndBytes/wmemQueuedBytes/sndbufBytes below to tell a
   * buffer-pressure stall apart from one caused by the path itself. Always 0/unset on a conntrack-only
   * edge. */
  rcvWndBytes?: number
  /** The peer's last-advertised receive window to this node on this edge, in bytes (tcp_sock.snd_wnd) -
   * same gauge treatment as rcvWndBytes right above. 0 means the peer stalled this connection, which
   * looks identical to a congested path from this node's own counters (cwndSegments/pacingBps) unless
   * this field is read too. */
  sndWndBytes?: number
  /** Bytes currently queued in this edge's own local send/write queue (sock.sk_wmem_queued) - a gauge,
   * same treatment as rcvWndBytes/sndWndBytes above. Read alongside sndbufBytes below: wmemQueuedBytes
   * at or near sndbufBytes means this edge's local send buffer is saturated - the application not
   * writing fast enough to notice, or itself backpressured by a congested path it cannot drain into. */
  wmemQueuedBytes?: number
  /** The current ceiling on wmemQueuedBytes above (sock.sk_sndbuf, SO_SNDBUF) - a gauge, same treatment
   * as the three fields above. */
  sndbufBytes?: number
  /** Cumulative receive-side buffer drops for this dependency, from the kernel's own per-socket counter
   * - summed the same way retransmits/failedAttempts are. A different failure mode from retransmits: the
   * receiving application not draining its socket fast enough, not the network losing a packet in
   * transit. Always 0 on a conntrack-only edge, where it means "not measured". */
  bufferDrops?: number
  /** How long a DNS response took to arrive after its matching query, in milliseconds - a gauge, only
   * ever set on the pod<->resolver edge itself (noise === 'dns'), matched by transaction id rather than
   * read from a socket (DNS is UDP, so there's no socket state machine the way rttMs has). Requires the
   * same optional name-capture opt-in as dnsQueryNames/sniHost. Unset means no sample yet - most often
   * because the response hasn't arrived, was lost, or this edge is conntrack-only. */
  dnsRttMs?: number
  /** How the far end was identified, when it was not certain. */
  note?: string
  connections?: number
  bytes?: number
  /** Set when `iface` above matched one side of a confirmed, named overlay ClusterLink between this
   * dependency's own two endpoints' clusters - see the backend's correlateClusterLinks for the exact
   * matching rule (cluster AND interface name together, never interface name alone). Lets the UI show
   * "this traffic crosses a confirmed tunnel" directly on this dependency's own edge, in addition to -
   * not instead of - the aggregate on the ClusterLink itself (flowsObserved and its own doc), which
   * still matters for a tunnel used by several distinct dependencies at once, or by none right now.
   * Deliberately missing flowsObserved/avgRttMs/avgLossPct: those are an aggregate across every
   * dependency crossing the link, not a fact about this one. */
  tunnelLink?: {
    fromCluster: string
    toCluster: string
    via: string
    redundancy: number
    encryption?: ClusterLink['encryption']
  }
  /** True when this edge's traffic has actually been seen (eBPF only) leaving its caller directly -
   * without first being redirected to a local mesh sidecar proxy - even though that caller is configured
   * to run one and does not exclude this exact port from it. Unlike mesh.ts's connectionVerdict (which
   * only ever infers a mesh's effect from configuration), this is read straight off the wire by the node
   * collector and then cross-checked server-side against that same declared configuration, so it only
   * ever flags a genuine surprise - injection that silently failed, an iptables rule that never applied,
   * hostNetwork traffic that skipped the pod's own netns - never a workload that was simply never meshed
   * or that explicitly excludes this port, where direct traffic is expected, not a finding. */
  meshBypass?: boolean
  /** A coarse, best-effort read of how a TLS handshake this edge's own ClientHello started visibly went,
   * read straight off the wire by the same optional name-capture hook that produces sniHost/meshBypass
   * above - never a certificate identity or validity check, which the collector has no way to perform at
   * all. 'ok' means several sustained application-data records were seen with no alert first; 'failed'
   * means a plaintext TLS alert record was seen before anything encrypted - the shape a fatal
   * handshake-time failure takes on the wire, never a normal close (every TLS version encrypts its own
   * close_notify once a handshake already finished). Unset means unknown: no ClientHello was ever seen on
   * this edge, name-capture is off, or this edge's later packets never visibly decided it either way. A
   * gauge: the latest connection's own outcome, not a history. Read alongside meshBypass above when
   * reasoning about a mesh workload - pairing the two is a stronger signal than either alone - but this
   * says nothing about whether a connection was supposed to go through a mesh in the first place. */
  tlsHandshake?: 'ok' | 'failed'
}

/** The graph. */
export interface Topology {
  clusters: Cluster[]
  nodes: MachineNode[]
  namespaces: Namespace[]
  services: Service[]
  devices: Device[]
  dependencies: Dependency[]
  applications: Application[]
  sites: Site[]
  siteLinks: SiteLink[]
  externalEndpoints: ExternalEndpoint[]
  /** Regional operators active in the org - only present when the caller has fetched them (an admin-only
   *  view; see RegionalOperatorsPage / TopologyPage). Optional so every other Topology producer (seed data,
   *  history snapshots, tests) is unaffected. */
  operators?: RegionalOperator[]
  /** Discovery agents, one per cluster that has one - unlike operators above, this IS part of the
   *  continuously-polled model (see ServerState.topology.agents), so it is never a one-off fetch. Named
   *  `discoveryAgents`, not `agents`: Control.agents (the enrollment/admin Agent[] this same Model also
   *  extends) already owns that name for a completely different shape. Still optional for the same
   *  "every other Topology producer is unaffected" reason operators is. */
  discoveryAgents?: DiscoveryAgent[]
}

/* ---------- control plane: how the graph was learned ---------- */

/** 0 registered · 1 infrastructure · 2 services · 3 dependencies · 4 control */
export type AccessTier = 0 | 1 | 2 | 3 | 4
export const ACCESS_TIERS: { value: AccessTier; label: string }[] = [
  { value: 0, label: 'Registered' },
  { value: 1, label: 'Infrastructure' },
  { value: 2, label: 'Services' },
  { value: 3, label: 'Dependencies' },
  { value: 4, label: 'Control' },
]

/** One line per tier: what it adds on top of the tier below it. Each one from tier 1 up names the tier directly
 *  below it by label ("Everything in X, plus...") so the inclusion reads as a fact of the sentence, not something
 *  a picker's visual has to imply on its own. The single source of truth for every tier picker and indicator in
 *  the app, so the wording never drifts between them. */
export const ACCESS_TIER_CAPTIONS: Record<AccessTier, string> = {
  0: 'Proves which cluster this is. Nothing else is read.',
  1: 'Nodes, storage classes and ingress classes — what the cluster is made of.',
  2: 'Everything in Infrastructure, plus namespaces, workloads, pods, services and ingresses — what runs on it.',
  3: 'Not available yet.',
  4: 'Not available yet.',
}

/**
 * What each implemented tier's Kubernetes RBAC *actually* grants, verb by verb, resource by resource —
 * the honest answer to "what does this need, and what do I have to do to grant it", surfaced by
 * TierLevels' per-rung "What this grants" disclosure (ApprovalCard, ConnectClusterWizard, AgentInsight).
 *
 * This is NOT the same text as ACCESS_TIER_CAPTIONS above, on purpose: that constant says what the agent
 * *collects* at a tier (its audience is "what will I see in the UI"); this one says what Kubernetes
 * *rule* was granted to let it do that (its audience is "what did I just hand a ServiceAccount"), and the
 * two can differ — tier 0's caption says "proves which cluster this is", but the actual rule it needs for
 * that is `get` on one resourceName ("kube-system"), not "read namespaces" in general; tier 2 needs `get`
 * on namespaces themselves (to enumerate scope), which nothing in its caption mentions at all.
 *
 * MUST be kept in sync with backend/internal/chart/continuum-agent/templates/rbac.yaml by hand — this is
 * frontend prose describing a Helm template, nothing wires them together at build time. What keeps them
 * from drifting apart silently is backend/internal/chart/agent_rbac_grants_test.go, which renders
 * rbac.yaml for every tier with `helm template` and fails if the real ClusterRole/Role rules stop
 * containing the apiGroup/resource/verb triples this constant claims. Change one, the other's test tells
 * you. (The same discipline ACCESS_TIER_CAPTIONS already asks for, and the reason consent.go's own
 * ceiling/floor split between server and chart exists: two places that can each only tell half the truth
 * must still never disagree about which half.)
 */
export interface AccessTierGrant {
  /** Every Kubernetes RBAC rule in force once this tier is installed, cumulative (tier 2's list already
   *  includes tier 1's and tier 0's) — one line per apiGroup+resource(s)+verbs rule, in the chart's own
   *  order. Tiers 3 and 4 are reserved placeholders with no backing RBAC at all: say so plainly, matching
   *  ACCESS_TIER_CAPTIONS, rather than inventing a permission that doesn't exist yet. */
  grants: string[]
  /** The install-time flag that selects this tier; shown next to the grants so "what it needs" and "how
   *  you give it" are never answered in two disconnected places — the full command carrying it is
   *  already printed wherever a tier is actually picked (the install command, or the `helm upgrade`
   *  this app prints to widen an existing install). */
  setBy?: string
}

/** Every install, at any tier, also carries these two — neither varies with `access.tier`, so they are
 *  not repeated per rung below. Sourced from the same rbac.yaml: the always-present identity Role, and
 *  the optional (default on) endpoint-resolution Role. */
export const ACCESS_TIER_BASELINE_GRANT =
  'Every tier also includes two fixed, tier-independent rules: get/update/patch on its own identity certificate Secret (one, created empty by the chart — it can read or write no other Secret, and create none), and, unless access.resolveApiEndpoint=false, get on the "kubernetes" Endpoints object in the default namespace (resolves the control plane\'s real address instead of the internal ClusterIP every in-cluster client is handed).'

export const ACCESS_TIER_GRANTS: Record<AccessTier, AccessTierGrant> = {
  0: {
    grants: ['get the namespace named "kube-system" (resourceName-scoped — this one namespace only, not list/watch of all of them)'],
  },
  1: {
    grants: [
      'get the namespace named "kube-system" (tier 0, unchanged)',
      'get/list/watch nodes',
      'get/list/watch storage classes (storage.k8s.io)',
      'get/list/watch ingress classes (networking.k8s.io)',
    ],
    setBy: '--set access.tier=1',
  },
  2: {
    grants: [
      'get the namespace named "kube-system" (tier 0, unchanged)',
      'get/list/watch nodes, storage classes, ingress classes (tier 1, unchanged)',
      'get/list/watch namespaces (all of them, to enumerate scope — cluster mode only; in rbac.mode=namespaced this and persistentvolumes drop out, since neither has a namespaced form), pods, services, persistentvolumeclaims, persistentvolumes',
      'get/list/watch deployments, statefulsets, daemonsets, replicasets (apps)',
      'get/list/watch ingresses (networking.k8s.io)',
      'get/list/watch horizontalpodautoscalers (autoscaling)',
      'get/list/watch poddisruptionbudgets (policy)',
      'get/list (not watch) PeerAuthentication, Istio mutual-TLS policy only (security.istio.io) — on by default, turned off with mesh.readPolicy=false',
    ],
    setBy: '--set access.tier=2',
  },
  3: { grants: ['Not available yet.'] },
  4: { grants: ['Not available yet.'] },
}

export interface AgentModule {
  name: string // infrastructure, services, dependencies, geo…
  status: 'ok' | 'skipped' | 'error'
  /** Why it did not run, e.g. "needs access tier 3". Feeds the completeness badge. */
  reason?: string
}

/** Where a GeoIP database places the address an agent connects from. A suggestion, never a fact. */
export interface GeoHint {
  /** ISO 3166-1 alpha-2. */
  country: string
  countryName?: string
  city?: string
  region?: string
  lat?: number
  lng?: number
  accuracyKm?: number
  /** "city" when a city and coordinates are known, else "country". */
  level: 'city' | 'country'
  database?: string
  /**
   * The cluster's own connecting address was private (loopback, NAT, CGNAT…) and could not be placed at
   * all; this is this server's own public address instead, looked up because the two share a network -
   * that is exactly why the cluster's own address looked private to us. Set only when an operator turned
   * this fallback on (off by default); never as precise as the cluster's own address would have been.
   */
  estimated?: boolean
  /**
   * Which network the address belongs to, from a second, optional ASN database - independent of
   * city/country, and usually steadier: a VPN or a cloud provider's egress can move the place shown
   * without changing whose network is actually carrying the traffic. Absent unless an operator
   * configured that second database.
   */
  asn?: number
  asOrg?: string
}

/** Why a connecting address could not be located at all - see GeoHint and Agent.connectingGeoReason.
 * "cgnat" and "private" both mean the address is, by construction, behind some NAT. */
export type GeoUnlocatableReason = 'cgnat' | 'private' | 'loopback' | 'link-local' | 'link-local-multicast' | 'multicast' | 'unspecified'

/** What the traffic observer says about itself. Absent until a collector has reported. */
export interface ObserverStatus {
  lastReport: string
  collectors: { node: string; method: 'ebpf' | 'conntrack'; bytesKnown: boolean }[]
  /** Observations the collectors had to discard since the server started. */
  lost: number
}

export interface AgentScope {
  /** In words: "namespaces shop, payments" or "label team=a". */
  description: string
  /** Namespaces that exist / that the agent reports on. */
  namespaces: number
  inScope: number
}

export interface Agent {
  id: string
  orgId: string
  name: string
  /** Set once the enrollment is approved and a cluster record exists. */
  clusterId?: string
  version: string
  accessTier: AccessTier
  /** `expired`: nobody approved the request in time; the agent asks again by itself, with a new code. */
  status: 'pending' | 'approved' | 'revoked' | 'rejected' | 'expired'
  /** kube-system UID the agent reported; a human confirms it on approval. */
  fingerprint: string
  connectingIp?: string
  connectingGeo?: GeoHint
  connectingGeoReason?: GeoUnlocatableReason
  certExpiresAt?: string
  lastHeartbeat?: string
  modules: AgentModule[]
  /* reported by a real server; absent on hand-made and sample agents */
  kubernetesVersion?: string
  /** Highest tier the RBAC installed in the cluster allows, and the ceiling the enrollment token set. */
  installedTier?: AccessTier
  tierCap?: AccessTier
  requestedAt?: string
  reason?: string
  connected?: boolean
  observer?: ObserverStatus
  /** Result of the last check of the server's picture against the cluster's own full picture. */
  consistency?: ConsistencyStatus
  /** How many addresses this agent has been asked to time (0: not measuring). */
  measuring?: number
  /** Which namespaces this agent was told to look at. Absent: all of them (or an older agent). */
  scope?: AgentScope
  /** What it has sent since the server started. Absent on sample agents and before its first connection. */
  link?: AgentLink
  /** The agent's clock minus the server's, in milliseconds, as the agent measured it. Absent: not measured or in step. */
  clockSkewMs?: number
  /** Pending only: it enrolled without an approval code (an older agent), so approval falls back to the fingerprint. */
  legacyEnrollment?: boolean
  /** Pending only: how many wrong codes are still allowed before the request is rejected. */
  approvalAttemptsLeft?: number
  /** Pending only: when the request expires if nobody approves it. */
  pendingExpiresAt?: string
  /** This agent's own Kubernetes namespace, from its last Hello. Absent before an agent that reports it has
   *  connected since the server started (an older agent, or one that has never connected). */
  namespace?: string
  /** The Helm release this agent's Hello says it was installed as. Absent under the same condition as namespace,
   *  in which case the commands below fall back to the chart's documented default release name. */
  releaseName?: string
  /** Set when the approved tier is below what the install allows: narrowing here changed what this agent
   *  reports, but the cluster's own RBAC still grants the wider tier until this command is run there too. */
  hardenHelm?: string
  /** Only for a revoked or rejected agent: what to run in the cluster to actually remove it. Visible to
   *  editors and above, like `hardenHelm`. */
  teardown?: {
    helm: string
    secret: string
    /** True when `namespace` was never reported, so these commands name the chart's documented default,
     *  which may not be where this agent actually lives. */
    namespaceGuessed: boolean
  }
}

/** A gauge of one agent's stream to the server. */
export interface AgentLink {
  /** When the current connection began; absent while disconnected. */
  connectedSince?: string
  connects: number
  /** Size of everything it sent, as encoded (before gRPC framing and TLS). */
  bytes: number
  syncs: number
  flows: number
  measurements: number
  heartbeats: number
  lastSync?: string
  lastFlows?: string
}

/** A regional operator's lifecycle: unlike Agent, there is no "pending" state - creating one issues its
 *  receiver credential immediately, since it never enrolls back to the server (see the operators feature's
 *  own design note: it is CRUD over a record plus a minted credential, not a live connection). */
export type OperatorStatus = 'active' | 'revoked'

/** How a regional operator's receiver authenticates what exports into it: 'mtls' is the client certificate
 *  alone (operators created from now on - no receiver bearer token exists), 'bearer' is a bearer token (every
 *  operator created before that, which keeps it). Absent reads as 'bearer', the conservative reading. */
export type ReceiverAuth = 'mtls' | 'bearer'

/** Whether an operator has said it is alive. 'unknown' covers both "never opted in to the heartbeat" and
 *  "opted in, nothing has arrived yet" - the server does not tell those apart, and neither does the UI. */
export type OperatorHealthState = 'unknown' | 'online' | 'offline'

/** An operator's liveness, computed by the server at read time from its opt-in heartbeat (online = one within
 *  the last 180 s). `reporting` is true only once at least one heartbeat has arrived. */
export interface OperatorHealth {
  state: OperatorHealthState
  lastSeenAt?: string
  reporting: boolean
}

/** Where a regional operator (or, via TelemetryIntent below, one agent's own bundled local operator)
 *  re-exports what it aggregates. 'operator' (chaining to another regional operator already active in
 *  this org) is now accepted by the server, alongside the original 'external' - see
 *  OperatorDestination.targetOperatorId. */
export type DestinationKind = 'external' | 'operator'

/** An OTLP export target, shaped like TelemetryInput's own export block (see lib/install.ts) so the same
 *  rendering logic applies to both a cluster's own telemetry export and a regional operator's. Also the
 *  destination shape for TelemetryIntent below - this is the server's `destinationDoc`, shared by both. */
export interface OperatorDestination {
  kind: DestinationKind
  endpoint: string
  insecure?: boolean
  caFile?: string
  authHeaderName?: string
  authSecretName?: string
  authSecretKey?: string
  /** Only meaningful for kind 'operator': the id of the active regional operator in this org this
   *  destination chains to. The server rejects a kind 'operator' destination that doesn't name one. */
  targetOperatorId?: string
}

/**
 * A regional operator: a standalone OTel Collector that aggregates telemetry already exported by a set of
 * approved agents' clusters (sourceClusterIds) and re-exports it to destination. It never connects back to
 * this server the way Agent does, unless its opt-in heartbeat (health) is on - not extending Provenance for the same reason Agent does not: this is
 * control-plane bookkeeping a person created directly, not a discovered record.
 */
export interface RegionalOperator {
  id: string
  orgId: string
  name: string
  /** Optional: where this operator conceptually lives, for UI grouping only. */
  siteId?: string
  status: OperatorStatus
  sourceClusterIds: string[]
  destination: OperatorDestination
  /** Which modalities ('metrics'/'logs'/'traces' - lib/install.ts's own Modality) this operator's
   *  receiver accepts. Empty/absent means "accepts anything" - an operator created before this field
   *  existed must read the same way, never as "accepts nothing" (see destinationCatalog.ts's own
   *  compatibility check, the one place besides creation/scope-editing this is read). */
  acceptedModalities?: string[]
  createdAt: string
  createdBy: string
  revokedAt?: string
  reason?: string
  /** How its receiver authenticates agents - see ReceiverAuth. Optional so an older server (or a fixture)
   *  that does not send it reads as 'bearer'. */
  receiverAuth?: ReceiverAuth
  /** Its opt-in heartbeat's verdict. Absent reads as never reported. */
  health?: OperatorHealth
}

/** One extractor signal a TelemetryIntent grants, and where it is sourced from - `id` matches one of
 *  TELEMETRY_SIGNALS' own ids (lib/consent.ts); `source` is 'builtin' for a signal with no source choice
 *  of its own, 'existing' for one scraping something already running, or 'bundle-<tool>' (e.g.
 *  'bundle-kepler', 'bundle-dcgm') for one that deploys its own bundled exporter - the same per-signal
 *  source knobs TelemetryInput already carries (energySource/acceleratorsSource), just named uniformly
 *  here instead of one bespoke field per signal. */
export interface SignalGrant {
  id: string
  source: string
}

export type TelemetryIntentStatus = 'active' | 'revoked'

/**
 * The server-side counterpart of a telemetry grant for one agent's bundled local-operator telemetry:
 * scope (namespaces/exclude), which extractor signals, and a destination - an external OTLP endpoint or
 * another regional operator already active in this org (OperatorDestination, with `kind: 'operator'`
 * naming it via `targetOperatorId`). Unlike RegionalOperator, this is per-agent bookkeeping, not a
 * standalone aggregation point - see backend/internal/server/admin_telemetry_intents.go's own doc
 * comments for the full lifecycle (no "pending" state, like RegionalOperator; revoke/delete, like it).
 */
export interface TelemetryIntent {
  id: string
  agentId: string
  name: string
  status: TelemetryIntentStatus
  namespaces: string[]
  exclude: string[]
  signals: SignalGrant[]
  destination: OperatorDestination
  createdAt: string
  createdBy: string
  revokedAt?: string
  reason?: string
}

/**
 * The discovery agent process running inside a cluster - the per-cluster telemetry pipeline itself
 * (what collects and reports facts), never anything it discovered. One per cluster (ClusterId is its
 * whole identity: there is no separate source-cluster selection the way RegionalOperator needs, since
 * this is always a 1:1 relationship). Extends Provenance for the same live/stale treatment Cluster/
 * MachineNode already get from it, even though an agent has nothing a person can override - there is
 * no `overrides` use here, only `stale`/`state`/`stateReason`.
 */
export interface DiscoveryAgent extends Provenance {
  id: string
  clusterId: string
  name: string
  /** This agent's most recent self-telemetry sample (RSS/goroutines as of its last heartbeat that
   *  carried one) - a light pointer to its own resource footprint, not a history. Undefined until the
   *  agent's first such heartbeat arrives. The full history (with derived CPU% and bandwidth share)
   *  lives on the System Health page (lib/selfHealth.ts's SelfTelemetrySample), never duplicated here. */
  self?: AgentSelfSnapshot
}

/** model.Agent.Self's own mirror - see DiscoveryAgent.self's own doc for why this is a snapshot, not a series. */
export interface AgentSelfSnapshot {
  t: string
  rssBytes: number
  goroutines: number
}

/** Whether the server's picture of a cluster matched what the cluster itself reported at the last check. */
export interface ConsistencyStatus {
  lastCheck: string
  checks: number
  /** Things the server had wrong at the last check (0 = it matched). It has been corrected either way. */
  differences: number
  summary?: string
}

/**
 * A different label or annotation found on these services that could have named the application
 * instead of the one discovery picked - what the winning signal outranked, not applied on its own.
 */
export interface GroupingAlternative {
  name: string
  origin: string
  confidence: Confidence
  signal: string
}

/** What approving a suggestion does. Suggestions without one are informational. */
export type SuggestionAction =
  | { type: 'add-device'; device: Device }
  | { type: 'add-external'; endpoint: ExternalEndpoint; dependency: Dependency }
  | { type: 'set-cluster-site'; clusterId: string; siteId: string }
  /** Create the site (or keep it, if a person already made it) and put the cluster there. */
  | { type: 'place-cluster'; clusterId: string; site: Site }
  | { type: 'set-service-application'; serviceIds: string[]; applicationId: string }
  | { type: 'create-application'; application: Application; serviceIds: string[]; alternatives?: GroupingAlternative[] }
  /** Unmanaged infrastructure the traffic hints at: opens the Connect wizard. */
  | { type: 'connect-cluster'; addresses: string[] }

export interface Suggestion {
  id: string
  orgId: string
  kind: 'device' | 'external-endpoint' | 'site' | 'application' | 'dependency' | 'infrastructure'
  title: string
  detail: string
  agentId?: string
  createdAt: string
  status: 'open' | 'accepted' | 'dismissed'
  decidedBy?: string
  decidedAt?: string
  apply?: SuggestionAction
}

export interface AuditEvent {
  id: string
  orgId: string
  at: string
  actor: string
  action: string
  targetKind: string
  targetId: string
  detail?: string
}

export interface Control {
  agents: Agent[]
  suggestions: Suggestion[]
  auditLog: AuditEvent[]
}

/**
 * A named set of Topology view options (which view, how it is grouped, what is shown), so a way of looking at
 * the system can be reopened in one click and shared with the workspace. It holds view options only:
 * no selection and no data, so it can never go stale.
 */
export interface SavedView {
  id: string
  orgId: string
  name: string
  /** URL search string of the view options, e.g. "view=infrastructure&group=tier". */
  params: string
  createdAt: string
}

/** Everything the app stores and exports. */
export interface Model extends Topology, Control {
  savedViews: SavedView[]
  /**
   * What people said about discovered records that are not (or not yet) known to this browser. Records that are
   * known carry their own overrides; a ref moves onto the record when discovery reports it.
   */
  refs: Record<string, DeclaredRef>
}

/** What Export writes and Import accepts (older files without a version are upgraded). */
export type TopologyFile = Model & { schemaVersion: number }

export const SITE_KINDS: { value: SiteKind; label: string }[] = [
  { value: 'cloud-region', label: 'Cloud region' },
  { value: 'data-center', label: 'Data center' },
  { value: 'edge-site', label: 'Edge site' },
]

export const DEVICE_KINDS: { value: DeviceKind; label: string }[] = [
  { value: 'sensor', label: 'Sensor' },
  { value: 'actuator', label: 'Actuator' },
  { value: 'camera', label: 'Camera' },
  { value: 'plc', label: 'PLC / controller' },
  { value: 'gateway', label: 'Gateway' },
  { value: 'tag', label: 'Tag / tracker' },
  { value: 'vehicle', label: 'Vehicle / robot' },
  { value: 'other', label: 'Other' },
]

export const CONNECTIVITY: { value: Connectivity; label: string }[] = [
  { value: 'ethernet', label: 'Ethernet' },
  { value: 'wifi', label: 'Wi-Fi' },
  { value: 'cellular', label: 'Cellular' },
  { value: 'satellite', label: 'Satellite' },
  { value: 'lora', label: 'LoRa' },
  { value: 'zigbee', label: 'Zigbee' },
  { value: 'ble', label: 'Bluetooth LE' },
  { value: 'serial', label: 'Serial / fieldbus' },
  { value: 'unknown', label: 'Unknown' },
]

export type ViewKind = 'application' | 'infrastructure'
export type GroupBy = 'cluster' | 'tier'

export const TIERS: { value: Tier; label: string }[] = [
  { value: 'cloud', label: 'Cloud' },
  { value: 'edge', label: 'Edge' },
  { value: 'far-edge', label: 'Far edge' },
]

export const TIER_ORDER: Record<Tier, number> = { cloud: 0, edge: 1, 'far-edge': 2 }

export const TIER_COLOR: Record<Tier, string> = {
  cloud: 'var(--color-tier-cloud)',
  edge: 'var(--color-tier-edge)',
  'far-edge': 'var(--color-tier-far)',
}

export const STATUS_COLOR: Record<Status, string> = {
  healthy: '#34d399',
  degraded: '#fbbf24',
  offline: '#f87171',
  unknown: '#7c8994',
}
