package estate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/httpkit"
	"continuum/internal/install"
	"continuum/internal/kernel"
	"continuum/internal/operators"
)

// serverClusterHost finds the cluster FUSION's gateway runs in, and says how it is known. A cluster a person chose, or the
// install named, is the answer even when no agent is registered there (then the state is noAgent, and there are no nodes to
// list). Failing both, it is worked out from what agents report: a workload of the server's namespace, then a node that has
// the load balancer's address; and when that finds nothing the state is unknown, with the registered clusters to choose from.
func (h *API) serverClusterHost(ctx context.Context, hub *Hub, cen operators.Central, namespace, workload string, gateway *kernel.KubeService) (operatorHost, bool, *ServerClusterDoc) {
	host, ok, doc := h.serverClusterFromAgents(ctx, hub, cen, namespace, workload, gateway)
	return h.withSelfProbe(host, ok, doc)
}

// serverClusterFromAgents is serverClusterHost before the server's own node probes are looked at: what the install, a
// person's choice and the registered agents say.
func (h *API) serverClusterFromAgents(ctx context.Context, hub *Hub, cen operators.Central, namespace, workload string, gateway *kernel.KubeService) (operatorHost, bool, *ServerClusterDoc) {
	hosts := hubHosts{hub}
	gatewayWorkload := func(w *continuumv1.WorkloadFacts) bool { return w.Namespace == namespace && w.Name == workload }
	id, source := hub.C.Settings().ServerClusterID, ServerClusterChosen
	if id == "" {
		id, source = "", ServerClusterInstall
		if cid, ok := kernel.ClusterIDForUID(hub.C.OrgID, cen.ClusterUID()); ok {
			id = cid
		}
	}
	if id != "" {
		if host, ok := hosts.clusterHost(ctx, id, gatewayWorkload); ok {
			return host, true, &ServerClusterDoc{State: serverClusterKnown, Source: source, ClusterID: id, Options: hosts.clusterOptions(ctx)}
		}
		return operatorHost{}, false, &ServerClusterDoc{State: serverClusterNoAgent, Source: source, ClusterID: id, Options: hosts.clusterOptions(ctx)}
	}
	host, ok := hosts.workloadHost(ctx, namespace, workload)
	if !ok {
		host, ok = hosts.namespaceHost(ctx, namespace)
	}
	if !ok && gateway != nil && gateway.LoadBalancer != "" {
		// No agent reports anything of that namespace. A load balancer address that is one of a cluster's nodes' own
		// (k3s's built-in load balancer, MetalLB on a node) still says which cluster the gateway is in.
		if lbHost, _, err := net.SplitHostPort(gateway.LoadBalancer); err == nil {
			host, ok = hosts.nodeHost(ctx, lbHost)
		}
	}
	if ok {
		return host, true, &ServerClusterDoc{State: serverClusterKnown, Source: ServerClusterMatched, ClusterID: host.ClusterID, Options: hosts.clusterOptions(ctx)}
	}
	return operatorHost{}, false, &ServerClusterDoc{State: serverClusterUnknown, Options: hosts.clusterOptions(ctx)}
}

// withSelfProbe adds what the server's own node probes report to what is known of the server's cluster.
//
//   - An agent there already lists the nodes: a node that agent has no interface addresses for takes the probe's. The
//     agent's own are never replaced.
//   - No agent is registered there, or the cluster is not known at all: the probes are the nodes. They run in the server's
//     own cluster by construction, so this settles which cluster that is even when nothing else did.
func (h *API) withSelfProbe(host operatorHost, found bool, doc *ServerClusterDoc) (operatorHost, bool, *ServerClusterDoc) {
	nodes := h.serverNodes()
	if len(nodes) == 0 {
		return host, found, doc
	}
	if found {
		byName := map[string]HostNode{}
		for _, n := range nodes {
			byName[n.Name] = n
		}
		filled := append([]HostNode(nil), host.Nodes...)
		for i, n := range filled {
			if len(n.Interfaces) == 0 {
				filled[i].Interfaces = byName[n.Name].Interfaces
			}
		}
		host.Nodes = filled
		doc.Nodes = ServerNodesAgent
		return host, true, doc
	}
	host = operatorHost{ClusterID: doc.ClusterID, ClusterName: "This server's cluster", Nodes: nodes}
	for _, o := range doc.Options {
		if o.ID == doc.ClusterID && o.Name != "" {
			host.ClusterName = o.Name
		}
	}
	doc.State, doc.Nodes = serverClusterKnown, ServerNodesProbe
	if doc.Source == "" {
		doc.Source = ServerClusterProbe
	}
	return host, true, doc
}

