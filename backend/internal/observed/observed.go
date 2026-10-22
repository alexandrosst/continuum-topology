package observed

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/flow/wire"
	"continuum/internal/interpret"
	"continuum/internal/model"
	"continuum/internal/netid"
	"continuum/internal/probe"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- what the server keeps: totals per distinct edge, per agent ----

const (
	MaxFlowEdges     = 20000
	maxFlowsPerBatch = 5000
	// DefaultStaleWindow only applies before any settings have ever been saved for an org (Core.Settings
	// falls back to DefaultSettings, whose FlowStaleSeconds already matches this); kept as a named
	// constant so this file's own tests can exercise Topology's staleness math without pulling
	// in the settings package.
	DefaultStaleWindow = 5 * time.Minute
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

type FlowTable struct {
	Edges map[string]*continuumv1.FlowEdge
}

func NewFlowTable() *FlowTable { return &FlowTable{Edges: map[string]*continuumv1.FlowEdge{}} }

func endpointKey(e *continuumv1.FlowEndpoint) string {
	if e.Kind == continuumv1.FlowEndpoint_EXTERNAL {
		return "x:" + e.Ip
	}
	return fmt.Sprintf("%d:%s", e.Kind, e.Ref)
}

func FlowKey(f *continuumv1.Flow) string {
	return endpointKey(f.Src) + ">" + endpointKey(f.Dst) + fmt.Sprintf("/%s:%d", f.Protocol, f.Port)
}

// maxPodFlowsPerBatch mirrors maxFlowsPerBatch for FlowBatch.pod_flows - a separate limit, not the same
// constant, because podFlows (aggregate.go) is already held to its own, smaller cap (MaxHeldPodFlows)
// independent of MaxHeldFlows/MaxFlowsPerBatch, so a batch legitimately near the main cap can still carry
// a pod-level breakdown near its own, smaller one.
const maxPodFlowsPerBatch = 4000

var errNodeLevel = errors.New("a node-level flow cannot become a dependency")

// DroppedItems is what SanitizeFlowBatch removed from one batch: how many, and the first reason, for the log.
type DroppedItems struct {
	N     int
	First error
}

func (d *DroppedItems) note(err error) {
	if d.N++; d.First == nil {
		d.First = err
	}
}

// SanitizeFlowBatch bounds what one agent may report, in place. A batch that is structurally broken (too many
// items, a window that cannot be) is refused, which ends the stream: that is a protocol violation. A single bad
// item is not: a flow or pod flow that wire.Check refuses is dropped and counted, the rest of the batch is kept,
// and the strings of the kept ones are cut (wire.Clip), so one odd edge never costs the 5000 good ones next to it.
//
// Flows with a NODE end (a node-level process or hostNetwork pod the agent could not pin to a pod) are well-formed
// but are dropped and counted too: no dependency is derived from them (see Topology), so storing them would
// only spend the edge budget.
func SanitizeFlowBatch(b *continuumv1.FlowBatch) (d DroppedItems, err error) {
	if len(b.Flows) > maxFlowsPerBatch || len(b.PodFlows) > maxPodFlowsPerBatch || len(b.Collectors) > 5000 ||
		b.WindowSeconds < 1 || b.WindowSeconds > 24*3600 {
		return d, errors.New("flow batch is malformed")
	}
	keep := func(f *continuumv1.Flow) bool {
		err := wire.Check(f)
		if err == nil && (f.Src.Kind == continuumv1.FlowEndpoint_NODE || f.Dst.Kind == continuumv1.FlowEndpoint_NODE) {
			err = errNodeLevel
		}
		if err != nil {
			d.note(err)
			return false
		}
		wire.Clip(f)
		return true
	}
	b.Flows = slices.DeleteFunc(b.Flows, func(f *continuumv1.Flow) bool { return !keep(f) })
	b.PodFlows = slices.DeleteFunc(b.PodFlows, func(f *continuumv1.Flow) bool { return !keep(f) })
	b.Collectors = slices.DeleteFunc(b.Collectors, func(c *continuumv1.CollectorInfo) bool {
		bad := c == nil || c.Node == "" || len(c.Node) > wire.MaxName || (c.Method != "ebpf" && c.Method != "conntrack")
		if bad {
			d.note(errors.New("a collector report is malformed"))
		}
		return bad
	})
	return d, nil
}

// BoundFlowFacts nulls out a reported LinkSaturation.saturation_pct that falls outside what a
// well-behaved collector ever computes it from (0-100 inclusive; see LinkSaturation.saturation_pct's
// own doc comment: never negative, since it is a ratio against an unsigned throughput, and clamped to
// 100 before the agent ever sends it) and returns how many entries, across every one of b's collectors,
// it had to drop this way. A data-integrity backstop independent of SanitizeFlowBatch's own shape
// checks above, for a compromised or merely buggy collector that skipped its own clamp: Iface and
// ThroughputBps on the same entry are left untouched, and the percentage is nulled - the same "not
// reported" the field already uses when the interface's rated speed could not be read at all - never
// clamped to 100, which would report a saturation level nothing actually measured.
func BoundFlowFacts(b *continuumv1.FlowBatch) int {
	n := 0
	for _, c := range b.Collectors {
		if c == nil {
			continue
		}
		for _, ls := range c.LinkSaturation {
			if ls == nil || ls.SaturationPct == nil {
				continue
			}
			if v := *ls.SaturationPct; v != v || v < 0 || v > 100 {
				ls.SaturationPct = nil
				n++
			}
		}
	}
	return n
}

// satAdd adds without wrapping: a counter fed by a compromised agent sticks at the maximum instead of
// rolling over to a small number that would hide the traffic (or, added to, make it look like none).
func satAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}

