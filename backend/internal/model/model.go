// Package model mirrors the schema-v3 records the UI consumes (src/lib/types.ts).
// Only what discovery can produce is defined here.
package model

type Evidence struct {
	Signal     string `json:"signal"`
	Confidence string `json:"confidence"` // high | medium | low
	Detail     string `json:"detail,omitempty"`
}

type Provenance struct {
	OrgID      string              `json:"orgId"`
	Source     string              `json:"source"` // always "discovered" from the server
	Key        string              `json:"key,omitempty"`
	LastSeen   string              `json:"lastSeen,omitempty"`
	DetectedAt string              `json:"detectedAt,omitempty"`
	AgentID    string              `json:"agentId,omitempty"`
	Revision   uint64              `json:"revision,omitempty"`
	Stale      bool                `json:"stale,omitempty"`
	DeletedAt  string              `json:"deletedAt,omitempty"`
	Evidence   map[string]Evidence `json:"evidence,omitempty"`
	// State is how far the record can be trusted right now: live | disconnected | stale | revoked. It is computed
	// from the clock and the agent's connection (see internal/twin), never stored, and empty for records that no
	// agent observes. StateReason is a sentence for a person ("stale for 2 h"); it contains ages.
	State       string `json:"state,omitempty"`
	StateReason string `json:"stateReason,omitempty"`
}

// ClearObservation removes what only the clock and the connection decide, so that recording a record for history
// does not record a new version every time an age ticks over.
func (p *Provenance) ClearObservation() { p.State, p.StateReason = "", "" }

type Cluster struct {
	Provenance
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Tier           string            `json:"tier"` // cloud | edge | far-edge
	Distribution   string            `json:"distribution"`
	Version        string            `json:"version"`
	Provider       string            `json:"provider"`
	Region         string            `json:"region"`
	Status         string            `json:"status"`
	Labels         map[string]string `json:"labels"`
	APIEndpoint    string            `json:"apiEndpoint,omitempty"`
	CNI            string            `json:"cni,omitempty"`
	Ingress        string            `json:"ingress,omitempty"`
	PodCIDR        string            `json:"podCidr,omitempty"`
	ServiceCIDR    string            `json:"serviceCidr,omitempty"`
	StorageClasses []string          `json:"storageClasses,omitempty"`
	CreatedAt      string            `json:"createdAt,omitempty"` // when the cluster was created (kube-system's creation time)
	// Mesh is the service mesh found in the cluster, if any. What it says is what the mesh is configured to do.
	Mesh *ClusterMesh `json:"mesh,omitempty"`
	// PendingPodCount is how many pods across the cluster are waiting to be scheduled (Pending phase, no
	// node assigned yet) - fewer than the whole cluster's when RBAC only scopes a few namespaces, same
	// limitation ServiceCIDR above already has. Nil means pods were never read (below tier 2), not zero.
	PendingPodCount *int32 `json:"pendingPodCount,omitempty"`
}

// ClusterMesh describes a service mesh from labels, annotations, container names and the mesh's policy objects.
type ClusterMesh struct {
	Kind    string `json:"kind"` // istio | linkerd | consul | kuma
	Mode    string `json:"mode"` // sidecar | ambient
	Version string `json:"version,omitempty"`
	// Mtls is the mesh-wide mutual-TLS mode between meshed workloads: strict | permissive | disabled | automatic | unknown.
	Mtls          string            `json:"mtls"`
	NamespaceMtls map[string]string `json:"namespaceMtls,omitempty"`
	// ControlPlane holds the ids of the services that are the mesh's control plane or gateways.
	ControlPlane []string `json:"controlPlane"`
	PolicyRead   bool     `json:"policyRead"`
	PolicyNote   string   `json:"policyNote,omitempty"`
}

// ServiceMesh is one service's relation to the mesh.
type ServiceMesh struct {
	Mesh          string   `json:"mesh"`
	Proxy         string   `json:"proxy,omitempty"` // sidecar | ambient; empty when no proxy handles its traffic
	Bypass        bool     `json:"bypass,omitempty"`
	ExcludedPorts []string `json:"excludedPorts,omitempty"` // "in:8080", "out:5432": ports kept out of the proxy
	ControlPlane  bool     `json:"controlPlane,omitempty"`
	// Source is where the answer came from: pods (a proxy was seen) | workload | namespace (what it asks for).
	Source string `json:"source,omitempty"`
}

type Resources struct {
	CPU      float64 `json:"cpu"`
	MemoryGb float64 `json:"memoryGb"`
	// DiskGb is root filesystem capacity ("ephemeral-storage" in Kubernetes' own Capacity/Allocatable) -
	// what pod ephemeral storage, images and logs actually share, not any one physical disk (Node.Disks
	// below has those, from the node probe). Always populated once the node itself is known (every real
	// kubelet reports this) - not a pointer, unlike Cluster.PendingPodCount above, because there is no real
	// "never collected" case here to distinguish from a genuine zero.
	DiskGb float64 `json:"diskGb,omitempty"`
}

type Accelerator struct {
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	Count  int64  `json:"count"`
}

// NetworkInterface is one physical uplink a node probe saw on the machine itself: never an address,
// only what the kind of link, its negotiated speed and its MTU are (any of which may be unknown).
type NetworkInterface struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // ethernet | wifi | cellular
	SpeedMbps int32  `json:"speedMbps,omitempty"`
	MTU       int32  `json:"mtu,omitempty"`
}

