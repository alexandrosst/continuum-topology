package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/interpret"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func wk(ns, kind, name string, reach ...*continuumv1.Address) *continuumv1.WorkloadFacts {
	return &continuumv1.WorkloadFacts{Key: ns + "/" + kind + "/" + name, Namespace: ns, Kind: kind, Name: name, Reachable: reach}
}

func cluster(id string, egress string, nodes []*continuumv1.NodeFacts, ws ...*continuumv1.WorkloadFacts) observedCluster {
	st := facts.New()
	for _, w := range ws {
		st.Workloads[w.Key] = w
	}
	for _, n := range nodes {
		st.Nodes[n.Key] = n
	}
	return observedCluster{id: id, name: id, agentID: "ag-" + id, state: st, flows: newFlowTable(), egressIP: egress}
}

func wep(key string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: key}
}
func xep(ip string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: ip}
}

func flowOf(src, dst *continuumv1.FlowEndpoint, port uint32, conns uint64) *continuumv1.Flow {
	return &continuumv1.Flow{Src: src, Dst: dst, Port: port, Protocol: "tcp", Connections: conns, BytesOut: 1000 * conns, BytesIn: 3000 * conns, Method: "ebpf", BytesKnown: true}
}

func feed(c *observedCluster, at time.Time, window int32, fs ...*continuumv1.Flow) {
	c.flows.apply(&continuumv1.FlowBatch{WindowSeconds: window, Flows: fs}, at)
}

