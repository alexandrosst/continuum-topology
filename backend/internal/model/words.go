package model

// The words that the records of this package write as plain strings, as types. A field of such a type is
// written to JSON exactly as before; what the type adds is a list of its words (WireWords) from which the
// generated TypeScript writes a union, so that a word the server can send and the page does not know is a test
// failure and not a surprise. A value that arrives from an agent may be outside the list: a type here lists the
// words the server itself writes or the page switches on, it does not reject anything.

// Confidence is how sure a detection is.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// WireWords is the list of Confidence's words, as the generated types give it.
func (Confidence) WireWords() []string {
	return []string{string(ConfidenceHigh), string(ConfidenceMedium), string(ConfidenceLow)}
}

// Source is where a record came from.
type Source string

const (
	SourceManual     Source = "manual"
	SourceDiscovered Source = "discovered"
	SourceImported   Source = "imported"
)

// WireWords is the list of Source's words, as the generated types give it.
func (Source) WireWords() []string {
	return []string{string(SourceManual), string(SourceDiscovered), string(SourceImported)}
}

// ObservationState is how far a record can be trusted right now.
type ObservationState string

const (
	ObservationStateLive         ObservationState = "live"
	ObservationStateDisconnected ObservationState = "disconnected"
	ObservationStateStale        ObservationState = "stale"
	ObservationStateRevoked      ObservationState = "revoked"
)

// WireWords is the list of ObservationState's words, as the generated types give it.
func (ObservationState) WireWords() []string {
	return []string{string(ObservationStateLive), string(ObservationStateDisconnected), string(ObservationStateStale), string(ObservationStateRevoked)}
}

// Tier is where a cluster sits between the cloud and the far edge.
type Tier string

const (
	TierCloud   Tier = "cloud"
	TierEdge    Tier = "edge"
	TierFarEdge Tier = "far-edge"
)

// WireWords is the list of Tier's words, as the generated types give it.
func (Tier) WireWords() []string {
	return []string{string(TierCloud), string(TierEdge), string(TierFarEdge)}
}

// Status is how a cluster, node or service is doing.
type Status string

const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusOffline  Status = "offline"
	StatusUnknown  Status = "unknown"
)

// WireWords is the list of Status's words, as the generated types give it.
func (Status) WireWords() []string {
	return []string{string(StatusHealthy), string(StatusDegraded), string(StatusOffline), string(StatusUnknown)}
}

// MtlsMode is the mutual-TLS mode of a mesh or a namespace.
type MtlsMode string

const (
	MtlsModeStrict     MtlsMode = "strict"
	MtlsModePermissive MtlsMode = "permissive"
	MtlsModeDisabled   MtlsMode = "disabled"
	MtlsModeAutomatic  MtlsMode = "automatic"
	MtlsModeUnknown    MtlsMode = "unknown"
)

// WireWords is the list of MtlsMode's words, as the generated types give it.
func (MtlsMode) WireWords() []string {
	return []string{string(MtlsModeStrict), string(MtlsModePermissive), string(MtlsModeDisabled), string(MtlsModeAutomatic), string(MtlsModeUnknown)}
}

// NodeRole is what a node does in its cluster.
type NodeRole string

const (
	NodeRoleControlPlane NodeRole = "control-plane"
	NodeRoleWorker       NodeRole = "worker"
)

// WireWords is the list of NodeRole's words, as the generated types give it.
func (NodeRole) WireWords() []string {
	return []string{string(NodeRoleControlPlane), string(NodeRoleWorker)}
}

// MachineKind is what a node runs on.
type MachineKind string

const (
	MachineKindVm         MachineKind = "vm"
	MachineKindBareMetal  MachineKind = "bare-metal"
	MachineKindEdgeDevice MachineKind = "edge-device"
)

// WireWords is the list of MachineKind's words, as the generated types give it.
func (MachineKind) WireWords() []string {
	return []string{string(MachineKindVm), string(MachineKindBareMetal), string(MachineKindEdgeDevice)}
}

// ServiceKind is the Kubernetes kind of a workload.
type ServiceKind string

const (
	ServiceKindDeployment  ServiceKind = "Deployment"
	ServiceKindStatefulSet ServiceKind = "StatefulSet"
	ServiceKindDaemonSet   ServiceKind = "DaemonSet"
	ServiceKindJob         ServiceKind = "Job"
)

// WireWords is the list of ServiceKind's words, as the generated types give it.
func (ServiceKind) WireWords() []string {
	return []string{string(ServiceKindDeployment), string(ServiceKindStatefulSet), string(ServiceKindDaemonSet), string(ServiceKindJob)}
}

// ManagedBy is the tool that manages a workload.
type ManagedBy string

const (
	ManagedByHelm      ManagedBy = "helm"
	ManagedByArgo      ManagedBy = "argo"
	ManagedByFlux      ManagedBy = "flux"
	ManagedByKustomize ManagedBy = "kustomize"
	ManagedByOperator  ManagedBy = "operator"
	ManagedByKubectl   ManagedBy = "kubectl"
	ManagedByUnknown   ManagedBy = "unknown"
)

