package estate

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/facts"
	"ikhnos/internal/install"
	"ikhnos/internal/operators"
	"ikhnos/internal/store"
)

// Where a regional operator can be reached from other clusters, worked out from what discovery already knows.
//
// A person used to read an operator's address off the cluster with kubectl and type it in. The agent in the operator's
// own cluster already reports that cluster's nodes and, for each workload, the addresses its Services are reachable on
// (a load balancer's address, an external IP, a node port). The operator is a Deployment named after its id in
// ikhnos-system, so those facts are enough to list the addresses it can be reached at. Nothing here is a claim that
// an address works from anywhere: that is a separate test, made from the sending cluster (see the address check).

// CandidateKind is the kind of address a candidate is.
type CandidateKind string

// The kinds of address a candidate can be, in the order they are offered.
const (
	candidateLoadBalancer CandidateKind = "load-balancer"
	candidateNodePort     CandidateKind = "node-port"
	// candidateNodeServicePort is a node's own address on the Service's own port: open on the node when the load balancer is the
	// node's (see nodeHostedLoadBalancer), and then reached the same way a node port is.
	candidateNodeServicePort CandidateKind = "node-service-port"
	candidateExternalIP      CandidateKind = "external-ip"
	candidateRecorded        CandidateKind = "recorded"
)

// WireWords is the list of CandidateKind's words, as the generated types give it.
func (CandidateKind) WireWords() []string {
	return []string{string(candidateLoadBalancer), string(candidateNodePort), string(candidateNodeServicePort), string(candidateExternalIP), string(candidateRecorded)}
}

// maxNodePortNodes bounds how many nodes are listed for a node port: it is open on every node, and a large cluster
// would otherwise bury the few addresses worth trying.
const maxNodePortNodes = 3

// AddressCandidate is one host:port an operator's receiver may be reached at.
type AddressCandidate struct {
	// ID is stable for the same address and kind, so a client can keep a choice across refreshes.
	ID      string `json:"id"`
	Address string `json:"address"`
	// Kind is load-balancer, node-port, node-service-port, external-ip or recorded (typed in by a person, or set earlier).
	Kind CandidateKind `json:"kind"`
	// Source says in words where the address comes from.
	Source string `json:"source"`
	// Node names the node a node-port address belongs to; absent for the other kinds.
	Node string `json:"node,omitempty"`
	// Network says what sort of network the address is on: mesh (a VPN or overlay such as WireGuard or Tailscale),
	// private (a LAN) or public. Absent when it is not known, as for a name typed in by a person.
	Network AddressNetwork `json:"network,omitempty"`
	// Interface names the node's network interface the address was read from, when the node probe reported it.
	Interface string `json:"interface,omitempty"`
	// Recorded is true for the address currently recorded for the operator.
	Recorded bool `json:"recorded,omitempty"`
}

// AddressCandidates is the answer for one operator.
type AddressCandidates struct {
	// Found says whether discovery has seen the operator's Deployment. When it has not (no agent in its cluster, or
	// not installed yet) the list can hold the recorded address only.
	Found           bool               `json:"found"`
	HostClusterID   string             `json:"hostClusterId,omitempty"`
	HostClusterName string             `json:"hostClusterName,omitempty"`
	Candidates      []AddressCandidate `json:"candidates"`
	// Hints are suggestions in words about getting a better address: turning on the node probe, or using a load
	// balancer provider the cluster already has. Empty when there is nothing to suggest.
	Hints []string `json:"hints,omitempty"`
	// Expose is set when the central operator's gateway cannot be reached from other clusters yet: the Service to change
	// and the commands that change it. Regional operators are exposed by their install command instead.
	Expose *ExposeDoc `json:"expose,omitempty"`
	// ServerCluster is set for the central operator only: whether the server knows which cluster it, and so FUSION's
	// gateway, runs in, and how.
	ServerCluster *ServerClusterDoc `json:"serverCluster,omitempty"`
}

// ServerClusterState is whether the server knows which cluster it runs in.
type ServerClusterState string

// WireWords is the list of ServerClusterState's words, as the generated types give it.
func (ServerClusterState) WireWords() []string {
	return []string{string(serverClusterKnown), string(serverClusterNoAgent), string(serverClusterUnknown)}
}

