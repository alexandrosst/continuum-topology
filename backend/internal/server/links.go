package server

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"continuum/internal/kernel"
	"continuum/internal/link"
	"continuum/internal/store"
)

// pairsOf is the pairs a cluster is in, by the id of the cluster at the other end.
func pairsOf(links []kernel.LinkSetting, cluster string) map[string]kernel.LinkSetting {
	out := map[string]kernel.LinkSetting{}
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
func peersOf(links []kernel.LinkSetting, cluster string) []string {
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

func withoutAddresses(addrs []string, drop map[string]bool) []string {
	kept := addrs[:0:0]
	for _, ip := range addrs {
		if !drop[ip] {
			kept = append(kept, ip)
		}
	}
	return kept
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

// approvedByCluster is the organisation's approved agents by the cluster they report for.
func approvedByCluster(agentList []store.Agent) map[string]store.Agent {
	byCluster := map[string]store.Agent{}
	for _, o := range agentList {
		if o.Status == store.StatusApproved && o.ClusterID != "" {
			byCluster[o.ClusterID] = o
		}
	}
	return byCluster
}

// sharedHint is the note for a peer that is told about but has addresses another peer of the same cluster claims too: they are
// left out for both, and the pair is measured through the rest. Nil when none is shared.
func sharedHint(a, o store.Agent, pp *plannedPeer) *LinkNote {
	if pp == nil || len(pp.clash) == 0 || len(pp.addrs) == 0 {
		return nil
	}
	var shared []string
	for _, k := range kernel.SortedKeys(pp.clash) {
		shared = append(shared, pp.clash[k]...)
	}
	shared = dedupeSorted(shared)
	return &LinkNote{Cluster: a.ClusterID, Peer: o.ClusterID, Code: "shared-address", Text: fmt.Sprintf("%s shares %s with %s, another cluster %s is paired with, so those addresses are left out for both and %s is measured through its other addresses only.", o.ClusterID, listed(shared), listed(kernel.SortedKeys(pp.clash)), a.ClusterID, o.ClusterID)}
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
