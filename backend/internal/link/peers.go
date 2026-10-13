// Package link measures the links between this cluster and the other clusters an administrator asked to watch.
//
// The server is the only place that knows which address belongs to which cluster, so it names the peers in the
// agent's Config (continuumv1.LinkPeer) and this package only ever works from that list: it labels what the node flow
// collectors already observe by the cluster pair it belongs to and publishes the result as Prometheus metrics. It
// reads no payloads and opens no connections of its own; active measurement lives in its sibling files.
package link

import (
	"net/netip"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

const (
	// MaxPeers and MaxAddresses bound what a Config may make the agent hold, whatever the server sends.
	MaxPeers     = 64
	MaxAddresses = 256
)

// Peer is one other cluster, as the agent accepts it.
type Peer struct {
	ID        string
	Name      string
	Addresses []netip.Addr

	// What the server asked to be measured towards this peer. The defaults, all zero, are the one-way probe on and no
	// throughput tests, so a Config that does not say is read as before.
	NoProbe bool
	// Throughput says this cluster runs the throughput tests towards the peer. ThroughputEvery replaces the cluster's own
	// interval between sets of tests (zero: keep it) and ThroughputBudget limits what they may carry towards this peer in a
	// day (zero: only the cluster's budget).
	Throughput       bool
	ThroughputEvery  time.Duration
	ThroughputBudget uint64
}

// Bounds on what a Config may ask for, whatever the server sends: the same as the server enforces, so a bug or a hostile
// server cannot make the agent test a link every few seconds.
const (
	MinThroughputEvery  = 10 * time.Minute
	MaxThroughputEvery  = 7 * 24 * time.Hour
	MaxThroughputBudget = 1 << 40
)

// Peers is the set of clusters this one measures its links to. It is replaced as a whole when a new Config arrives and
// read without locks by the flow path.
type Peers struct {
	cur atomic.Pointer[peerSet]
}

type peerSet struct {
	own   string
	peers []Peer // sorted by ID
	byIP  map[netip.Addr]string
}

// NewPeers is an empty set.
func NewPeers() *Peers {
	p := &Peers{}
	p.cur.Store(&peerSet{byIP: map[netip.Addr]string{}})
	return p
}

// Set replaces the peers with those a Config names. It returns what it dropped and why, so the agent can say so. An
// address two peers both claim is dropped from both (a flow to it cannot be placed), and a peer with no usable
// address is dropped.
func (p *Peers) Set(own string, in []*continuumv1.LinkPeer) (dropped []string) {
	next := &peerSet{own: own, byIP: map[netip.Addr]string{}}
	owner := map[netip.Addr]string{}
	clash := map[netip.Addr]bool{}
	seen := map[string]bool{}
	for _, lp := range in {
		if lp == nil || lp.ClusterId == "" || lp.ClusterId == own || seen[lp.ClusterId] {
			continue
		}
		if len(next.peers) >= MaxPeers {
			dropped = append(dropped, "more than "+strconv.Itoa(MaxPeers)+" link peers; the rest were left out")
			break
		}
		seen[lp.ClusterId] = true
		pe := Peer{ID: lp.ClusterId, Name: lp.Name, NoProbe: lp.NoProbe, Throughput: lp.Throughput}
		if pe.Throughput {
			if s := lp.ThroughputIntervalSeconds; s > 0 {
				pe.ThroughputEvery = min(max(time.Duration(s)*time.Second, MinThroughputEvery), MaxThroughputEvery)
			}
			pe.ThroughputBudget = min(lp.ThroughputBudgetBytes, MaxThroughputBudget)
		}
		for i, s := range lp.Addresses {
			if i >= MaxAddresses {
				dropped = append(dropped, "link peer "+lp.ClusterId+" has more than "+strconv.Itoa(MaxAddresses)+" addresses; the rest were left out")
				break
			}
			a, err := netip.ParseAddr(s)
			if err != nil {
				continue
			}
			a = a.Unmap()
			if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() {
				continue
			}
			if o, ok := owner[a]; ok && o != lp.ClusterId {
				clash[a] = true
				continue
			}
			owner[a] = lp.ClusterId
			pe.Addresses = append(pe.Addresses, a)
		}
		if len(pe.Addresses) == 0 {
			dropped = append(dropped, "link peer "+lp.ClusterId+" has no usable address")
			continue
		}
		next.peers = append(next.peers, pe)
	}
	for a, c := range owner {
		if !clash[a] {
			next.byIP[a] = c
		}
	}
	if len(clash) > 0 {
		dropped = append(dropped, strconv.Itoa(len(clash))+" address(es) belong to more than one peer cluster and are not used")
	}
	sort.Slice(next.peers, func(i, j int) bool { return next.peers[i].ID < next.peers[j].ID })
	p.cur.Store(next)
	return dropped
}

// Own is this cluster's id as the last Config named it.
func (p *Peers) Own() string { return p.cur.Load().own }

// List is the accepted peers, sorted by id.
func (p *Peers) List() []Peer { return append([]Peer(nil), p.cur.Load().peers...) }

// Lookup says which peer cluster an address belongs to; the empty string means none.
func (p *Peers) Lookup(ip string) string {
	set := p.cur.Load()
	if len(set.byIP) == 0 {
		return ""
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	return set.byIP[a.Unmap()]
}

// Empty reports whether no link is being measured.
func (p *Peers) Empty() bool { return len(p.cur.Load().peers) == 0 }
