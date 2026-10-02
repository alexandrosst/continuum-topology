package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/interpret"
	"continuum/internal/model"
	"continuum/internal/netid"

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
	// Flows are fed as having first been seen a minute before observedTopology is asked to report on
	// them, not at the same instant - past unmatchedGrace (12s), so an address this test expects to show
	// up unmatched/unlabeled (192.168.0.5, the ambiguous one) actually does, instead of being withheld as
	// still-too-new. See unmatchedGrace's own comment in observed.go for why that grace period exists.
	seenAt := now.Add(-1 * time.Minute)
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

	feed(&edge, seenAt, 60,
		flowOf(wep(ingest), wep(cache), 6379, 10),           // inside a cluster
		flowOf(wep(ingest), xep("198.51.100.7"), 443, 5),    // another cluster's load balancer
		flowOf(wep(ingest), xep("198.51.100.20"), 30080, 2), // a node port on another cluster's node
		flowOf(wep(ingest), xep("192.168.0.5"), 80, 1),      // an address two clusters both use
		flowOf(wep(ingest), xep("93.184.216.34"), 5432, 4),  // the internet
		flowOf(wep(ingest), xep("198.51.100.99"), 22, 1),    // another cluster's egress address, no workload
		&continuumv1.Flow{Src: wep(ingest), Dst: wep(cache), Port: 53, Protocol: "tcp", Connections: 1, Method: "ebpf", Noise: "dns"}, // machinery
	)
	// The cloud side sees the same connection arrive; because the edge reported the outbound edge, no duplicate appears.
	feed(&cloud, seenAt, 60,
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
	if deps[in.d].Service != "Redis" {
		t.Errorf("a well-known port must be named as a guess, got Service=%q", deps[in.d].Service)
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
			if e.Kind != "database" || e.Port != 5432 || e.Service != "PostgreSQL" {
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

// A regression test for a real bug: apply() used to build a stored edge's Key without ever copying
// Iface or RttUs onto it, so both fields silently stayed empty forever no matter what the collector
// reported - the eBPF side, resolve.go and aggregate.go could all be working perfectly and the UI would
// still never show an interface or an RTT. Retransmits, the cumulative counter, is checked alongside it.
func TestFlowTableCarriesIfaceRetransmitsAndRTT(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Connections: 1, BytesOut: 100, BytesIn: 200,
		Method: "ebpf", BytesKnown: true, Iface: "eth0", Retransmits: 3, RttUs: 15000}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)

	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.Key.Iface != "eth0" {
		t.Errorf("iface = %q, want %q", e.Key.Iface, "eth0")
	}
	if e.Key.RttUs != 15000 {
		t.Errorf("rtt_us = %d, want 15000", e.Key.RttUs)
	}
	if e.Retransmits != 3 || e.WindowRetransmits != 3 {
		t.Errorf("retransmits = %d/%d, want 3/3", e.Retransmits, e.WindowRetransmits)
	}

	// A route change and more loss on the next report: iface/rtt are gauges (latest wins), retransmits accumulate.
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", Iface: "wlan0", Retransmits: 2, RttUs: 20000}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.Key.Iface != "wlan0" {
		t.Errorf("iface after route change = %q, want %q", e.Key.Iface, "wlan0")
	}
	if e.Key.RttUs != 20000 {
		t.Errorf("rtt_us after new sample = %d, want 20000", e.Key.RttUs)
	}
	if e.Retransmits != 5 {
		t.Errorf("cumulative retransmits = %d, want 5", e.Retransmits)
	}

	// A conntrack report (no iface, no RTT, no retransmits) must not blank out what eBPF already established.
	f3 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Connections: 1, Method: "conntrack"}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f3}}, now.Add(2*time.Minute))
	e = tbl.edges[k]
	if e.Key.Iface != "wlan0" || e.Key.RttUs != 20000 {
		t.Errorf("a zero-value report must not blank a previously known iface/rtt, got %q / %d", e.Key.Iface, e.Key.RttUs)
	}
}

