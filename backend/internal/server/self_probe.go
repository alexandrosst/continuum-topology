package server

import (
	"net/netip"
	"sort"
	"time"
)

// The server's chart can ship the same node probe an agent's chart does. It runs on every node of the cluster the server
// is in and reports to a receiver inside the server, signed with a secret the chart generates in the same release, so no
// agent has to be enrolled for the server's own cluster to be known. What it adds is what an agent's node facts already
// hold for addresses: the addresses on each node's own network interfaces, mesh ones included. A real agent in that
// cluster always outranks it, since an agent also knows readiness, pod ranges and topology.

// selfProbeSilent is how many reporting intervals of silence make a node's report too old to believe; the same three the
// agent's receiver uses.
const selfProbeSilent = 3

// selfProbeNodes lists the nodes whose probes have reported lately, with the addresses they read from their interfaces.
// Nil when the server has no such probes or none has reported.
func (a *Admin) selfProbeNodes() []hostNode {
	if a.SelfProbe == nil {
		return nil
	}
	every := a.SelfProbeEvery
	if every <= 0 {
		every = 3 * time.Minute
	}
	reports := a.SelfProbe.Snapshot(selfProbeSilent * every)
	names := make([]string, 0, len(reports))
	for n := range reports {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []hostNode
	for _, name := range names {
		// A probe that reports is running on that node, which is the most this says about it being usable.
		n := hostNode{Name: name, Ready: true}
		for _, ha := range reports[name].GetHostAddresses() {
			if len(n.Interfaces) >= 32 { // the probe sends at most 16; a larger list is not believed
				break
			}
			if p, err := netip.ParsePrefix(ha.Address); err == nil {
				n.Interfaces = append(n.Interfaces, hostAddr{Interface: ha.Interface, Kind: ha.Kind, IP: p.Addr().String()})
			}
		}
		out = append(out, n)
	}
	return out
}

// withSelfProbe adds what the server's own node probes report to what is known of the server's cluster.
//
//   - An agent there already lists the nodes: a node that agent has no interface addresses for takes the probe's. The
//     agent's own are never replaced.
//   - No agent is registered there, or the cluster is not known at all: the probes are the nodes. They run in the server's
//     own cluster by construction, so this settles which cluster that is even when nothing else did.
func (a *Admin) withSelfProbe(host operatorHost, found bool, doc *ServerClusterDoc) (operatorHost, bool, *ServerClusterDoc) {
	nodes := a.selfProbeNodes()
	if len(nodes) == 0 {
		return host, found, doc
	}
	if found {
		byName := map[string]hostNode{}
		for _, n := range nodes {
			byName[n.Name] = n
		}
		filled := append([]hostNode(nil), host.Nodes...)
		for i, n := range filled {
			if len(n.Interfaces) == 0 {
				filled[i].Interfaces = byName[n.Name].Interfaces
			}
		}
		host.Nodes = filled
		doc.Nodes = "agent"
		return host, true, doc
	}
	host = operatorHost{ClusterID: doc.ClusterID, ClusterName: "This server's cluster", Nodes: nodes}
	for _, o := range doc.Options {
		if o.ID == doc.ClusterID && o.Name != "" {
			host.ClusterName = o.Name
		}
	}
	doc.State, doc.Nodes = serverClusterKnown, "probe"
	if doc.Source == "" {
		doc.Source = "probe"
	}
	return host, true, doc
}
