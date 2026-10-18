package server

import (
	"fmt"
	"net/netip"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/connectivity"
	"continuum/internal/kernel"
	"continuum/internal/link"
	"continuum/internal/observed"
	"continuum/internal/store"
)

// linkPeers builds the peer list for one agent: for each cluster it is paired with, the addresses that cluster's traffic
// comes from or goes to (its nodes' addresses, and the address its agent connects from). Called with h.mu held; agents is
// the organisation's agents, read beforehand because that may touch the store.
//
// Nothing is sent below access tier 2 (observed traffic is workload-level information, so link metrics follow the same
// rule), nor while the flow collector is paused for this agent, nor for a peer whose agent is not approved or has told the
// server nothing yet.
//
// A pair is probed from one side only. The agent that is not the pair's runner is told to skip the probe (SkipProbe) only
// while the runner is itself told about this agent, which is what makes it measure: so a runner that is not approved, below
// tier 2, paused, silent, or whose list is full never leaves the pair with nobody measuring. flowPausedOf says which agents
// (by agent id) have the flow collector paused; it is read by the caller beforehand because that may touch the store, and
// only the runners' entries are looked at.
func (h *Hub) linkPeers(a store.Agent, agentList []store.Agent, links []kernel.LinkSetting, flowPaused bool, flowPausedOf map[string]bool) []*continuumv1.LinkPeer {
	return h.peerList(a, agentList, links, flowPaused, flowPausedOf, true)
}

// peerList is linkPeers; assign says whether to hand out the second cluster's SkipProbe (false when working out what the
// other side was told, which must not recurse).
func (h *Hub) peerList(a store.Agent, agentList []store.Agent, links []kernel.LinkSetting, flowPaused bool, flowPausedOf map[string]bool, assign bool) []*continuumv1.LinkPeer {
	if a.ClusterID == "" || a.AccessTier < 2 || flowPaused {
		return nil
	}
	pairs := connectivity.PairsOf(links, a.ClusterID)
	if len(pairs) == 0 {
		return nil
	}
	var out []*continuumv1.LinkPeer
	for _, pp := range h.planPeers(a, agentList, links).Sent {
		id := pp.ID
		addrs := pp.Addrs
		if len(addrs) > link.MaxAddresses {
			addrs = addrs[:link.MaxAddresses]
		}
		lp := &continuumv1.LinkPeer{ClusterId: id, Name: pp.Agent.Name, Addresses: addrs}
		l := pairs[id]
		lp.NoProbe = l.NoProbe
		lp.Via = l.Via
		// One side of the pair runs the throughput tests: the one whose id sorts first.
		if t := l.Throughput; t != nil && a.ClusterID < id {
			lp.Throughput = true
			lp.ThroughputIntervalSeconds = t.IntervalMinutes * 60
			lp.ThroughputBudgetBytes = uint64(t.BudgetMB) << 20
		}
		// The same side runs the one-way probe and the connect timing; this one only serves them, while the runner measures.
		if assign && !l.NoProbe && connectivity.LinkRunner(a.ClusterID, id) == id && h.runnerMeasures(pp.Agent, a, agentList, links, flowPausedOf) {
			lp.SkipProbe = true
		}
		out = append(out, lp)
	}
	return out
}