// ServerClusterSource is how a known cluster is known.
type ServerClusterSource string

// How the server's cluster is known.
const (
	// ServerClusterInstall: the chart passed its identity.
	ServerClusterInstall ServerClusterSource = "install"
	// ServerClusterChosen: a person picked it.
	ServerClusterChosen ServerClusterSource = "chosen"
	// ServerClusterMatched: worked out from what agents report, and a person may correct it.
	ServerClusterMatched ServerClusterSource = "matched"
	// ServerClusterProbe: the server's own node probes reported from it.
	ServerClusterProbe ServerClusterSource = "probe"
)

// WireWords is the list of ServerClusterSource's words, as the generated types give it.
func (ServerClusterSource) WireWords() []string {
	return []string{string(ServerClusterInstall), string(ServerClusterChosen), string(ServerClusterMatched), string(ServerClusterProbe)}
}

// ServerClusterNodes is where the nodes listed for the server's cluster come from.
type ServerClusterNodes string

// Where the server's nodes come from.
const (
	// ServerNodesAgent: an agent registered in the cluster.
	ServerNodesAgent ServerClusterNodes = "agent"
	// ServerNodesProbe: the node probes that ship with the server's chart, which need no agent.
	ServerNodesProbe ServerClusterNodes = "probe"
)

// WireWords is the list of ServerClusterNodes' words, as the generated types give it.
func (ServerClusterNodes) WireWords() []string {
	return []string{string(ServerNodesAgent), string(ServerNodesProbe)}
}

// Server cluster states.
const (
	// serverClusterKnown: the cluster is known and an agent there reports its nodes.
	serverClusterKnown ServerClusterState = "known"
	// serverClusterNoAgent: the cluster is known, but no agent is registered in it, so its nodes cannot be listed.
	serverClusterNoAgent ServerClusterState = "noAgent"
	// serverClusterUnknown: neither the install, a person nor what agents report says which cluster it is.
	serverClusterUnknown ServerClusterState = "unknown"
)

// ServerClusterDoc says which cluster the server runs in and how that is known.
type ServerClusterDoc struct {
	State ServerClusterState `json:"state"`
	// Source is how a known cluster is known: install (the chart passed its identity), chosen (a person picked it), matched
	// (worked out from what agents report, and a person may correct it) or probe (the server's own node probes reported from it).
	Source    ServerClusterSource `json:"source,omitempty"`
	ClusterID string              `json:"clusterId,omitempty"`
	// Options are the registered clusters to choose from (to set the cluster, or to change it).
	Options []ClusterOption `json:"options,omitempty"`
	// Nodes says where the nodes listed come from: agent (an agent registered in the cluster) or probe (the node probes
	// that ship with the server's chart, which need no agent). Empty when no nodes are listed.
	Nodes ServerClusterNodes `json:"nodes,omitempty"`
}

// ExposeDoc names the gateway's Service and the kubectl command that makes it reachable from other clusters.
type ExposeDoc struct {
	Service   string `json:"service"`
	Namespace string `json:"namespace"`
	// Commands change the Service's type in place: no pod restarts and its cluster address stays. A later helm upgrade puts
	// the type back to the chart's value unless fusion.central.service.type is set to match.
	Commands struct {
		NodePort     string `json:"nodePort"`
		LoadBalancer string `json:"loadBalancer"`
	} `json:"commands"`
	// Value is the chart value that keeps the change across upgrades.
	Value string `json:"value"`
}

// ExposeCommand is the kubectl line that gives a Service the type.
func ExposeCommand(namespace, service string, typ install.ServiceType) (string, error) {
	return install.SetServiceType{Namespace: namespace, Service: service, Type: typ}.Render()
}

// operatorHost is what discovery reports about the cluster an operator runs in, copied out of the hub's state.
type operatorHost struct {
	ClusterID, ClusterName string
	Reachable              []reachableAddr
	PodNodes               []string
	Nodes                  []HostNode
	// Providers names the load balancer or mesh providers the cluster runs ("metallb", "kube-vip", "tailscale").
	Providers []string
}

type reachableAddr struct {
	IP, Kind string
	Port     int32
}

type HostNode struct {
	Name     string
	Ready    bool
	Internal []string
	External []string
	// Interfaces are the addresses on the node's own network interfaces, as its node probe reports them.
	Interfaces []HostAddr
	PodCIDRs   []string
}