// Disk is one physical block device a node probe saw on the machine itself: capacity and type only,
// never a serial number, WWN or any other per-disk identifier.
type Disk struct {
	Name      string `json:"name"` // kernel device name, e.g. "sda", "nvme0n1" - not stable across reboots
	Model     string `json:"model,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Type      string `json:"type,omitempty"` // hdd | ssd | nvme
}

// TunnelInterface is one overlay/tunnel network interface a node probe found up on the machine -
// identified generically from the kernel's own link kind (see continuumv1.TunnelInterface's doc for the
// full list of covered drivers and the documented gaps), never guessed from an interface's name. Confirmed
// carries this node's own server-side correlation verdict for whether another onboarded node's address
// falls inside one of Routes' prefixes - see Confirmed's own comment for what that does and does not
// establish.
type TunnelInterface struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // wireguard | vxlan | geneve | gre | gretap | ip6gre | ip6gretap | ipip | sit | vti | vti6 | xfrm
	Addresses []string `json:"addresses,omitempty"`
	Routes    []string `json:"routes,omitempty"`
	// Mtu is this tunnel interface's own MTU, read from the same netlink link dump as Kind - 0 when
	// unreported. Worth surfacing on its own: a tunnel with a lower MTU than the physical path underneath
	// it is a classic, easy-to-miss overlay gotcha (packets above it silently fragment, or get dropped
	// outright when a middlebox blocks fragmentation), and this is the one place that fact is visible at
	// all without logging into the machine.
	Mtu int32 `json:"mtu,omitempty"`
	// Up is this tunnel's administrative state (netlink IFF_UP, the same flag `ip link set up/down`
	// toggles) - not a guarantee the tunnel is currently passing traffic, only that it has not been
	// disabled. Deliberately not based on the kernel's operational-state field: several common tunnel
	// drivers (WireGuard among them) never report anything but "unknown" there even while fully up and
	// carrying traffic, which would make that signal actively misleading rather than merely unavailable.
	Up bool `json:"up"`
	// Confirmed names the other node this tunnel was matched to, when one of Routes' prefixes contains an
	// address another onboarded node (in this cluster or a different one) is independently known by -
	// set server-side, never by the probe itself, the same "declared vs. confirmed" distinction
	// Dependency.sources already draws elsewhere. Empty means this tunnel's other end is not visible
	// anywhere else in the topology, not that it doesn't exist - most tunnels legitimately lead somewhere
	// outside any onboarded cluster (a home gateway, a SaaS VPN concentrator).
	Confirmed string `json:"confirmed,omitempty"`
}

type Node struct {
	Provenance
	ID        string `json:"id"`
	Name      string `json:"name"`
	ClusterID string `json:"clusterId"`
	Role      string `json:"role"` // control-plane | worker
	// Aliases are the names this machine had before (a rename keeps the record); IdentityBasis is what its
	// identity is made of: provider-id | system-uuid | machine-id | name.
	Aliases       []string `json:"aliases,omitempty"`
	IdentityBasis string   `json:"identityBasis,omitempty"`
	Kind          string   `json:"kind"` // vm | bare-metal | edge-device
	IP            string   `json:"ip"`
	OS            string   `json:"os"`
	CPU           float64  `json:"cpu"`
	MemoryGb      float64  `json:"memoryGb"`
	// DiskGb mirrors Resources.DiskGb above: this node's own root filesystem capacity, not wrapped in a
	// *Resources pointer since - like CPU/MemoryGb just above - it is always known once the node itself
	// is (kubelet reports it as part of node Capacity, not a separate observation that can be absent).
	// See Resources.DiskGb's own comment for why this is a plain float64, not a pointer like PendingPodCount.
	DiskGb         float64           `json:"diskGb,omitempty"`
	Status         string            `json:"status"`
	Labels         map[string]string `json:"labels"`
	Arch           string            `json:"arch,omitempty"`
	Kernel         string            `json:"kernel,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
	KubeletVersion string            `json:"kubeletVersion,omitempty"`
	InstanceType   string            `json:"instanceType,omitempty"`
	Zone           string            `json:"zone,omitempty"`
	HardwareModel  string            `json:"hardwareModel,omitempty"`
	// Virtualization names the hypervisor or cloud platform under a VM ("KVM/QEMU", "Amazon EC2"); set only from the node probe.
	Virtualization string `json:"virtualization,omitempty"`
	// Connectivity is the physical uplink kind (ethernet | wifi | cellular) the node probe saw on a machine that is not a VM.
	Connectivity string `json:"connectivity,omitempty"`
	// HasBattery: the machine can run without mains power (laptop, board with a battery hat).
	HasBattery bool `json:"hasBattery,omitempty"`
	// Probed: a node probe reported on this machine, so kind and hardware come from the machine itself.
	Probed bool `json:"probed,omitempty"`
	// CPUModel is the CPU model name the probe read from the machine (e.g. "Intel(R) Xeon(R) Platinum
	// 8259CL CPU @ 2.50GHz"), the real host CPU regardless of any cgroup limit - unlike CPU below, which
	// is Kubernetes' allocatable millicore view and can be far smaller on a shared or throttled node.
	CPUModel string `json:"cpuModel,omitempty"`
	// CPUThreads is the number of logical CPUs (hardware threads) the probe saw on the machine itself.
	CPUThreads int32 `json:"cpuThreads,omitempty"`
	// NetworkInterfaces are the physical uplinks the probe saw, with whatever speed/MTU sysfs reported.
	// Connectivity above is derived from these (the kinds present); this is the fuller, per-interface view.
	NetworkInterfaces []NetworkInterface `json:"networkInterfaces,omitempty"`
	// Disks are the physical block devices the probe saw, capacity and type only (never a serial/WWN).
	Disks []Disk `json:"disks,omitempty"`
	// Tunnels are the overlay/tunnel interfaces the probe found up (WireGuard, VXLAN, GRE, IPIP/SIT,
	// route-based IPsec, ...) - see TunnelInterface's own comment for exactly what is, and is not, covered.
	Tunnels []TunnelInterface `json:"tunnels,omitempty"`
	// HostSubnets are this node's own routable network prefix(es) (e.g. "10.0.5.12/24"), taken only from
	// whichever interface owns the machine's default route - see continuumv1.HostProbe.host_subnets' own
	// doc. Used server-side, alongside Tunnels, to look for two onboarded clusters joined at the network
	// level (see ClusterLink); never shown as a claim on its own that two addresses are related.
	HostSubnets  []string      `json:"hostSubnets,omitempty"`
	ProviderID   string        `json:"providerId,omitempty"`
	Allocatable  *Resources    `json:"allocatable,omitempty"`
	Requested    *Resources    `json:"requested,omitempty"`
	Accelerators []Accelerator `json:"accelerators,omitempty"`
	Taints       []string      `json:"taints,omitempty"`
	Conditions   []string      `json:"conditions,omitempty"`
	CreatedAt    string        `json:"createdAt,omitempty"`
	PodCapacity  int32         `json:"podCapacity,omitempty"` // most pods the kubelet will run
	PodCount     *int32        `json:"podCount,omitempty"`    // nil when pods are not read (unknown, not zero)
}