// planPeers works out the peer list of agent a. The peers that cannot be told about (no approved agent, no address a can tell
// from its own, every address also claimed by another peer) are filtered out FIRST and the list is cut at link.MaxPeers
// after, in id order: a cut that came first would let peers with no agent yet use up the room of those that have one. For a
// hub with no more than link.MaxPeers usable peers the list is the same as without a cut. Called with h.mu held.
func (h *Hub) planPeers(a store.Agent, agentList []store.Agent, links []kernel.LinkSetting) *connectivity.PeerPlan {
	plan := &connectivity.PeerPlan{ByID: map[string]*connectivity.PlannedPeer{}, Cut: map[string]bool{}}
	if a.ClusterID == "" {
		return plan
	}
	byCluster := connectivity.ApprovedByCluster(agentList)
	// An address the paired cluster shares with this one (two clusters on the same private range, say) cannot tell traffic
	// to the peer from traffic inside this cluster, so it is left out rather than mislabelled.
	own := h.ownAddresses(a)
	var cands []*connectivity.PlannedPeer
	claims := map[string][]*connectivity.PlannedPeer{}
	for _, id := range connectivity.PeersOf(links, a.ClusterID) {
		o, ok := byCluster[id]
		if !ok {
			continue
		}
		addrs := connectivity.WithoutAddresses(h.peerAddresses(o), own)
		if len(addrs) == 0 {
			continue
		}
		pp := &connectivity.PlannedPeer{ID: id, Agent: o, Addrs: addrs}
		cands = append(cands, pp)
		plan.ByID[id] = pp
		for _, ad := range addrs {
			claims[ad] = append(claims[ad], pp)
		}
	}
	// An address two of the peers claim (two clusters behind one NAT share the address their agents connect from) says nothing
	// about which of them traffic belongs to. The agent drops it from both and a peer left with none; doing it here too keeps
	// the SkipProbe decision, which asks whether the runner is told about the peer, true to what the runner is sent, and lets
	// the Sites screen say why.
	for _, pp := range cands {
		kept := pp.Addrs[:0:0]
		for _, ad := range pp.Addrs {
			if len(claims[ad]) < 2 {
				kept = append(kept, ad)
				continue
			}
			if pp.Clash == nil {
				pp.Clash = map[string][]string{}
			}
			for _, other := range claims[ad] {
				if other != pp {
					pp.Clash[other.ID] = append(pp.Clash[other.ID], ad)
				}
			}
		}
		pp.Addrs = kept
	}
	for _, pp := range cands {
		if len(pp.Addrs) == 0 {
			continue
		}
		if len(plan.Sent) >= link.MaxPeers {
			plan.Cut[pp.ID] = true
			continue
		}
		plan.Sent = append(plan.Sent, pp)
	}
	return plan
}

// planner is planPeers memoised per agent, for a caller (the notes, the checklists) that asks about every pair of the
// organisation. Called with h.mu held; the closure is for one call's use.
func (h *Hub) planner(links []kernel.LinkSetting, agentList []store.Agent) func(store.Agent) *connectivity.PeerPlan {
	memo := map[string]*connectivity.PeerPlan{}
	return func(a store.Agent) *connectivity.PeerPlan {
		p, ok := memo[a.ID]
		if !ok {
			p = h.planPeers(a, agentList, links)
			memo[a.ID] = p
		}
		return p
	}
}

// ownAddresses are the addresses that mean "inside agent a's cluster": the ones other clusters would be offered for it (see
// observed.LinkPeerAddresses, the same function, so that an address two clusters share is removed on both sides) and the public address
// its agent connects from. Called with h.mu held.
func (h *Hub) ownAddresses(a store.Agent) map[string]bool {
	own := map[string]bool{}
	for _, ip := range h.peerAddresses(a) {
		own[ip] = true
	}
	return own
}

// peerNodeAddresses are the addresses agent o's cluster answers to as its nodes and their interfaces report them, canonical,
// sorted, without repeats. Called with h.mu held.
func (h *Hub) peerNodeAddresses(o store.Agent) []string {
	if v := h.views[o.ID]; v != nil && v.state != nil {
		return connectivity.DedupeSorted(observed.LinkPeerAddresses(v.state))
	}
	return nil
}

// peerAddresses are the addresses agent o's cluster is reached at, as far as the server knows them: its nodes' addresses (see
// observed.LinkPeerAddresses) and the public address its agent connects from, sorted. The latter is the NAT's or gateway's, not a node's:
// it stays in the list because traffic from a NATed cluster arrives from it, and the meter can only attribute it if it is
// listed. Called with h.mu held.
func (h *Hub) peerAddresses(o store.Agent) []string {
	addrs := h.peerNodeAddresses(o)
	if ip := publicIP(o.ConnectingIP); ip != "" {
		if a, err := netip.ParseAddr(ip); err == nil {
			addrs = append(addrs[:len(addrs):len(addrs)], a.Unmap().String())
		}
	}
	return connectivity.DedupeSorted(addrs)
}