// HostAddr is one address read from a node's network interface.
type HostAddr struct {
	Interface, Kind, IP string
}

// hubHosts finds the clusters of the approved agents, from what the hub holds of them.
type hubHosts struct{ *Hub }

// operatorHost finds the cluster the operator with this id runs in. The second result is false when no approved
// agent has reported its Deployment.
func (h hubHosts) operatorHost(ctx context.Context, operatorID string) (operatorHost, bool) {
	return h.workloadHost(ctx, operatorNamespace, operators.OperatorServiceName(operatorID))
}

// workloadHost finds the cluster a Deployment runs in, from what the approved agents report about their clusters.
// The regional operators are all in ikhnos-system under a name worked out from their id; the central operator's
// gateway is the server's own install, in the server's namespace under FUSION's name.
func (h hubHosts) workloadHost(ctx context.Context, namespace, want string) (operatorHost, bool) {
	return h.hostWhere(ctx, func(w *ikhnosv1.WorkloadFacts) bool { return w.Namespace == namespace && w.Name == want })
}

// namespaceHost finds a cluster by something else running in a namespace, for when the Deployment itself is not among
// what an agent reports (the agent is there, but has not been told to read that Deployment or its Service). It says which
// cluster, and so which nodes; it says nothing about the Deployment's Service, which the caller must get another way.
func (h hubHosts) namespaceHost(ctx context.Context, namespace string) (operatorHost, bool) {
	host, ok := h.hostWhere(ctx, func(w *ikhnosv1.WorkloadFacts) bool { return w.Namespace == namespace })
	host.Reachable, host.PodNodes = nil, nil
	return host, ok
}

func (h hubHosts) hostWhere(ctx context.Context, match func(*ikhnosv1.WorkloadFacts) bool) (operatorHost, bool) {
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return operatorHost{}, false
	}
	var out operatorHost
	found := false
	h.ReadStates(func(states map[string]*facts.State) {
		for _, a := range agentList {
			st := states[a.ID]
			if a.Status != store.StatusApproved || st == nil {
				continue
			}
			for _, w := range st.Workloads {
				if match(w) {
					out, found = clusterHost(a, st, w), true
					return
				}
			}
		}
	})
	return out, found
}

// clusterHost finds a cluster by its id. It is found whenever an approved agent is registered there, even one that has
// reported nothing yet (then it has no nodes to list). A workload that match accepts, if the agent reports one, says which
// node the pod is on.
func (h hubHosts) clusterHost(ctx context.Context, clusterID string, match func(*ikhnosv1.WorkloadFacts) bool) (operatorHost, bool) {
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return operatorHost{}, false
	}
	var out operatorHost
	found := false
	h.ReadStates(func(states map[string]*facts.State) {
		for _, a := range agentList {
			if a.Status != store.StatusApproved || a.ClusterID != clusterID {
				continue
			}
			st := states[a.ID]
			if st == nil {
				if !found {
					out, found = operatorHost{ClusterID: a.ClusterID, ClusterName: a.Name}, true
				}
				continue
			}
			var wl *ikhnosv1.WorkloadFacts
			for _, w := range st.Workloads {
				if match != nil && match(w) {
					wl = w
					break
				}
			}
			out, found = clusterHost(a, st, wl), true
			return
		}
	})
	return out, found
}

