package server

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"

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
}

// operatorHost is what discovery reports about the cluster an operator runs in, copied out of the hub's state.
type operatorHost struct {
	ClusterID, ClusterName string
	Reachable              []reachableAddr
	PodNodes               []string
	Nodes                  []hostNode
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
				host.Nodes = append(host.Nodes, hostNode{Name: n.Name, Ready: n.Ready, Internal: append([]string(nil), n.InternalIps...), External: append([]string(nil), n.ExternalIps...)})
			}
			return host, true
		}
	}
	return operatorHost{}, false
}

// operatorNamespace is where every install command puts a regional operator.
const operatorNamespace = "continuum-system"

// addressCandidates lists the addresses an operator can be reached at, best guess first: a load balancer's address,
// then a node port on the nodes its pods run on, then external IPs, then node ports on the nodes' external addresses,
// and last the address already recorded. An address found twice is listed once.
func addressCandidates(h operatorHost, recorded string) []AddressCandidate {
	var out []AddressCandidate
	seen := map[string]bool{}
	add := func(kind, addr, source, node string) {
		if addr == "" || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, AddressCandidate{ID: kind + "|" + addr, Address: addr, Kind: kind, Source: source, Node: node, Recorded: addr == recorded})
	}
	hp := func(ip string, port int32) string { return net.JoinHostPort(ip, strconv.Itoa(int(port))) }

	for _, r := range h.Reachable {
		if r.Kind == "load-balancer" && r.IP != "" {
			add(candidateLoadBalancer, hp(r.IP, r.Port), "The address the load balancer gave the operator's Service.", "")
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
	nodeAddrs := func(which string, pick func(hostNode) []string) {
		for _, n := range nodes {
			for _, ip := range pick(n) {
				for _, p := range nodePorts {
					add(candidateNodePort, hp(ip, p), "Node "+n.Name+", "+which+" address, on the node port the operator was installed with.", n.Name)
				}
			}
		}
	}
	nodeAddrs("internal", func(n hostNode) []string { return n.Internal })
	for _, r := range h.Reachable {
		if r.Kind == "external-ip" && r.IP != "" {
			add(candidateExternalIP, hp(r.IP, r.Port), "An external IP set on the operator's Service.", "")
		}
	}
	nodeAddrs("external", func(n hostNode) []string { return n.External })

	if recorded != "" {
		if seen[recorded] {
			for i := range out {
				if out[i].Address == recorded {
					out[i].Recorded = true
				}
			}
		} else {
			add(candidateRecorded, recorded, "Recorded for this operator.", "")
			if n := len(out); n > 0 {
				out[n-1].Recorded = true
			}
		}
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
	writeJSON(w, 200, res)
}