func TestFlowTableCarriesFailedAttempts(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")
	// A window with nothing but failures: connections stays 0, failed_attempts carries the whole story.
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", FailedAttempts: 2}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.Connections != 0 || e.FailedAttempts != 2 || e.WindowFailedAttempts != 2 {
		t.Errorf("connections=%d failedAttempts=%d/%d, want 0, 2/2", e.Connections, e.FailedAttempts, e.WindowFailedAttempts)
	}
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", FailedAttempts: 3}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.FailedAttempts != 5 || e.WindowFailedAttempts != 3 || e.Connections != 1 {
		t.Errorf("cumulative failedAttempts=%d window=%d connections=%d, want 5/3/1", e.FailedAttempts, e.WindowFailedAttempts, e.Connections)
	}
}

// TestDependencyStatsIncludeRetransmitsAndRTT checks the fields surface all the way to model.Dependency,
// not just onto the stored FlowEdge.
func TestDependencyStatsIncludeRetransmitsAndRTT(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 2, BytesOut: 1000, BytesIn: 2000,
		Method: "ebpf", BytesKnown: true, Iface: "eth0", Retransmits: 4, RttUs: 8000}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	var d *struct {
		iface       string
		retransmits uint64
		rttMs       float64
		perMin      float64
	}
	for _, dep := range deps {
		if dep.Port == 5432 {
			d = &struct {
				iface       string
				retransmits uint64
				rttMs       float64
				perMin      float64
			}{dep.Iface, dep.Retransmits, dep.RttMs, 0}
			if dep.Stats != nil {
				d.perMin = dep.Stats.RetransmitsPerMin
			}
		}
	}
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.iface != "eth0" {
		t.Errorf("iface = %q, want eth0", d.iface)
	}
	if d.retransmits != 4 {
		t.Errorf("retransmits = %d, want 4", d.retransmits)
	}
	if d.rttMs != 8 {
		t.Errorf("rttMs = %v, want 8 (8000us)", d.rttMs)
	}
	if want := float64(4) * 60 / 60; d.perMin != want {
		t.Errorf("retransmitsPerMin = %v, want %v", d.perMin, want)
	}
}

// TestDependencyStatsIncludeJitterHandshakeAndLossPct is Part R's own version of
// TestDependencyStatsIncludeRetransmitsAndRTT above: JitterMs/HandshakeMs are gauges carried through the
// exact same Key.jitter_us/Key.handshake_us path RttMs already uses, and LossPct is computed here (not
// carried on the wire) from window_retransmits/window_segs_out - this test is also what pins that it must
// stay unset, not a fabricated 0%, when nothing has reported a segs_out yet (see the second dependency fed
// below with retransmits but no segs_out).
func TestDependencyStatsIncludeJitterHandshakeAndLossPct(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	z := "a/Deployment/z"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"), wk("a", "Deployment", "z"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 2, BytesOut: 1000, BytesIn: 2000,
		Method: "ebpf", BytesKnown: true, RttUs: 8000, JitterUs: 1500, HandshakeUs: 12000, Retransmits: 2, SegsOut: 100}
	// A second dependency with retransmits but no segs_out at all (a conntrack-only report, say) - LossPct
	// must stay nil for this one, not divide by zero into a fabricated 0%.
	fNoSegs := &continuumv1.Flow{Src: wep(x), Dst: wep(z), Port: 5432, Protocol: "tcp", Connections: 1, Retransmits: 1}
	feed(&c, now, 60, f, fNoSegs)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	sv := func(c, k string) string { return interpret.ServiceID(c, k) }
	var withSegs, withoutSegs *model.Dependency
	for i := range deps {
		dep := &deps[i]
		if dep.Port != 5432 {
			continue
		}
		if dep.To == sv("a", y) {
			withSegs = dep
		} else if dep.To == sv("a", z) {
			withoutSegs = dep
		}
	}
	if withSegs == nil || withoutSegs == nil {
		t.Fatalf("expected both dependencies, got withSegs=%v withoutSegs=%v", withSegs, withoutSegs)
	}
	if withSegs.JitterMs != 1.5 {
		t.Errorf("jitterMs = %v, want 1.5 (1500us)", withSegs.JitterMs)
	}
	if withSegs.HandshakeMs != 12 {
		t.Errorf("handshakeMs = %v, want 12 (12000us)", withSegs.HandshakeMs)
	}
	if withSegs.Stats == nil || withSegs.Stats.LossPct == nil {
		t.Fatal("lossPct should be set when segs_out was reported")
	}
	if want := float64(2) / float64(100) * 100; *withSegs.Stats.LossPct != want {
		t.Errorf("lossPct = %v, want %v (2 retransmits / 100 segs_out)", *withSegs.Stats.LossPct, want)
	}
	if withoutSegs.Stats != nil && withoutSegs.Stats.LossPct != nil {
		t.Errorf("lossPct = %v, want unset (nil) - no segs_out was ever reported for this edge", *withoutSegs.Stats.LossPct)
	}
}

