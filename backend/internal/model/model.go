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
}

type Accelerator struct {
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	Count  int64  `json:"count"`
}

type Node struct {
	Provenance
	ID        string `json:"id"`
	Name      string `json:"name"`
	ClusterID string `json:"clusterId"`
	Role      string `json:"role"` // control-plane | worker
	// Aliases are the names this machine had before (a rename keeps the record); IdentityBasis is what its
	// identity is made of: provider-id | system-uuid | machine-id | name.
	Aliases        []string          `json:"aliases,omitempty"`
	IdentityBasis  string            `json:"identityBasis,omitempty"`
	Kind           string            `json:"kind"` // vm | bare-metal | edge-device
	IP             string            `json:"ip"`
	OS             string            `json:"os"`
	CPU            float64           `json:"cpu"`
	MemoryGb       float64           `json:"memoryGb"`
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
	Probed       bool          `json:"probed,omitempty"`
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
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	ClusterID       string            `json:"clusterId"`
	Kind            string            `json:"kind"`
	Image           string            `json:"image"`
	Replicas        int32             `json:"replicas"`
	NodeIDs         []string          `json:"nodeIds"`
	Status          string            `json:"status"`
	Labels          map[string]string `json:"labels"`
	ReadyReplicas   int32             `json:"readyReplicas"`
	ImageDigest     string            `json:"imageDigest,omitempty"`
	CPURequestM     int64             `json:"cpuRequestM,omitempty"`
	MemRequestMi    int64             `json:"memRequestMi,omitempty"`
	CPULimitM       int64             `json:"cpuLimitM,omitempty"`
	MemLimitMi      int64             `json:"memLimitMi,omitempty"`
	Ports           []int32           `json:"ports,omitempty"`
	Exposure        string            `json:"exposure,omitempty"`
	Hosts           []string          `json:"hosts,omitempty"`
	ManagedBy       string            `json:"managedBy,omitempty"`
	NodeSelector    map[string]string `json:"nodeSelector,omitempty"`
	Tolerations     []string          `json:"tolerations,omitempty"`
	Restarts        int32             `json:"restarts,omitempty"`
	ApplicationHint string            `json:"applicationHint,omitempty"`
	CreatedAt       string            `json:"createdAt,omitempty"`
	Volumes         []Volume          `json:"volumes,omitempty"`
	Autoscaler      *Autoscaler       `json:"autoscaler,omitempty"`
	Disruption      *Disruption       `json:"disruption,omitempty"`
	Mesh            *ServiceMesh      `json:"mesh,omitempty"`
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

// ExternalEndpoint is something outside every onboarded cluster that traffic was seen going to or
// coming from. Only its address is known.
type ExternalEndpoint struct {
	Provenance
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	Kind string `json:"kind"` // saas | database | unknown
}

type DependencyStats struct {
	BytesPerSec       float64 `json:"bytesPerSec,omitempty"`
	ConnectionsPerMin float64 `json:"connectionsPerMin,omitempty"`
	WindowSec         int32   `json:"windowSec,omitempty"`
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
	Label      string   `json:"label,omitempty"`
	Stale      bool     `json:"stale,omitempty"`
	// Noise marks traffic that is machinery rather than the applications' own: dns | system.
	Noise string `json:"noise,omitempty"`
	// CrossCluster: the two ends are in different onboarded clusters.
	CrossCluster bool `json:"crossCluster,omitempty"`
	// Via is how it was observed (ebpf | conntrack); Note says how the far end was identified, when it was not certain.
	Via         string           `json:"via,omitempty"`
	Note        string           `json:"note,omitempty"`
	Connections uint64           `json:"connections,omitempty"`
	Bytes       uint64           `json:"bytes,omitempty"`
	Stats       *DependencyStats `json:"stats,omitempty"`
}