// ClusterOption is a cluster an agent is registered in, for choosing among.
type ClusterOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// clusterOptions lists the clusters approved agents are registered in, one entry per cluster, by name.
func (h hubHosts) clusterOptions(ctx context.Context) []ClusterOption {
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []ClusterOption{}
	for _, a := range agentList {
		if a.Status != store.StatusApproved || a.ClusterID == "" || seen[a.ClusterID] {
			continue
		}
		seen[a.ClusterID] = true
		out = append(out, ClusterOption{ID: a.ClusterID, Name: a.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// nodeHost finds a cluster by an address one of its nodes has: the one Kubernetes gives it or one on its own interfaces.
// It is the way to the right cluster when no agent reports anything of the workload's namespace but the Service's load
// balancer address is a node's own (k3s's built-in load balancer, or MetalLB announcing on a node). Like namespaceHost it
// says which cluster and so which nodes, and nothing about the Deployment's Service. An address that no node has finds nothing.
func (h hubHosts) nodeHost(ctx context.Context, ip string) (operatorHost, bool) {
	want, err := netip.ParseAddr(ip)
	if err != nil {
		return operatorHost{}, false
	}
	has := func(list []string) bool {
		for _, x := range list {
			if a, err := netip.ParseAddr(x); err == nil && a == want {
				return true
			}
		}
		return false
	}
	agentList, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return operatorHost{}, false
	}
	var out operatorHost
	found := false
	h.ReadStates(func(states map[string]*facts.State) {
		for _, a := range agentList {
			st := states[a.ID]
			if a.Status != store.StatusApproved || st == nil {
				continue
			}
			for _, n := range st.Nodes {
				owns := has(n.InternalIps) || has(n.ExternalIps)
				if !owns && n.Probe != nil {
					for _, ha := range n.Probe.HostAddresses {
						if p, err := netip.ParsePrefix(ha.Address); err == nil && p.Addr() == want {
							owns = true
						}
					}
				}
				if owns {
					out, found = clusterHost(a, st, nil), true
					out.Reachable, out.PodNodes = nil, nil
					return
				}
			}
		}
	})
	return out, found
}

// clusterHost describes the cluster of an agent's view, with what the agent says about the workload w (nil when it is not
// known). The caller holds the hub's lock.
func clusterHost(a store.Agent, st *facts.State, w *ikhnosv1.WorkloadFacts) operatorHost {
	host := operatorHost{ClusterID: a.ClusterID, ClusterName: a.Name}
	if w != nil {
		host.PodNodes = append([]string(nil), w.NodeNames...)
		for _, r := range w.Reachable {
			host.Reachable = append(host.Reachable, reachableAddr{IP: r.Ip, Port: r.Port, Kind: r.Kind})
		}
	}
	for _, n := range st.Nodes {
		hn := HostNode{Name: n.Name, Ready: n.Ready, Internal: append([]string(nil), n.InternalIps...), External: append([]string(nil), n.ExternalIps...), PodCIDRs: append([]string(nil), n.PodCidrs...)}
		if n.Probe != nil {
			for _, ha := range n.Probe.HostAddresses {
				if len(hn.Interfaces) >= 32 { // the probe sends at most 16; a larger list is not believed
					break
				}
				if p, err := netip.ParsePrefix(ha.Address); err == nil {
					hn.Interfaces = append(hn.Interfaces, HostAddr{Interface: ha.Interface, Kind: ha.Kind, IP: p.Addr().String()})
				}
			}
		}
		host.Nodes = append(host.Nodes, hn)
	}
	host.Providers = providersIn(st.Workloads)
	return host
}

// AddressNetwork is what sort of network an address is on.
type AddressNetwork string

// WireWords is the list of AddressNetwork's words, as the generated types give it.
func (AddressNetwork) WireWords() []string {
	return []string{string(networkMesh), string(networkPrivate), string(networkPublic)}
}

// Network classes an address can have.
const (
	networkMesh    AddressNetwork = "mesh"
	networkPrivate AddressNetwork = "private"
	networkPublic  AddressNetwork = "public"
)

// meshNames are the interface names the common mesh VPNs give their links; the link driver (WireGuard, tun) and the
// carrier-grade range 100.64.0.0/10 that Tailscale and Netbird hand out are the other two signs.
var meshNames = []string{"tailscale", "wt", "wg", "nebula", "zt", "netbird"}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// classifyNetwork says whether an address on an interface is a mesh, a private or a public one. Mesh is judged first,
// because a mesh address is usually also in a private range and the difference is what a person choosing needs.
func classifyNetwork(iface, kind, ip string) AddressNetwork {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	if kind == "wireguard" || kind == "tun" || cgnat.Contains(a) {
		return networkMesh
	}
	for _, p := range meshNames {
		if strings.HasPrefix(strings.ToLower(iface), p) {
			return networkMesh
		}
	}
	if a.IsPrivate() {
		return networkPrivate
	}
	return networkPublic
}

// providersIn finds the load balancer and mesh providers among a cluster's reported workloads.
func providersIn(ws map[string]*ikhnosv1.WorkloadFacts) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, w := range ws {
		switch {
		case w.Namespace == "metallb-system":
			add("metallb")
		case strings.HasPrefix(w.Name, "kube-vip"):
			add("kube-vip")
		case w.Namespace == "tailscale":
			add("tailscale")
		}
	}
	sort.Strings(out)
	return out
}

// providerHint says in a sentence how to use a provider the cluster already has.
var providerHint = map[string]string{
	"metallb":   "MetalLB runs in this cluster: install the operator with service.type=LoadBalancer and it gets an address of its own that stays put when a node goes away.",
	"kube-vip":  "kube-vip runs in this cluster: install the operator with service.type=LoadBalancer and it gets an address of its own that stays put when a node goes away.",
	"tailscale": "The Tailscale operator runs in this cluster: exposing the operator's Service through it gives it a mesh address of its own, whichever node the pod is on.",
}

// operatorNamespace is where every install command puts a regional operator.
const operatorNamespace = "ikhnos-system"

// addressCandidates lists the addresses an operator can be reached at, best guess first: a load balancer's address,
// then a node port on the nodes its pods run on, then external IPs, then node ports on the nodes' external addresses,
// and last the address already recorded. An address found twice is listed once.
func addressCandidates(h operatorHost, recorded string) []AddressCandidate {
	var out []AddressCandidate
	seen := map[string]bool{}
	add := func(c AddressCandidate) {
		if c.Address == "" || seen[c.Address] {
			return
		}
		seen[c.Address] = true
		c.ID = string(c.Kind) + "|" + c.Address
		c.Recorded = c.Address == recorded
		out = append(out, c)
	}
	hp := func(ip string, port int32) string { return net.JoinHostPort(ip, strconv.Itoa(int(port))) }

	for _, r := range h.Reachable {
		if r.Kind == "load-balancer" && r.IP != "" {
			add(AddressCandidate{Kind: candidateLoadBalancer, Address: hp(r.IP, r.Port), Source: "The address the load balancer gave the operator's Service.", Network: classifyNetwork("", "", r.IP)})
		}
	}

	// A node port is open on every node. So is the Service's own port when the load balancer is the node itself (k3s's built-in one
	// listens on the Service port on its node, and says so by giving the Service the node's address): the evidence is that
	// address being one of the cluster's nodes' own, never a guess from the cluster's name. Ready nodes only, those running the
	// operator's pod first.
	type nodePortSpec struct {
		port int32
		kind CandidateKind
		what string
	}
	var ports []nodePortSpec
	for _, r := range h.Reachable {
		if r.Kind == "node-port" {
			ports = append(ports, nodePortSpec{r.Port, candidateNodePort, "on the Service's node port."})
		}
	}
	for _, r := range h.Reachable {
		if r.Kind == "load-balancer" && r.IP != "" && r.Port != 0 && h.nodeOwns(r.IP) {
			ports = append(ports, nodePortSpec{r.Port, candidateNodeServicePort, "on the Service's own port: its load balancer address is a node's own, so the node answers on that port too."})
		}
	}
	var nodes []HostNode
	if len(ports) > 0 {
		onPod := map[string]bool{}
		for _, n := range h.PodNodes {
			onPod[n] = true
		}
		for _, n := range h.Nodes {
			if n.Ready {
				nodes = append(nodes, n)
			}
		}
		sort.SliceStable(nodes, func(i, j int) bool {
			if onPod[nodes[i].Name] != onPod[nodes[j].Name] {
				return onPod[nodes[i].Name]
			}
			return nodes[i].Name < nodes[j].Name
		})
		// With the pod's node unknown there is no telling which of the nodes it is, so more of them are listed.
		limit := maxNodePortNodes
		if len(h.PodNodes) == 0 {
			limit = 2 * maxNodePortNodes
		}
		if len(nodes) > limit {
			nodes = nodes[:limit]
		}
	}
	nodeAddrs := func(pick func(HostNode) []nodeAddr) {
		for _, n := range nodes {
			for _, na := range pick(n) {
				for _, p := range ports {
					if len(out) >= maxCandidates {
						return
					}
					add(AddressCandidate{Kind: p.kind, Address: hp(na.ip, p.port), Source: "Node " + n.Name + ", " + na.what + ", " + p.what, Node: n.Name, Network: na.network, Interface: na.iface})
				}
			}
		}
	}
	// What Kubernetes calls the node's internal address, then what the node's own interfaces add on a LAN or a mesh.
	nodeAddrs(func(n HostNode) []nodeAddr { return n.addresses(true, false) })
	for _, r := range h.Reachable {
		if r.Kind == "external-ip" && r.IP != "" {
			add(AddressCandidate{Kind: candidateExternalIP, Address: hp(r.IP, r.Port), Source: "An external IP set on the operator's Service.", Network: classifyNetwork("", "", r.IP)})
		}
	}
	// Last, the public ones: what Kubernetes calls the external address and any public address on an interface.
	nodeAddrs(func(n HostNode) []nodeAddr { return n.addresses(false, true) })

	if recorded != "" {
		if seen[recorded] {
			for i := range out {
				if out[i].Address == recorded {
					out[i].Recorded = true
				}
			}
		} else {
			add(AddressCandidate{Kind: candidateRecorded, Address: recorded, Source: "Recorded for this operator."})
			if n := len(out); n > 0 {
				out[n-1].Recorded = true
			}
		}
	}
	return out
}

// nodeOwns says whether ip is an address of one of the cluster's nodes: the one Kubernetes gives it or one on its own interfaces.
func (h operatorHost) nodeOwns(ip string) bool {
	want, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	is := func(x string) bool { a, err := netip.ParseAddr(x); return err == nil && a == want }
	for _, n := range h.Nodes {
		for _, list := range [][]string{n.Internal, n.External} {
			for _, x := range list {
				if is(x) {
					return true
				}
			}
		}
		for _, a := range n.Interfaces {
			if is(a.IP) {
				return true
			}
		}
	}
	return false
}

// maxCandidates bounds the node-port addresses listed in all, so a node with many interfaces cannot bury the rest.
const maxCandidates = 16

// nodeAddr is one address a node can be dialled on.
type nodeAddr struct {
	ip, what, iface string
	network         AddressNetwork
}

// addresses lists a node's addresses in the order they are offered: its internal address first, then those on its
// own interfaces by how local they are (a LAN, then a mesh), and last the public ones. near picks the first group
// and far the second, so the two calls together list each address once. Addresses that mean something only inside the
// cluster (a pod range's) are left out, as is an address listed under two names.
func (n HostNode) addresses(near, far bool) []nodeAddr {
	var pods []netip.Prefix
	for _, c := range n.PodCIDRs {
		if p, err := netip.ParsePrefix(c); err == nil {
			pods = append(pods, p)
		}
	}
	inPods := func(ip string) bool {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			return false
		}
		for _, p := range pods {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	var internal, local, mesh, public []nodeAddr
	seen := map[string]bool{}
	take := func(list *[]nodeAddr, ip, iface, kind, what string) {
		if ip == "" || seen[ip] || inPods(ip) {
			return
		}
		seen[ip] = true
		*list = append(*list, nodeAddr{ip: ip, what: what, network: classifyNetwork(iface, kind, ip), iface: iface})
	}
	for _, ip := range n.Internal {
		take(&internal, ip, "", "", "internal address")
	}
	for _, a := range n.Interfaces {
		switch classifyNetwork(a.Interface, a.Kind, a.IP) {
		case networkMesh:
			take(&mesh, a.IP, a.Interface, a.Kind, "mesh address on "+a.Interface)
		case networkPublic:
			take(&public, a.IP, a.Interface, a.Kind, "public address on "+a.Interface)
		default:
			take(&local, a.IP, a.Interface, a.Kind, "address on "+a.Interface)
		}
	}
	var external []nodeAddr
	for _, ip := range n.External {
		take(&external, ip, "", "", "external address")
	}
	// A person picks among these: the IPv4 address of a network before its IPv6 ones.
	for _, l := range [][]nodeAddr{internal, local, mesh, public, external} {
		sort.SliceStable(l, func(i, j int) bool { return !strings.Contains(l[i].ip, ":") && strings.Contains(l[j].ip, ":") })
	}
	var out []nodeAddr
	if near {
		out = append(out, internal...)
		out = append(out, local...)
		out = append(out, mesh...)
	}
	if far {
		out = append(out, external...)
		out = append(out, public...)
	}
	return out
}

// hints suggests, in words, how to get a better address than the ones listed: turn on the node probe when no node
// reports its interfaces, or use the load balancer provider the cluster already runs when no load balancer address
// exists. Nothing is said when there is nothing to improve.
func (h operatorHost) hints(candidates []AddressCandidate) []string {
	var out []string
	hasLB := false
	for _, c := range candidates {
		if c.Kind == candidateLoadBalancer {
			hasLB = true
		}
	}
	if !hasLB {
		for _, p := range h.Providers {
			if t := providerHint[p]; t != "" {
				out = append(out, t)
			}
		}
	}
	hasNodePort := false
	for _, c := range candidates {
		if c.Kind == candidateNodePort || c.Kind == candidateNodeServicePort {
			hasNodePort = true
		}
	}
	probed := false
	for _, n := range h.Nodes {
		if len(n.Interfaces) > 0 {
			probed = true
		}
	}
	if hasNodePort && len(h.Nodes) > 0 && !probed {
		out = append(out, "No node reports its network interfaces, so only the addresses Kubernetes knows are listed. Turn on the node probe in the agent to also offer each node's mesh and other addresses.")
	}
	// An address outside the cluster is listed but no node port: the nodes' own addresses have nothing to be dialled on.
	if !hasNodePort && len(h.Nodes) > 0 {
		for _, c := range candidates {
			if c.Kind == candidateLoadBalancer || c.Kind == candidateExternalIP {
				out = append(out, "The Service has no node port (a load balancer Service installed without one), so the nodes' own addresses, a mesh one included, are not offered. Allow node ports on the Service to have them listed.")
				break
			}
		}
	}
	// The probe is on and no node has a mesh interface: say what it did see, so a missing mesh address can be told from a missing probe.
	if hasNodePort && probed {
		var seen []string
		mesh := false
		for _, n := range h.Nodes {
			for _, a := range n.Interfaces {
				if classifyNetwork(a.Interface, a.Kind, a.IP) == networkMesh {
					mesh = true
				}
				if len(seen) < 6 {
					seen = append(seen, n.Name+": "+a.Interface+" "+a.IP)
				}
			}
		}
		if !mesh {
			out = append(out, "No node reports a mesh interface (WireGuard, Tailscale, Netbird and the like), so no mesh address is offered. The nodes report: "+strings.Join(seen, "; ")+". A mesh that runs on a machine outside the cluster's nodes is not seen from here: type its address under Another address.")
		}
	}
	return out
}

// noHostHint says why the nodes' own addresses are missing when no agent has reported where the Deployment runs.
func noHostHint(central bool) string {
	if central {
		return "The server does not know which cluster FUSION's gateway runs in, or that cluster has no agent, so the nodes' own addresses (a mesh one, say) cannot be listed; only the load balancer's is known, from the Service. Connect the server's cluster with the node probe on to have them offered."
	}
	return "No agent has reported where this operator runs, so the nodes' own addresses (a mesh one, say) cannot be listed. Run an agent with the node probe on in its cluster to have them offered."
}

// exposureHint says why nothing is listed when no address outside the cluster is known, and what to change. A node's own
// addresses are offered only with a port to dial them on, so a Service that is only reachable inside its cluster has none.
func exposureHint(candidates []AddressCandidate, found, central bool) string {
	for _, c := range candidates {
		switch c.Kind {
		case candidateLoadBalancer, candidateNodePort, candidateNodeServicePort, candidateExternalIP:
			return ""
		}
	}
	switch {
	case central && found:
		return "FUSION's gateway is only reachable inside its own cluster, so there is no node or load balancer address to offer yet. Expose it with fusion.central.service.type=NodePort (or LoadBalancer) in the server chart; the node addresses, mesh ones included, then appear here."
	case central:
		return "Nothing outside the server's cluster is known about FUSION's gateway. Expose it with fusion.central.service.type=NodePort (or LoadBalancer) in the server chart. Its node addresses are listed once an agent in the server's cluster reports it."
	case found:
		return "This operator's Service is only reachable inside its own cluster, so there is no node or load balancer address to offer yet. Install it with a node port or a load balancer; the node addresses, mesh ones included, then appear here."
	}
	return "No agent has reported this operator in a cluster yet. Once one does, the addresses it can be reached at appear here."
}