// WireWords is the list of ManagedBy's words, as the generated types give it.
func (ManagedBy) WireWords() []string {
	return []string{string(ManagedByHelm), string(ManagedByArgo), string(ManagedByFlux), string(ManagedByKustomize), string(ManagedByOperator), string(ManagedByKubectl), string(ManagedByUnknown)}
}

// EndpointKind is what one end of a dependency is.
type EndpointKind string

const (
	EndpointKindService  EndpointKind = "service"
	EndpointKindDevice   EndpointKind = "device"
	EndpointKindExternal EndpointKind = "external"
)

// WireWords is the list of EndpointKind's words, as the generated types give it.
func (EndpointKind) WireWords() []string {
	return []string{string(EndpointKindService), string(EndpointKindDevice), string(EndpointKindExternal)}
}

// DependencySource is how a dependency came to be known.
type DependencySource string

const (
	DependencySourceDeclared DependencySource = "declared"
	DependencySourceObserved DependencySource = "observed"
	DependencySourceManual   DependencySource = "manual"
)

// WireWords is the list of DependencySource's words, as the generated types give it.
func (DependencySource) WireWords() []string {
	return []string{string(DependencySourceDeclared), string(DependencySourceObserved), string(DependencySourceManual)}
}

// ExternalKind is what an external endpoint is.
type ExternalKind string

const (
	ExternalKindSaas     ExternalKind = "saas"
	ExternalKindDatabase ExternalKind = "database"
	ExternalKindUnknown  ExternalKind = "unknown"
)

// WireWords is the list of ExternalKind's words, as the generated types give it.
func (ExternalKind) WireWords() []string {
	return []string{string(ExternalKindSaas), string(ExternalKindDatabase), string(ExternalKindUnknown)}
}

// PeerKind is what a pod's peer is.
type PeerKind string

const (
	PeerKindService  PeerKind = "service"
	PeerKindExternal PeerKind = "external"
)

// WireWords is the list of PeerKind's words, as the generated types give it.
func (PeerKind) WireWords() []string {
	return []string{string(PeerKindService), string(PeerKindExternal)}
}

// PodDirection is a pod's own role in a flow.
type PodDirection string

const (
	PodDirectionOut PodDirection = "out"
	PodDirectionIn  PodDirection = "in"
)

// WireWords is the list of PodDirection's words, as the generated types give it.
func (PodDirection) WireWords() []string {
	return []string{string(PodDirectionOut), string(PodDirectionIn)}
}

// PathSource is why a measured path exists.
type PathSource string

const (
	PathSourceObserved PathSource = "observed"
	PathSourceManual   PathSource = "manual"
)

// WireWords is the list of PathSource's words, as the generated types give it.
func (PathSource) WireWords() []string {
	return []string{string(PathSourceObserved), string(PathSourceManual)}
}

// LinkKind is how two clusters are joined.
type LinkKind string

const (
	LinkKindOverlay LinkKind = "overlay"
	LinkKindSubnet  LinkKind = "subnet"
)

// WireWords is the list of LinkKind's words, as the generated types give it.
func (LinkKind) WireWords() []string {
	return []string{string(LinkKindOverlay), string(LinkKindSubnet)}
}

// TunnelEncryption is the verdict on whether a tunnel encrypts.
type TunnelEncryption string

const (
	TunnelEncryptionEncrypted TunnelEncryption = "encrypted"
	TunnelEncryptionPlaintext TunnelEncryption = "plaintext"
	TunnelEncryptionUnknown   TunnelEncryption = "unknown"
)

// WireWords is the list of TunnelEncryption's words, as the generated types give it.
func (TunnelEncryption) WireWords() []string {
	return []string{string(TunnelEncryptionEncrypted), string(TunnelEncryptionPlaintext), string(TunnelEncryptionUnknown)}
}

// PairStatus is whether two clusters can reach each other, and how.
type PairStatus string

const (
	PairStatusTunnel      PairStatus = "tunnel"
	PairStatusSubnet      PairStatus = "subnet"
	PairStatusUnexplained PairStatus = "unexplained"
	PairStatusUnknown     PairStatus = "unknown"
)

// WireWords is the list of PairStatus's words, as the generated types give it.
func (PairStatus) WireWords() []string {
	return []string{string(PairStatusTunnel), string(PairStatusSubnet), string(PairStatusUnexplained), string(PairStatusUnknown)}
}

// Noise is the machinery that a dependency's traffic is.
type Noise string

const (
	NoiseDns    Noise = "dns"
	NoiseSystem Noise = "system"
)

// WireWords is the list of Noise's words, as the generated types give it.
func (Noise) WireWords() []string {
	return []string{string(NoiseDns), string(NoiseSystem)}
}