func TestObservedTopologyAcrossClusters(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	lb := &continuumv1.Address{Ip: "198.51.100.7", Port: 443, Kind: "load-balancer"}
	np := &continuumv1.Address{Port: 30080, Kind: "node-port"}
	edge := cluster("edge", "203.0.113.1", nil, wk("iot", "Deployment", "ingest"), wk("iot", "Deployment", "cache"))
	cloud := cluster("cloud", "198.51.100.99",
		[]*continuumv1.NodeFacts{{Key: "n1", InternalIps: []string{"10.0.0.5"}, ExternalIps: []string{"198.51.100.20"}}},
		wk("platform", "Deployment", "gateway", lb), wk("platform", "Deployment", "orchestrator", np), wk("platform", "Deployment", "db"))
	other := cluster("other", "", nil, wk("x", "Deployment", "dup", &continuumv1.Address{Ip: "192.168.0.5", Port: 80, Kind: "load-balancer"}))
	third := cluster("third", "", nil, wk("y", "Deployment", "dup2", &continuumv1.Address{Ip: "192.168.0.5", Port: 80, Kind: "load-balancer"}))

	ingest, cache := "iot/Deployment/ingest", "iot/Deployment/cache"
	gw, orch, db := "platform/Deployment/gateway", "platform/Deployment/orchestrator", "platform/Deployment/db"

	feed(&edge, now, 60,
		flowOf(wep(ingest), wep(cache), 6379, 10),           // inside a cluster
		flowOf(wep(ingest), xep("198.51.100.7"), 443, 5),    // another cluster's load balancer
		flowOf(wep(ingest), xep("198.51.100.20"), 30080, 2), // a node port on another cluster's node
		flowOf(wep(ingest), xep("192.168.0.5"), 80, 1),      // an address two clusters both use
		flowOf(wep(ingest), xep("93.184.216.34"), 5432, 4),  // the internet
		flowOf(wep(ingest), xep("198.51.100.99"), 22, 1),    // another cluster's egress address, no workload
		&continuumv1.Flow{Src: wep(ingest), Dst: wep(cache), Port: 53, Protocol: "tcp", Connections: 1, Method: "ebpf", Noise: "dns"}, // machinery
	)
	// The cloud side sees the same connection arrive; because the edge reported the outbound edge, no duplicate appears.
	feed(&cloud, now, 60,
		flowOf(xep("203.0.113.1"), wep(gw), 443, 5),
		flowOf(xep("203.0.113.77"), wep(db), 5432, 3), // an unknown caller from the internet
		flowOf(xep("203.0.113.1"), wep(db), 5432, 6),  // the edge cluster, but its own agent reported nothing to db: kept, as a cluster-level caller
	)

	deps, exts := observedTopology("org", []observedCluster{edge, cloud, other, third}, now, 24*time.Hour)
	sv := func(c, k string) string { return interpret.ServiceID(c, k) }
	find := func(from, to string, port int) *struct{ d int } {
		for i, d := range deps {
			if d.From == from && d.To == to && d.Port == port {
				return &struct{ d int }{i}
			}
		}
		return nil
	}

	in := find(sv("edge", ingest), sv("edge", cache), 6379)
	if in == nil || deps[in.d].CrossCluster || deps[in.d].Confidence != "high" || deps[in.d].Via != "ebpf" || deps[in.d].Connections != 10 {
		t.Fatalf("in-cluster edge = %+v", in)
	}
	if st := deps[in.d].Stats; st == nil || st.ConnectionsPerMin != 10 || st.BytesPerSec != float64(10*4000)/60 {
		t.Errorf("rates = %+v", st)
	}

	x := find(sv("edge", ingest), sv("cloud", gw), 443)
	if x == nil || !deps[x.d].CrossCluster || deps[x.d].Confidence != "medium" || deps[x.d].Note == "" {
		t.Fatalf("load balancer edge = %+v", x)
	}
	if n := find(sv("edge", ingest), sv("cloud", orch), 30080); n == nil || !deps[n.d].CrossCluster {
		t.Fatalf("node port edge missing")
	}
	// The inbound record of the load-balancer connection is the same edge seen from the other end: not repeated.
	for _, d := range deps {
		if d.To == sv("cloud", gw) && d.FromKind == "external" {
			t.Errorf("duplicate inbound edge: %+v", d)
		}
	}

	amb := 0
	for _, e := range exts {
		if e.Host == "192.168.0.5" {
			amb++
			if e.Evidence["identity"].Signal == "" {
				t.Error("an ambiguous address must say why it was not resolved")
			}
		}
	}
	if amb != 1 {
		t.Fatalf("ambiguous address should stay one external endpoint, got %d", amb)
	}

	web := ""
	for _, e := range exts {
		if e.Host == "93.184.216.34" {
			web = e.ID
			if e.Kind != "database" || e.Port != 5432 {
				t.Errorf("external = %+v", e)
			}
		}
	}
	if web == "" || find(sv("edge", ingest), web, 5432) == nil {
		t.Fatal("an unresolved address becomes an external endpoint")
	}
	if e := find(sv("edge", ingest), "", 22); e != nil {
		t.Fatal("unexpected")
	}
	var egressEP string
	for _, e := range exts {
		if e.Host == "198.51.100.99" {
			egressEP = e.ID
			if e.Evidence["identity"].Signal != "address belongs to cluster cloud" {
				t.Errorf("evidence = %v", e.Evidence)
			}
		}
	}
	if egressEP == "" {
		t.Error("an address that belongs to another cluster but no workload is named as such")
	}

	// inbound from a stranger, and from a cluster that did not report an outbound edge to that workload
	sawStranger, sawCluster := false, false
	for _, d := range deps {
		if d.To == sv("cloud", db) && d.FromKind == "external" {
			for _, e := range exts {
				if e.ID == d.From && e.Host == "203.0.113.77" && d.CrossCluster == false {
					sawStranger = true
				}
				if e.ID == d.From && e.Host == "203.0.113.1" && d.CrossCluster {
					sawCluster = true
				}
			}
		}
	}
	if !sawStranger || !sawCluster {
		t.Errorf("inbound: stranger=%v cluster=%v", sawStranger, sawCluster)
	}

	dns := find(sv("edge", ingest), sv("edge", cache), 53)
	if dns == nil || deps[dns.d].Noise != "dns" {
		t.Error("noise flag must survive")
	}
	for i := 1; i < len(deps); i++ {
		if deps[i-1].ID >= deps[i].ID {
			t.Fatal("output must be sorted (deterministic)")
		}
	}
}

