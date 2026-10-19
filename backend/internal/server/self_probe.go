package server

import (
	"continuum/internal/estate"
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
func (a *Admin) selfProbeNodes() []estate.HostNode {
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
	var out []estate.HostNode
	for _, name := range names {
		// A probe that reports is running on that node, which is the most this says about it being usable.
		n := estate.HostNode{Name: name, Ready: true}
		for _, ha := range reports[name].GetHostAddresses() {
			if len(n.Interfaces) >= 32 { // the probe sends at most 16; a larger list is not believed
				break
			}
			if p, err := netip.ParsePrefix(ha.Address); err == nil {
				n.Interfaces = append(n.Interfaces, estate.HostAddr{Interface: ha.Interface, Kind: ha.Kind, IP: p.Addr().String()})
			}
		}
		out = append(out, n)
	}
	return out
}