// ObservedVia is how a dependency was observed.
type ObservedVia string

const (
	ObservedViaEbpf      ObservedVia = "ebpf"
	ObservedViaConntrack ObservedVia = "conntrack"
)

// WireWords is the list of ObservedVia's words, as the generated types give it.
func (ObservedVia) WireWords() []string {
	return []string{string(ObservedViaEbpf), string(ObservedViaConntrack)}
}

// TLSHandshake is the outcome of the latest handshake on an edge.
type TLSHandshake string

const (
	TLSHandshakeOk     TLSHandshake = "ok"
	TLSHandshakeFailed TLSHandshake = "failed"
)

// WireWords is the list of TLSHandshake's words, as the generated types give it.
func (TLSHandshake) WireWords() []string {
	return []string{string(TLSHandshakeOk), string(TLSHandshakeFailed)}
}

// SuggestionKind is what a suggestion proposes to add.
type SuggestionKind string

const (
	SuggestionKindDevice           SuggestionKind = "device"
	SuggestionKindExternalEndpoint SuggestionKind = "external-endpoint"
	SuggestionKindSite             SuggestionKind = "site"
	SuggestionKindApplication      SuggestionKind = "application"
	SuggestionKindDependency       SuggestionKind = "dependency"
	SuggestionKindInfrastructure   SuggestionKind = "infrastructure"
)

// WireWords is the list of SuggestionKind's words, as the generated types give it.
func (SuggestionKind) WireWords() []string {
	return []string{string(SuggestionKindDevice), string(SuggestionKindExternalEndpoint), string(SuggestionKindSite), string(SuggestionKindApplication), string(SuggestionKindDependency), string(SuggestionKindInfrastructure)}
}

// SuggestionStatus is where a suggestion stands.
type SuggestionStatus string

const (
	SuggestionStatusOpen      SuggestionStatus = "open"
	SuggestionStatusAccepted  SuggestionStatus = "accepted"
	SuggestionStatusDismissed SuggestionStatus = "dismissed"
)

// WireWords is the list of SuggestionStatus's words, as the generated types give it.
func (SuggestionStatus) WireWords() []string {
	return []string{string(SuggestionStatusOpen), string(SuggestionStatusAccepted), string(SuggestionStatusDismissed)}
}

// MeshProxy is how a mesh handles a service's traffic.
type MeshProxy string

const (
	MeshProxySidecar MeshProxy = "sidecar"
	MeshProxyAmbient MeshProxy = "ambient"
)

// WireWords is the list of MeshProxy's words, as the generated types give it.
func (MeshProxy) WireWords() []string {
	return []string{string(MeshProxySidecar), string(MeshProxyAmbient)}
}

// IdentityBasis is what a node's identity is made of.
type IdentityBasis string

const (
	IdentityBasisProviderId IdentityBasis = "provider-id"
	IdentityBasisSystemUuid IdentityBasis = "system-uuid"
	IdentityBasisMachineId  IdentityBasis = "machine-id"
	IdentityBasisName       IdentityBasis = "name"
)

// WireWords is the list of IdentityBasis's words, as the generated types give it.
func (IdentityBasis) WireWords() []string {
	return []string{string(IdentityBasisProviderId), string(IdentityBasisSystemUuid), string(IdentityBasisMachineId), string(IdentityBasisName)}
}

// Connectivity is how a node reaches the network.
type Connectivity string

const (
	ConnectivityEthernet  Connectivity = "ethernet"
	ConnectivityWifi      Connectivity = "wifi"
	ConnectivityCellular  Connectivity = "cellular"
	ConnectivitySatellite Connectivity = "satellite"
	ConnectivityLora      Connectivity = "lora"
	ConnectivityZigbee    Connectivity = "zigbee"
	ConnectivityBle       Connectivity = "ble"
	ConnectivitySerial    Connectivity = "serial"
	ConnectivityUnknown   Connectivity = "unknown"
)

// WireWords is the list of Connectivity's words, as the generated types give it.
func (Connectivity) WireWords() []string {
	return []string{string(ConnectivityEthernet), string(ConnectivityWifi), string(ConnectivityCellular), string(ConnectivitySatellite), string(ConnectivityLora), string(ConnectivityZigbee), string(ConnectivityBle), string(ConnectivitySerial), string(ConnectivityUnknown)}
}

// ServiceExposure is how a workload is reachable.
type ServiceExposure string

const (
	ServiceExposureInternal     ServiceExposure = "internal"
	ServiceExposureNodePort     ServiceExposure = "node-port"
	ServiceExposureLoadBalancer ServiceExposure = "load-balancer"
	ServiceExposureIngress      ServiceExposure = "ingress"
)

// WireWords is the list of ServiceExposure's words, as the generated types give it.
func (ServiceExposure) WireWords() []string {
	return []string{string(ServiceExposureInternal), string(ServiceExposureNodePort), string(ServiceExposureLoadBalancer), string(ServiceExposureIngress)}
}
