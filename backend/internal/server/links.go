package server

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/link"
	"continuum/internal/store"
)

// maxLinks bounds how many cluster pairs one organisation can ask to have watched.
const maxLinks = 100

// LinkSetting is an administrator's request to watch the link between two clusters. A pair is measured in both directions:
// each cluster is told about the other and nothing else, so a cluster only ever learns the addresses of the clusters it was
// paired with. A pair that is not listed is not measured at all.
//
// By default a listed pair gets the passive link metrics (each cluster's own), the one-way probe and the connect timing, and
// the route trace, and no throughput tests (those load the link, so they are asked for pair by pair). Both clusters get the
// same choices. The one-way probe measures both directions from one end and the connect timing is the same round trip from
// either, so only the cluster whose id sorts first runs them (like the throughput tests), and the other one only serves them;
// the route trace runs on both, as the two directions can take different routes.
type LinkSetting struct {
	A string `json:"a"`
	B string `json:"b"`
	// NoProbe turns the one-way probe, the connect timing and the route trace off for the pair; the passive metrics stay.
	NoProbe bool `json:"noProbe,omitempty"`
	// Via pins the active measurements of the pair (one-way probe, connect timing, route trace, throughput tests) to one kind
	// of the peer's addresses: "mesh", "private" or "public". Empty, the default, follows the address the real traffic between
	// the clusters is seen using. The passive metrics always match against all of the peer's addresses.
	Via string `json:"via,omitempty"`
	// Throughput, when present, asks for the throughput tests (iperf3) on the pair. Only one cluster of the pair runs
	// them, in both directions, and the other serves them: the one whose id sorts first, so the link is not loaded twice.
	Throughput *LinkThroughput `json:"throughput,omitempty"`
}

// LinkThroughput tunes the throughput tests of one pair.
type LinkThroughput struct {
	// IntervalMinutes replaces the cluster's own time between two sets of tests (10 minutes to 7 days; 0 keeps it).
	IntervalMinutes uint32 `json:"intervalMinutes,omitempty"`
	// BudgetMB is the most the tests may carry on this link in a day, in addition to the cluster's own budget (up to 1 TiB;
	// 0 means only the cluster's budget applies).
	BudgetMB uint32 `json:"budgetMB,omitempty"`
}

const (
	minThroughputMinutes  = 10
	maxThroughputMinutes  = 7 * 24 * 60
	maxThroughputBudgetMB = 1 << 20
)