// listOperatorAddressCandidates answers GET .../operators/{id}/address-candidates.
func (h *API) listOperatorAddressCandidates(w http.ResponseWriter, r *http.Request) {
	hub := h.For(r)
	op, err := h.Operators(r).GetOperator(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res := AddressCandidates{Candidates: []AddressCandidate{}}
	namespace, workload, recorded := operatorNamespace, operators.OperatorServiceName(op.ID), op.Address
	var gateway *kernel.KubeService
	cen := h.central()
	if op.ID == operators.CentralOperatorID && cen != nil {
		// The gateway is the server's own install: its Deployment is in the server's namespace, and the server reads its
		// Service itself, which is how a load balancer's address is found even where no agent runs in that cluster.
		namespace, workload = cen.Namespace(), cen.ServiceName()
		if cen.Exposed() {
			recorded = cen.Endpoint()
		}
		gateway = cen.GatewayService(r.Context())
	}
	var host operatorHost
	var found bool
	if op.ID == operators.CentralOperatorID && cen != nil {
		// The gateway lies in the server's own cluster, so which cluster that is comes first from what is known of the
		// server: a person's choice, else what the install told it. Only then is it worked out from what agents report.
		host, found, res.ServerCluster = h.serverClusterHost(r.Context(), hub, cen, namespace, workload, gateway)
	} else {
		host, found = hubHosts{hub}.workloadHost(r.Context(), namespace, workload)
	}
	if found {
		res.Found, res.HostClusterID, res.HostClusterName = true, host.ClusterID, host.ClusterName
	}
	if gateway != nil && gateway.NodePort != 0 {
		// A node port is open on every node: the server read it from the Service, whatever an agent reports.
		host.Reachable = append(host.Reachable, reachableAddr{Port: int32(gateway.NodePort), Kind: "node-port"})
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
	if found {
		res.Hints = host.hints(res.Candidates)
		if len(host.PodNodes) == 0 && len(host.Nodes) > 0 {
			res.Hints = append(res.Hints, "No agent reports which node runs the pod, so the nodes are listed in name order. Any of them works, since its node port forwards to the pod, but the pod's own node saves a hop.")
		}
	}
	if hint := exposureHint(res.Candidates, found, op.ID == operators.CentralOperatorID); hint != "" && op.ID == operators.CentralOperatorID && cen != nil {
		// The page shows this as a card with the command in it, so the sentence is not repeated as a hint.
		e := &ExposeDoc{Service: workload, Namespace: namespace, Value: "fusion.central.service.type"}
		nodePort, err1 := ExposeCommand(namespace, workload, install.NodePort)
		loadBalancer, err2 := ExposeCommand(namespace, workload, install.LoadBalancer)
		if err := errors.Join(err1, err2); err != nil {
			h.installConfigError(r, err) // logged; the page is shown without the commands
		} else {
			e.Commands.NodePort, e.Commands.LoadBalancer = nodePort, loadBalancer
			res.Expose = e
		}
	} else if hint != "" {
		res.Hints = append(res.Hints, hint)
	} else if !found {
		res.Hints = append(res.Hints, noHostHint(op.ID == operators.CentralOperatorID))
	}
	httpkit.WriteJSON(w, 200, res)
}

// putServerCluster answers PUT .../operators/central/server-cluster: a person says which cluster the server, and so FUSION's
// gateway, runs in, for when the install did not tell it. An empty cluster id takes the choice back.
func (h *API) putServerCluster(w http.ResponseWriter, r *http.Request) {
	hub := h.For(r)
	if r.PathValue("id") != operators.CentralOperatorID || h.central() == nil {
		httpkit.WriteErr(w, 404, "only the central operator has a server cluster")
		return
	}
	var body serverClusterRequest
	if err := httpkit.Decode(r, &body); err != nil {
		httpkit.WriteErr(w, 400, err.Error())
		return
	}
	id := strings.TrimSpace(body.ClusterID)
	if id != "" {
		known := false
		hosts := hubHosts{hub}
		for _, o := range hosts.clusterOptions(r.Context()) {
			known = known || o.ID == id
		}
		if !known {
			httpkit.WriteErr(w, 400, "no approved agent is registered in that cluster")
			return
		}
	}
	s := hub.C.Settings()
	s.ServerClusterID = id
	if _, err := hub.C.SaveSettings(r.Context(), httpkit.Actor(r), s); err != nil {
		h.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, serverClusterChoiceDoc{ClusterID: id})
}