type Namespace struct {
	Provenance
	ID        string            `json:"id"`
	ClusterID string            `json:"clusterId"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	// Mesh, MeshProxy: the namespace asks for its workloads to join a mesh (istio | linkerd | ...) through this kind of proxy.
	Mesh      string `json:"mesh,omitempty"`
	MeshProxy string `json:"meshProxy,omitempty"`
	// MeshOff: the namespace explicitly opts out (istio-injection=disabled, linkerd.io/inject=disabled).
	MeshOff bool `json:"meshOff,omitempty"`
	// Mtls is the mutual-TLS mode this namespace has when it differs from the mesh-wide one.
	Mtls string `json:"mtls,omitempty"`
}

type Service struct {
	Provenance
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	ClusterID     string            `json:"clusterId"`
	Kind          string            `json:"kind"`
	Image         string            `json:"image"`
	Replicas      int32             `json:"replicas"`
	NodeIDs       []string          `json:"nodeIds"`
	Status        string            `json:"status"`
	Labels        map[string]string `json:"labels"`
	ReadyReplicas int32             `json:"readyReplicas"`
	ImageDigest   string            `json:"imageDigest,omitempty"`
	CPURequestM   int64             `json:"cpuRequestM,omitempty"`
	MemRequestMi  int64             `json:"memRequestMi,omitempty"`
	CPULimitM     int64             `json:"cpuLimitM,omitempty"`
	MemLimitMi    int64             `json:"memLimitMi,omitempty"`
	Ports         []int32           `json:"ports,omitempty"`
	Exposure      string            `json:"exposure,omitempty"`
	Hosts         []string          `json:"hosts,omitempty"`
	ManagedBy     string            `json:"managedBy,omitempty"`
	NodeSelector  map[string]string `json:"nodeSelector,omitempty"`
	Tolerations   []string          `json:"tolerations,omitempty"`
	Restarts      int32             `json:"restarts,omitempty"`
	// OOMKills: containers, across this workload's pods, whose most recently known termination reason is
	// OOMKilled. This is a live snapshot of Kubernetes' own per-container LastTerminationState (which holds
	// only the ONE most recent termination reason), not a cumulative historical tally - a container OOM-
	// killed repeatedly while staying on the same pod still contributes at most 1, and that 1 reverts to 0
	// the moment it next fails for any other reason. Still a sharper signal than Restarts above for the
	// common case: a restart can be a crash, a deploy, or a liveness-probe failure, while a nonzero value
	// here means the container's last restart specifically was an OOM kill.
	OOMKills        int32        `json:"oomKills,omitempty"`
	ApplicationHint string       `json:"applicationHint,omitempty"`
	CreatedAt       string       `json:"createdAt,omitempty"`
	Volumes         []Volume     `json:"volumes,omitempty"`
	Autoscaler      *Autoscaler  `json:"autoscaler,omitempty"`
	Disruption      *Disruption  `json:"disruption,omitempty"`
	Mesh            *ServiceMesh `json:"mesh,omitempty"`
	// Pods are this service's individual replicas right now - the same minimal, privacy-conscious facts
	// the probe's own TunnelInterface/Disk carry (no address), so a scaling event (a pod noticeably
	// younger than its siblings) or one unusually-crashy replica among otherwise-healthy ones is visible
	// instead of only the aggregate Replicas/ReadyReplicas/Restarts/OOMKills above, which flatten exactly
	// that into a single number. Unset below tier 2 (pods are not read at all) or when a workload
	// genuinely has none scheduled yet - the zero value of this slice intentionally cannot distinguish the
	// two, the same "absence" ambiguity Node.PodCount's own *int32 is careful to avoid for a single count,
	// but not worth a pointer-to-slice here just to keep that distinction for a per-replica list.
	Pods []Pod `json:"pods,omitempty"`
}

// Pod is one replica backing a Service right now. See Service.Pods' own comment for what this is for and
// why it deliberately carries no address.
type Pod struct {
	Name string `json:"name"`
	// NodeID is set only when the pod's own node is itself known to this topology (the usual case); empty
	// when the node was filtered out of scope or the pod is not yet scheduled, the same rule Service's own
	// NodeIDs already follows.
	NodeID string `json:"nodeId,omitempty"`
	// Pending | Running | Succeeded | Failed | Unknown - exactly as Kubernetes reports it.
	Phase string `json:"phase"`
	// No omitempty: false is a real, meaningful value here (the pod is not ready), not an absent one - the
	// same reasoning Service.ReadyReplicas above already follows. Dropping it on encoding/json's usual
	// omitempty-on-zero-value behavior would make a not-ready pod indistinguishable on the wire from one
	// whose readiness was never collected at all.
	Ready bool `json:"ready"`
	// This pod's own restart count (every container's, summed) - Service.Restarts above is already this
	// same number summed again across every pod, which is exactly what flattens one unusually-crashy
	// replica among otherwise-healthy ones into an unremarkable average.
	Restarts int32 `json:"restarts,omitempty"`
	// CreatedAt is this pod's own age, unlike Service.CreatedAt (the workload object's own age, which never
	// changes on a routine scale-up) - the one fact that can actually show a recent scaling event.
	CreatedAt string `json:"createdAt,omitempty"`
	// Traffic is this one pod's own breakdown of who it talks to, right now - unlike Dependency (the
	// service-level edges the rest of the topology is drawn from, historically accumulated and always
	// present), this is a point-in-time snapshot from the agent's latest report, and nil whenever this
	// pod had no traffic of its own to show: below access tier 2, before the agent's first flow report
	// since this pod started, or simply because every one of its connections currently goes through a
	// Service address rather than being addressed to this pod directly (see resolve.go's own note on
	// why a Service-mediated destination never claims a specific pod). Meant to be fetched on demand -
	// when this one pod is expanded - never drawn as permanent edges on the canvas: see Service.Pods'
	// own comment on why a per-replica list exists at all, and aggregate.go's podFlows for why this can
	// exist without the whole topology paying per-pod cardinality.
	Traffic []PodPeer `json:"traffic,omitempty"`
}

// PodPeer is one line of a Pod's own traffic breakdown: this pod, and one workload or external address
// it has been talking to. Several PodPeers can share the same Peer (e.g. one in, one out, or two
// different ports) - each is its own observed edge, never merged across ports/protocols/directions the
// way Dependency already merges them at the service level.
type PodPeer struct {
	// Peer is the other side's identity: a Service ID (joinable against Service.ID, same as
	// Dependency.From/To already give for the "service" case) when PeerKind is "service", or the raw
	// address when PeerKind is "external" - Dependency already resolves that case too, but doing the same
	// here would need every cluster's reachability data assembled together, which happens later than one
	// agent's own interpretation does; showing the bare address is accordingly its own honest limit, not
	// a privacy choice.
	Peer     string `json:"peer"`
	PeerKind string `json:"peerKind"` // service | external
	// Direction is this pod's own role in the flow: "out" when this pod is the caller, "in" when it is
	// the one receiving.
	Direction   string `json:"direction"`
	Port        uint32 `json:"port"`
	Protocol    string `json:"protocol"`
	Connections uint64 `json:"connections"`
	BytesOut    uint64 `json:"bytesOut,omitempty"`
	BytesIn     uint64 `json:"bytesIn,omitempty"`
}

// Volume is a persistent volume claim used by a service.
type Volume struct {
	Name          string   `json:"name"`
	StorageClass  string   `json:"storageClass,omitempty"`
	SizeGb        float64  `json:"sizeGb"`
	AccessModes   []string `json:"accessModes,omitempty"`
	Phase         string   `json:"phase,omitempty"`
	PinnedNodeIDs []string `json:"pinnedNodeIds,omitempty"` // set when the data can only be used from these nodes
}

type Autoscaler struct {
	Min     int32    `json:"min"`
	Max     int32    `json:"max"`
	Current int32    `json:"current"`
	Targets []string `json:"targets,omitempty"`
}

type Disruption struct {
	MinAvailable   string `json:"minAvailable,omitempty"`
	MaxUnavailable string `json:"maxUnavailable,omitempty"`
	Allowed        int32  `json:"allowed"`
}

type Application struct {
	Provenance
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Origin      string `json:"origin"`
	Confidence  string `json:"confidence"`
}

// Suggestion is something discovery found that a person must confirm.
type Suggestion struct {
	ID        string `json:"id"`
	OrgID     string `json:"orgId"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	AgentID   string `json:"agentId,omitempty"`
	CreatedAt string `json:"createdAt"`
	Status    string `json:"status"`
	Apply     any    `json:"apply,omitempty"`
}