// A workload very commonly talks to the same peer on the same port over both UDP and TCP - DNS being
// the textbook case (UDP first, a TCP fallback for answers too large for one datagram). flowTable keys
// on protocol too, so these arrive as two distinct edges; observedTopology's own dependency id must not
// collapse them back into one, or the merged record's Protocol and traffic counters become a coin flip
// depending on Go's (randomized) map iteration order.
func TestObservedTopologyKeepsDifferentProtocolsOnTheSameServiceAndPortSeparate(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	edge := cluster("edge", "", nil, wk("iot", "Deployment", "ingest"), wk("kube-system", "Deployment", "dns"))
	ingest, dns := "iot/Deployment/ingest", "kube-system/Deployment/dns"

	udpFlow := flowOf(wep(ingest), wep(dns), 53, 20)
	udpFlow.Protocol = "udp"
	tcpFlow := flowOf(wep(ingest), wep(dns), 53, 3) // flowOf defaults to "tcp"

	feed(&edge, now, 60, udpFlow, tcpFlow)

	deps, _ := observedTopology("org", []observedCluster{edge}, now, 24*time.Hour)
	sv := func(c, k string) string { return interpret.ServiceID(c, k) }

	var udpConns, tcpConns uint64
	var sawUDP, sawTCP bool
	for _, d := range deps {
		if d.From != sv("edge", ingest) || d.To != sv("edge", dns) || d.Port != 53 {
			continue
		}
		switch d.Protocol {
		case "UDP":
			sawUDP, udpConns = true, d.Connections
		case "TCP":
			sawTCP, tcpConns = true, d.Connections
		default:
			t.Fatalf("unexpected protocol %q on a merged dependency: %+v", d.Protocol, d)
		}
	}
	if !sawUDP || !sawTCP {
		t.Fatalf("expected separate UDP and TCP dependencies on iot/dns:53, got udp=%v tcp=%v (all: %+v)", sawUDP, sawTCP, deps)
	}
	if udpConns != 20 {
		t.Errorf("UDP dependency's connections = %d, want 20 (must not include the TCP flow's count)", udpConns)
	}
	if tcpConns != 3 {
		t.Errorf("TCP dependency's connections = %d, want 3 (must not include the UDP flow's count)", tcpConns)
	}
}

