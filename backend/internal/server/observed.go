package server

import (
	"container/heap"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/interpret"
	"continuum/internal/model"
	"continuum/internal/netid"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- what the server keeps: totals per distinct edge, per agent ----

const (
	maxFlowEdges     = 20000
	maxFlowsPerBatch = 5000
	// defaultStaleWindow only applies before any settings have ever been saved for an org (Core.Settings
	// falls back to DefaultSettings, whose FlowStaleSeconds already matches this); kept as a named
	// constant so this file's own tests can exercise observedTopology's staleness math without pulling
	// in the settings package.
	defaultStaleWindow = 5 * time.Minute
	// How long a newly-observed external address is withheld from the topology while its identity is
	// still unknown, giving netid.ResolveCached/ResolveASNCached (both async, both kicked off as soon as
	// this address is first seen) a chance to land before it's ever shown. Without this, a brand-new
	// address appears as a bare, unlabeled node on one poll and then - once resolution catches up a few
	// seconds later - vanishes and reappears grouped under its real name, which reads as a glitch rather
	// than the eventually-consistent behavior it actually is. Comfortably longer than one resolution
	// attempt (netid.resolveTimeout is 3s) plus one poll cycle (2-5s, see useServerPolling), so the common
	// single-lookup case resolves before the grace period even elapses; an address that's still unmatched
	// once it does elapse is shown anyway, still unlabeled, rather than staying invisible indefinitely.
	unmatchedGrace = 12 * time.Second
)

type flowTable struct {
	edges map[string]*continuumv1.FlowEdge
}

func newFlowTable() *flowTable { return &flowTable{edges: map[string]*continuumv1.FlowEdge{}} }

func endpointKey(e *continuumv1.FlowEndpoint) string {
	if e.Kind == continuumv1.FlowEndpoint_EXTERNAL {
		return "x:" + e.Ip
	}
	return fmt.Sprintf("%d:%s", e.Kind, e.Ref)
}

func flowKey(f *continuumv1.Flow) string {
	return endpointKey(f.Src) + ">" + endpointKey(f.Dst) + fmt.Sprintf("/%s:%d", f.Protocol, f.Port)
}

// validateFlowBatch bounds what one agent may report, and refuses anything malformed. It is the last
// line of defence against a compromised agent inventing traffic or filling memory.
func validateFlowBatch(b *continuumv1.FlowBatch) error {
	if len(b.Flows) > maxFlowsPerBatch || b.WindowSeconds < 1 || b.WindowSeconds > 24*3600 {
		return fmt.Errorf("flow batch is malformed")
	}
	if len(b.Collectors) > 5000 {
		return fmt.Errorf("flow batch is malformed")
	}
	for _, c := range b.Collectors {
		if c == nil || c.Node == "" || len(c.Node) > 253 || (c.Method != "ebpf" && c.Method != "conntrack") {
			return fmt.Errorf("collector info is malformed")
		}
	}
	for _, f := range b.Flows {
		if f.Src == nil || f.Dst == nil || f.Port == 0 || f.Port > 65535 || (f.Protocol != "tcp" && f.Protocol != "udp") || (f.Method != "ebpf" && f.Method != "conntrack") ||
			(f.Noise != "" && f.Noise != "dns" && f.Noise != "system") {
			return fmt.Errorf("flow is malformed")
		}
		if err := validateFlowEndpoints(f); err != nil {
			return err
		}
	}
	return nil
}

func validateFlowEndpoints(f *continuumv1.Flow) error {
	for _, e := range []*continuumv1.FlowEndpoint{f.Src, f.Dst} {
		switch e.Kind {
		case continuumv1.FlowEndpoint_WORKLOAD:
			if e.Ref == "" || len(e.Ref) > maxStr {
				return fmt.Errorf("flow endpoint is malformed")
			}
		case continuumv1.FlowEndpoint_EXTERNAL:
			if a, err := netip.ParseAddr(e.Ip); err != nil || a.String() != e.Ip || a.Is4In6() || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() {
				return fmt.Errorf("flow endpoint is malformed")
			}
		default:
			return fmt.Errorf("flow endpoint is malformed")
		}
	}
	if f.Src.Kind == continuumv1.FlowEndpoint_EXTERNAL && f.Dst.Kind == continuumv1.FlowEndpoint_EXTERNAL {
		return fmt.Errorf("flow has no workload end")
	}
	return nil
}

// validateFlowKey is the check a stored edge's identity must pass again when it is loaded.
func validateFlowKey(f *continuumv1.Flow) error {
	if f == nil || f.Src == nil || f.Dst == nil || f.Port == 0 || f.Port > 65535 || (f.Protocol != "tcp" && f.Protocol != "udp") {
		return fmt.Errorf("a stored flow is malformed")
	}
	return validateFlowEndpoints(f)
}

// maxStoredDNSNames mirrors aggregate.go's own maxHeldDNSNames - a resolver edge can legitimately field
// many different lookups over its life, but "recently asked about" is the point, not a full log.
const maxStoredDNSNames = 8

// mergeDNSNames folds add's distinct, non-empty names into cur, newest first, capped - the server-side
// twin of aggregate.go's function of the same name and shape (a different package, and a different
// struct's field, but the exact same list-vs-gauge reasoning: see model.Dependency.DnsQueryNames).
func mergeDNSNames(cur, add []string) []string {
	for _, n := range add {
		if n == "" {
			continue
		}
		found := false
		for _, c := range cur {
			if c == n {
				found = true
				break
			}
		}
		if !found {
			cur = append([]string{n}, cur...)
		}
	}
	if len(cur) > maxStoredDNSNames {
		cur = cur[:maxStoredDNSNames]
	}
	return cur
}

// satAdd adds without wrapping: a counter fed by a compromised agent sticks at the maximum instead of
// rolling over to a small number that would hide the traffic (or, added to, make it look like none).
func satAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}