// CreateApplication is the action behind an application-grouping suggestion.
type CreateApplication struct {
	Type        string      `json:"type"` // "create-application"
	Application Application `json:"application"`
	ServiceIDs  []string    `json:"serviceIds"`
	// Other labels or annotations found on these services that could have named the application
	// instead, strongest first. The winning one above is always index -1 of the full cascade; these
	// are what it beat, offered so a person can pick one of them instead of typing a name by hand.
	Alternatives []GroupingAlternative `json:"alternatives,omitempty"`
}

// GroupingAlternative is a different name discovery could have used for this application, from a
// label or annotation that the winning rule outranked. Never applied on its own - a person chooses it.
type GroupingAlternative struct {
	Name       string `json:"name"`
	Origin     string `json:"origin"`
	Confidence string `json:"confidence"`
	Signal     string `json:"signal"`
}

type Topology struct {
	Clusters    []Cluster    `json:"clusters"`
	Nodes       []Node       `json:"nodes"`
	Namespaces  []Namespace  `json:"namespaces"`
	Services    []Service    `json:"services"`
	Suggestions []Suggestion `json:"suggestions"`
	// Observed traffic, worked out from the flows agents report. Unlike the records above these are
	// derived every time the state is built (rates and cross-cluster matches change with every window
	// and whenever a cluster is onboarded), so the UI does not store them in the workspace.
	Dependencies      []Dependency       `json:"dependencies"`
	ExternalEndpoints []ExternalEndpoint `json:"externalEndpoints"`
	// Paths are network paths an agent measured from its cluster (connect time to an address that
	// the cluster talks to, or that an administrator asked to be measured).
	Paths []Path `json:"paths"`
	// ClusterLinks are server-confirmed network-level relationships between two onboarded clusters
	// (overlay/tunnel, or same flat subnet) - see ClusterLink's own doc. Derived fresh with Dependencies
	// and ExternalEndpoints above, not stored in the workspace.
	ClusterLinks []ClusterLink `json:"clusterLinks"`
}