// normalizeLinks checks the pairs and puts each in a canonical order (A before B), without duplicates, so the same pair
// entered either way round is one pair.
func normalizeLinks(in []LinkSetting) ([]LinkSetting, error) {
	if len(in) > maxLinks {
		return nil, fmt.Errorf("at most %d linked cluster pairs", maxLinks)
	}
	at := map[[2]string]int{}
	out := make([]LinkSetting, 0, len(in))
	for _, l := range in {
		l.A, l.B = strings.TrimSpace(l.A), strings.TrimSpace(l.B)
		if l.A == "" || l.B == "" {
			return nil, fmt.Errorf("a linked pair needs two clusters")
		}
		if len(l.A) > 64 || len(l.B) > 64 {
			return nil, fmt.Errorf("a cluster id is at most 64 characters")
		}
		if l.A == l.B {
			return nil, fmt.Errorf("a cluster cannot be linked to itself")
		}
		if l.B < l.A {
			l.A, l.B = l.B, l.A
		}
		if !link.ValidVia(l.Via) {
			return nil, fmt.Errorf("the pair of %s and %s: measure through must be mesh, private or public addresses, or empty to follow the traffic", l.A, l.B)
		}
		if t := l.Throughput; t != nil {
			if t.IntervalMinutes != 0 && (t.IntervalMinutes < minThroughputMinutes || t.IntervalMinutes > maxThroughputMinutes) {
				return nil, fmt.Errorf("throughput tests of %s and %s: the interval must be between %d minutes and 7 days", l.A, l.B, minThroughputMinutes)
			}
			if t.BudgetMB > maxThroughputBudgetMB {
				return nil, fmt.Errorf("throughput tests of %s and %s: the daily budget is at most 1 TiB", l.A, l.B)
			}
			cp := *t
			l.Throughput = &cp
		}
		// The same pair entered twice (or the other way round) is one pair; the last entry's choices stand.
		k := [2]string{l.A, l.B}
		if i, ok := at[k]; ok {
			out[i] = l
			continue
		}
		at[k] = len(out)
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, nil
}

// pairsOf is the pairs a cluster is in, by the id of the cluster at the other end.
func pairsOf(links []LinkSetting, cluster string) map[string]LinkSetting {
	out := map[string]LinkSetting{}
	for _, l := range links {
		switch cluster {
		case l.A:
			out[l.B] = l
		case l.B:
			out[l.A] = l
		}
	}
	return out
}

// peersOf is the clusters paired with one cluster, sorted.
func peersOf(links []LinkSetting, cluster string) []string {
	var out []string
	for _, l := range links {
		switch cluster {
		case l.A:
			out = append(out, l.B)
		case l.B:
			out = append(out, l.A)
		}
	}
	sort.Strings(out)
	return out
}

// linkRunner is the cluster of a pair that runs the one-way probe, the connect timing and the throughput tests: the one whose
// id sorts first. The other only serves them.
func linkRunner(a, b string) string { return min(a, b) }

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
func (h *Hub) linkPeers(a store.Agent, agents []store.Agent, links []LinkSetting, flowPaused bool, flowPausedOf map[string]bool) []*continuumv1.LinkPeer {
	return h.peerList(a, agents, links, flowPaused, flowPausedOf, true)
}

// peerList is linkPeers; assign says whether to hand out the second cluster's SkipProbe (false when working out what the
// other side was told, which must not recurse).
func (h *Hub) peerList(a store.Agent, agents []store.Agent, links []LinkSetting, flowPaused bool, flowPausedOf map[string]bool, assign bool) []*continuumv1.LinkPeer {
	if a.ClusterID == "" || a.AccessTier < 2 || flowPaused {
		return nil
	}
	pairs := pairsOf(links, a.ClusterID)
	if len(pairs) == 0 {
		return nil
	}
	var out []*continuumv1.LinkPeer
	for _, pp := range h.planPeers(a, agents, links).sent {
		id := pp.id
		addrs := pp.addrs
		if len(addrs) > link.MaxAddresses {
			addrs = addrs[:link.MaxAddresses]
		}
		lp := &continuumv1.LinkPeer{ClusterId: id, Name: pp.agent.Name, Addresses: addrs}
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
		if assign && !l.NoProbe && linkRunner(a.ClusterID, id) == id && h.runnerMeasures(pp.agent, a, agents, links, flowPausedOf) {
			lp.SkipProbe = true
		}
		out = append(out, lp)
	}
	return out
}

// plannedPeer is one cluster paired with agent a's, as a's peer list holds it.
type plannedPeer struct {
	id    string
	agent store.Agent
	// addrs is what a is told: the cluster's addresses less those of a's own cluster and those another peer of a claims too.
	addrs []string
	// clash is, by the id of the other peer, the addresses both peers claim, which are left out of both.
	clash map[string][]string
}

// peerPlan is what peerList, linkNotes and the checklist all work from, so that what a pair says it is missing is exactly what
// the agent is not sent.
type peerPlan struct {
	// byID holds every paired cluster that has an approved agent and an address a can tell from its own, told or not.
	byID map[string]*plannedPeer
	// sent are the peers a is told, in id order: those left with an address after the shared ones are removed, cut at
	// link.MaxPeers.
	sent []*plannedPeer
	// cut are the peers that had an address to be told but no room in the list.
	cut map[string]bool
}

// planPeers works out the peer list of agent a. The peers that cannot be told about (no approved agent, no address a can tell
// from its own, every address also claimed by another peer) are filtered out FIRST and the list is cut at link.MaxPeers
// after, in id order: a cut that came first would let peers with no agent yet use up the room of those that have one. For a
// hub with no more than link.MaxPeers usable peers the list is the same as without a cut. Called with h.mu held.
func (h *Hub) planPeers(a store.Agent, agents []store.Agent, links []LinkSetting) *peerPlan {
	plan := &peerPlan{byID: map[string]*plannedPeer{}, cut: map[string]bool{}}
	if a.ClusterID == "" {
		return plan
	}
	byCluster := approvedByCluster(agents)
	// An address the paired cluster shares with this one (two clusters on the same private range, say) cannot tell traffic
	// to the peer from traffic inside this cluster, so it is left out rather than mislabelled.
	own := h.ownAddresses(a)
	var cands []*plannedPeer
	claims := map[string][]*plannedPeer{}
	for _, id := range peersOf(links, a.ClusterID) {
		o, ok := byCluster[id]
		if !ok {
			continue
		}
		addrs := withoutAddresses(h.peerAddresses(o), own)
		if len(addrs) == 0 {
			continue
		}
		pp := &plannedPeer{id: id, agent: o, addrs: addrs}
		cands = append(cands, pp)
		plan.byID[id] = pp
		for _, ad := range addrs {
			claims[ad] = append(claims[ad], pp)
		}
	}
	// An address two of the peers claim (two clusters behind one NAT share the address their agents connect from) says nothing
	// about which of them traffic belongs to. The agent drops it from both and a peer left with none; doing it here too keeps
	// the SkipProbe decision, which asks whether the runner is told about the peer, true to what the runner is sent, and lets
	// the Sites screen say why.
	for _, pp := range cands {
		kept := pp.addrs[:0:0]
		for _, ad := range pp.addrs {
			if len(claims[ad]) < 2 {
				kept = append(kept, ad)
				continue
			}
			if pp.clash == nil {
				pp.clash = map[string][]string{}
			}
			for _, other := range claims[ad] {
				if other != pp {
					pp.clash[other.id] = append(pp.clash[other.id], ad)
				}
			}
		}
		pp.addrs = kept
	}
	for _, pp := range cands {
		if len(pp.addrs) == 0 {
			continue
		}
		if len(plan.sent) >= link.MaxPeers {
			plan.cut[pp.id] = true
			continue
		}
		plan.sent = append(plan.sent, pp)
	}
	return plan
}

// planner is planPeers memoised per agent, for a caller (the notes, the checklists) that asks about every pair of the
// organisation. Called with h.mu held; the closure is for one call's use.
func (h *Hub) planner(links []LinkSetting, agents []store.Agent) func(store.Agent) *peerPlan {
	memo := map[string]*peerPlan{}
	return func(a store.Agent) *peerPlan {
		p, ok := memo[a.ID]
		if !ok {
			p = h.planPeers(a, agents, links)
			memo[a.ID] = p
		}
		return p
	}
}

// ownAddresses are the addresses that mean "inside agent a's cluster": the ones other clusters would be offered for it (see
// linkPeerAddresses, the same function, so that an address two clusters share is removed on both sides) and the public address
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
		return dedupeSorted(linkPeerAddresses(v.state))
	}
	return nil
}

