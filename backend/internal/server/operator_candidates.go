package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/store"
)

// Where a regional operator can be reached from other clusters, worked out from what discovery already knows.
//
// A person used to read an operator's address off the cluster with kubectl and type it in. The agent in the operator's
// own cluster already reports that cluster's nodes and, for each workload, the addresses its Services are reachable on
// (a load balancer's address, an external IP, a node port). The operator is a Deployment named after its id in
// continuum-system, so those facts are enough to list the addresses it can be reached at. Nothing here is a claim that
// an address works from anywhere: that is a separate test, made from the sending cluster (see the address check).

// The kinds of address a candidate can be, in the order they are offered.
const (
	candidateLoadBalancer = "load-balancer"
	candidateNodePort     = "node-port"
	candidateExternalIP   = "external-ip"
	candidateRecorded     = "recorded"
)

// maxNodePortNodes bounds how many nodes are listed for a node port: it is open on every node, and a large cluster
// would otherwise bury the few addresses worth trying.
const maxNodePortNodes = 3

// AddressCandidate is one host:port an operator's receiver may be reached at.
type AddressCandidate struct {
	// ID is stable for the same address and kind, so a client can keep a choice across refreshes.
	ID      string `json:"id"`
	Address string `json:"address"`
	// Kind is load-balancer, node-port, external-ip or recorded (typed in by a person, or set earlier).
	Kind string `json:"kind"`
	// Source says in words where the address comes from.
	Source string `json:"source"`
	// Node names the node a node-port address belongs to; absent for the other kinds.
	Node string `json:"node,omitempty"`
	// Network says what sort of network the address is on: mesh (a VPN or overlay such as WireGuard or Tailscale),
	// private (a LAN) or public. Absent when it is not known, as for a name typed in by a person.
	Network string `json:"network,omitempty"`
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
}

// operatorHost is what discovery reports about the cluster an operator runs in, copied out of the hub's state.
type operatorHost struct {
	ClusterID, ClusterName string
	Reachable              []reachableAddr
	PodNodes               []string
	Nodes                  []hostNode
	// Providers names the load balancer or mesh providers the cluster runs ("metallb", "kube-vip", "tailscale").
	Providers []string
}

type reachableAddr struct {
	IP, Kind string
	Port     int32
}

type hostNode struct {
	Name     string
	Ready    bool
	Internal []string
	External []string
	// Interfaces are the addresses on the node's own network interfaces, as its node probe reports them.
	Interfaces []hostAddr
	PodCIDRs   []string
}

// hostAddr is one address read from a node's network interface.
type hostAddr struct {
	Interface, Kind, IP string
}

// operatorHost finds the cluster the operator with this id runs in. The second result is false when no approved
// agent has reported its Deployment.
func (h *Hub) operatorHost(ctx context.Context, operatorID string) (operatorHost, bool) {
	return h.workloadHost(ctx, operatorNamespace, operatorServiceName(operatorID))
}

