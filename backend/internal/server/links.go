package server

import (
	"fmt"
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
	want := peersOf(links, a.ClusterID)
	if len(want) == 0 {
		return nil
	}
	if len(want) > link.MaxPeers {
		want = want[:link.MaxPeers]
	}
	byCluster := map[string]store.Agent{}
	for _, o := range agents {
		if o.Status == store.StatusApproved && o.ClusterID != "" {
			byCluster[o.ClusterID] = o
		}
	}
	// An address the paired cluster shares with this one (two clusters on the same private range, say) cannot tell traffic
	// to the peer from traffic inside this cluster, so it is left out rather than mislabelled.
	own := map[string]bool{}
	if v := h.views[a.ID]; v != nil && v.state != nil {
		for _, ip := range nodeAddresses(v.state) {
			own[ip] = true
		}
	}
	if ip := publicIP(a.ConnectingIP); ip != "" {
		own[ip] = true
	}
	var out []*continuumv1.LinkPeer
	for _, id := range want {
		o, ok := byCluster[id]
		if !ok {
			continue
		}
		var addrs []string
		if v := h.views[o.ID]; v != nil && v.state != nil {
			addrs = nodeAddresses(v.state)
		}
		if ip := publicIP(o.ConnectingIP); ip != "" {
			addrs = append(addrs, ip)
		}
		addrs = dedupeSorted(addrs)
		kept := addrs[:0:0]
		for _, ip := range addrs {
			if !own[ip] {
				kept = append(kept, ip)
			}
		}
		addrs = kept
		if len(addrs) == 0 {
			continue
		}
		if len(addrs) > link.MaxAddresses {
			addrs = addrs[:link.MaxAddresses]
		}
		lp := &continuumv1.LinkPeer{ClusterId: id, Name: o.Name, Addresses: addrs}
		l := pairs[id]
		lp.NoProbe = l.NoProbe
		// One side of the pair runs the throughput tests: the one whose id sorts first.
		if t := l.Throughput; t != nil && a.ClusterID < id {
			lp.Throughput = true
			lp.ThroughputIntervalSeconds = t.IntervalMinutes * 60
			lp.ThroughputBudgetBytes = uint64(t.BudgetMB) << 20
		}
		// The same side runs the one-way probe and the connect timing; this one only serves them, while the runner measures.
		if assign && !l.NoProbe && linkRunner(a.ClusterID, id) == id && h.runnerMeasures(o, a, agents, links, flowPausedOf) {
			lp.SkipProbe = true
		}
		out = append(out, lp)
	}
	return out
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