// Path is the measured quality of the network path from one cluster to an address.
type Path struct {
	ID          string `json:"id"`
	FromCluster string `json:"fromCluster"`
	FromName    string `json:"fromName"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Label       string `json:"label,omitempty"`
	// ToCluster is set when the address belongs to another onboarded cluster.
	ToCluster string `json:"toCluster,omitempty"`
	ToName    string `json:"toName,omitempty"`
	// Source: observed (the cluster already talks to this address) | manual (an administrator asked for it).
	Source string  `json:"source"`
	RTTMin float64 `json:"rttMinMs"`
	RTTP50 float64 `json:"rttP50Ms"`
	RTTP95 float64 `json:"rttP95Ms"`
	// LossPct is the share of connection attempts that failed in the recent window (0-100).
	LossPct float64 `json:"lossPct"`
	Samples int     `json:"samples"`
	At      string  `json:"at"`
	Stale   bool    `json:"stale,omitempty"`
}

// ClusterLink is a server-confirmed fact that two onboarded clusters' networks are directly joined -
// either through an overlay/tunnel (one cluster's TunnelInterface routes are independently confirmed to
// reach a node address the other cluster reports, the same two-way match TunnelInterface.Confirmed
// already does for a single node) or because nodes in each cluster simply sit on the same flat subnet
// with no tunnel at all (their HostSubnets prefixes overlap, past the same specificity floor used
// elsewhere to reject a coincidental match on a broad shared private supernet). Never guessed from
// naming, labels or an administrator's say-so - always derived from what two independent nodes already,
// separately reported about their own addresses and routes. Absence of a ClusterLink between two
// clusters means no such relationship was corroborated from both sides, not that the clusters are
// definitely unconnected - the same "absence is not disproof" framing TunnelInterface.Confirmed draws.
type ClusterLink struct {
	FromCluster string `json:"fromCluster"`
	FromName    string `json:"fromName"`
	ToCluster   string `json:"toCluster"`
	ToName      string `json:"toName"`
	// Kind: "overlay" (joined through a tunnel/overlay interface) | "subnet" (same flat network segment,
	// no tunnel involved).
	Kind string `json:"kind"`
	// Via names the specific evidence behind this link: the tunnel interface's name and kind for an
	// overlay link (e.g. "wg0 (wireguard)"), or the shared subnet prefix for a subnet link (e.g.
	// "10.0.5.0/24") - so the UI never has to say just "connected" with nothing to point at.
	Via string `json:"via"`
	// Redundancy is how many independently corroborating node pairs back this link - always at least 1
	// for anything reported here. More than 1 is itself a fact worth seeing: it means there is more than
	// one path between these two clusters for this Kind (e.g. a second WireGuard peering kept for
	// failover), so losing one does not necessarily cut the clusters off from each other. Previously this
	// was silently discarded - every corroborating pair past the first was matched, found, and dropped
	// without a trace, which erased exactly the "is this a single point of failure" fact this field now
	// keeps.
	Redundancy int `json:"redundancy"`
	// FromNode/ToNode name the specific node on each side whose tunnel (or shared subnet) first
	// corroborated this link, so the evidence points at an actual machine rather than only a cluster pair
	// and a driver name. When Redundancy is more than 1, these two name only the first matching pair found
	// - not an exhaustive list of every corroborating node.
	FromNode string `json:"fromNode,omitempty"`
	ToNode   string `json:"toNode,omitempty"`
	// FromAddress/ToAddress are the two tunnel interfaces' own addresses that confirmed an "overlay" link
	// (e.g. "10.8.0.1/24" and "10.8.0.2/24") - empty for a "subnet" link, where Via (the shared network)
	// already is the complete evidence.
	FromAddress string `json:"fromAddress,omitempty"`
	ToAddress   string `json:"toAddress,omitempty"`
	// FlowsObserved/AvgRttMs/AvgLossPct roll up the live Dependency flows actually crossing this link's
	// confirmed tunnel - matched by the calling service's own cluster (whichever of FromCluster/ToCluster
	// it belongs to) and its Dependency.Iface against the confirmed tunnel interface name(s) correlated
	// on that same side (every corroborating node pair's interface, not just the first - see Redundancy).
	// Only ever set for Kind == "overlay": a "subnet" link has no specific interface to correlate
	// flows against, just a shared network. AvgRttMs is 0 (omitted) when none of the matched flows have
	// a measured RTT sample yet, the same "0 means not measured" convention Dependency.RttMs itself
	// uses. AvgLossPct is a pointer for the same reason Dependency.Stats.LossPct is one: nil means none
	// of the matched flows have a measured loss percentage yet, never a fabricated 0%.
	FlowsObserved int      `json:"flowsObserved,omitempty"`
	AvgRttMs      float64  `json:"avgRttMs,omitempty"`
	AvgLossPct    *float64 `json:"avgLossPct,omitempty"`
	// AvgRtoRetransmitsPerMin averages Dependency.Stats.RtoRetransmitsPerMin across the same matched
	// flows AvgRttMs/AvgLossPct roll up - RTO retransmits specifically (see RtoRetransmits' own doc on
	// Dependency) rather than the raw, undifferentiated retransmit rate, because this field exists to
	// say something about the tunnel's own link quality, and a fast retransmit recovering from ordinary
	// reordering says nothing about that. Plain float64 like AvgRttMs, not a pointer like AvgLossPct: 0
	// means "not measured" and "genuinely none happened" alike, the same tolerated ambiguity
	// RtoRetransmitsPerMin's own doc already accepts for a per-minute rate (unlike a percentage, where
	// the zero-denominator case needs telling apart from a real 0%).
	AvgRtoRetransmitsPerMin float64 `json:"avgRtoRetransmitsPerMin,omitempty"`
	// AvgMssBytes averages Dependency.MssBytes across the same matched flows AvgRttMs/AvgLossPct roll
	// up - the effective segment size actually in use on traffic crossing this tunnel right now. This is
	// where MssBytes matters most: a healthy direct path's MSS is bounded by the interface MTU alone,
	// while this link's own encapsulation overhead (VXLAN/WireGuard/GRE headers) eats into it further, so
	// a falling AvgMssBytes over time is a real, measured sign of growing per-packet overhead on this
	// specific tunnel - not available at all for an unconfirmed link, which has no specific interface to
	// correlate flows against. 0 means none of the matched flows have a measured MSS sample yet, the
	// same "0 means not measured" convention AvgRttMs itself uses.
	AvgMssBytes float64 `json:"avgMssBytes,omitempty"`
	// Encryption classifies an "overlay" link's confirmed tunnel driver (see TunnelInterface.Kind) as
	// "encrypted" (WireGuard, or an IPsec virtual-tunnel kind: vti/vti6/xfrm) or "plaintext" (a
	// tunneling/encapsulation protocol with no cryptography of its own: vxlan, geneve, gre and its
	// variants, ipip, sit) - see TunnelEncryptionPosture. This is an inference from the driver type
	// alone, the same honesty line the mesh mTLS verdict already draws elsewhere: a WireGuard or IPsec
	// kind makes it very likely the tunnel is encrypted, but nothing here proves a given packet actually
	// was - only the wire could. Only set from the first corroborating pair for a link, same as
	// FromNode/ToNode. Empty for a "subnet" link, which has no tunnel driver to classify at all.
	Encryption string `json:"encryption,omitempty"`
}

// DependencyTunnelLink is the confirmed, cross-cluster overlay ClusterLink a Dependency's own Iface was
// matched onto - see Dependency.TunnelLink's own doc for exactly when this is set. A deliberately smaller
// copy of ClusterLink's own evidence fields: FlowsObserved/AvgRttMs/AvgLossPct stay on ClusterLink alone,
// since those are an aggregate across every dependency crossing the link, not a fact about this one.
type DependencyTunnelLink struct {
	FromCluster string `json:"fromCluster"`
	ToCluster   string `json:"toCluster"`
	Via         string `json:"via"`
	Redundancy  int    `json:"redundancy"`
	Encryption  string `json:"encryption,omitempty"`
}

// encryptedTunnelKinds are the TunnelInterface.Kind values whose protocol encrypts traffic by design.
var encryptedTunnelKinds = map[string]bool{"wireguard": true, "vti": true, "vti6": true, "xfrm": true}

// plaintextTunnelKinds are the TunnelInterface.Kind values that tunnel/encapsulate traffic but carry no
// cryptography of their own - real encryption there, if any, comes from something this package has no
// visibility into (a separate IPsec policy, a mesh sidecar riding on top).
var plaintextTunnelKinds = map[string]bool{
	"vxlan": true, "geneve": true, "gre": true, "gretap": true, "ip6gre": true, "ip6gretap": true, "ipip": true, "sit": true,
}

// TunnelEncryptionPosture classifies a TunnelInterface.Kind as "encrypted", "plaintext", or "unknown" (any
// kind outside the two sets above - never fabricated as one or the other). See ClusterLink.Encryption's
// own doc for what this inference does, and does not, establish.
func TunnelEncryptionPosture(kind string) string {
	switch {
	case encryptedTunnelKinds[kind]:
		return "encrypted"
	case plaintextTunnelKinds[kind]:
		return "plaintext"
	default:
		return "unknown"
	}
}

// ExternalEndpoint is something outside every onboarded cluster that traffic was seen going to or
// coming from. Only its address is known.
type ExternalEndpoint struct {
	Provenance
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	Kind string `json:"kind"` // saas | database | unknown
	// Service names the application usually found on Port (e.g. "PostgreSQL", "Kafka") - a guess from the
	// port number alone, the same way Kind's "database" bucket already was; empty when the port isn't one
	// of the well-known ones. Never inferred from payload: nothing here reads a byte of one.
	Service string `json:"service,omitempty"`
	// Name is a short, human label for who this address belongs to (e.g. "GitHub"), set only when the
	// server matched it against a small, bundled table of provider-published ranges (see
	// internal/netid) - never from anything sent in traffic. Empty means no match, same meaning as an
	// empty Service: this is a filled-in-when-possible convenience, not a claim that nothing is there.
	Name string `json:"name,omitempty"`
	// IPs is every individual address observed that resolved to this same endpoint identity - just
	// [Host] for the common case of one address, one endpoint, but more than one when several addresses
	// collapsed into a single non-Shared provider match (see internal/netid's Match.Shared and
	// observed.go's external()): several GitHub IPs, or several of Google's own edge addresses, are all
	// "the same thing" and share one topology node, but nothing about which individual addresses actually
	// made up that traffic is lost - it's here instead of scattered across separate nodes. Sorted for a
	// stable order across polls.
	IPs []string `json:"ips,omitempty"`
}

type DependencyStats struct {
	BytesPerSec       float64 `json:"bytesPerSec,omitempty"`
	ConnectionsPerMin float64 `json:"connectionsPerMin,omitempty"`
	// RetransmitsPerMin is 0 both when there is genuinely no loss and when nothing eBPF-observed has
	// reported yet (conntrack cannot see retransmits at all) - Dependency.Via says which case it is.
	RetransmitsPerMin float64 `json:"retransmitsPerMin,omitempty"`
	// RtoRetransmitsPerMin is the subset of RetransmitsPerMin that the RTO timer itself fired for - no
	// ACK at all came back within a full round-trip-plus-backoff, as opposed to a fast retransmit
	// recovering from ordinary reordering without ever stalling the connection. Same "0 means not
	// measured, not measured-as-zero" story as RetransmitsPerMin, and the same eBPF-only availability.
	RtoRetransmitsPerMin float64 `json:"rtoRetransmitsPerMin,omitempty"`
	// FailedAttemptsPerMin is the same "0 means not measured, not measured-as-zero" story as
	// RetransmitsPerMin: only eBPF sees a connection attempt that never got established at all.
	FailedAttemptsPerMin float64 `json:"failedAttemptsPerMin,omitempty"`
	// LossPct is a real loss percentage for the most recent window - retransmitted segments divided by
	// segments sent, computed here rather than carried on the wire, so the zero-denominator case (no
	// segs_out observed yet - conntrack, or an eBPF report too early to have sent anything) stays an
	// explicit "undefined" (this field simply absent) rather than a fabricated 0%. RetransmitsPerMin above
	// still carries the raw rate for anyone who wants it without a percentage attached.
	LossPct   *float64 `json:"lossPct,omitempty"`
	WindowSec int32    `json:"windowSec,omitempty"`
}

// Dependency is a service-to-service edge that was seen on the wire.
type Dependency struct {
	ID         string   `json:"id"`
	OrgID      string   `json:"orgId"`
	From       string   `json:"from"`
	FromKind   string   `json:"fromKind"` // service | external
	To         string   `json:"to"`
	ToKind     string   `json:"toKind"`
	Sources    []string `json:"sources"`
	Confidence string   `json:"confidence"`
	FirstSeen  string   `json:"firstSeen,omitempty"`
	LastSeen   string   `json:"lastSeen,omitempty"`
	Protocol   string   `json:"protocol"`
	Port       int      `json:"port,omitempty"`
	// Service names the application usually found on Port (e.g. "PostgreSQL", "Kafka") - inferred from the
	// port number alone, same as ExternalEndpoint.Service, and just as much a guess: a workload can run
	// anything on any port. Empty when the port isn't one of the well-known ones. Never a substitute for
	// Protocol, which is the transport (tcp/udp) and part of this Dependency's identity; Service is purely
	// descriptive and never affects identity or grouping.
	Service string `json:"service,omitempty"`
	Label   string `json:"label,omitempty"`
	Stale   bool   `json:"stale,omitempty"`
	// Noise marks traffic that is machinery rather than the applications' own: dns | system.
	Noise string `json:"noise,omitempty"`
	// CrossCluster: the two ends are in different onboarded clusters.
	CrossCluster bool `json:"crossCluster,omitempty"`
	// Via is how it was observed (ebpf | conntrack); Note says how the far end was identified, when it was not certain.
	Via string `json:"via,omitempty"`
	// The caller's physical network interface (e.g. "eth0", "wlan0"), when the collector's kernel route
	// lookup could name one - never guessed from the port or address. Empty means not known, which on a
	// single-homed node is simply not interesting and on a multi-homed one (an edge box with both
	// ethernet and a cellular backhaul, say) is worth surfacing rather than assuming.
	Iface string `json:"iface,omitempty"`
	// Retransmits is the cumulative count of TCP segments retransmitted over this edge's whole life, from
	// the kernel's own congestion-control counters - never inferred from timing. Always 0 on a
	// conntrack-only edge (Via != "ebpf"), which has no socket to read this from, so a 0 there means
	// "not measured", not "no loss".
	Retransmits uint64 `json:"retransmits,omitempty"`
	// RtoRetransmits is the cumulative subset of Retransmits that the RTO timer itself fired for - the
	// kernel got no ACK at all within a full round-trip-plus-backoff, as opposed to a fast retransmit
	// that recovered from ordinary packet reordering without ever stalling the connection. This is the
	// real leading indicator of a degrading link: Retransmits alone cannot tell the two apart, and a
	// connection can carry plenty of the ordinary kind while never actually timing out. Same conntrack
	// caveat as Retransmits: always 0 there, meaning "not measured".
	RtoRetransmits uint64 `json:"rtoRetransmits,omitempty"`
	// FailedAttempts is the cumulative count of connection attempts between these two ends that never
	// reached ESTABLISHED - refused, timed out, reset mid-handshake, or unreachable - summed the same way
	// Retransmits is, and with the same conntrack caveat: always 0 on a conntrack-only edge, where it
	// means "not measured", not "every attempt succeeded". A dependency that is all failed attempts and
	// no successful Connections at all is exactly the case this exists to surface: something the
	// application keeps trying and never reaching.
	FailedAttempts uint64 `json:"failedAttempts,omitempty"`
	// SniHost is the server name a TLS ClientHello asked for on this exact edge (the SNI extension),
	// read once from the clear-text handshake before anything is encrypted - never a certificate, never
	// application data. Unset unless the node collector's optional name-capture is turned on (off by
	// default) and this edge's traffic is actually TLS. A gauge: the latest hostname seen, not a history.
	SniHost string `json:"sniHost,omitempty"`
	// DnsQueryNames is the distinct domain names this edge's traffic has asked its resolver to look up,
	// newest first, capped - only ever populated on the pod<->resolver edge itself (Noise == "dns"), and
	// only when the same optional name-capture is on. Unlike SniHost, this only ever grows by addition:
	// a resolver edge legitimately fields many different lookups, and which ones is the point.
	DnsQueryNames []string `json:"dnsQueryNames,omitempty"`
	// RttMs is the most recently sampled smoothed round-trip time in milliseconds, from the kernel's own
	// TCP RTT estimator. A gauge (the latest sample), not an average over the edge's life. 0 means no
	// sample yet, not "no delay" - most often because too little has been exchanged to measure one, or
	// because this edge is conntrack-only.
	RttMs float64 `json:"rttMs,omitempty"`
	// JitterMs is the RTT estimator's own mean-deviation sample in milliseconds - the variance behind
	// RttMs, read at the exact same moments. Same gauge/0-means-no-sample treatment as RttMs.
	JitterMs float64 `json:"jitterMs,omitempty"`
	// HandshakeMs is how long the most recently-established connection on this edge took to go from its
	// first SYN to ESTABLISHED, in milliseconds - a gauge set once per connection (the latest one to
	// establish wins, the same "latest sample replaces the last" treatment as every other gauge here),
	// distinct from RttMs, which is the ongoing steady-state round trip. 0 means no sample.
	HandshakeMs float64 `json:"handshakeMs,omitempty"`
	// CwndSegments is the kernel's own current congestion window, in segments, from the same
	// congestion-control bookkeeping RttMs already reads - a gauge, same "0 means no sample" treatment.
	// Read alongside PacingBps: together they say what the kernel itself currently believes this
	// connection's send rate is bounded by.
	CwndSegments uint32 `json:"cwndSegments,omitempty"`
	// PacingBps is the pacing rate TCP's own congestion control last set for this edge, bytes/sec - a
	// gauge, same treatment as CwndSegments right above. 0 means no pacer is active yet (e.g. a very
	// young connection), not "idle".
	PacingBps uint64 `json:"pacingBps,omitempty"`
	// MssBytes is the kernel's own current effective segment size for this edge, in bytes (struct
	// tcp_sock.mss_cache) - a gauge, same "0 means no sample" treatment as CwndSegments/PacingBps above.
	// Most informative on an edge whose TunnelLink (below) is set: an overlay tunnel's own encapsulation
	// headers (VXLAN/WireGuard/GRE) eat into the path MTU, so the kernel's PMTU discovery shrinks this
	// below the plain interface MTU - a falling MssBytes on a tunneled edge is a real, measured sign of
	// that overhead, not a guess from the tunnel's declared type. Always 0 on a conntrack-only edge.
	MssBytes uint32 `json:"mssBytes,omitempty"`
	// BufferDrops is the cumulative count of this edge's receive-side buffer drops, from the kernel's own
	// per-socket counter - summed the same way Retransmits/FailedAttempts are. A different failure mode
	// from Retransmits: the receiving application not draining its socket fast enough, not the network
	// losing a packet in transit. Always 0 on a conntrack-only edge, where it means "not measured".
	BufferDrops uint64 `json:"bufferDrops,omitempty"`
	// DnsRttMs is how long a DNS response took to arrive after its matching query, in milliseconds - a
	// gauge, only ever set on the pod<->resolver edge itself (Noise == "dns"), matched by transaction id
	// rather than read from a socket (DNS is UDP, so there is no socket state machine the way TCP's
	// RttMs has). Requires the same optional name-capture opt-in as DnsQueryNames/SniHost. 0 means no
	// sample yet - most often because the response hasn't arrived, was lost, or this edge is
	// conntrack-only.
	DnsRttMs    float64          `json:"dnsRttMs,omitempty"`
	Note        string           `json:"note,omitempty"`
	Connections uint64           `json:"connections,omitempty"`
	Bytes       uint64           `json:"bytes,omitempty"`
	Stats       *DependencyStats `json:"stats,omitempty"`
	// TunnelLink is set when Iface (above) matched one side of a confirmed, named overlay ClusterLink
	// between this dependency's own two endpoints' clusters - see correlateClusterLinks' own matching
	// rule (cluster AND interface name together, never interface name alone: a generic name like "wg0"
	// is commonly reused across entirely unrelated tunnels). Lets the UI show "this traffic crosses a
	// confirmed tunnel, and here is what kind" directly on the edge that carries it, in addition to -
	// not instead of - the aggregate rollup on ClusterLink itself (FlowsObserved and its own doc), which
	// still matters for a tunnel used by several distinct dependencies at once, or by none right now.
	// Nil when Iface is empty, or non-empty but did not match any confirmed tunnel.
	TunnelLink *DependencyTunnelLink `json:"tunnelLink,omitempty"`
	// MeshBypass is true when this edge's own traffic has actually been seen (eBPF only, never
	// conntrack) leaving its caller directly - without first being redirected to a local mesh sidecar
	// proxy - even though that caller is configured to run one and does not declare this exact port out
	// of it. Unlike mesh.ts's connectionVerdict (which only ever infers a mesh's effect from
	// configuration: proxies present, ports excluded, mutual-TLS policy), this is read straight off the
	// wire by the node collector's optional name-capture (flow.c's observe_egress/note_mesh_bypass) and
	// then cross-checked against that same declared configuration here on the server
	// (applyMeshBypassFacts) - so it only ever flags a genuine surprise: injection that silently failed,
	// an iptables rule that never applied, hostNetwork traffic that skipped the pod's own netns, and the
	// like. Always false for a workload with no sidecar configured at all, or one that explicitly
	// excludes this port - that traffic bypassing the mesh is expected, not a finding.
	MeshBypass bool `json:"meshBypass,omitempty"`
}