func (t *flowTable) apply(b *continuumv1.FlowBatch, now time.Time) {
	// One batch, one instant: every edge touched by this call - new or existing, however many flows the
	// batch carries - shares the exact same timestamp, so there is no need for a fresh *timestamppb.
	// Timestamp allocation per flow (two, previously, on a new edge's first report) when one, reused by
	// every FirstSeen/LastSeen write this call makes, says the same thing. Nothing here ever mutates a
	// Timestamp in place (every reader goes through AsTime()), so sharing the pointer across however many
	// FlowEdges this call touches is safe.
	nowPb := timestamppb.New(now)
	for _, f := range b.Flows {
		k := flowKey(f)
		e := t.edges[k]
		if e == nil {
			e = &continuumv1.FlowEdge{Key: &continuumv1.Flow{Src: f.Src, Dst: f.Dst, Port: f.Port, Protocol: f.Protocol, Noise: f.Noise, Method: f.Method, Iface: f.Iface, RttUs: f.RttUs, JitterUs: f.JitterUs, HandshakeUs: f.HandshakeUs, Cwnd: f.Cwnd, PacingBps: f.PacingBps, DnsRttUs: f.DnsRttUs}, FirstSeen: nowPb}
			t.edges[k] = e
		}
		e.LastSeen = nowPb
		e.Connections = satAdd(e.Connections, f.Connections)
		e.BytesOut = satAdd(e.BytesOut, f.BytesOut)
		e.BytesIn = satAdd(e.BytesIn, f.BytesIn)
		e.Retransmits = satAdd(e.Retransmits, f.Retransmits)
		e.SegsOut = satAdd(e.SegsOut, uint64(f.SegsOut))
		e.BufferDrops = satAdd(e.BufferDrops, uint64(f.BufferDrops))
		e.FailedAttempts = satAdd(e.FailedAttempts, f.FailedAttempts)
		e.WindowSeconds, e.WindowConnections, e.WindowBytes, e.WindowRetransmits, e.WindowSegsOut, e.WindowBufferDrops, e.WindowFailedAttempts = b.WindowSeconds, f.Connections, satAdd(f.BytesOut, f.BytesIn), f.Retransmits, uint64(f.SegsOut), uint64(f.BufferDrops), f.FailedAttempts
		if f.BytesKnown {
			e.Key.BytesKnown = true
		}
		if f.Method == "ebpf" {
			e.Key.Method = "ebpf"
		}
		e.Key.Noise = f.Noise
		// Iface and RttUs are gauges, not identity or running totals: a route can change and RTT drifts
		// over a long-lived edge's life, so each report's non-empty/non-zero reading replaces the last
		// rather than being merged with it (see flow.c's own put_iface comment for the same reasoning).
		if f.Iface != "" {
			e.Key.Iface = f.Iface
		}
		if f.RttUs != 0 {
			e.Key.RttUs = f.RttUs
		}
		if f.JitterUs != 0 {
			e.Key.JitterUs = f.JitterUs
		}
		if f.HandshakeUs != 0 {
			e.Key.HandshakeUs = f.HandshakeUs
		}
		if f.Cwnd != 0 {
			e.Key.Cwnd = f.Cwnd
		}
		if f.PacingBps != 0 {
			e.Key.PacingBps = f.PacingBps
		}
		if f.DnsRttUs != 0 {
			e.Key.DnsRttUs = f.DnsRttUs
		}
		if f.SniHost != "" {
			e.Key.SniHost = f.SniHost // a gauge too, for the same reason as Iface/RttUs above
		}
		e.DnsQueryNames = mergeDNSNames(e.DnsQueryNames, f.DnsQueryNames)
	}
	if over := len(t.edges) - maxFlowEdges; over > 0 { // forget the `over` edges unseen for longest
		// Finds the `over` oldest entries in one O(n log over) pass instead of sort.Slice-ing the entire
		// table in O(n log n): over is normally just however many edges this one batch pushed past the
		// cap (often a handful), while n is the whole table, up to maxFlowEdges itself - at the cap, that
		// was a full 20000-entry sort on every single apply() call for the sake of evicting a few. See
		// oldestEdges' own doc for how the bounded heap gets there.
		for _, k := range oldestEdges(t.edges, over) {
			delete(t.edges, k)
		}
	}
}

