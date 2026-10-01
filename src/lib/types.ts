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
 *  agent (see Address's own doc comment on the same rule). */
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
  /** Cumulative receive-side buffer drops for this dependency, from the kernel's own per-socket counter
   * - summed the same way retransmits/failedAttempts are. A different failure mode from retransmits: the
   * receiving application not draining its socket fast enough, not the network losing a packet in
   * transit. Always 0 on a conntrack-only edge, where it means "not measured". */
  bufferDrops?: number
  /** How the far end was identified, when it was not certain. */
  note?: string
  connections?: number
  bytes?: number
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

/** Where a regional operator re-exports what it aggregates. 'operator' (chaining to another regional
 *  operator) is reserved for future use and rejected by the server today - only 'external' is accepted. */
export type DestinationKind = 'external' | 'operator'

/** An OTLP export target, shaped like TelemetryInput's own export block (see lib/install.ts) so the same
 *  rendering logic applies to both a cluster's own telemetry export and a regional operator's. */
export interface OperatorDestination {
  kind: DestinationKind
  endpoint: string
  insecure?: boolean
  caFile?: string
  authHeaderName?: string
  authSecretName?: string
  authSecretKey?: string
  /** Only meaningful for kind 'operator', which is not yet accepted - always absent today. */
  targetOperatorId?: string
}

/**
 * A regional operator: a standalone OTel Collector that aggregates telemetry already exported by a set of
 * approved agents' clusters (sourceClusterIds) and re-exports it to destination. It never connects back to
 * this server the way Agent does - not extending Provenance for the same reason Agent does not: this is
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
  createdAt: string
  createdBy: string
  revokedAt?: string
  reason?: string
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
