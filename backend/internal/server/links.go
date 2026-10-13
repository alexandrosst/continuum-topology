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

// LinkSetting is an administrator's request to watch the link between two clusters. A pair is measured in both directions
// and by both clusters: each is told about the other and nothing else, so a cluster only ever learns the addresses of the
// clusters it was paired with. A pair that is not listed is not measured at all.
type LinkSetting struct {
	A string `json:"a"`
	B string `json:"b"`
}

// normalizeLinks checks the pairs and puts each in a canonical order (A before B), without duplicates, so the same pair
// entered either way round is one pair.
func normalizeLinks(in []LinkSetting) ([]LinkSetting, error) {
	if len(in) > maxLinks {
		return nil, fmt.Errorf("at most %d linked cluster pairs", maxLinks)
	}
	seen := map[LinkSetting]bool{}
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
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out, nil
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

// linkPeers builds the peer list for one agent: for each cluster it is paired with, the addresses that cluster's traffic
// comes from or goes to (its nodes' addresses, and the address its agent connects from). Called with h.mu held; agents is
// the organisation's agents, read beforehand because that may touch the store.
//
// Nothing is sent below access tier 2 (observed traffic is workload-level information, so link metrics follow the same
// rule), nor while the flow collector is paused for this agent, nor for a peer whose agent is not approved or has told the
// server nothing yet.
func (h *Hub) linkPeers(a store.Agent, agents []store.Agent, links []LinkSetting, flowPaused bool) []*continuumv1.LinkPeer {
	if a.ClusterID == "" || a.AccessTier < 2 || flowPaused {
		return nil
	}
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
		out = append(out, &continuumv1.LinkPeer{ClusterId: id, Name: o.Name, Addresses: addrs})
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