// edgeAge is one edge's cache key paired with when it was last seen - the only two fields oldestEdges
// needs to pick evictions, so apply() never has to copy a whole *continuumv1.FlowEdge just to sort by one
// of its fields.
type edgeAge struct {
	key string
	at  time.Time
}

// ageHeap is a bounded max-heap of the oldest-looking edgeAges seen so far during a single linear scan: its
// root (index 0) is always the entry with the LATEST `at` among those currently held. That sounds backwards
// for a heap of "oldest" entries, but it's exactly what oldestEdges needs: once the heap holds `n` entries,
// the single edge most likely to NOT belong in the final "n oldest" answer is whichever one is currently
// the newest of the bunch, i.e. the root - so a new, genuinely older candidate only ever needs to evict that
// one entry, never re-examine the rest.
type ageHeap []edgeAge

func (h ageHeap) Len() int           { return len(h) }
func (h ageHeap) Less(i, j int) bool { return h[i].at.After(h[j].at) }
func (h ageHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *ageHeap) Push(x any)        { *h = append(*h, x.(edgeAge)) }
func (h *ageHeap) Pop() any {
	old := *h
	last := len(old) - 1
	x := old[last]
	*h = old[:last]
	return x
}

// oldestEdges returns the keys of the n least-recently-seen edges in edges, without ever sorting the whole
// map: it scans every entry exactly once, keeping only a bounded max-heap of size n (the n oldest seen so
// far). Once that heap is full, each further candidate either stays out immediately (it's newer than
// everything already kept - a single comparison against the heap's root) or swaps in for the current
// newest kept entry. Both cases cost O(log n), so the whole scan is O(len(edges) * log n) - the same
// eviction result sort.Slice would give, for less work whenever n (how many are actually being evicted)
// is smaller than the table itself, which is the normal case here: n is usually just one batch's overflow,
// not the whole multi-thousand-edge table.
func oldestEdges(edges map[string]*continuumv1.FlowEdge, n int) []string {
	if n <= 0 {
		return nil
	}
	h := make(ageHeap, 0, n)
	for k, e := range edges {
		age := edgeAge{k, e.LastSeen.AsTime()}
		switch {
		case len(h) < n:
			heap.Push(&h, age)
		case age.at.Before(h[0].at):
			heap.Pop(&h)
			heap.Push(&h, age)
		}
	}
	out := make([]string, len(h))
	for i, a := range h {
		out[i] = a.key
	}
	return out
}

func (t *flowTable) marshal() ([]byte, error) {
	m := &continuumv1.FlowTable{}
	keys := make([]string, 0, len(t.edges))
	for k := range t.edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.Edges = append(m.Edges, t.edges[k])
	}
	return proto.Marshal(m)
}

func unmarshalFlowTable(b []byte) (*flowTable, error) {
	var m continuumv1.FlowTable
	if err := proto.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	t := newFlowTable()
	for _, e := range m.Edges {
		if e.Key != nil && e.Key.Src != nil && e.Key.Dst != nil && e.FirstSeen != nil && e.LastSeen != nil {
			t.edges[flowKey(e.Key)] = e
		}
	}
	return t, nil
}

// ---- turning edges into dependencies, across clusters ----

// observedCluster is one onboarded cluster as far as traffic resolution is concerned.
type observedCluster struct {
	id, name, agentID string
	state             *facts.State
	flows             *flowTable
	egressIP          string
}

type target struct{ cluster, workload string }

type addrIndex struct {
	reach     map[string][]target // "ip:port" of a load balancer or external IP -> workload
	nodeIPs   map[string][]string // node address -> clusters
	// nodePorts is "this cluster's own node's port" -> workload: a port reachable on any address one of
	// this cluster's own nodes has, regardless of which specific address was dialed - populated from
	// both an explicit NodePort and a load-balancer whose port binds across every node address (see
	// the load-balancer case in buildAddrIndex for why the latter belongs here too).
	nodePorts map[string]map[int32][]string
	egress    map[string][]string // a cluster's connecting address -> clusters
}