func (t *FlowTable) Apply(b *continuumv1.FlowBatch, now time.Time) {
	// One batch, one instant: every edge touched by this call - new or existing, however many flows the
	// batch carries - shares the exact same timestamp, so there is no need for a fresh *timestamppb.
	// Timestamp allocation per flow. Nothing here ever mutates a Timestamp in place (every reader goes through
	// AsTime()), so sharing the pointer across however many FlowEdges this call touches is safe.
	nowPb := timestamppb.New(now)
	for _, f := range b.Flows {
		k := FlowKey(f)
		e := t.Edges[k]
		if e == nil {
			e = &continuumv1.FlowEdge{Key: &continuumv1.Flow{Src: f.Src, Dst: f.Dst, Port: f.Port, Protocol: f.Protocol, Method: f.Method}, FirstSeen: nowPb}
			t.Edges[k] = e
		}
		e.LastSeen = nowPb
		e.Connections = satAdd(e.Connections, f.Connections)
		e.BytesOut = satAdd(e.BytesOut, f.BytesOut)
		e.BytesIn = satAdd(e.BytesIn, f.BytesIn)
		e.Retransmits = satAdd(e.Retransmits, f.Retransmits)
		e.RtoRetransmits = satAdd(e.RtoRetransmits, uint64(f.RtoRetransmits))
		e.SegsOut = satAdd(e.SegsOut, uint64(f.SegsOut))
		e.BufferDrops = satAdd(e.BufferDrops, uint64(f.BufferDrops))
		e.MeshBypassSyns = satAdd(e.MeshBypassSyns, uint64(f.MeshBypassSyns))
		e.FailedAttempts = satAdd(e.FailedAttempts, f.FailedAttempts)
		e.WindowSeconds, e.WindowConnections, e.WindowBytes, e.WindowRetransmits, e.WindowRtoRetransmits, e.WindowSegsOut, e.WindowBufferDrops, e.WindowMeshBypassSyns, e.WindowFailedAttempts = b.WindowSeconds, f.Connections, satAdd(f.BytesOut, f.BytesIn), f.Retransmits, uint64(f.RtoRetransmits), uint64(f.SegsOut), uint64(f.BufferDrops), uint64(f.MeshBypassSyns), f.FailedAttempts
		// Everything about the edge that is a reading rather than a total follows the same rules as the agent's
		// own aggregation across a window (wire.MergeGauges); the totals above are the server's, in its own width.
		wire.MergeGauges(e.Key, f)
		e.DnsQueryNames = wire.MergeDNSNames(e.DnsQueryNames, f.DnsQueryNames)
	}
	if over := len(t.Edges) - MaxFlowEdges; over > 0 { // forget the `over` edges unseen for longest
		for _, k := range wire.Oldest(over, func(yield func(string, int64) bool) {
			for k, e := range t.Edges {
				if !yield(k, e.LastSeen.AsTime().UnixNano()) {
					return
				}
			}
		}) {
			delete(t.Edges, k)
		}
	}
}