func TestObservedStaleAndUnknownWorkloads(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := cluster("c", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	feed(&c, now.Add(-48*time.Hour), 60, flowOf(wep("a/Deployment/x"), wep("a/Deployment/y"), 80, 1))
	feed(&c, now.Add(-time.Minute), 60, flowOf(wep("a/Deployment/x"), wep("a/Deployment/ghost"), 80, 1)) // a workload the cluster does not have
	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	if len(deps) != 1 || !deps[0].Stale {
		t.Fatalf("an edge unseen for 48 h is stale, not deleted; an edge to an unknown workload is dropped: %+v", deps)
	}
	feed(&c, now, 60, flowOf(wep("a/Deployment/x"), wep("a/Deployment/y"), 80, 1))
	deps, _ = observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	if deps[0].Stale || deps[0].Connections != 2 {
		t.Fatalf("seeing it again revives it and adds up: %+v", deps[0])
	}
}

func TestFlowBatchValidationAndTable(t *testing.T) {
	good := flowOf(wep("a/Deployment/x"), xep("1.2.3.4"), 443, 1)
	if err := validateFlowBatch(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{good}}); err != nil {
		t.Fatal(err)
	}
	bad := map[string]*continuumv1.Flow{
		"no src":           {Dst: wep("a/Deployment/x"), Port: 1, Protocol: "tcp", Method: "ebpf"},
		"port 0":           flowOf(wep("a"), xep("1.2.3.4"), 0, 1),
		"sctp":             {Src: wep("a"), Dst: xep("1.2.3.4"), Port: 1, Protocol: "sctp", Method: "ebpf"},
		"unknown method":   {Src: wep("a"), Dst: xep("1.2.3.4"), Port: 1, Protocol: "tcp", Method: "x"},
		"unknown noise":    {Src: wep("a"), Dst: xep("1.2.3.4"), Port: 1, Protocol: "tcp", Method: "ebpf", Noise: "fun"},
		"not an address":   flowOf(wep("a"), xep("evil.example.com"), 1, 1),
		"non-canonical ip": flowOf(wep("a"), xep("::ffff:1.2.3.4"), 1, 1),
		"unresolved kind":  flowOf(wep("a"), &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_UNRESOLVED, Ip: "1.2.3.4"}, 1, 1),
		"two outside ends": flowOf(xep("1.2.3.4"), xep("5.6.7.8"), 1, 1),
		"empty ref":        flowOf(wep(""), xep("1.2.3.4"), 1, 1),
	}
	for name, f := range bad {
		if validateFlowBatch(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f}}) == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if validateFlowBatch(&continuumv1.FlowBatch{WindowSeconds: 0, Flows: []*continuumv1.Flow{good}}) == nil {
		t.Error("a window of zero seconds must be refused")
	}

	// persistence round trip and the cap
	tb := newFlowTable()
	now := time.Now()
	tb.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{good}}, now)
	data, err := tb.marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := unmarshalFlowTable(data)
	if err != nil || len(back.edges) != 1 {
		t.Fatalf("round trip: %v %v", back, err)
	}
	for _, e := range back.edges {
		if e.Connections != 1 || e.FirstSeen == nil || e.Key.Dst.Ip != "1.2.3.4" {
			t.Fatalf("edge = %v", e)
		}
	}
	for i := 0; i < maxFlowEdges+50; i++ {
		f := flowOf(wep("a/Deployment/x"), wep("b/Deployment/"+string(rune('a'+i%26))+time.Duration(i).String()), 80, 1)
		tb.edges[flowKey(f)] = &continuumv1.FlowEdge{Key: f, FirstSeen: timestamppb.New(now), LastSeen: timestamppb.New(now.Add(time.Duration(i) * time.Second))}
	}
	tb.apply(&continuumv1.FlowBatch{WindowSeconds: 60}, now)
	if len(tb.edges) != maxFlowEdges {
		t.Fatalf("the table is bounded: %d", len(tb.edges))
	}
}

func TestFlowCountersSaturateInsteadOfWrapping(t *testing.T) {
	const max = ^uint64(0)
	if satAdd(1, 2) != 3 || satAdd(max, 1) != max || satAdd(max-1, 5) != max || satAdd(max, max) != max || satAdd(0, 0) != 0 {
		t.Fatal("satAdd")
	}
	f := flowOf(wep("a/Deployment/x"), wep("b/Deployment/y"), 80, 1)
	f.Connections, f.BytesOut, f.BytesIn = max-2, max-10, max
	tb := newFlowTable()
	now := time.Now()
	tb.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f}}, now)
	// A second batch that would wrap each counter past zero.
	f2 := flowOf(wep("a/Deployment/x"), wep("b/Deployment/y"), 80, 1)
	f2.Connections, f2.BytesOut, f2.BytesIn = 100, 100, 100
	tb.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now)
	if len(tb.edges) != 1 {
		t.Fatalf("edges: %d", len(tb.edges))
	}
	for _, e := range tb.edges {
		if e.Connections != max || e.BytesOut != max || e.BytesIn != max {
			t.Errorf("counters wrapped: %d %d %d", e.Connections, e.BytesOut, e.BytesIn)
		}
		if e.WindowBytes != 200 {
			t.Errorf("window bytes %d", e.WindowBytes)
		}
	}
	// A single window's out+in that overflows saturates too.
	tb2 := newFlowTable()
	f3 := flowOf(wep("a/Deployment/x"), wep("b/Deployment/y"), 80, 1)
	f3.BytesOut, f3.BytesIn = max, max
	tb2.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f3}}, now)
	for _, e := range tb2.edges {
		if e.WindowBytes != max {
			t.Errorf("window bytes wrapped: %d", e.WindowBytes)
		}
	}
}