func uniq(in []string) []string {
	// The overwhelmingly common case at every call site here (a single node IP, a single owning cluster)
	// is 0 or 1 elements, already trivially unique and already sorted - skip the map allocation and the
	// sort for it.
	if len(in) <= 1 {
		return in
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func buildAddrIndex(cs []observedCluster) *addrIndex {
	ix := &addrIndex{reach: map[string][]target{}, nodeIPs: map[string][]string{}, nodePorts: map[string]map[int32][]string{}, egress: map[string][]string{}}
	for _, c := range cs {
		if c.egressIP != "" {
			ix.egress[c.egressIP] = append(ix.egress[c.egressIP], c.id)
		}
		for _, n := range c.state.Nodes {
			for _, ip := range append(append([]string{}, n.InternalIps...), n.ExternalIps...) {
				ix.nodeIPs[ip] = append(ix.nodeIPs[ip], c.id)
			}
			// A node's own overlay/tunnel addresses (a Netbird, Tailscale or other mesh peer address,
			// for example) are just as much this cluster's own address space as its InternalIps/
			// ExternalIps above - when two clusters are joined only by such a mesh, the traffic this
			// agent reports between them is addressed to exactly this, never to a Kubernetes-visible
			// node IP. Without this, resolveExternal/clusterOfAddr below have no way to recognize the
			// peer side of that traffic as belonging to a known cluster at all, and every such flow
			// falls through to being recorded as an unresolved external endpoint, by its raw tunnel IP,
			// forever. Indexed unconditionally (not gated on TunnelInterface.Confirmed): confirmation is
			// a cross-node correlation verdict that correlateTunnels computes later, from topo.Nodes,
			// after observedTopology has already run - this index only needs to know "this address is
			// one of this node's own, reported by its own probe," which is true whether or not the
			// far end is independently confirmed elsewhere in the topology.
			if n.Probe != nil {
				for _, t := range n.Probe.Tunnels {
					if t == nil {
						continue
					}
					for _, a := range t.Addresses {
						if p, err := netip.ParsePrefix(a); err == nil {
							ip := p.Addr().String()
							ix.nodeIPs[ip] = append(ix.nodeIPs[ip], c.id)
						}
					}
				}
			}
		}
		for _, w := range c.state.Workloads {
			for _, a := range w.Reachable {
				switch a.Kind {
				case "node-port":
					if ix.nodePorts[c.id] == nil {
						ix.nodePorts[c.id] = map[int32][]string{}
					}
					ix.nodePorts[c.id][a.Port] = append(ix.nodePorts[c.id][a.Port], w.Key)
				case "load-balancer":
					k := fmt.Sprintf("%s:%d", a.Ip, a.Port)
					ix.reach[k] = append(ix.reach[k], target{c.id, w.Key})
					// A load balancer's reported ingress address is not always the only address its
					// port is actually reachable on. k3s's built-in ServiceLB (klipper-lb) and other
					// host-network-bound implementations report one of the cluster's own node
					// addresses as the ingress IP and then bind the service's port on that node's
					// host network - which means every address that node has, including one never
					// reported as an ingress address at all, such as a Netbird/Tailscale mesh peer
					// address. Recorded by (cluster, port) alone, the same as node-port just above and
					// for the same reason: resolveExternal only ever consults this after it already
					// knows the destination address belongs to one of this cluster's own nodes (via
					// nodeIPs), so a dedicated, per-service load-balancer VIP (MetalLB and similar)
					// can never be misattributed this way - a real VIP is never also one of a node's
					// own reported addresses.
					if ix.nodePorts[c.id] == nil {
						ix.nodePorts[c.id] = map[int32][]string{}
					}
					ix.nodePorts[c.id][a.Port] = append(ix.nodePorts[c.id][a.Port], w.Key)
				default:
					k := fmt.Sprintf("%s:%d", a.Ip, a.Port)
					ix.reach[k] = append(ix.reach[k], target{c.id, w.Key})
				}
			}
		}
	}
	return ix
}

// wellKnownPort names the application usually found on a port, purely from the port number: a guess,
// the same way any other soft signal in this system is a guess, never a fact - a workload can run
// anything on any port, and this never claims otherwise (nothing here reads a single byte of payload;
// see flow.c's own boundary comment). It exists because the port is already known for free from what
// the collector already reports, so naming a well-known one costs nothing further to compute and beats
// showing only "tcp:5432" when the number means something to nobody who doesn't have it memorized.
// The database-only subset (isDatabase) keeps dbPort's older, narrower classification for
// ExternalEndpoint.Kind, whose three values ("saas" | "database" | "unknown") predate this table and
// stay as they are; the full name additionally lands in the new Service field on both Dependency and
// ExternalEndpoint.
var wellKnownPorts = map[uint32]struct {
	name       string
	isDatabase bool
}{
	5432:  {"PostgreSQL", true},
	3306:  {"MySQL/MariaDB", true},
	6379:  {"Redis", true},
	27017: {"MongoDB", true},
	9042:  {"Cassandra", true},
	11211: {"Memcached", true},
	1433:  {"SQL Server", true},
	1521:  {"Oracle", true},
	5984:  {"CouchDB", true},
	7000:  {"Cassandra (inter-node)", true},
	9200:  {"Elasticsearch", true},
	2181:  {"ZooKeeper", true},
	9092:  {"Kafka", true},
	5672:  {"RabbitMQ (AMQP)", true},
	53:    {"DNS", false},
	80:    {"HTTP", false},
	443:   {"HTTPS/TLS", false},
	8080:  {"HTTP", false},
	8443:  {"HTTPS/TLS", false},
	22:    {"SSH", false},
	25:    {"SMTP", false},
	587:   {"SMTP (submission)", false},
}

func wellKnownPort(p uint32) (name string, isDatabase bool) {
	w, ok := wellKnownPorts[p]
	if !ok {
		return "", false
	}
	return w.name, w.isDatabase
}

func dbPort(p uint32) bool {
	_, isDatabase := wellKnownPort(p)
	return isDatabase
}

// observedTopology derives dependencies and external endpoints from every cluster's flows.
func observedTopology(org string, cs []observedCluster, now time.Time, stale time.Duration) ([]model.Dependency, []model.ExternalEndpoint) {
	ix := buildAddrIndex(cs)
	byID := map[string]*observedCluster{}
	for i := range cs {
		byID[cs[i].id] = &cs[i]
	}
	deps := map[string]*model.Dependency{}
	exts := map[string]model.ExternalEndpoint{}
	// lossRetransmits/lossSegsOut hold the running sums behind each dependency's LossPct, kept apart from
	// the numbers already summed above (ConnectionsPerMin and friends) because a real loss percentage is a
	// ratio, not a rate: summing retransmits/segs_out per edge and then adding those ratios together (as
	// this used to do) is wrong whenever two edges merging into one Dependency have different segs_out -
	// the fix is to sum numerator and denominator separately here and divide exactly once, below, after
	// every contributing edge has been added.
	lossRetransmits := map[string]uint64{}
	lossSegsOut := map[string]uint64{}
	stamp := now.UTC().Format(time.RFC3339)

	external := func(agentID, ip string, port uint32, note string, firstSeen time.Time) (string, bool) {
		id := "ext-" + interpret.Hash("obs", ip, fmt.Sprint(port))
		// A match against a single-owner range (Shared == false) is a stable identity: every address
		// that resolves to it really is "the same thing" (every GitHub IP is github.com), so those
		// endpoints share one id instead of one per address - this is what lets several IPs collapse
		// into one topology node. A Shared match (a CDN edge fronting many unrelated origins) never
		// changes the id: two different sites sitting behind the same edge are not the same thing just
		// because they share it, so each keeps its own per-(ip,port) identity as it does today.
		match, matched, resolvedHost, resolvedASN := netid.Match{}, false, "", ""
		if addr, err := netip.ParseAddr(ip); err == nil {
			match, matched = netid.Lookup(addr)
			if !matched {
				// No static range covers this address (most of the internet doesn't, by design - see
				// netid's own doc). Fall back to a cached reverse-DNS lookup: this never blocks the
				// caller (a cache miss just starts a background resolution and returns not-yet-known),
				// so a first sighting of a new IP shows up unlabeled and picks up its label on a later
				// poll once the lookup lands - the same eventually-consistent pattern this app already
				// uses everywhere else for poll-derived data.
				if host, ok := netid.ResolveCached(ip); ok {
					resolvedHost = host
					match, matched = netid.MatchHost(host)
				}
			}
			if !matched {
				// Neither a curated range nor a known reverse-DNS suffix: ask who originates this address
				// on the public internet (IP-to-ASN, also cached/non-blocking, same eventually-consistent
				// shape as the reverse-DNS fallback above) rather than leaving it a bare, unlabeled IP.
				// This is the one tier that needs no per-provider maintenance - a provider we've never
				// hand-curated still gets a real name. It can't tell us Shared vs not, so it always reports
				// Shared: true - a safe default that keeps unrelated addresses as separate nodes rather
				// than risking a wrong merge.
				if org, asnLabel, ok := netid.ResolveASNCached(ip); ok {
					resolvedASN = asnLabel
					match = netid.Match{
						Name:   org,
						Kind:   "unknown",
						Shared: true,
						Detail: "Identified by IP-to-ASN lookup (" + asnLabel + "): this names the network operator, not a hand-curated or reverse-DNS-verified match, so the actual service behind it isn't confirmed.",
					}
					matched = true
				}
			}
			if matched && !match.Shared {
				// Port stays part of the identity even for a known match: several IPs that are all
				// "github.com" collapse into one node per port, so git-over-SSH (22) and the HTTPS API
				// (443) still show as the separate endpoints they are, rather than one node hiding two
				// different protocols worth of traffic.
				id = "ext-" + interpret.Hash("obs-known", match.Name, fmt.Sprint(port))
			}
		}
		if !matched && now.Sub(firstSeen) < unmatchedGrace {
			// Still within the grace window and nothing identified it yet (see unmatchedGrace's own
			// comment) - don't add it to the topology on this poll. The caller must skip creating a
			// Dependency for it too, not just skip storing it here, or the dependency would point at an
			// external endpoint id that was never added to exts.
			return "", false
		}
		if e, ok := exts[id]; !ok {
			kind := "unknown"
			svc, isDatabase := wellKnownPort(port)
			if isDatabase {
				kind = "database"
			}
			var name string
			if matched {
				kind, name = match.Kind, match.Name
			}
			e := model.ExternalEndpoint{
				Provenance: model.Provenance{OrgID: org, Source: "discovered", Key: "obs/" + ip, LastSeen: stamp, DetectedAt: stamp, AgentID: agentID},
				ID:         id, Host: ip, Port: int(port), Kind: kind, Service: svc, Name: name, IPs: []string{ip},
			}
			switch {
			case matched && resolvedHost != "":
				// Reverse DNS, not a hand-curated range: still useful (a label beats a bare IP), but a
				// resolver's answer is inherently a notch less certain than a range the provider
				// themselves published, so this is marked medium rather than high confidence, and the
				// resolved hostname is kept in Detail as the evidence for that judgment.
				e.Evidence = map[string]model.Evidence{"identity": {
					Signal:     "reverse DNS resolved to " + resolvedHost + ", matching known provider: " + match.Name,
					Confidence: "medium",
					Detail:     match.Detail,
				}}
			case matched && resolvedASN != "":
				// Neither a curated range nor a reverse-DNS suffix - an IP-to-ASN lookup named the network
				// operator instead. The weakest of the three signals (it names who announces the address on
				// the public internet, not necessarily who is actually running the service on it), so this
				// gets low rather than medium/high confidence.
				e.Evidence = map[string]model.Evidence{"identity": {
					Signal:     "IP-to-ASN lookup identified the network operator (" + resolvedASN + "): " + match.Name,
					Confidence: "low",
					Detail:     match.Detail,
				}}
			case matched:
				ev := model.Evidence{Signal: "matched a known public range: " + match.Name, Confidence: "high"}
				if match.Detail != "" {
					ev.Detail = match.Detail
				}
				e.Evidence = map[string]model.Evidence{"identity": ev}
			case note != "":
				e.Evidence = map[string]model.Evidence{"identity": {Signal: note, Confidence: "low"}}
			}
			exts[id] = e
		} else if !slices.Contains(e.IPs, ip) {
			// A later address collapsing into an id created by an earlier one (a non-Shared provider
			// match, e.g. several of Google's or GitHub's own addresses) - record it too, so a viewer can
			// still see every individual address that made up this node's traffic, sorted for a stable
			// order across polls, rather than losing everything but whichever address happened to be
			// seen first.
			e.IPs = append(e.IPs, ip)
			sort.Strings(e.IPs)
			exts[id] = e
		}
		return id, true
	}
	serviceOf := func(cluster, key string) (string, bool) {
		c := byID[cluster]
		if c == nil || c.state.Workloads[key] == nil {
			return "", false
		}
		return interpret.ServiceID(cluster, key), true
	}
	// resolveExternal says which workload (in which cluster) an outside address is, when that can be told.
	resolveExternal := func(from string, ip string, port uint32) (t target, note string, ok bool) {
		var cands []target
		cands = append(cands, ix.reach[fmt.Sprintf("%s:%d", ip, port)]...)
		for _, cl := range uniq(ix.nodeIPs[ip]) {
			for _, key := range ix.nodePorts[cl][int32(port)] {
				cands = append(cands, target{cl, key})
			}
		}
		seen := map[target]bool{}
		var distinct []target
		for _, c := range cands {
			if !seen[c] {
				seen[c] = true
				distinct = append(distinct, c)
			}
		}
		sort.Slice(distinct, func(i, j int) bool {
			return distinct[i].cluster+distinct[i].workload < distinct[j].cluster+distinct[j].workload
		})
		switch len(distinct) {
		case 0:
			return target{}, "", false
		case 1:
			return distinct[0], "matched by address to a load balancer or node port of that workload", true
		}
		clusters := map[string]bool{}
		for _, d := range distinct {
			clusters[d.cluster] = true
		}
		return target{}, fmt.Sprintf("address matches %d workloads in %d clusters; not resolved", len(distinct), len(clusters)), false
	}
	clusterOfAddr := func(ip string) (string, bool) {
		if c := uniq(ix.egress[ip]); len(c) == 1 {
			return c[0], true
		}
		if c := uniq(ix.nodeIPs[ip]); len(c) == 1 {
			return c[0], true
		}
		return "", false
	}

	type pending struct {
		c *observedCluster
		e *continuumv1.FlowEdge
	}
	var inbound []pending
	outboundTo := map[string]bool{} // "srcCluster>dstServiceID" for outbound edges that resolved to a workload in another cluster

	add := func(from, fromKind, to, toKind string, e *continuumv1.FlowEdge, cross bool, note string) {
		port := int(e.Key.Port)
		// Protocol is part of the identity, not just a field on it: a workload very commonly talks to
		// the same peer on the same port over both UDP and TCP (DNS being the obvious case - UDP first,
		// TCP fallback for large answers), and those are two distinct edges in flowTable/flowKey. Leaving
		// protocol out of this hash used to merge such pairs into one Dependency, whose Protocol field
		// then depended on map iteration order (non-deterministic) and whose Connections/Bytes silently
		// summed traffic from two different protocols under one label.
		id := "dep-obs-" + interpret.Hash(from, to, e.Key.Protocol, fmt.Sprint(port))
		d := deps[id]
		if d == nil {
			svc, _ := wellKnownPort(e.Key.Port)
			d = &model.Dependency{ID: id, OrgID: org, From: from, FromKind: fromKind, To: to, ToKind: toKind, Sources: []string{"observed"},
				Confidence: "high", Protocol: strings.ToUpper(e.Key.Protocol), Port: port, Service: svc, Via: e.Key.Method, CrossCluster: cross, Noise: e.Key.Noise, Note: note,
				FirstSeen: e.FirstSeen.AsTime().UTC().Format(time.RFC3339), LastSeen: e.LastSeen.AsTime().UTC().Format(time.RFC3339)}
			if cross && note != "" {
				d.Confidence = "medium"
			}
			deps[id] = d
		}
		d.Connections = satAdd(d.Connections, e.Connections)
		d.Bytes = satAdd(d.Bytes, satAdd(e.BytesOut, e.BytesIn))
		if fs := e.FirstSeen.AsTime().UTC().Format(time.RFC3339); fs < d.FirstSeen {
			d.FirstSeen = fs
		}
		if ls := e.LastSeen.AsTime().UTC().Format(time.RFC3339); ls > d.LastSeen {
			d.LastSeen = ls
		}
		if e.Key.Method == "ebpf" {
			d.Via = "ebpf"
		}
		if e.Key.Iface != "" {
			d.Iface = e.Key.Iface
		}
		if e.Key.RttUs != 0 {
			d.RttMs = float64(e.Key.RttUs) / 1000
		}
		if e.Key.JitterUs != 0 {
			d.JitterMs = float64(e.Key.JitterUs) / 1000
		}
		if e.Key.HandshakeUs != 0 {
			d.HandshakeMs = float64(e.Key.HandshakeUs) / 1000
		}
		if e.Key.Cwnd != 0 {
			d.CwndSegments = e.Key.Cwnd
		}
		if e.Key.PacingBps != 0 {
			d.PacingBps = e.Key.PacingBps
		}
		if e.Key.DnsRttUs != 0 {
			d.DnsRttMs = float64(e.Key.DnsRttUs) / 1000
		}
		d.Retransmits = satAdd(d.Retransmits, e.Retransmits)
		d.BufferDrops = satAdd(d.BufferDrops, e.BufferDrops)
		d.FailedAttempts = satAdd(d.FailedAttempts, e.FailedAttempts)
		if e.Key.SniHost != "" {
			d.SniHost = e.Key.SniHost
		}
		d.DnsQueryNames = mergeDNSNames(d.DnsQueryNames, e.DnsQueryNames)
		if e.Key.Noise == "" {
			d.Noise = ""
		}
		if e.WindowSeconds > 0 {
			st := d.Stats
			if st == nil {
				st = &model.DependencyStats{}
				d.Stats = st
			}
			st.WindowSec = e.WindowSeconds
			st.ConnectionsPerMin += float64(e.WindowConnections) * 60 / float64(e.WindowSeconds)
			st.RetransmitsPerMin += float64(e.WindowRetransmits) * 60 / float64(e.WindowSeconds)
			st.FailedAttemptsPerMin += float64(e.WindowFailedAttempts) * 60 / float64(e.WindowSeconds)
			if e.Key.BytesKnown {
				st.BytesPerSec += float64(e.WindowBytes) / float64(e.WindowSeconds)
			}
			// A real loss percentage, not just a raw retransmit count - but a ratio, so it is not safe to
			// accumulate the way the *PerMin rates above are: sum the raw numerator and denominator here,
			// per edge, and divide exactly once in the final pass below, once every edge contributing to
			// this Dependency across this poll has been added.
			if e.WindowSegsOut > 0 {
				lossRetransmits[id] += e.WindowRetransmits
				lossSegsOut[id] += e.WindowSegsOut
			}
		}
	}

	for i := range cs {
		c := &cs[i]
		for _, e := range c.flows.edges {
			if e.Key.Src.Kind == continuumv1.FlowEndpoint_EXTERNAL {
				inbound = append(inbound, pending{c, e})
				continue
			}
			from, ok := serviceOf(c.id, e.Key.Src.Ref)
			if !ok {
				continue
			}
			switch e.Key.Dst.Kind {
			case continuumv1.FlowEndpoint_WORKLOAD:
				if to, ok := serviceOf(c.id, e.Key.Dst.Ref); ok {
					add(from, "service", to, "service", e, false, "")
				}
			case continuumv1.FlowEndpoint_EXTERNAL:
				ip := e.Key.Dst.Ip
				if t, note, ok := resolveExternal(c.id, ip, e.Key.Port); ok {
					if to, ok := serviceOf(t.cluster, t.workload); ok {
						cross := t.cluster != c.id
						add(from, "service", to, "service", e, cross, note)
						if cross {
							outboundTo[c.id+">"+to] = true
						}
						continue
					}
				} else if note != "" {
					if id, ready := external(c.agentID, ip, e.Key.Port, note, e.FirstSeen.AsTime()); ready {
						add(from, "service", id, "external", e, false, note)
					}
					continue
				}
				what := ""
				if cl, ok := clusterOfAddr(ip); ok && cl != c.id {
					what = "address belongs to cluster " + byID[cl].name
				}
				if id, ready := external(c.agentID, ip, e.Key.Port, what, e.FirstSeen.AsTime()); ready {
					add(from, "service", id, "external", e, false, what)
				}
			}
		}
	}
	// Connections that arrived from outside. When the caller is another onboarded cluster whose own
	// agent already reported the outbound edge to this workload, that edge is the better description
	// (it names the calling workload), so the inbound record is not added a second time.
	for _, p := range inbound {
		to, ok := serviceOf(p.c.id, p.e.Key.Dst.Ref)
		if !ok {
			continue
		}
		ip := p.e.Key.Src.Ip
		if cl, ok := clusterOfAddr(ip); ok && cl != p.c.id {
			// outboundTo is already keyed exactly "cl>to" - a direct lookup, not a scan over every entry.
			if outboundTo[cl+">"+to] {
				continue
			}
			if id, ready := external(p.c.agentID, ip, 0, "address belongs to cluster "+byID[cl].name, p.e.FirstSeen.AsTime()); ready {
				add(id, "external", to, "service", p.e, true, "caller identified only as a cluster, by its address")
			}
			continue
		}
		if id, ready := external(p.c.agentID, ip, 0, "", p.e.FirstSeen.AsTime()); ready {
			add(id, "external", to, "service", p.e, false, "")
		}
	}

	var outDeps []model.Dependency
	for _, d := range deps {
		if last, err := time.Parse(time.RFC3339, d.LastSeen); err == nil && now.Sub(last) > stale {
			d.Stale = true
		}
		if segs := lossSegsOut[d.ID]; segs > 0 {
			pct := float64(lossRetransmits[d.ID]) / float64(segs) * 100
			d.Stats.LossPct = &pct
		}
		outDeps = append(outDeps, *d)
	}
	sort.Slice(outDeps, func(i, j int) bool { return outDeps[i].ID < outDeps[j].ID })
	outExt := make([]model.ExternalEndpoint, 0, len(exts))
	for _, e := range exts {
		outExt = append(outExt, e)
	}
	sort.Slice(outExt, func(i, j int) bool { return outExt[i].ID < outExt[j].ID })
	return outDeps, outExt
}

// ---- observer health: what the traffic observer says about itself ----

// CollectorDoc is one node's collector as the dashboard shows it.
type CollectorDoc struct {
	Node       string `json:"node"`
	Method     string `json:"method"`
	BytesKnown bool   `json:"bytesKnown"`
}

// ObserverDoc summarises the collectors behind an agent: which nodes are observed, how, and what was
// dropped, so a quiet graph can be told apart from a blind one.
type ObserverDoc struct {
	LastReport string         `json:"lastReport"`
	Collectors []CollectorDoc `json:"collectors"`
	// Lost counts observations the collectors had to discard since the server last started.
	Lost uint64 `json:"lost"`
}

type observerHealth struct {
	at         time.Time
	collectors []*continuumv1.CollectorInfo
	lost       uint64
}

func (o *observerHealth) note(b *continuumv1.FlowBatch, now time.Time) {
	o.at, o.collectors = now, b.Collectors
	o.lost += b.Lost
}

func (o *observerHealth) doc(now time.Time) *ObserverDoc {
	if o.at.IsZero() {
		return nil
	}
	d := &ObserverDoc{LastReport: o.at.UTC().Format(time.RFC3339), Lost: o.lost, Collectors: []CollectorDoc{}}
	// A collector list older than the reporting TTL says nothing about who is observed now.
	if now.Sub(o.at) < 10*time.Minute {
		for _, c := range o.collectors {
			d.Collectors = append(d.Collectors, CollectorDoc{Node: c.Node, Method: c.Method, BytesKnown: c.BytesKnown})
		}
	}
	return d
}