// workloadHost finds the cluster a Deployment runs in, from what the approved agents report about their clusters.
// The regional operators are all in continuum-system under a name worked out from their id; the central operator's
// gateway is the server's own install, in the server's namespace under FUSION's name.
func (h *Hub) workloadHost(ctx context.Context, namespace, want string) (operatorHost, bool) {
	agents, err := h.C.Store.ListAgents(ctx, h.C.OrgID)
	if err != nil {
		return operatorHost{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range agents {
		v := h.views[a.ID]
		if a.Status != store.StatusApproved || v == nil || v.state == nil {
			continue
		}
		for _, w := range v.state.Workloads {
			if w.Namespace != namespace || w.Name != want {
				continue
			}
			host := operatorHost{ClusterID: a.ClusterID, ClusterName: a.Name, PodNodes: append([]string(nil), w.NodeNames...)}
			for _, r := range w.Reachable {
				host.Reachable = append(host.Reachable, reachableAddr{IP: r.Ip, Port: r.Port, Kind: r.Kind})
			}
			for _, n := range v.state.Nodes {
				hn := hostNode{Name: n.Name, Ready: n.Ready, Internal: append([]string(nil), n.InternalIps...), External: append([]string(nil), n.ExternalIps...), PodCIDRs: append([]string(nil), n.PodCidrs...)}
				if n.Probe != nil {
					for _, a := range n.Probe.HostAddresses {
						if len(hn.Interfaces) >= 32 { // the probe sends at most 16; a larger list is not believed
							break
						}
						if p, err := netip.ParsePrefix(a.Address); err == nil {
							hn.Interfaces = append(hn.Interfaces, hostAddr{Interface: a.Interface, Kind: a.Kind, IP: p.Addr().String()})
						}
					}
				}
				host.Nodes = append(host.Nodes, hn)
			}
			host.Providers = providersIn(v.state.Workloads)
			return host, true
		}
	}
	return operatorHost{}, false
}

// Network classes an address can have.
const (
	networkMesh    = "mesh"
	networkPrivate = "private"
	networkPublic  = "public"
)

// meshNames are the interface names the common mesh VPNs give their links; the link driver (WireGuard, tun) and the
// carrier-grade range 100.64.0.0/10 that Tailscale and Netbird hand out are the other two signs.
var meshNames = []string{"tailscale", "wt", "wg", "nebula", "zt", "netbird"}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// classifyNetwork says whether an address on an interface is a mesh, a private or a public one. Mesh is judged first,
// because a mesh address is usually also in a private range and the difference is what a person choosing needs.
func classifyNetwork(iface, kind, ip string) string {
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
func providersIn(ws map[string]*continuumv1.WorkloadFacts) []string {
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
const operatorNamespace = "continuum-system"

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
		c.ID = c.Kind + "|" + c.Address
		c.Recorded = c.Address == recorded
		out = append(out, c)
	}
	hp := func(ip string, port int32) string { return net.JoinHostPort(ip, strconv.Itoa(int(port))) }

	for _, r := range h.Reachable {
		if r.Kind == "load-balancer" && r.IP != "" {
			add(AddressCandidate{Kind: candidateLoadBalancer, Address: hp(r.IP, r.Port), Source: "The address the load balancer gave the operator's Service.", Network: classifyNetwork("", "", r.IP)})
		}
	}

	// A node port is open on every node. Ready nodes only, those running the operator's pod first.
	var nodePorts []int32
	for _, r := range h.Reachable {
		if r.Kind == "node-port" {
			nodePorts = append(nodePorts, r.Port)
		}
	}
	var nodes []hostNode
	if len(nodePorts) > 0 {
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
		if len(nodes) > maxNodePortNodes {
			nodes = nodes[:maxNodePortNodes]
		}
	}
	nodeAddrs := func(pick func(hostNode) []nodeAddr) {
		for _, n := range nodes {
			for _, na := range pick(n) {
				for _, p := range nodePorts {
					if len(out) >= maxCandidates {
						return
					}
					add(AddressCandidate{Kind: candidateNodePort, Address: hp(na.ip, p), Source: "Node " + n.Name + ", " + na.what + ", on the node port the operator was installed with.", Node: n.Name, Network: na.network, Interface: na.iface})
				}
			}
		}
	}
	// What Kubernetes calls the node's internal address, then what the node's own interfaces add on a LAN or a mesh.
	nodeAddrs(func(n hostNode) []nodeAddr { return n.addresses(true, false) })
	for _, r := range h.Reachable {
		if r.Kind == "external-ip" && r.IP != "" {
			add(AddressCandidate{Kind: candidateExternalIP, Address: hp(r.IP, r.Port), Source: "An external IP set on the operator's Service.", Network: classifyNetwork("", "", r.IP)})
		}
	}
	// Last, the public ones: what Kubernetes calls the external address and any public address on an interface.
	nodeAddrs(func(n hostNode) []nodeAddr { return n.addresses(false, true) })

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

// maxCandidates bounds the node-port addresses listed in all, so a node with many interfaces cannot bury the rest.
const maxCandidates = 12

// nodeAddr is one address a node can be dialled on.
type nodeAddr struct {
	ip, what, network, iface string
}

// addresses lists a node's addresses in the order they are offered: its internal address first, then those on its
// own interfaces by how local they are (a LAN, then a mesh), and last the public ones. near picks the first group
// and far the second, so the two calls together list each address once. Addresses that mean something only inside the
// cluster (a pod range's) are left out, as is an address listed under two names.
func (n hostNode) addresses(near, far bool) []nodeAddr {
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
	probed := false
	for _, n := range h.Nodes {
		if len(n.Interfaces) > 0 {
			probed = true
		}
	}
	if len(h.Nodes) > 0 && !probed {
		out = append(out, "No node reports its network interfaces, so only the addresses Kubernetes knows are listed. Turn on the node probe in the agent to also offer each node's mesh and other addresses.")
	}
	return out
}

// listOperatorAddressCandidates answers GET .../operators/{id}/address-candidates.
func (a *Admin) listOperatorAddressCandidates(w http.ResponseWriter, r *http.Request) {
	c := a.core(r)
	op, err := c.GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	res := AddressCandidates{Candidates: []AddressCandidate{}}
	namespace, workload, recorded := operatorNamespace, operatorServiceName(op.ID), op.Address
	var gateway *KubeService
	if op.ID == CentralOperatorID && a.Fusion != nil {
		// The gateway is the server's own install: its Deployment is in the server's namespace, and the server reads its
		// Service itself, which is how a load balancer's address is found even where no agent runs in that cluster.
		namespace, workload = a.Fusion.Namespace, a.Fusion.ServiceName()
		if a.Fusion.Exposed() {
			recorded = a.Fusion.CentralEndpoint()
		}
		gateway = a.Fusion.gatewayService(r.Context())
	}
	host, found := a.tn(r).Hub.workloadHost(r.Context(), namespace, workload)
	if found {
		res.Found, res.HostClusterID, res.HostClusterName = true, host.ClusterID, host.ClusterName
	}
	if gateway != nil && gateway.LoadBalancer != "" {
		if h, p, err := net.SplitHostPort(gateway.LoadBalancer); err == nil {
			if port, err := strconv.Atoi(p); err == nil {
				host.Reachable = append(host.Reachable, reachableAddr{IP: h, Port: int32(port), Kind: "load-balancer"})
				res.Found = true
			}
		}
	}
	if list := addressCandidates(host, recorded); list != nil {
		res.Candidates = list
	}
	if res.Found {
		res.Hints = host.hints(res.Candidates)
	}
	writeJSON(w, 200, res)
}
