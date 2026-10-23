package connectivity

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"ikhnos/internal/kernel"
	"ikhnos/internal/link"
	"ikhnos/internal/store"
)

// PairsOf is the pairs a cluster is in, by the id of the cluster at the other end.
func PairsOf(links []kernel.LinkSetting, cluster string) map[string]kernel.LinkSetting {
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

// PeersOf is the clusters paired with one cluster, sorted.
func PeersOf(links []kernel.LinkSetting, cluster string) []string {
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

// LinkRunner is the cluster of a pair that runs the one-way probe, the connect timing and the throughput tests: the one whose
// id sorts first. The other only serves them.
func LinkRunner(a, b string) string { return min(a, b) }

// PlannedPeer is one cluster paired with agent a's, as a's peer list holds it.
type PlannedPeer struct {
	ID    string
	Agent store.Agent
	// addrs is what a is told: the cluster's addresses less those of a's own cluster and those another peer of a claims too.
	Addrs []string
	// clash is, by the id of the other peer, the addresses both peers claim, which are left out of both.
	Clash map[string][]string
}

// PeerPlan is what peerList, linkNotes and the checklist all work from, so that what a pair says it is missing is exactly what
// the agent is not sent.
type PeerPlan struct {
	// byID holds every paired cluster that has an approved agent and an address a can tell from its own, told or not.
	ByID map[string]*PlannedPeer
	// sent are the peers a is told, in id order: those left with an address after the shared ones are removed, cut at
	// link.MaxPeers.
	Sent []*PlannedPeer
	// cut are the peers that had an address to be told but no room in the list.
	Cut map[string]bool
}

func WithoutAddresses(addrs []string, drop map[string]bool) []string {
	kept := addrs[:0:0]
	for _, ip := range addrs {
		if !drop[ip] {
			kept = append(kept, ip)
		}
	}
	return kept
}

func DedupeSorted(in []string) []string {
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

// NoteCode is what a LinkNote is about. The Sites screen keys on it, and the generated types give the page the whole list.
type NoteCode string

// The words of a NoteCode.
const (
	NoteNoAgent         NoteCode = "no-agent"           // the cluster has no approved agent
	NoteTier            NoteCode = "tier"               // its agent is below the access tier that link measuring needs
	NotePaused          NoteCode = "paused"             // its flow collector is paused, which pauses link measuring too
	NotePeerSilent      NoteCode = "peer-silent"        // the peer's agent has reported no node address yet
	NoteSharedAddress   NoteCode = "shared-address"     // addresses of the peer are claimed by another peer, or by the cluster itself
	NoteTooManyPeers    NoteCode = "too-many-peers"     // more peers than one agent holds
	NoteNoAddressOfKind NoteCode = "no-address-of-kind" // the pair asks for a kind of address the peer has none of
	NotePrivateRange    NoteCode = "private-range"      // a hint for a pair that measures nothing yet
)

// WireWords is the list of NoteCode's words, as the generated types give it.
func (NoteCode) WireWords() []string {
	return []string{string(NoteNoAgent), string(NoteTier), string(NotePaused), string(NotePeerSilent), string(NoteSharedAddress), string(NoteTooManyPeers), string(NoteNoAddressOfKind), string(NotePrivateRange)}
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
	Code NoteCode `json:"code"`
	Text string   `json:"text"`
}

// ApprovedByCluster is the organisation's approved agents by the cluster they report for.
func ApprovedByCluster(agentList []store.Agent) map[string]store.Agent {
	byCluster := map[string]store.Agent{}
	for _, o := range agentList {
		if o.Status == store.StatusApproved && o.ClusterID != "" {
			byCluster[o.ClusterID] = o
		}
	}
	return byCluster
}

// SharedHint is the note for a peer that is told about but has addresses another peer of the same cluster claims too: they are
// left out for both, and the pair is measured through the rest. Nil when none is shared.
func SharedHint(a, o store.Agent, pp *PlannedPeer) *LinkNote {
	if pp == nil || len(pp.Clash) == 0 || len(pp.Addrs) == 0 {
		return nil
	}
	var shared []string
	for _, k := range kernel.SortedKeys(pp.Clash) {
		shared = append(shared, pp.Clash[k]...)
	}
	shared = DedupeSorted(shared)
	return &LinkNote{Cluster: a.ClusterID, Peer: o.ClusterID, Code: NoteSharedAddress, Text: fmt.Sprintf("%s shares %s with %s, another cluster %s is paired with, so those addresses are left out for both and %s is measured through its other addresses only.", o.ClusterID, Listed(shared), Listed(kernel.SortedKeys(pp.Clash)), a.ClusterID, o.ClusterID)}
}

// maxListedAddresses bounds how many addresses (or clusters) a note spells out.
const maxListedAddresses = 6

// listed joins at most maxListedAddresses of items for a note's text and counts the rest: "a, b, c +4 more".
func Listed(items []string) string {
	if len(items) <= maxListedAddresses {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(items[:maxListedAddresses], ", "), len(items)-maxListedAddresses)
}

func HasAddress(addrs []string, s string) bool {
	for _, x := range addrs {
		if x == s {
			return true
		}
	}
	return false
}

// HasAddressOfKind says whether any of the addresses is of the kind via (see link.Class).
func HasAddressOfKind(addrs []string, via string) bool {
	for _, s := range addrs {
		if a, err := netip.ParseAddr(s); err == nil && link.Class(a) == via {
			return true
		}
	}
	return false
}
