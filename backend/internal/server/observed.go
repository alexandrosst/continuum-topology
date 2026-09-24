package server

import (
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/interpret"
	"continuum/internal/model"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---- what the server keeps: totals per distinct edge, per agent ----

const (
	maxFlowEdges       = 20000
	maxFlowsPerBatch   = 5000
	defaultStaleWindow = 24 * time.Hour
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

// satAdd adds without wrapping: a counter fed by a compromised agent sticks at the maximum instead of
// rolling over to a small number that would hide the traffic (or, added to, make it look like none).
func satAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}

func (t *flowTable) apply(b *continuumv1.FlowBatch, now time.Time) {
	for _, f := range b.Flows {
		k := flowKey(f)
		e := t.edges[k]
		if e == nil {
			e = &continuumv1.FlowEdge{Key: &continuumv1.Flow{Src: f.Src, Dst: f.Dst, Port: f.Port, Protocol: f.Protocol, Noise: f.Noise, Method: f.Method, Iface: f.Iface, RttUs: f.RttUs}, FirstSeen: timestamppb.New(now)}
			t.edges[k] = e
		}
		e.LastSeen = timestamppb.New(now)
		e.Connections = satAdd(e.Connections, f.Connections)
		e.BytesOut = satAdd(e.BytesOut, f.BytesOut)
		e.BytesIn = satAdd(e.BytesIn, f.BytesIn)
		e.Retransmits = satAdd(e.Retransmits, f.Retransmits)
		e.WindowSeconds, e.WindowConnections, e.WindowBytes, e.WindowRetransmits = b.WindowSeconds, f.Connections, satAdd(f.BytesOut, f.BytesIn), f.Retransmits
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
	}
	if len(t.edges) > maxFlowEdges { // forget the edges unseen for longest
		type kv struct {
			k string
			t time.Time
		}
		all := make([]kv, 0, len(t.edges))
		for k, e := range t.edges {
			all = append(all, kv{k, e.LastSeen.AsTime()})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		for _, x := range all[:len(all)-maxFlowEdges] {
			delete(t.edges, x.k)
		}
	}
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
	nodePorts map[string]map[int32][]string
	egress    map[string][]string // a cluster's connecting address -> clusters
}

func uniq(in []string) []string {
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
		}
		for _, w := range c.state.Workloads {
			for _, a := range w.Reachable {
				switch a.Kind {
				case "node-port":
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
	stamp := now.UTC().Format(time.RFC3339)

	external := func(agentID, ip string, port uint32, note string) string {
		id := "ext-" + interpret.Hash("obs", ip, fmt.Sprint(port))
		if _, ok := exts[id]; !ok {
			kind := "unknown"
			svc, isDatabase := wellKnownPort(port)
			if isDatabase {
				kind = "database"
			}
			e := model.ExternalEndpoint{
				Provenance: model.Provenance{OrgID: org, Source: "discovered", Key: "obs/" + ip, LastSeen: stamp, DetectedAt: stamp, AgentID: agentID},
				ID:         id, Host: ip, Port: int(port), Kind: kind, Service: svc,
			}
			if note != "" {
				e.Evidence = map[string]model.Evidence{"identity": {Signal: note, Confidence: "low"}}
			}
			exts[id] = e
		}
		return id
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
		d.Retransmits = satAdd(d.Retransmits, e.Retransmits)
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
			if e.Key.BytesKnown {
				st.BytesPerSec += float64(e.WindowBytes) / float64(e.WindowSeconds)
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
					id := external(c.agentID, ip, e.Key.Port, note)
					add(from, "service", id, "external", e, false, note)
					continue
				}
				what := ""
				if cl, ok := clusterOfAddr(ip); ok && cl != c.id {
					what = "address belongs to cluster " + byID[cl].name
				}
				id := external(c.agentID, ip, e.Key.Port, what)
				add(from, "service", id, "external", e, false, what)
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
			dup := false
			for k := range outboundTo {
				if k == cl+">"+to {
					dup = true
				}
			}
			if dup {
				continue
			}
			id := external(p.c.agentID, ip, 0, "address belongs to cluster "+byID[cl].name)
			add(id, "external", to, "service", p.e, true, "caller identified only as a cluster, by its address")
			continue
		}
		id := external(p.c.agentID, ip, 0, "")
		add(id, "external", to, "service", p.e, false, "")
	}

	var outDeps []model.Dependency
	for _, d := range deps {
		if last, err := time.Parse(time.RFC3339, d.LastSeen); err == nil && now.Sub(last) > stale {
			d.Stale = true
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