// runnerMeasures says whether the runner of a pair, agent r, has been told to measure towards agent a: that is, whether a is
// in the peer list r would be sent. It reuses linkPeers, so it holds exactly when r is approved, at tier 2, not paused, has a
// peer list with room for a, and a has reported addresses r can use.
func (h *Hub) runnerMeasures(r, a store.Agent, agentList []store.Agent, links []kernel.LinkSetting, flowPausedOf map[string]bool) bool {
	for _, lp := range h.peerList(r, agentList, links, flowPausedOf[r.ID], flowPausedOf, false) {
		if lp.ClusterId == a.ClusterID {
			return !lp.NoProbe
		}
	}
	return false
}

// runnersPaused is, for the agents that run the probes of the pairs agent a is in, whether their flow collector is paused. The
// consent may have to be read from the store, so call it before taking h.mu, and only when a has pairs.
func (h *Hub) runnersPaused(a store.Agent, agentList []store.Agent, links []kernel.LinkSetting) map[string]bool {
	runs := map[string]bool{}
	for _, id := range connectivity.PeersOf(links, a.ClusterID) {
		if connectivity.LinkRunner(a.ClusterID, id) == id {
			runs[id] = true
		}
	}
	if len(runs) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, o := range agentList {
		if runs[o.ClusterID] && o.Status == store.StatusApproved {
			out[o.ID] = h.consentOf(o.ID).Has("flow")
		}
	}
	return out
}

// linkNotes works out, for each listed pair, what keeps either cluster from being told about the other. It mirrors peerList's
// conditions one by one, so a pair that has no note is one whose agents are handed each other. Called with h.mu held;
// agents is the organisation's agents and flowPausedOf (by agent id) says whose flow collector is paused, both read
// beforehand because that may touch the store.
func (h *Hub) linkNotes(links []kernel.LinkSetting, agentList []store.Agent, flowPausedOf map[string]bool) []connectivity.LinkNote {
	byCluster := connectivity.ApprovedByCluster(agentList)
	plan := h.planner(links, agentList)
	var out []connectivity.LinkNote
	noAgent := map[string]bool{}
	for _, l := range links {
		for _, end := range [][2]string{{l.A, l.B}, {l.B, l.A}} {
			c, peer := end[0], end[1]
			a, ok := byCluster[c]
			if !ok {
				if !noAgent[c] {
					noAgent[c] = true
					out = append(out, connectivity.LinkNote{Cluster: c, Code: "no-agent", Text: "No approved agent reports for this cluster, so it can neither measure its pairs nor be measured."})
				}
				continue
			}
			o, ok := byCluster[peer]
			if !ok {
				continue // the peer's own note says it
			}
			p := plan(a)
			if n := h.endNote(l, a, o, p, flowPausedOf[a.ID]); n != nil {
				out = append(out, *n)
				continue
			}
			if n := connectivity.SharedHint(a, o, p.ByID[peer]); n != nil {
				out = append(out, *n)
			}
			if n := h.rangeHint(a, o, p.ByID[peer]); n != nil {
				out = append(out, *n)
			}
		}
	}
	return out
}

