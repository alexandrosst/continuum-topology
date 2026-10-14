// Package link measures the links between this cluster and the other clusters an administrator asked to watch.
//
// The server is the only place that knows which address belongs to which cluster, so it names the peers in the
// agent's Config (continuumv1.LinkPeer) and this package only ever works from that list: it labels what the node flow
// collectors already observe by the cluster pair it belongs to and publishes the result as Prometheus metrics. It
// reads no payloads and opens no connections of its own; active measurement lives in its sibling files.
package link

import (
	"net/netip"
	"slices"
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
	// SkipProbe says the other cluster of the pair runs the one-way probe and the connect timing, so this one only serves
	// them (its responder stays up) and exports no series of them. The route trace still runs here: the two directions can
	// take different routes. NoProbe wins: with it nothing is measured at all.
	SkipProbe bool
	// Via pins the active measurements (one-way probe, connect timing, route trace, throughput tests) to one kind of address
	// of the peer: mesh, private or public (see Class). Empty follows the traffic: the addresses it is seen using first.
	Via string
	// Preferred are the addresses of the peer the real traffic is seen using, the busiest first. The agent works them out from
	// what the flow collectors report and hands them to the prober, so the prober cannot tell more than the agent saw; they are
	// never exported as series.
	Preferred []netip.Addr
	// Throughput says this cluster runs the throughput tests towards the peer. ThroughputEvery replaces the cluster's own
	// interval between sets of tests (zero: keep it) and ThroughputBudget limits what they may carry towards this peer in a
	// day (zero: only the cluster's budget).
	Throughput       bool
	ThroughputEvery  time.Duration
	ThroughputBudget uint64
	// Iperf are the credentials of the throughput tests towards this peer, issued by the server to the cluster that runs the
	// tests. Nil: none (an older server, a manual Secret, or an agent that does not serve them). Secret: see Secret.
	Iperf *IperfClient
}

// Probed says this cluster runs the one-way probe and the connect timing towards the peer.
func (p Peer) Probed() bool { return !p.NoProbe && !p.SkipProbe }

// Traced says this cluster traces the route to the peer.
func (p Peer) Traced() bool { return !p.NoProbe }

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
	// dropIperf makes Set ignore the iperf3 credentials a Config carries: an agent that does not serve them to the prober (the
	// install uses a Secret of its own) has no use for them and does not keep them.
	dropIperf atomic.Bool
}

// DropIperf says whether the credentials of the throughput tests are to be left out of what Set keeps.
func (p *Peers) DropIperf(drop bool) { p.dropIperf.Store(drop) }

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
	return p.set(own, in, nil)
}

// set is Set with the addresses in use, by peer id, which only the prober is told (the agent keeps its own table in the
// meter). An address that is not among the peer's usable ones is ignored.
func (p *Peers) set(own string, in []*continuumv1.LinkPeer, preferred map[string][]string) (dropped []string) {
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
		pe := Peer{ID: lp.ClusterId, Name: lp.Name, NoProbe: lp.NoProbe, SkipProbe: lp.SkipProbe, Throughput: lp.Throughput}
		if ValidVia(lp.Via) {
			pe.Via = lp.Via // a kind this agent does not know is read as "follow the traffic"
		}
		if pe.Throughput {
			if s := lp.ThroughputIntervalSeconds; s > 0 {
				pe.ThroughputEvery = min(max(time.Duration(s)*time.Second, MinThroughputEvery), MaxThroughputEvery)
			}
			pe.ThroughputBudget = min(lp.ThroughputBudgetBytes, MaxThroughputBudget)
			if ic := lp.IperfClient; ic != nil && !p.dropIperf.Load() {
				c := IperfClient{PublicKeyPEM: ic.PublicKeyPem, Username: ic.Username, Password: Secret(ic.Password)}
				if err := c.Validate(); err != nil {
					dropped = append(dropped, "link peer "+lp.ClusterId+": the iperf3 credentials were refused: "+err.Error())
				} else {
					pe.Iperf = &c
				}
			}
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
			if o, ok := owner[a]; ok {
				// The same peer naming an address twice (also as ::ffff:a.b.c.d) is one address; another peer's is a clash.
				if o != lp.ClusterId {
					clash[a] = true
				}
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
	// A clashing address is unusable for everyone, whichever peer named it first: take it out of every peer's list (so
	// nothing probes what the passive side cannot attribute) and drop a peer that is left with nothing.
	if len(clash) > 0 {
		kept := next.peers[:0]
		for _, pe := range next.peers {
			addrs := make([]netip.Addr, 0, len(pe.Addresses))
			for _, a := range pe.Addresses {
				if !clash[a] {
					addrs = append(addrs, a)
				}
			}
			if len(addrs) == 0 {
				dropped = append(dropped, "link peer "+pe.ID+" has no usable address")
				continue
			}
			pe.Addresses = addrs
			kept = append(kept, pe)
		}
		next.peers = kept
	}
	for a, c := range owner {
		if !clash[a] {
			next.byIP[a] = c
		}
	}
	if len(clash) > 0 {
		dropped = append(dropped, strconv.Itoa(len(clash))+" address(es) belong to more than one peer cluster and are not used")
	}
	for i := range next.peers {
		next.peers[i].Preferred = usable(next.peers[i].Addresses, preferred[next.peers[i].ID])
	}
	sort.Slice(next.peers, func(i, j int) bool { return next.peers[i].ID < next.peers[j].ID })
	p.cur.Store(next)
	return dropped
}

// usable are the addresses in want that are among have, in the order of want and without repeats. have is the peer's accepted
// list, so a stale or invented preference cannot name an address the peer does not have.
func usable(have []netip.Addr, want []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range want {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap()
		if slices.Contains(have, a) && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// Own is this cluster's id as the last Config named it.
func (p *Peers) Own() string { return p.cur.Load().own }

// List is the accepted peers, sorted by id.
func (p *Peers) List() []Peer { return append([]Peer(nil), p.cur.Load().peers...) }

// Has reports whether id is one of the accepted peers, without copying the list.
func (p *Peers) Has(id string) bool {
	set := p.cur.Load()
	i := sort.Search(len(set.peers), func(i int) bool { return set.peers[i].ID >= id })
	return i < len(set.peers) && set.peers[i].ID == id
}

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