// peerAddresses are the addresses agent o's cluster is reached at, as far as the server knows them: its nodes' addresses (see
// linkPeerAddresses) and the public address its agent connects from, sorted. The latter is the NAT's or gateway's, not a node's:
// it stays in the list because traffic from a NATed cluster arrives from it, and the meter can only attribute it if it is
// listed. Called with h.mu held.
func (h *Hub) peerAddresses(o store.Agent) []string {
	addrs := h.peerNodeAddresses(o)
	if ip := publicIP(o.ConnectingIP); ip != "" {
		if a, err := netip.ParseAddr(ip); err == nil {
			addrs = append(addrs[:len(addrs):len(addrs)], a.Unmap().String())
		}
	}
	return dedupeSorted(addrs)
}

func withoutAddresses(addrs []string, drop map[string]bool) []string {
	kept := addrs[:0:0]
	for _, ip := range addrs {
		if !drop[ip] {
			kept = append(kept, ip)
		}
	}
	return kept
}

// runnerMeasures says whether the runner of a pair, agent r, has been told to measure towards agent a: that is, whether a is
// in the peer list r would be sent. It reuses linkPeers, so it holds exactly when r is approved, at tier 2, not paused, has a
// peer list with room for a, and a has reported addresses r can use.
func (h *Hub) runnerMeasures(r, a store.Agent, agents []store.Agent, links []LinkSetting, flowPausedOf map[string]bool) bool {
	for _, lp := range h.peerList(r, agents, links, flowPausedOf[r.ID], flowPausedOf, false) {
		if lp.ClusterId == a.ClusterID {
			return !lp.NoProbe
		}
	}
	return false
}

// runnersPaused is, for the agents that run the probes of the pairs agent a is in, whether their flow collector is paused. The
// consent may have to be read from the store, so call it before taking h.mu, and only when a has pairs.
func (h *Hub) runnersPaused(a store.Agent, agents []store.Agent, links []LinkSetting) map[string]bool {
	runs := map[string]bool{}
	for _, id := range peersOf(links, a.ClusterID) {
		if linkRunner(a.ClusterID, id) == id {
			runs[id] = true
		}
	}
	if len(runs) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, o := range agents {
		if runs[o.ClusterID] && o.Status == store.StatusApproved {
			out[o.ID] = h.consentOf(o.ID).has("flow")
		}
	}
	return out
}

func dedupeSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// LinkNote explains why a pair is not (fully) measured from one of its clusters: the reasons the server has for sending that
// cluster's agent no peer list, or one without the other cluster. Nothing here is secret: it is what the administrator who
// set the pair up needs to see, and it names only clusters and addresses of the organisation's own agents.
type LinkNote struct {
	// Cluster is the cluster the note is about, Peer the other end of the pair (empty when the note is about the cluster alone).
	Cluster string `json:"cluster"`
	Peer    string `json:"peer,omitempty"`
	// Code is no-agent | tier | paused | peer-silent | shared-address | too-many-peers | no-address-of-kind | private-range. The first
	// seven say what keeps a cluster from measuring the peer (shared-address is also given, as a hint that leaves the pair measured,
	// when only some of the peer's addresses are claimed by another peer); private-range is only a hint, for a pair that measures
	// nothing yet (the Sites screen leaves it out when it does).
	Code string `json:"code"`
	Text string `json:"text"`
}

// linkNotes works out, for each listed pair, what keeps either cluster from being told about the other. It mirrors peerList's
// conditions one by one, so a pair that has no note is one whose agents are handed each other. Called with h.mu held;
// agents is the organisation's agents and flowPausedOf (by agent id) says whose flow collector is paused, both read
// beforehand because that may touch the store.
func (h *Hub) linkNotes(links []LinkSetting, agents []store.Agent, flowPausedOf map[string]bool) []LinkNote {
	byCluster := approvedByCluster(agents)
	plan := h.planner(links, agents)
	var out []LinkNote
	noAgent := map[string]bool{}
	for _, l := range links {
		for _, end := range [][2]string{{l.A, l.B}, {l.B, l.A}} {
			c, peer := end[0], end[1]
			a, ok := byCluster[c]
			if !ok {
				if !noAgent[c] {
					noAgent[c] = true
					out = append(out, LinkNote{Cluster: c, Code: "no-agent", Text: "No approved agent reports for this cluster, so it can neither measure its pairs nor be measured."})
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
			if n := sharedHint(a, o, p.byID[peer]); n != nil {
				out = append(out, *n)
			}
			if n := h.rangeHint(a, o, p.byID[peer]); n != nil {
				out = append(out, *n)
			}
		}
	}
	return out
}

// approvedByCluster is the organisation's approved agents by the cluster they report for.
func approvedByCluster(agents []store.Agent) map[string]store.Agent {
	byCluster := map[string]store.Agent{}
	for _, o := range agents {
		if o.Status == store.StatusApproved && o.ClusterID != "" {
			byCluster[o.ClusterID] = o
		}
	}
	return byCluster
}

// endNote is the reason, if any, that agent a (one end of pair l, with approved agent o at the other, and plan the peer list a is
// given) is not told about its peer: the one linkNotes reports for that end. Nil when a is handed o. Called with h.mu held.
func (h *Hub) endNote(l LinkSetting, a, o store.Agent, plan *peerPlan, flowPaused bool) *LinkNote {
	c, peer := a.ClusterID, o.ClusterID
	switch {
	case a.AccessTier < 2:
		return &LinkNote{Cluster: c, Peer: peer, Code: "tier", Text: fmt.Sprintf("This cluster's agent is at access tier %d. Link measuring needs tier 2, so the agent is not told which clusters to measure.", a.AccessTier)}
	case flowPaused:
		return &LinkNote{Cluster: c, Peer: peer, Code: "paused", Text: "The flow collector is paused for this cluster's agent, which pauses link measuring there too."}
	}
	raw := h.peerAddresses(o)
	if len(raw) == 0 {
		return &LinkNote{Cluster: c, Peer: peer, Code: "peer-silent", Text: "The agent of " + peer + " has not reported any node address yet, so there is nothing to measure towards."}
	}
	pp := plan.byID[peer]
	switch {
	case pp == nil:
		return &LinkNote{Cluster: c, Peer: peer, Code: "shared-address", Text: fmt.Sprintf("Every address of %s (%s) is also an address of this cluster, or the public address its agent connects from, so traffic to %s cannot be told from its own traffic and the pair is left out here.", peer, listed(raw), peer)}
	case len(pp.addrs) == 0:
		others := sortedKeys(pp.clash)
		return &LinkNote{Cluster: c, Peer: peer, Code: "shared-address", Text: fmt.Sprintf("Every address of %s (%s) is also an address of %s, another cluster %s is paired with, so traffic to the two cannot be told apart and %s is left out here. Give the clusters addresses of their own, or pair %s with only one of them.", peer, listed(raw), listed(others), c, peer, c)}
	case plan.cut[peer]:
		return &LinkNote{Cluster: c, Peer: peer, Code: "too-many-peers", Text: fmt.Sprintf("This cluster is paired with more clusters that can be measured than one agent holds (%d), so %s, which sorts after them, is left out. Remove the pairs this cluster does not need.", link.MaxPeers, peer)}
	case l.Via != "" && (!l.NoProbe || l.Throughput != nil) && !hasAddressOfKind(pp.addrs, l.Via):
		return &LinkNote{Cluster: c, Peer: peer, Code: "no-address-of-kind", Text: fmt.Sprintf("The pair is set to measure through %s addresses, but %s has none, so the active measurements towards it fail until the choice or the addresses change.", l.Via, peer)}
	}
	return nil
}

// sharedHint is the note for a peer that is told about but has addresses another peer of the same cluster claims too: they are
// left out for both, and the pair is measured through the rest. Nil when none is shared.
func sharedHint(a, o store.Agent, pp *plannedPeer) *LinkNote {
	if pp == nil || len(pp.clash) == 0 || len(pp.addrs) == 0 {
		return nil
	}
	var shared []string
	for _, k := range sortedKeys(pp.clash) {
		shared = append(shared, pp.clash[k]...)
	}
	shared = dedupeSorted(shared)
	return &LinkNote{Cluster: a.ClusterID, Peer: o.ClusterID, Code: "shared-address", Text: fmt.Sprintf("%s shares %s with %s, another cluster %s is paired with, so those addresses are left out for both and %s is measured through its other addresses only.", o.ClusterID, listed(shared), listed(sortedKeys(pp.clash)), a.ClusterID, o.ClusterID)}
}

// rangeHint is the hint for a pair whose peer has no public address: the addresses the cluster is told to measure towards are all of
// a mesh or a private range, which the two clusters reach each other on only if they share that network (one LAN, a VPN or the mesh
// itself). A measurement towards an address nothing routes to fails, and the retransmits of the attempts show up in the link's
// own figures, so the hint says where to look before the figures do. Nil when a node of the peer has a public address among the
// usable ones or none is usable. Only the nodes' own addresses count: the public address the peer's agent connects from is its
// NAT's or gateway's, which a node of this cluster reaches only if that gateway forwards the measurement ports, and treating it as
// a way across would hide the hint from every NATed cluster. Called with h.mu held.
func (h *Hub) rangeHint(a, o store.Agent, pp *plannedPeer) *LinkNote {
	if pp == nil {
		return nil
	}
	isNode := map[string]bool{}
	for _, s := range h.peerNodeAddresses(o) {
		isNode[s] = true
	}
	var mesh, private int
	var usable []string
	for _, s := range pp.addrs {
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
	text := fmt.Sprintf("Every node address of %s that %s is told to measure towards (%s) is a %s one, so the measurements work only if the two clusters share that network, a LAN, a VPN or the mesh itself. If nothing arrives, check that a node of this cluster can reach those addresses.", peer, c, listed(usable), kinds)
	if egress := publicIP(o.ConnectingIP); egress != "" && !isNode[egress] && hasAddress(pp.addrs, egress) {
		text += fmt.Sprintf(" The public address the agent of %s connects from (%s) is not counted: it is the address of its gateway, which leads to a node only if the measurement ports are forwarded.", peer, egress)
	}
	return &LinkNote{Cluster: c, Peer: peer, Code: "private-range", Text: text}
}

// maxListedAddresses bounds how many addresses (or clusters) a note spells out.
const maxListedAddresses = 6

// listed joins at most maxListedAddresses of items for a note's text and counts the rest: "a, b, c +4 more".
func listed(items []string) string {
	if len(items) <= maxListedAddresses {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(items[:maxListedAddresses], ", "), len(items)-maxListedAddresses)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasAddress(addrs []string, s string) bool {
	for _, x := range addrs {
		if x == s {
			return true
		}
	}
	return false
}

// hasAddressOfKind says whether any of the addresses is of the kind via (see link.Class).
func hasAddressOfKind(addrs []string, via string) bool {
	for _, s := range addrs {
		if a, err := netip.ParseAddr(s); err == nil && link.Class(a) == via {
			return true
		}
	}
	return false
}