// TestDependencyStatsLossPctSumsAcrossMergedEdges is the case the old implementation got wrong: two
// raw edges - here, the same source workload reaching the same target workload over two different
// addresses (e.g. a load balancer backed by more than one IP) - resolve to one Dependency, and each
// edge has its own, genuinely different window_segs_out. Averaging-by-summing the two ratios
// (10% + 100%) used to produce a nonsensical 110% loss; the right answer sums retransmits and
// segs_out separately across both edges and divides exactly once: (10+1)/(100+1)*100.
func TestDependencyStatsLossPctSumsAcrossMergedEdges(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil,
		wk("a", "Deployment", "x"),
		wk("a", "Deployment", "y",
			&continuumv1.Address{Ip: "203.0.113.10", Port: 5432, Kind: "load-balancer"},
			&continuumv1.Address{Ip: "203.0.113.11", Port: 5432, Kind: "load-balancer"},
		),
	)
	f1 := &continuumv1.Flow{Src: wep(x), Dst: xep("203.0.113.10"), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", Retransmits: 10, SegsOut: 100}
	f2 := &continuumv1.Flow{Src: wep(x), Dst: xep("203.0.113.11"), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", Retransmits: 1, SegsOut: 1}
	feed(&c, now, 60, f1, f2)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	sv := func(c, k string) string { return interpret.ServiceID(c, k) }
	var d *model.Dependency
	for i := range deps {
		if deps[i].Port == 5432 && deps[i].To == sv("a", y) {
			d = &deps[i]
		}
	}
	if d == nil {
		t.Fatal("expected a merged dependency for port 5432")
	}
	if d.Stats == nil || d.Stats.LossPct == nil {
		t.Fatal("lossPct should be set - both edges reported segs_out")
	}
	want := float64(10+1) / float64(100+1) * 100
	if got := *d.Stats.LossPct; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("lossPct = %v, want %v (sum of retransmits over sum of segs_out, not 10%%+100%%=110%%)", got, want)
	}
	if *d.Stats.LossPct > 100 {
		t.Errorf("lossPct = %v, a loss percentage can never exceed 100", *d.Stats.LossPct)
	}
}

// TestDependencyStatsIncludeCwndPacingAndBufferDrops is Part R's remaining three fields:
// CwndSegments/PacingBps are gauges carried through Key.cwnd/Key.pacing_bps the same way RttMs/JitterMs
// already are, and BufferDrops is summed across every FlowEdge folded into the dependency, the same way
// Retransmits/FailedAttempts already are.
func TestDependencyStatsIncludeCwndPacingAndBufferDrops(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf",
		Cwnd: 10, PacingBps: 125000, BufferDrops: 3}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	var d *model.Dependency
	for i := range deps {
		if deps[i].Port == 5432 {
			d = &deps[i]
		}
	}
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.CwndSegments != 10 {
		t.Errorf("cwndSegments = %d, want 10", d.CwndSegments)
	}
	if d.PacingBps != 125000 {
		t.Errorf("pacingBps = %d, want 125000", d.PacingBps)
	}
	if d.BufferDrops != 3 {
		t.Errorf("bufferDrops = %d, want 3", d.BufferDrops)
	}
}

// TestDependencyStatsIncludeDnsRttMs pins the last Part R field: DnsRttMs is a gauge carried through
// Key.dns_rtt_us the same way RttMs/JitterMs/HandshakeMs/CwndSegments/PacingBps already are.
func TestDependencyStatsIncludeDnsRttMs(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 53, Protocol: "udp", Method: "ebpf", Noise: "dns", DnsRttUs: 4200}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	var d *model.Dependency
	for i := range deps {
		if deps[i].Port == 53 {
			d = &deps[i]
		}
	}
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.DnsRttMs != 4.2 {
		t.Errorf("dnsRttMs = %v, want 4.2 (4200us)", d.DnsRttMs)
	}
}