// endNote is the reason, if any, that agent a (one end of pair l, with approved agent o at the other, and plan the peer list a is
// given) is not told about its peer: the one linkNotes reports for that end. Nil when a is handed o. Called with h.mu held.
func (h *Hub) endNote(l kernel.LinkSetting, a, o store.Agent, plan *connectivity.PeerPlan, flowPaused bool) *connectivity.LinkNote {
	c, peer := a.ClusterID, o.ClusterID
	switch {
	case a.AccessTier < 2:
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "tier", Text: fmt.Sprintf("This cluster's agent is at access tier %d. Link measuring needs tier 2, so the agent is not told which clusters to measure.", a.AccessTier)}
	case flowPaused:
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "paused", Text: "The flow collector is paused for this cluster's agent, which pauses link measuring there too."}
	}
	raw := h.peerAddresses(o)
	if len(raw) == 0 {
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "peer-silent", Text: "The agent of " + peer + " has not reported any node address yet, so there is nothing to measure towards."}
	}
	pp := plan.ByID[peer]
	switch {
	case pp == nil:
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "shared-address", Text: fmt.Sprintf("Every address of %s (%s) is also an address of this cluster, or the public address its agent connects from, so traffic to %s cannot be told from its own traffic and the pair is left out here.", peer, connectivity.Listed(raw), peer)}
	case len(pp.Addrs) == 0:
		others := kernel.SortedKeys(pp.Clash)
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "shared-address", Text: fmt.Sprintf("Every address of %s (%s) is also an address of %s, another cluster %s is paired with, so traffic to the two cannot be told apart and %s is left out here. Give the clusters addresses of their own, or pair %s with only one of them.", peer, connectivity.Listed(raw), connectivity.Listed(others), c, peer, c)}
	case plan.Cut[peer]:
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "too-many-peers", Text: fmt.Sprintf("This cluster is paired with more clusters that can be measured than one agent holds (%d), so %s, which sorts after them, is left out. Remove the pairs this cluster does not need.", link.MaxPeers, peer)}
	case l.Via != "" && (!l.NoProbe || l.Throughput != nil) && !connectivity.HasAddressOfKind(pp.Addrs, l.Via):
		return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "no-address-of-kind", Text: fmt.Sprintf("The pair is set to measure through %s addresses, but %s has none, so the active measurements towards it fail until the choice or the addresses change.", l.Via, peer)}
	}
	return nil
}

// rangeHint is the hint for a pair whose peer has no public address: the addresses the cluster is told to measure towards are all of
// a mesh or a private range, which the two clusters reach each other on only if they share that network (one LAN, a VPN or the mesh
// itself). A measurement towards an address nothing routes to fails, and the retransmits of the attempts show up in the link's
// own figures, so the hint says where to look before the figures do. Nil when a node of the peer has a public address among the
// usable ones or none is usable. Only the nodes' own addresses count: the public address the peer's agent connects from is its
// NAT's or gateway's, which a node of this cluster reaches only if that gateway forwards the measurement ports, and treating it as
// a way across would hide the hint from every NATed cluster. Called with h.mu held.
func (h *Hub) rangeHint(a, o store.Agent, pp *connectivity.PlannedPeer) *connectivity.LinkNote {
	if pp == nil {
		return nil
	}
	isNode := map[string]bool{}
	for _, s := range h.peerNodeAddresses(o) {
		isNode[s] = true
	}
	var mesh, private int
	var usable []string
	for _, s := range pp.Addrs {
		if !isNode[s] {
			continue
		}
		ad, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		switch link.Class(ad) {
		case link.ClassMesh:
			mesh++
		case link.ClassPrivate:
			private++
		case link.ClassPublic:
			return nil
		default:
			continue
		}
		usable = append(usable, s)
	}
	if mesh+private == 0 {
		return nil
	}
	kinds := "private"
	switch {
	case mesh > 0 && private > 0:
		kinds = "mesh (100.64.0.0/10) and private"
	case mesh > 0:
		kinds = "mesh (100.64.0.0/10)"
	}
	c, peer := a.ClusterID, o.ClusterID
	text := fmt.Sprintf("Every node address of %s that %s is told to measure towards (%s) is a %s one, so the measurements work only if the two clusters share that network, a LAN, a VPN or the mesh itself. If nothing arrives, check that a node of this cluster can reach those addresses.", peer, c, connectivity.Listed(usable), kinds)
	if egress := publicIP(o.ConnectingIP); egress != "" && !isNode[egress] && connectivity.HasAddress(pp.Addrs, egress) {
		text += fmt.Sprintf(" The public address the agent of %s connects from (%s) is not counted: it is the address of its gateway, which leads to a node only if the measurement ports are forwarded.", peer, egress)
	}
	return &connectivity.LinkNote{Cluster: c, Peer: peer, Code: "private-range", Text: text}
}