func (t *FlowTable) Marshal() ([]byte, error) {
	m := &continuumv1.FlowTable{}
	keys := make([]string, 0, len(t.Edges))
	for k := range t.Edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.Edges = append(m.Edges, t.Edges[k])
	}
	return proto.Marshal(m)
}

func UnmarshalFlowTable(b []byte) (*FlowTable, error) {
	var m continuumv1.FlowTable
	if err := proto.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	t := NewFlowTable()
	for _, e := range m.Edges {
		if e.Key != nil && e.Key.Src != nil && e.Key.Dst != nil && e.FirstSeen != nil && e.LastSeen != nil {
			t.Edges[FlowKey(e.Key)] = e
		}
	}
	return t, nil
}

// ---- turning edges into dependencies, across clusters ----

// Cluster is one onboarded cluster as far as traffic resolution is concerned.
type Cluster struct {
	ID, Name, AgentID string
	State             *facts.State
	Flows             *FlowTable
	EgressIP          string
}

type target struct{ cluster, workload string }

type AddrIndex struct {
	reach   map[string][]target // "ip:port" of a load balancer or external IP -> workload
	nodeIPs map[string][]string // node address -> clusters
	// nodePorts is "this cluster's own node's port" -> workload: a port reachable on any address one of
	// this cluster's own nodes has, regardless of which specific address was dialed - populated from
	// both an explicit NodePort and a load-balancer whose port binds across every node address (see
	// the load-balancer case in BuildAddrIndex for why the latter belongs here too).
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

// nodeAddresses is every address one of a cluster's nodes answers to: its reported internal and external addresses, and its
// own overlay/tunnel addresses (a Netbird, Tailscale or other mesh peer address, for example).
//
// Those tunnel addresses are just as much this cluster's own address space as its InternalIps/ExternalIps: when two clusters
// are joined only by such a mesh, the traffic this agent reports between them is addressed to exactly this, never to a
// Kubernetes-visible node IP. Without them, resolveExternal/clusterOfAddr have no way to recognize the peer side of that
// traffic as belonging to a known cluster at all, and every such flow falls through to being recorded as an unresolved
// external endpoint, by its raw tunnel IP, forever. They are included unconditionally (not gated on
// TunnelInterface.Confirmed): confirmation is a cross-node correlation verdict that connectivity.CorrelateTunnels computes later, from
// topo.Nodes, after Topology has already run - what is needed here is only "this address is one of this node's
// own, reported by its own probe," which is true whether or not the far end is independently confirmed elsewhere.
func nodeAddresses(st *facts.State) []string {
	var out []string
	if st == nil {
		return nil
	}
	for _, n := range st.Nodes {
		out = append(out, n.InternalIps...)
		out = append(out, n.ExternalIps...)
		if n.Probe != nil {
			for _, t := range n.Probe.Tunnels {
				if t == nil {
					continue
				}
				for _, a := range t.Addresses {
					if p, err := netip.ParsePrefix(a); err == nil {
						out = append(out, p.Addr().String())
					}
				}
			}
		}
	}
	return out
}

// LinkPeerAddresses is the addresses another cluster can try to reach this one at, for the cluster-link metrics: its nodes'
// reported internal and external addresses, the addresses on the nodes' own interfaces (the probe's HostAddresses: a LAN, a
// second NIC, a Tailscale or ZeroTier tun device, none of which the Node object lists), and the addresses of the tunnel
// interfaces that are a mesh and not a container network. Every address is in its canonical form (an IPv4-mapped IPv6 address
// as plain IPv4), so two spellings of one address are one.
//
// It is not nodeAddresses, which topology attribution uses and which takes every tunnel address: a CNI's overlay device
// (flannel.1 carries 10.42.N.0 on every node, tunl0 a pod-block address) is the cluster's own and can only be a candidate that
// times out, or, when two clusters use the same pod range, a name for the other cluster's pod traffic. So a tunnel or host
// address is dropped when its interface is one a container network makes (probe.ContainerNetworkInterface, the test the probe
// itself applies to HostAddresses) or when it lies in a pod or service range of the cluster. The InternalIps and ExternalIps
// are never tested against the pod ranges: on a flat network (VPC CNI, Azure CNI) the pod range contains the nodes' own
// addresses, and filtering them would empty the list. A WireGuard mesh device (wt0, wg0) stays.
func LinkPeerAddresses(st *facts.State) []string {
	if st == nil {
		return nil
	}
	var inside []netip.Prefix
	addPrefix := func(s string) {
		if p, err := netip.ParsePrefix(strings.TrimSpace(s)); err == nil {
			inside = append(inside, p.Masked())
		}
	}
	for _, n := range st.Nodes {
		for _, c := range n.PodCidrs {
			addPrefix(c)
		}
	}
	if st.Cluster != nil && st.Cluster.ServiceCidr != "" {
		addPrefix(st.Cluster.ServiceCidr)
	}
	insideCluster := func(a netip.Addr) bool {
		for _, p := range inside {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	var out []string
	add := func(s string) {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a.Unmap().String())
		}
	}
	// A prefix as the probe reports it ("100.64.0.45/32"); the address alone is what is offered.
	addProbe := func(s string) {
		p, err := netip.ParsePrefix(s)
		if err != nil || insideCluster(p.Addr().Unmap()) {
			return
		}
		out = append(out, p.Addr().Unmap().String())
	}
	for _, n := range st.Nodes {
		for _, ip := range n.InternalIps {
			add(ip)
		}
		for _, ip := range n.ExternalIps {
			add(ip)
		}
		if n.Probe == nil {
			continue
		}
		for _, h := range n.Probe.HostAddresses {
			if h != nil && !probe.ContainerNetworkInterface(h.Interface, h.Kind) {
				addProbe(h.Address)
			}
		}
		for _, t := range n.Probe.Tunnels {
			if t == nil || probe.ContainerNetworkInterface(t.Name, t.Kind) {
				continue
			}
			for _, a := range t.Addresses {
				addProbe(a)
			}
		}
	}
	return out
}

func BuildAddrIndex(cs []Cluster) *AddrIndex {
	ix := &AddrIndex{reach: map[string][]target{}, nodeIPs: map[string][]string{}, nodePorts: map[string]map[int32][]string{}, egress: map[string][]string{}}
	for _, c := range cs {
		if c.EgressIP != "" {
			ix.egress[c.EgressIP] = append(ix.egress[c.EgressIP], c.ID)
		}
		// A node's own addresses, including its overlay/tunnel ones - see nodeAddresses for why the tunnel ones count.
		for _, ip := range nodeAddresses(c.State) {
			ix.nodeIPs[ip] = append(ix.nodeIPs[ip], c.ID)
		}
		for _, w := range c.State.Workloads {
			for _, a := range w.Reachable {
				switch a.Kind {
				case "node-port":
					if ix.nodePorts[c.ID] == nil {
						ix.nodePorts[c.ID] = map[int32][]string{}
					}
					ix.nodePorts[c.ID][a.Port] = append(ix.nodePorts[c.ID][a.Port], w.Key)
				case "load-balancer":
					k := fmt.Sprintf("%s:%d", a.Ip, a.Port)
					ix.reach[k] = append(ix.reach[k], target{c.ID, w.Key})
					// A load balancer's reported ingress address is not always the only address its
					// port is actually reachable on. k3s's built-in ServiceLB (klipper-lb) and other
					// host-network-bound implementations report one of the cluster's own node
					// addresses as the ingress IP and then bind the service's port on that node's
					// host network - which means every address that node has, including one never
					// reported as an ingress address at all, such as a Netbird/Tailscale mesh peer
					// address. Only extended to (cluster, port) the way node-port already is when the
					// reported ingress address is itself provably one of this cluster's own node
					// addresses (checked against nodeIPs, already fully populated for this cluster by
					// the node loop above, which always runs first within this same iteration) - a
					// dedicated, per-service load balancer VIP (MetalLB's L2/BGP mode, a cloud LB) is
					// never also one of a node's own reported addresses, so it is deliberately left
					// out of this fallback and kept to the exact ip:port match above; collapsing the
					// two would let an unrelated connection that merely happens to reach some node on
					// a VIP's port number be misattributed to that VIP's workload, which an exact-IP
					// mismatch should instead leave unresolved.
					if slices.Contains(ix.nodeIPs[a.Ip], c.ID) {
						if ix.nodePorts[c.ID] == nil {
							ix.nodePorts[c.ID] = map[int32][]string{}
						}
						ix.nodePorts[c.ID][a.Port] = append(ix.nodePorts[c.ID][a.Port], w.Key)
					}
				default:
					k := fmt.Sprintf("%s:%d", a.Ip, a.Port)
					ix.reach[k] = append(ix.reach[k], target{c.ID, w.Key})
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

// Topology derives dependencies and external endpoints from every cluster's flows.
// ApplyMeshBypassFacts turns Topology's tentative model.Dependency.MeshBypass (set wherever a
// direct, non-redirected egress SYN was merely observed - see FlowEdge.MeshBypassSyns) into the real,
// cross-checked fact: true only where the caller's own declared mesh configuration (services, built
// separately by interpret.Interpret from each cluster's own WorkloadFacts) actually calls for this
// traffic to be intercepted. Run once, after every cluster's services and every observed dependency are
// both in hand (see hub.go's buildTopology) - Topology itself never sees a model.Service at all,
// only raw flow edges, and must not guess.
func ApplyMeshBypassFacts(deps []model.Dependency, services []model.Service) {
	byID := make(map[string]*model.Service, len(services))
	for i := range services {
		byID[services[i].ID] = &services[i]
	}
	for i := range deps {
		d := &deps[i]
		if !d.MeshBypass {
			continue
		}
		d.MeshBypass = false
		if d.FromKind != "service" {
			continue // an external caller, or one this cluster's own agent never reported as a service
		}
		svc := byID[d.From]
		if svc == nil || svc.Mesh == nil || svc.Mesh.Bypass || svc.Mesh.Proxy != facts.ProxySidecar {
			// Not configured to run a sidecar at all (or explicitly opted out, or running the mesh's
			// control plane/ambient mode instead - ambient has no per-pod sidecar to redirect to, so this
			// whole check does not apply to it): direct egress here is exactly what was asked for.
			continue
		}
		if d.Port != 0 && facts.PortExcluded(svc.Mesh.ExcludedPorts, "out", d.Port) {
			continue // the workload itself declared this exact port out of its proxy
		}
		d.MeshBypass = true
	}
}

func Topology(org string, cs []Cluster, now time.Time, stale time.Duration) ([]model.Dependency, []model.ExternalEndpoint) {
	ix := BuildAddrIndex(cs)
	byID := map[string]*Cluster{}
	for i := range cs {
		byID[cs[i].ID] = &cs[i]
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
				ID:         id, Host: ip, Port: int(port), Kind: model.ExternalKind(kind), Service: svc, Name: name, IPs: []string{ip},
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
		if c == nil || c.State.Workloads[key] == nil {
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
		c *Cluster
		e *continuumv1.FlowEdge
	}
	var inbound []pending
	outboundTo := map[string]bool{} // "srcCluster>dstServiceID" for outbound edges that resolved to a workload in another cluster

	add := func(from, fromKind, to, toKind string, e *continuumv1.FlowEdge, cross bool, note string) {
		port := int(e.Key.Port)
		// Protocol is part of the identity, not just a field on it: a workload very commonly talks to
		// the same peer on the same port over both UDP and TCP (DNS being the obvious case - UDP first,
		// TCP fallback for large answers), and those are two distinct edges in FlowTable/FlowKey. Leaving
		// protocol out of this hash used to merge such pairs into one Dependency, whose Protocol field
		// then depended on map iteration order (non-deterministic) and whose Connections/Bytes silently
		// summed traffic from two different protocols under one label.
		id := "dep-obs-" + interpret.Hash(from, to, e.Key.Protocol, fmt.Sprint(port))
		d := deps[id]
		if d == nil {
			svc, _ := wellKnownPort(e.Key.Port)
			d = &model.Dependency{ID: id, OrgID: org, From: from, FromKind: model.EndpointKind(fromKind), To: to, ToKind: model.EndpointKind(toKind), Sources: []model.DependencySource{model.DependencySourceObserved},
				Confidence: "high", Protocol: strings.ToUpper(e.Key.Protocol), Port: port, Service: svc, Via: model.ObservedVia(e.Key.Method), CrossCluster: cross, Noise: model.Noise(e.Key.Noise), Note: note,
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
		// A gauge like RttMs/CwndSegments above, not a running total - see model.Dependency.MssBytes for
		// why this is only really meaningful on a confirmed cluster-link tunnel edge (tunnels.go).
		if e.Key.MssBytes != 0 {
			d.MssBytes = e.Key.MssBytes
		}
		// Gauges too, but see model.Dependency.RcvWndBytes' own doc for why these four are *uint32, not
		// plain uint32 like MssBytes/CwndSegments above: 0 is a real, meaningful sample for all four (a
		// zero window, or a drained queue), so copying the pointer straight across - nil stays nil,
		// nothing fabricates a 0 for an edge that was never sampled, and a real 0 is preserved rather
		// than being read back as "unset" - is what keeps that distinction all the way out to the API.
		d.RcvWndBytes = e.Key.RcvWndBytes
		d.SndWndBytes = e.Key.SndWndBytes
		d.WmemQueuedBytes = e.Key.WmemQueuedBytes
		d.SndbufBytes = e.Key.SndbufBytes
		// See model.Dependency.TlsHandshake's own doc for exactly what "ok"/"failed" can and cannot tell -
		// mapped from the wire enum to that plain string here, at the one place a RawFlow's internal
		// numbering turns into the model the rest of the server and the UI actually read.
		switch e.Key.TlsHandshake {
		case continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK:
			d.TlsHandshake = "ok"
		case continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED:
			d.TlsHandshake = "failed"
		}
		d.Retransmits = satAdd(d.Retransmits, e.Retransmits)
		d.RtoRetransmits = satAdd(d.RtoRetransmits, e.RtoRetransmits)
		d.BufferDrops = satAdd(d.BufferDrops, e.BufferDrops)
		// Tentative: true here means only "a direct, non-redirected egress SYN was observed on this edge
		// at some point" (see FlowEdge.MeshBypassSyns/flow.c's note_mesh_bypass), nothing yet about
		// whether this workload was ever supposed to be meshed at all. ApplyMeshBypassFacts (hub.go),
		// run once every dependency and every service from every cluster are both in hand, clears this
		// back to false wherever the caller's own declared mesh configuration does not actually call for
		// interception - this layer has no access to that configuration (it never sees a Service, only
		// raw flow edges) and must not guess.
		if e.MeshBypassSyns > 0 {
			d.MeshBypass = true
		}
		d.FailedAttempts = satAdd(d.FailedAttempts, e.FailedAttempts)
		if e.Key.SniHost != "" {
			d.SniHost = e.Key.SniHost
		}
		d.DnsQueryNames = wire.MergeDNSNames(d.DnsQueryNames, e.DnsQueryNames)
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
			st.RtoRetransmitsPerMin += float64(e.WindowRtoRetransmits) * 60 / float64(e.WindowSeconds)
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
		for _, e := range c.Flows.Edges {
			if e.Key.Src.Kind == continuumv1.FlowEndpoint_EXTERNAL {
				inbound = append(inbound, pending{c, e})
				continue
			}
			// A NODE-kind source (a node-level process, or a hostNetwork pod resolve.go could not pin to a pod)
			// is not a workload and never matches serviceOf. New ones are dropped, and counted, on arrival
			// (SanitizeFlowBatch); a table stored by an older server may still hold some, which end up here.
			from, ok := serviceOf(c.ID, e.Key.Src.Ref)
			if !ok {
				continue
			}
			switch e.Key.Dst.Kind {
			case continuumv1.FlowEndpoint_WORKLOAD:
				if to, ok := serviceOf(c.ID, e.Key.Dst.Ref); ok {
					add(from, "service", to, "service", e, false, "")
				}
			case continuumv1.FlowEndpoint_EXTERNAL:
				ip := e.Key.Dst.Ip
				if t, note, ok := resolveExternal(c.ID, ip, e.Key.Port); ok {
					if to, ok := serviceOf(t.cluster, t.workload); ok {
						cross := t.cluster != c.ID
						add(from, "service", to, "service", e, cross, note)
						if cross {
							outboundTo[c.ID+">"+to] = true
						}
						continue
					}
				} else if note != "" {
					if id, ready := external(c.AgentID, ip, e.Key.Port, note, e.FirstSeen.AsTime()); ready {
						add(from, "service", id, "external", e, false, note)
					}
					continue
				}
				what := ""
				if cl, ok := clusterOfAddr(ip); ok && cl != c.ID {
					what = "address belongs to cluster " + byID[cl].Name
				}
				if id, ready := external(c.AgentID, ip, e.Key.Port, what, e.FirstSeen.AsTime()); ready {
					add(from, "service", id, "external", e, false, what)
				}
			}
		}
	}
	// Connections that arrived from outside. When the caller is another onboarded cluster whose own
	// agent already reported the outbound edge to this workload, that edge is the better description
	// (it names the calling workload), so the inbound record is not added a second time.
	for _, p := range inbound {
		to, ok := serviceOf(p.c.ID, p.e.Key.Dst.Ref)
		if !ok {
			continue
		}
		ip := p.e.Key.Src.Ip
		if cl, ok := clusterOfAddr(ip); ok && cl != p.c.ID {
			// outboundTo is already keyed exactly "cl>to" - a direct lookup, not a scan over every entry.
			if outboundTo[cl+">"+to] {
				continue
			}
			if id, ready := external(p.c.AgentID, ip, 0, "address belongs to cluster "+byID[cl].Name, p.e.FirstSeen.AsTime()); ready {
				add(id, "external", to, "service", p.e, true, "caller identified only as a cluster, by its address")
			}
			continue
		}
		if id, ready := external(p.c.AgentID, ip, 0, "", p.e.FirstSeen.AsTime()); ready {
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
	// Dropped counts the flows the server could not use and left out of otherwise accepted reports (malformed, or
	// node-level traffic that cannot become a dependency), since the server last started.
	Dropped uint64 `json:"dropped"`
}

type Health struct {
	at         time.Time
	collectors []*continuumv1.CollectorInfo
	lost       uint64
	// dropped counts the items of accepted batches that SanitizeFlowBatch removed, since the server started;
	// droppedLogged limits how often that is warned about.
	dropped       uint64
	droppedLogged time.Time
}

// noteDropped counts n dropped items and reports whether this is a time to log them (at most once per RefusalEvery).
func (o *Health) NoteDropped(n int, now time.Time) bool {
	o.dropped += uint64(n)
	if now.Sub(o.droppedLogged) < RefusalEvery {
		return false
	}
	o.droppedLogged = now
	return true
}

func (o *Health) Note(b *continuumv1.FlowBatch, now time.Time) {
	o.at, o.collectors = now, b.Collectors
	o.lost += b.Lost
}

func (o *Health) Doc(now time.Time) *ObserverDoc {
	if o.at.IsZero() {
		return nil
	}
	d := &ObserverDoc{LastReport: o.at.UTC().Format(time.RFC3339), Lost: o.lost, Dropped: o.dropped, Collectors: []CollectorDoc{}}
	// A collector list older than the reporting TTL says nothing about who is observed now.
	if now.Sub(o.at) < 10*time.Minute {
		for _, c := range o.collectors {
			d.Collectors = append(d.Collectors, CollectorDoc{Node: c.Node, Method: c.Method, BytesKnown: c.BytesKnown})
		}
	}
	return d
}

// clusterOf says which onboarded cluster an address belongs to, when that is unambiguous.
func (ix *AddrIndex) ClusterOf(ip string, port int) string {
	if ts := ix.reach[fmt.Sprintf("%s:%d", ip, port)]; len(ts) > 0 {
		c := ts[0].cluster
		for _, t := range ts {
			if t.cluster != c {
				return ""
			}
		}
		return c
	}
	if c := uniq(ix.nodeIPs[ip]); len(c) == 1 {
		return c[0]
	}
	if c := uniq(ix.egress[ip]); len(c) == 1 {
		return c[0]
	}
	return ""
}

// RefusalEvery is how often one agent's refused messages are written to the audit trail and the event list;
// an agent that keeps sending the same thing would otherwise fill both.
const RefusalEvery = 10 * time.Minute