// TestDependencyStatsIncludeFailedAttempts checks failed connection attempts surface all the way to
// model.Dependency, the same way retransmits do (see TestDependencyStatsIncludeRetransmitsAndRTT).
func TestDependencyStatsIncludeFailedAttempts(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Method: "ebpf", FailedAttempts: 6}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	var failedAttempts uint64
	var perMin float64
	var found bool
	for _, dep := range deps {
		if dep.Port == 5432 {
			found = true
			failedAttempts = dep.FailedAttempts
			if dep.Stats != nil {
				perMin = dep.Stats.FailedAttemptsPerMin
			}
		}
	}
	if !found {
		t.Fatal("dependency not found")
	}
	if failedAttempts != 6 {
		t.Errorf("failedAttempts = %d, want 6", failedAttempts)
	}
	if perMin != 6 {
		t.Errorf("failedAttemptsPerMin = %v, want 6", perMin)
	}
}

func TestFlowTableCarriesSniHostAndDnsQueryNames(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: "93.184.216.34"}
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf", SniHost: "example.com"}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil || e.Key.SniHost != "example.com" {
		t.Fatalf("sniHost = %q, want example.com", e.GetKey().GetSniHost())
	}
	// A later report on the same edge with no SNI (a conntrack report, or simply a window the eBPF
	// collector never saw a fresh ClientHello in) must not blank out the hostname already known.
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf"}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.Key.SniHost != "example.com" {
		t.Errorf("a report with no SNI must not blank a previously known one, got %q", e.Key.SniHost)
	}

	dnsSrc, resolver := wep("a/Deployment/x"), wep("kube-system/Deployment/coredns")
	g1 := &continuumv1.Flow{Src: dnsSrc, Dst: resolver, Port: 53, Protocol: "udp", Connections: 1, Method: "ebpf", Noise: "dns", DnsQueryNames: []string{"api.example.com"}}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{g1}}, now)
	g2 := &continuumv1.Flow{Src: dnsSrc, Dst: resolver, Port: 53, Protocol: "udp", Connections: 1, Method: "ebpf", Noise: "dns", DnsQueryNames: []string{"cdn.example.com"}}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{g2}}, now.Add(time.Minute))
	dk := flowKey(g1)
	de := tbl.edges[dk]
	if de == nil || len(de.DnsQueryNames) != 2 || de.DnsQueryNames[0] != "cdn.example.com" || de.DnsQueryNames[1] != "api.example.com" {
		t.Errorf("dnsQueryNames = %v, want [cdn.example.com api.example.com] accumulated across both reports", de.GetDnsQueryNames())
	}
}

// TestDependencyIncludesSniHostAndDnsQueryNames checks both fields surface all the way to
// model.Dependency, the same way retransmits and failedAttempts already do.
func TestDependencyIncludesSniHostAndDnsQueryNames(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	// Fed as first seen a minute ago, not at the same instant as the observedTopology call - past
	// unmatchedGrace (12s), so this unmatched-provider external IP is actually added to the topology
	// instead of withheld as still-too-new. See unmatchedGrace's own comment in observed.go.
	seenAt := now.Add(-1 * time.Minute)
	x := "a/Deployment/x"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"))
	f := &continuumv1.Flow{Src: wep(x), Dst: xep("93.184.216.34"), Port: 443, Protocol: "tcp", Method: "ebpf", SniHost: "example.com", DnsQueryNames: []string{"example.com"}}
	feed(&c, seenAt, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	var sniHost string
	var names []string
	var found bool
	for _, dep := range deps {
		if dep.Port == 443 {
			found = true
			sniHost = dep.SniHost
			names = dep.DnsQueryNames
		}
	}
	if !found {
		t.Fatal("dependency not found")
	}
	if sniHost != "example.com" {
		t.Errorf("sniHost = %q, want example.com", sniHost)
	}
	if len(names) != 1 || names[0] != "example.com" {
		t.Errorf("dnsQueryNames = %v, want [example.com]", names)
	}
}

func TestWellKnownPort(t *testing.T) {
	if name, isDB := wellKnownPort(5432); name != "PostgreSQL" || !isDB {
		t.Errorf("postgres = %q %v", name, isDB)
	}
	if name, isDB := wellKnownPort(80); name != "HTTP" || isDB {
		t.Errorf("http must be named but not a database: %q %v", name, isDB)
	}
	if name, isDB := wellKnownPort(54321); name != "" || isDB {
		t.Errorf("an unlisted port must guess nothing: %q %v", name, isDB)
	}
	// dbPort is kept only as the narrower, pre-existing "is this a database port" question ExternalEndpoint.Kind
	// still asks; it must agree with wellKnownPort's own isDatabase bit rather than drifting into its own list.
	if !dbPort(6379) || dbPort(80) {
		t.Error("dbPort must track wellKnownPorts' isDatabase bit")
	}
}

func TestExternalKnownRangeSetsIdentityAndAggregates(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	// Same reasoning as TestObservedTopologyAcrossClusters: 93.184.216.34 below is deliberately outside
	// every bundled range, and this test expects it to show up unmatched/unlabeled regardless - which
	// only happens once it's past unmatchedGrace, so it's fed as a minute old rather than brand new.
	seenAt := now.Add(-1 * time.Minute)
	c := cluster("c", "", nil, wk("app", "Deployment", "worker"))
	worker := "app/Deployment/worker"

	feed(&c, seenAt, 60,
		flowOf(wep(worker), xep("140.82.112.3"), 443, 3),  // GitHub - single-owner range
		flowOf(wep(worker), xep("140.82.112.4"), 443, 2),  // a different GitHub IP, same port
		flowOf(wep(worker), xep("140.82.112.3"), 22, 1),   // GitHub again, but a different port (git over SSH)
		flowOf(wep(worker), xep("104.16.1.1"), 443, 4),    // Cloudflare edge - shared, must not merge with...
		flowOf(wep(worker), xep("104.24.5.5"), 443, 1),    // ...a second, different Cloudflare-fronted address
		flowOf(wep(worker), xep("93.184.216.34"), 443, 1), // not in the bundled table at all
	)

	_, exts := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	byID := map[string]struct {
		host, kind, name string
		port             int
	}{}
	for _, e := range exts {
		byID[e.ID] = struct {
			host, kind, name string
			port             int
		}{e.Host, e.Kind, e.Name, e.Port}
	}

	ghHTTPS, ghSSH := 0, 0
	cfSeen := map[string]bool{}
	unknownSeen := false
	for _, e := range exts {
		switch {
		case e.Name == "GitHub" && e.Port == 443:
			ghHTTPS++
			if e.Kind != "saas" {
				t.Errorf("github kind = %q, want saas", e.Kind)
			}
		case e.Name == "GitHub" && e.Port == 22:
			ghSSH++
		case e.Name == "Cloudflare":
			cfSeen[e.Host] = true
			if e.Kind != "saas" {
				t.Errorf("cloudflare kind = %q, want saas", e.Kind)
			}
		case e.Host == "93.184.216.34":
			unknownSeen = true
			if e.Kind != "unknown" || e.Name != "" {
				t.Errorf("unmatched address must stay unknown/unnamed, got kind=%q name=%q", e.Kind, e.Name)
			}
		}
	}

	// Two different GitHub IPs on the same port collapse into ONE node - this is the aggregation the
	// known-range match is for.
	if ghHTTPS != 1 {
		t.Errorf("expected exactly one GitHub:443 node (two IPs should have merged), got %d", ghHTTPS)
	}
	// The same identity on a different port stays a separate node - port is still part of identity.
	if ghSSH != 1 {
		t.Errorf("expected a separate GitHub:22 node, got %d", ghSSH)
	}
	// Two different Cloudflare-fronted addresses must NOT merge - Shared means "don't aggregate."
	if len(cfSeen) != 2 {
		t.Errorf("expected two distinct Cloudflare-fronted nodes (Shared must not aggregate), got %d: %v", len(cfSeen), cfSeen)
	}
	if !unknownSeen {
		t.Error("expected the unmatched address to still appear, classified unknown")
	}

	// Evidence: a known-range match is recorded at high confidence, distinctly from the low-confidence
	// generic "seen in traffic" note used elsewhere.
	for _, e := range exts {
		if e.Name != "GitHub" || e.Port != 443 {
			continue
		}
		ev, ok := e.Evidence["identity"]
		if !ok || ev.Confidence != "high" || ev.Signal == "" {
			t.Errorf("github evidence = %+v %v, want a high-confidence identity signal", ev, ok)
		}
	}
}

func TestExternalASNFallbackNamesAnAddressNoOtherTierCovers(t *testing.T) {
	// Neither entries nor hostSuffixes cover this address (no PTR record at all here, same shape as this
	// fix's original motivating case) - only the ASN tier can name it.
	defer netid.SetLookupAddrForTest(func(ctx context.Context, ip string) ([]string, error) {
		return nil, errors.New("no PTR record")
	})()
	defer netid.SetLookupTXTForTest(func(ctx context.Context, name string) ([]string, error) {
		if strings.HasSuffix(name, ".origin.asn.cymru.com") {
			return []string{"64512 | 203.0.113.0/24 | US | arin | 2010-01-01"}, nil
		}
		return []string{"64512 | US | arin | 2010-01-01 | EXAMPLE-NET-OPERATOR"}, nil
	})()

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := cluster("c", "", nil, wk("app", "Deployment", "worker"))
	worker := "app/Deployment/worker"
	feed(&c, now, 60, flowOf(wep(worker), xep("203.0.113.55"), 443, 1))

	// Neither ResolveCached nor ResolveASNCached blocks - both just kick off a background lookup on a cache
	// miss - so poll observedTopology until the ASN answer has landed and been reflected, rather than
	// asserting on a single call.
	deadline := time.Now().Add(2 * time.Second)
	var name, kind, signal, confidence string
	for time.Now().Before(deadline) && name == "" {
		_, exts := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
		for _, e := range exts {
			if e.Host != "203.0.113.55" || e.Name == "" {
				continue
			}
			name, kind = e.Name, e.Kind
			if ev, ok := e.Evidence["identity"]; ok {
				signal, confidence = ev.Signal, ev.Confidence
			}
		}
		if name == "" {
			time.Sleep(5 * time.Millisecond)
		}
	}

	if name != "EXAMPLE-NET-OPERATOR" {
		t.Fatalf("name = %q, want the ASN lookup's org name once it lands", name)
	}
	if kind != "unknown" {
		t.Errorf("kind = %q, want unknown (an ASN lookup names the network operator, not a service kind)", kind)
	}
	if confidence != "low" {
		t.Errorf("evidence confidence = %q, want low - the weakest of the three identity signals", confidence)
	}
	if !strings.Contains(signal, "AS64512") {
		t.Errorf("evidence signal = %q, want it to cite the ASN (AS64512)", signal)
	}
}

// Pins oldestEdges directly, independent of flowTable/apply plumbing: given a handful of ages, it must
// return exactly the n least-recently-seen keys - not merely the right count, which is all the older
// sort.Slice-based version's own test (TestFlowBatchValidationAndTable's cap check, above) ever verified.
func TestOldestEdgesPicksTheLeastRecentlySeenKeys(t *testing.T) {
	base := time.Now()
	edges := map[string]*continuumv1.FlowEdge{
		"newest":  {LastSeen: timestamppb.New(base.Add(5 * time.Minute))},
		"oldest":  {LastSeen: timestamppb.New(base)},
		"middle1": {LastSeen: timestamppb.New(base.Add(1 * time.Minute))},
		"middle2": {LastSeen: timestamppb.New(base.Add(2 * time.Minute))},
		"middle3": {LastSeen: timestamppb.New(base.Add(3 * time.Minute))},
	}

	got := oldestEdges(edges, 2)
	want := map[string]bool{"oldest": true, "middle1": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] || got[0] == got[1] {
		t.Fatalf("oldestEdges(edges, 2) = %v, want exactly {oldest, middle1} in either order", got)
	}

	// n covering the whole map: every key comes back, nothing is left out or duplicated.
	all := oldestEdges(edges, len(edges))
	if len(all) != len(edges) {
		t.Fatalf("oldestEdges(edges, len(edges)) returned %d keys, want %d", len(all), len(edges))
	}
	seen := map[string]bool{}
	for _, k := range all {
		if seen[k] {
			t.Fatalf("oldestEdges returned %q twice: %v", k, all)
		}
		seen[k] = true
	}

	if got := oldestEdges(edges, 0); len(got) != 0 {
		t.Fatalf("oldestEdges(edges, 0) = %v, want none", got)
	}
}
