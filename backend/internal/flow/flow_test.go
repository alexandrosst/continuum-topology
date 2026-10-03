package flow

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/probe"

	"google.golang.org/protobuf/encoding/protojson"
)

func testIndex() *collect.Index {
	return &collect.Index{
		Pods:        map[string]string{"10.42.0.5": "shop/Deployment/cart", "10.42.0.7": "shop/Deployment/db", "10.42.0.9": "kube-system/Deployment/coredns"},
		PodNames:    map[string]string{"10.42.0.5": "cart-7d9f8b-abc12", "10.42.0.7": "db-6c5d4a-xyz34"},
		Services:    map[string][]string{"10.43.0.20": {"shop/Deployment/cart"}, "10.43.0.21": {"shop/Deployment/db"}, "10.43.0.10": {"kube-system/Deployment/coredns"}},
		Nodes:       map[string]string{"192.168.1.10": "n1"},
		Opaque:      map[string]bool{"10.43.0.1": true},
		NodePorts:   map[int32][]string{30080: {"shop/Deployment/cart"}},
		PodCIDRs:    []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")},
		ServiceCIDR: netip.MustParsePrefix("10.43.0.0/16"),
	}
}

func raw(client bool, local, peer string, port uint32) *continuumv1.RawFlow {
	return &continuumv1.RawFlow{Client: client, LocalIp: local, PeerIp: peer, Port: port, Protocol: "tcp", Connections: 3, BytesOut: 300, BytesIn: 900}
}

func TestResolveAttributesAndDrops(t *testing.T) {
	r := NewResolver(testIndex)
	type want struct {
		ok            bool
		src, dst      string
		srcKind, dstK continuumv1.FlowEndpoint_Kind
		noise         string
	}
	W, E := continuumv1.FlowEndpoint_WORKLOAD, continuumv1.FlowEndpoint_EXTERNAL
	cases := []struct {
		name string
		in   *continuumv1.RawFlow
		want want
	}{
		{"pod calls a Service by cluster IP", raw(true, "10.42.0.5", "10.43.0.21", 5432), want{true, "shop/Deployment/cart", "shop/Deployment/db", W, W, ""}},
		{"pod calls a pod directly", raw(true, "10.42.0.5", "10.42.0.7", 5432), want{true, "shop/Deployment/cart", "shop/Deployment/db", W, W, ""}},
		{"pod calls the internet", raw(true, "10.42.0.5", "93.184.216.34", 443), want{true, "shop/Deployment/cart", "93.184.216.34", W, E, ""}},
		{"pod calls another cluster's address", raw(true, "10.42.0.5", "198.51.100.7", 80), want{true, "shop/Deployment/cart", "198.51.100.7", W, E, ""}},
		{"a CNI gateway address inside the pod range is not a phantom external endpoint", raw(true, "10.42.0.5", "10.42.0.1", 80), want{ok: false}},
		{"an unresolvable address inside the Service range is not a phantom external endpoint", raw(true, "10.42.0.5", "10.43.0.99", 80), want{ok: false}},
		{"inbound from a CNI gateway address is not a phantom external endpoint", raw(false, "10.42.0.5", "10.42.0.2", 8080), want{ok: false}},
		{"dns is noise", raw(true, "10.42.0.5", "10.43.0.10", 53), want{true, "shop/Deployment/cart", "kube-system/Deployment/coredns", W, W, "dns"}},
		{"traffic touching kube-system is system noise", raw(true, "10.42.0.9", "10.43.0.21", 5432), want{true, "kube-system/Deployment/coredns", "shop/Deployment/db", W, W, "system"}},
		{"a node port reaches the workload behind it", raw(true, "10.42.0.5", "192.168.1.10", 30080), want{true, "shop/Deployment/cart", "shop/Deployment/cart", W, W, ""}},
		{"a node service is not application traffic", raw(true, "10.42.0.5", "192.168.1.10", 10250), want{ok: false}},
		{"the API server's Service is dropped", raw(true, "10.42.0.5", "10.43.0.1", 443), want{ok: false}},
		{"node processes are not workloads", raw(true, "192.168.1.10", "93.184.216.34", 443), want{ok: false}},
		{"a pod we cannot place is dropped", raw(true, "10.42.0.200", "93.184.216.34", 443), want{ok: false}},
		{"loopback is dropped", raw(true, "127.0.0.1", "127.0.0.1", 8080), want{ok: false}},
		{"inbound from outside is recorded from the receiving side", raw(false, "10.42.0.5", "203.0.113.50", 8080), want{true, "203.0.113.50", "shop/Deployment/cart", E, W, ""}},
		{"inbound from a pod of this cluster is counted by the caller, not here", raw(false, "10.42.0.7", "10.42.0.5", 5432), want{ok: false}},
		{"inbound masqueraded through a node is counted by the caller", raw(false, "10.42.0.5", "192.168.1.10", 8080), want{ok: false}},
		{"inbound to a node process is dropped", raw(false, "192.168.1.10", "203.0.113.50", 22), want{ok: false}},
		{"IPv4-mapped IPv6 is understood", raw(true, "::ffff:10.42.0.5", "::ffff:10.43.0.21", 5432), want{true, "shop/Deployment/cart", "shop/Deployment/db", W, W, ""}},
		{"garbage is refused", raw(true, "not-an-ip", "10.43.0.21", 5432), want{ok: false}},
		{"port zero is refused", raw(true, "10.42.0.5", "10.43.0.21", 0), want{ok: false}},
	}
	for _, c := range cases {
		f, ok := r.Resolve(c.in, "ebpf", true)
		if ok != c.want.ok {
			t.Errorf("%s: ok=%v, want %v", c.name, ok, c.want.ok)
			continue
		}
		if !ok {
			continue
		}
		if ref(f.Src) != c.want.src || ref(f.Dst) != c.want.dst || f.Src.Kind != c.want.srcKind || f.Dst.Kind != c.want.dstK || f.Noise != c.want.noise {
			t.Errorf("%s: got %v -> %v noise=%q", c.name, f.Src, f.Dst, f.Noise)
		}
	}
	udp := raw(true, "10.42.0.5", "10.43.0.21", 5432)
	udp.Protocol = "udp"
	if f, ok := r.Resolve(udp, "conntrack", true); !ok || f.Protocol != "udp" || f.Noise != "" {
		t.Errorf("udp between workloads must be kept as udp: %v %v", f, ok)
	}
	ntp := raw(true, "10.42.0.5", "93.184.216.34", 123)
	ntp.Protocol = "udp"
	if f, ok := r.Resolve(ntp, "conntrack", true); !ok || f.Noise != "system" {
		t.Errorf("time sync is machinery, not a dependency: %v %v", f, ok)
	}
	sctp := raw(true, "10.42.0.5", "10.43.0.21", 5432)
	sctp.Protocol = "sctp"
	if _, ok := r.Resolve(sctp, "ebpf", true); ok {
		t.Error("only TCP and UDP are observed")
	}
}

// A connection attempt that never reached ESTABLISHED (0 connections, some failed_attempts) is
// attributed exactly like a successful one - same src/dst/noise rules - with the failure count carried
// onto the resolved Flow rather than dropped for having nothing else to say.
func TestResolveCarriesFailedAttemptsThroughUnattributedOtherwise(t *testing.T) {
	r := NewResolver(testIndex)
	fr := &continuumv1.RawFlow{Client: true, LocalIp: "10.42.0.5", PeerIp: "93.184.216.34", Port: 443, Protocol: "tcp",
		FailedAttempts: 4, FailedRefused: 1, FailedTimeout: 3}
	f, ok := r.Resolve(fr, "ebpf", true)
	if !ok {
		t.Fatal("a pure-failure observation must still resolve")
	}
	if f.Connections != 0 || f.FailedAttempts != 4 {
		t.Errorf("got connections=%d failedAttempts=%d, want 0 and 4", f.Connections, f.FailedAttempts)
	}
	if ref(f.Src) != "shop/Deployment/cart" || f.Dst.Kind != continuumv1.FlowEndpoint_EXTERNAL {
		t.Errorf("attribution should be unaffected by this being a failure: %v -> %v", f.Src, f.Dst)
	}
}

// SrcPod/DstPod carry the specific pod's own name only when that side resolved to one particular live
// pod - never through a Service (which hides exactly that), and never once the pod has aged out of the
// live index into the resolver's "recent" memory (recent only remembers the workload, not the pod).
func TestResolveSetsPodNamesOnlyFromASpecificLivePod(t *testing.T) {
	r := NewResolver(testIndex)
	// pod calls another pod directly: both ends are specific, live pods.
	f, ok := r.Resolve(raw(true, "10.42.0.5", "10.42.0.7", 5432), "ebpf", true)
	if !ok || f.SrcPod != "cart-7d9f8b-abc12" || f.DstPod != "db-6c5d4a-xyz34" {
		t.Errorf("pod-to-pod should carry both pod names: src=%q dst=%q", f.SrcPod, f.DstPod)
	}
	// pod calls a Service by cluster IP: the backing pod that actually answered is unknowable, so DstPod
	// must stay empty even though the Service happens to resolve to the same workload as above.
	f, ok = r.Resolve(raw(true, "10.42.0.5", "10.43.0.21", 5432), "ebpf", true)
	if !ok || f.SrcPod != "cart-7d9f8b-abc12" || f.DstPod != "" {
		t.Errorf("a Service-mediated destination must not claim one specific pod: src=%q dst=%q", f.SrcPod, f.DstPod)
	}
	// inbound from outside, received by a known local pod.
	f, ok = r.Resolve(raw(false, "10.42.0.5", "203.0.113.50", 8080), "ebpf", true)
	if !ok || f.DstPod != "cart-7d9f8b-abc12" {
		t.Errorf("inbound traffic received by a known pod should carry its name: dst=%q", f.DstPod)
	}
}

func TestResolveRemembersPodsThatAreGone(t *testing.T) {
	ix := testIndex()
	now := time.Now()
	r := NewResolver(func() *collect.Index { return ix })
	r.now = func() time.Time { return now }
	if _, ok := r.Resolve(raw(true, "10.42.0.5", "10.43.0.21", 5432), "ebpf", true); !ok {
		t.Fatal("known pod")
	}
	// the pod finishes and leaves the cluster's index
	delete(ix.Pods, "10.42.0.5")
	delete(ix.PodNames, "10.42.0.5")
	now = now.Add(10 * time.Second)
	f, ok := r.Resolve(raw(true, "10.42.0.5", "10.43.0.21", 5432), "ebpf", true)
	if !ok || ref(f.Src) != "shop/Deployment/cart" {
		t.Fatalf("a pod that just finished is still attributed: %v %v", f, ok)
	}
	if f.SrcPod != "" {
		t.Errorf("a pod only remembered through history must not claim a pod name: got %q", f.SrcPod)
	}
	// but not forever, since addresses are reused
	now = now.Add(time.Hour)
	if _, ok := r.Resolve(raw(true, "10.42.0.5", "10.43.0.21", 5432), "ebpf", true); ok {
		t.Fatal("an old mapping must expire")
	}
}

func TestAggregatorSumsAndBounds(t *testing.T) {
	a := NewAggregator()
	t0 := time.Now()
	a.now = func() time.Time { return t0 }
	a.since = t0
	mk := func(src, dst string, conns uint64) *continuumv1.Flow {
		return &continuumv1.Flow{Src: workload(src), Dst: workload(dst), Port: 80, Protocol: "tcp", Connections: conns, BytesOut: 10, BytesIn: 20, Method: "conntrack", BytesKnown: true}
	}
	withFailure := mk("a", "b", 2)
	withFailure.FailedAttempts = 1
	a.Add(withFailure)
	second := mk("a", "b", 3)
	second.FailedAttempts = 2
	a.Add(second)
	a.Add(mk("a", "c", 9))
	a.AddLost(4)
	t0 = t0.Add(60 * time.Second)
	b := a.Flush()
	if b.WindowSeconds != 60 || b.Lost != 4 || len(b.Flows) != 2 || b.Seq != 1 {
		t.Fatalf("batch = %v", b)
	}
	if b.Flows[0].Dst.Ref != "c" || b.Flows[1].Connections != 5 || b.Flows[1].BytesOut != 20 || b.Flows[1].FailedAttempts != 3 {
		t.Fatalf("busiest first, sums added (including failed attempts): %v", b.Flows)
	}
	if a.Flush() != nil {
		t.Fatal("nothing seen, nothing to send")
	}
	for i := 0; i < MaxFlowsPerBatch+10; i++ {
		a.Add(mk("s"+strconv.Itoa(i), "d", 1))
	}
	t0 = t0.Add(time.Minute)
	b = a.Flush()
	if len(b.Flows) != MaxFlowsPerBatch || b.Lost != 10 {
		t.Fatalf("a window is bounded and says what it left out: %d flows, lost %d", len(b.Flows), b.Lost)
	}
}

func TestAggregatorCarriesSniHostAndDnsQueryNames(t *testing.T) {
	a := NewAggregator()
	base := func() *continuumv1.Flow {
		return &continuumv1.Flow{Src: workload("app"), Dst: workload("coredns"), Port: 53, Protocol: "udp", Connections: 1, Method: "ebpf"}
	}
	f1 := base()
	f1.DnsQueryNames = []string{"first.example.com"}
	a.Add(f1)
	f2 := base()
	f2.DnsQueryNames = []string{"second.example.com"}
	a.Add(f2)
	// The same name again must not duplicate the list.
	f3 := base()
	f3.DnsQueryNames = []string{"first.example.com"}
	a.Add(f3)
	b := a.Flush()
	if len(b.Flows) != 1 {
		t.Fatalf("all three should merge into one held edge: %d", len(b.Flows))
	}
	// mergeDNSNames prepends each newly-seen distinct name, so the most recently first-seen name (here,
	// "second.example.com", added after "first.example.com" and never repeated) ends up in front; the
	// repeat of "first.example.com" in f3 must not have moved it or duplicated it.
	got := b.Flows[0].DnsQueryNames
	if len(got) != 2 || got[0] != "second.example.com" || got[1] != "first.example.com" {
		t.Errorf("dns query names = %v, want [second.example.com first.example.com] (newest-distinct first, no duplicate)", got)
	}

	sni := a2(t)
	tf1 := &continuumv1.Flow{Src: workload("app"), Dst: &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: "93.184.216.34"}, Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf", SniHost: "example.com"}
	sni.Add(tf1)
	tf2 := &continuumv1.Flow{Src: workload("app"), Dst: &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: "93.184.216.34"}, Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf"} // no SNI on this report
	sni.Add(tf2)
	sb := sni.Flush()
	if len(sb.Flows) != 1 || sb.Flows[0].SniHost != "example.com" || sb.Flows[0].Connections != 2 {
		t.Errorf("sniHost=%q connections=%d, want example.com/2 (a report with none must not blank a known one)", sb.Flows[0].SniHost, sb.Flows[0].Connections)
	}
}

// TestAggregatorCarriesTlsHandshake pins TlsHandshake through Add/Flush with the same gauge treatment as
// SniHost right above: the latest decided outcome (OK or FAILED) wins, and a later report with no
// ClientHello seen that window (UNKNOWN, the zero value) must never blank out an already-decided one.
func TestAggregatorCarriesTlsHandshake(t *testing.T) {
	a := a2(t)
	dst := &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: "93.184.216.34"}
	f1 := &continuumv1.Flow{Src: workload("app"), Dst: dst, Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf", TlsHandshake: continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK}
	a.Add(f1)
	f2 := &continuumv1.Flow{Src: workload("app"), Dst: dst, Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf"} // no ClientHello seen this window
	a.Add(f2)
	b := a.Flush()
	if len(b.Flows) != 1 || b.Flows[0].TlsHandshake != continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK {
		t.Errorf("tlsHandshake = %v, want OK (a report with none must not blank a known one)", b.Flows[0].TlsHandshake)
	}
}

// TestAggregatorCarriesJitterSegsOutAndHandshake pins the three Part R gauge/sum fields through Add: SegsOut
// sums like Retransmits/bytes (the denominator for a loss percentage computed downstream, never here);
// JitterUs and HandshakeUs are gauges like RttUs - the latest non-zero sample wins, and a later report with
// no sample (0) must never blank out an already-known one.
func TestAggregatorCarriesJitterSegsOutAndHandshake(t *testing.T) {
	a := a2(t)
	f1 := &continuumv1.Flow{Src: workload("app"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", JitterUs: 500, SegsOut: 10, HandshakeUs: 2000}
	a.Add(f1)
	f2 := &continuumv1.Flow{Src: workload("app"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", SegsOut: 15} // no jitter/handshake sample this time
	a.Add(f2)
	b := a.Flush()
	if len(b.Flows) != 1 {
		t.Fatalf("both should merge into one held edge: %d", len(b.Flows))
	}
	got := b.Flows[0]
	if got.SegsOut != 25 {
		t.Errorf("segsOut = %d, want 25 (summed like retransmits)", got.SegsOut)
	}
	if got.JitterUs != 500 {
		t.Errorf("jitterUs = %d, want 500 (a later report with no sample must not blank a known gauge)", got.JitterUs)
	}
	if got.HandshakeUs != 2000 {
		t.Errorf("handshakeUs = %d, want 2000 (same gauge treatment)", got.HandshakeUs)
	}
}

// TestAggregatorCarriesCwndPacingAndBufferDrops pins the remaining three Part R fields: Cwnd and
// PacingBps are gauges, same latest-non-zero-wins treatment as JitterUs/HandshakeUs above; BufferDrops
// sums like SegsOut/Retransmits (it is the receive-side counterpart to those two sender-side counters).
func TestAggregatorCarriesCwndPacingAndBufferDrops(t *testing.T) {
	a := a2(t)
	f1 := &continuumv1.Flow{Src: workload("app"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", Cwnd: 10, PacingBps: 125000, BufferDrops: 1}
	a.Add(f1)
	f2 := &continuumv1.Flow{Src: workload("app"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", BufferDrops: 2} // no cwnd/pacing sample this time
	a.Add(f2)
	b := a.Flush()
	if len(b.Flows) != 1 {
		t.Fatalf("both should merge into one held edge: %d", len(b.Flows))
	}
	got := b.Flows[0]
	if got.Cwnd != 10 {
		t.Errorf("cwnd = %d, want 10 (a later report with no sample must not blank a known gauge)", got.Cwnd)
	}
	if got.PacingBps != 125000 {
		t.Errorf("pacingBps = %d, want 125000 (same gauge treatment)", got.PacingBps)
	}
	if got.BufferDrops != 3 {
		t.Errorf("bufferDrops = %d, want 3 (summed like segsOut/retransmits)", got.BufferDrops)
	}
}

// TestAggregatorCarriesDnsRttUs pins the last Part R field: DnsRttUs is a gauge, same latest-non-zero-
// wins treatment as Cwnd/PacingBps above, carried through from the synthetic zero-count row
// observer_bpf.go emits for a DNS-latency sample (see its own Collect() doc comment on why that row has
// no counts of its own) the same way a DnsQueryNames row already merges into the same held edge.
func TestAggregatorCarriesDnsRttUs(t *testing.T) {
	a := a2(t)
	f1 := &continuumv1.Flow{Src: workload("app"), Dst: workload("resolver"), Port: 53, Protocol: "udp", Method: "ebpf", DnsRttUs: 4200}
	a.Add(f1)
	f2 := &continuumv1.Flow{Src: workload("app"), Dst: workload("resolver"), Port: 53, Protocol: "udp", Method: "ebpf", Connections: 1} // no latency sample this time
	a.Add(f2)
	b := a.Flush()
	if len(b.Flows) != 1 {
		t.Fatalf("both should merge into one held edge: %d", len(b.Flows))
	}
	got := b.Flows[0]
	if got.DnsRttUs != 4200 {
		t.Errorf("dnsRttUs = %d, want 4200 (a later report with no sample must not blank a known gauge)", got.DnsRttUs)
	}
}

// TestAggregatorBuildsASeparatePodLevelBreakdown pins podFlows' whole point: two distinct pod pairs
// behind the same workload pair stay distinguishable there, even though flows above (deliberately)
// collapses them into one edge.
func TestAggregatorBuildsASeparatePodLevelBreakdown(t *testing.T) {
	a := a2(t)
	f1 := &continuumv1.Flow{Src: workload("shop/Deployment/cart"), Dst: workload("shop/Deployment/db"), Port: 5432, Protocol: "tcp", Connections: 2, BytesOut: 100, SrcPod: "cart-aaa", DstPod: "db-xxx"}
	a.Add(f1)
	f2 := &continuumv1.Flow{Src: workload("shop/Deployment/cart"), Dst: workload("shop/Deployment/db"), Port: 5432, Protocol: "tcp", Connections: 3, BytesOut: 200, SrcPod: "cart-bbb", DstPod: "db-xxx"}
	a.Add(f2)
	// a second report for the first pod pair must merge, not duplicate.
	f3 := &continuumv1.Flow{Src: workload("shop/Deployment/cart"), Dst: workload("shop/Deployment/db"), Port: 5432, Protocol: "tcp", Connections: 1, BytesOut: 10, SrcPod: "cart-aaa", DstPod: "db-xxx"}
	a.Add(f3)
	// traffic with no pod identity on either side must not appear in podFlows at all.
	a.Add(&continuumv1.Flow{Src: workload("shop/Deployment/cart"), Dst: &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: "93.184.216.34"}, Port: 443, Protocol: "tcp", Connections: 1})
	b := a.Flush()
	if len(b.Flows) != 2 {
		t.Fatalf("the workload-level table still collapses by pod: %d flows, want 2 (cart->db, cart->internet)", len(b.Flows))
	}
	if len(b.PodFlows) != 2 {
		t.Fatalf("the pod-level table keeps the two distinct pod pairs apart: %d, want 2", len(b.PodFlows))
	}
	byPods := map[string]*continuumv1.Flow{}
	for _, f := range b.PodFlows {
		byPods[f.SrcPod+">"+f.DstPod] = f
	}
	if g := byPods["cart-aaa>db-xxx"]; g == nil || g.Connections != 3 || g.BytesOut != 110 {
		t.Errorf("cart-aaa>db-xxx = %+v, want connections=3 bytesOut=110 (merged across two reports)", g)
	}
	if g := byPods["cart-bbb>db-xxx"]; g == nil || g.Connections != 3 || g.BytesOut != 200 {
		t.Errorf("cart-bbb>db-xxx = %+v, want connections=3 bytesOut=200", g)
	}
}

// TestPodFlowsNeverAliasesTheWorkloadLevelEntry guards the exact aliasing bug addPod's own doc comment
// warns about: a brand-new podFlows entry must be an independent object from flows' entry for the same
// key, or a later, unrelated pod pair reusing that workload pair's flows[k] object would silently
// inflate this pod pair's already-flushed-independent counters too.
func TestPodFlowsNeverAliasesTheWorkloadLevelEntry(t *testing.T) {
	a := a2(t)
	f1 := &continuumv1.Flow{Src: workload("cart"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, SrcPod: "cart-aaa"}
	a.Add(f1) // first touch of key k: flows[k] is literally f1, and podFlows[pk1] must be a clone of it
	f2 := &continuumv1.Flow{Src: workload("cart"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 100, SrcPod: "cart-bbb"}
	a.Add(f2) // same k, a different pod pair: this merges into flows[k] (now f1, mutated in place)
	b := a.Flush()
	for _, f := range b.PodFlows {
		if f.SrcPod == "cart-aaa" && f.Connections != 1 {
			t.Fatalf("cart-aaa's own entry must still read 1, not have absorbed cart-bbb's 100: got %d", f.Connections)
		}
	}
}

// TestPodFlowsResetsEveryFlushUnlikeFlows pins podFlows' "right now" semantics: unlike flows, nothing
// about it survives a Flush, even though the exact same pod pair keeps reporting traffic.
func TestPodFlowsResetsEveryFlushUnlikeFlows(t *testing.T) {
	a := a2(t)
	a.Add(&continuumv1.Flow{Src: workload("cart"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 5, SrcPod: "cart-aaa"})
	b1 := a.Flush()
	if len(b1.PodFlows) != 1 || b1.PodFlows[0].Connections != 5 {
		t.Fatalf("first window = %v", b1.PodFlows)
	}
	if b2 := a.Flush(); b2 != nil {
		t.Fatalf("an immediate second flush with nothing new added must report nothing: %v", b2)
	}
	a.Add(&continuumv1.Flow{Src: workload("cart"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 2, SrcPod: "cart-aaa"})
	b3 := a.Flush()
	if len(b3.PodFlows) != 1 || b3.PodFlows[0].Connections != 2 {
		t.Fatalf("a later window must start from zero, not carry the first window's 5 forward: %v", b3.PodFlows)
	}
}

// TestPodFlowsIsBoundedAndPausingForgetsIt mirrors bounded_test.go's own coverage of flows (eviction,
// and a paused aggregator holding nothing) for the separate pod-level table.
func TestPodFlowsIsBoundedAndPausingForgetsIt(t *testing.T) {
	a := NewAggregator()
	a.SetPodMax(10)
	for i := 0; i < 100; i++ {
		a.Add(&continuumv1.Flow{Src: workload("cart"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, SrcPod: "cart-" + strconv.Itoa(i)})
	}
	b := a.Flush()
	if len(b.PodFlows) > 20 {
		t.Fatalf("holding %d pod-level entries with a cap of 10", len(b.PodFlows))
	}
	a.Add(&continuumv1.Flow{Src: workload("cart"), Dst: workload("db"), Port: 5432, Protocol: "tcp", Connections: 1, SrcPod: "cart-x"})
	a.SetPaused(true)
	if b := a.Flush(); b != nil {
		t.Fatal("pausing must forget the pod-level table too")
	}
}

// a2 is a second, freshly time-controlled Aggregator for the SNI half of the test above, which needs its
// own window rather than sharing the first Aggregator's already-flushed one.
func a2(t *testing.T) *Aggregator {
	t.Helper()
	a := NewAggregator()
	now := time.Now()
	a.now = func() time.Time { return now }
	a.since = now
	return a
}

func TestReceiverVerifiesAndAttributes(t *testing.T) {
	secret := []byte("flow-secret-0123456789abcdef0123")
	p := NewPipeline(secret, testIndex, nil)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	post := func(sec []byte, node string, rep *continuumv1.FlowReport, mutate func(*http.Request)) int {
		body, _ := protojson.Marshal(rep)
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req, _ := http.NewRequest("POST", srv.URL+PathReport, bytes.NewReader(body))
		req.Header.Set(probe.HeaderNode, node)
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.Sign(sec, ts, node, body))
		if mutate != nil {
			mutate(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	rep := &continuumv1.FlowReport{Method: "ebpf", Node: "n1", BytesKnown: true, Lost: 2, Flows: []*continuumv1.RawFlow{raw(true, "10.42.0.5", "10.43.0.21", 5432), raw(true, "10.42.0.5", "10.43.0.1", 443)}}
	if c := post(secret, "n1", rep, nil); c != 204 {
		t.Fatalf("status %d", c)
	}
	p.Aggregator.now = func() time.Time { return time.Now().Add(30 * time.Second) }
	b := p.Aggregator.Flush()
	if b == nil || len(b.Flows) != 1 || b.Lost != 2 || b.Flows[0].Dst.Ref != "shop/Deployment/db" || b.Flows[0].Connections != 3 {
		t.Fatalf("batch = %v", b)
	}
	if c := post([]byte("another secret, long enough!!!!!!"), "n1", rep, nil); c != 401 {
		t.Errorf("wrong secret: %d", c)
	}
	// the node probe's secret must not work here: a compromised probe pod cannot forge traffic
	if c := post([]byte("probe-secret-0123456789abcdef01234"), "n1", rep, nil); c != 401 {
		t.Errorf("another secret: %d", c)
	}
	if c := post(secret, "../x", rep, nil); c != 400 {
		t.Errorf("bad node name: %d", c)
	}
	if c := post(secret, "n1", &continuumv1.FlowReport{Method: "magic"}, nil); c != 400 {
		t.Errorf("unknown method: %d", c)
	}
	big := &continuumv1.FlowReport{Method: "ebpf"}
	for i := 0; i < MaxRawFlows+1; i++ {
		big.Flows = append(big.Flows, &continuumv1.RawFlow{Client: true, LocalIp: "10.42.0.5", PeerIp: "1.1.1.1", Port: 1, Protocol: "tcp"})
	}
	if c := post(secret, "n1", big, nil); c != 400 && c != 413 {
		t.Errorf("too many flows: %d", c)
	}
	huge := &continuumv1.FlowReport{Method: "ebpf", Node: strings.Repeat("x", MaxBody)}
	if c := post(secret, "n1", huge, nil); c != 413 {
		t.Errorf("oversized body: %d", c)
	}
	if c := post(secret, "n1", rep, func(r *http.Request) { r.Header.Set(probe.HeaderTime, "1") }); c != 401 {
		t.Errorf("stale timestamp: %d", c)
	}
}

// The conntrack collector cannot tell which end is a pod, so it offers every connection from both ends.
// Exactly one of them may survive attribution, or traffic would be counted twice.
func TestOfferingBothEndsCountsEachConnectionOnce(t *testing.T) {
	r := NewResolver(testIndex)
	count := func(raws ...*continuumv1.RawFlow) int {
		n := 0
		for _, rw := range raws {
			if _, ok := r.Resolve(rw, "conntrack", true); ok {
				n++
			}
		}
		return n
	}
	// cart -> db Service (NAT'd to the db pod).
	if n := count(raw(true, "10.42.0.5", "10.43.0.21", 5432), raw(false, "10.42.0.7", "10.42.0.5", 5432)); n != 1 {
		t.Errorf("pod to Service kept %d times, want 1", n)
	}
	// A client outside the cluster arriving on a NodePort, answered by the cart pod.
	if n := count(raw(true, "203.0.113.9", "192.168.1.10", 30080), raw(false, "10.42.0.5", "203.0.113.9", 8080)); n != 1 {
		t.Errorf("external inbound kept %d times, want 1", n)
	}
	// A pod calling out to the internet, seen from the far end as well.
	if n := count(raw(true, "10.42.0.5", "93.184.216.34", 443), raw(false, "93.184.216.34", "10.42.0.5", 443)); n != 1 {
		t.Errorf("pod to internet kept %d times, want 1", n)
	}
}

func TestQuietCollectorsAreStillReported(t *testing.T) {
	a := NewAggregator()
	if a.Flush() != nil {
		t.Fatal("nothing seen, nothing to say")
	}
	a.Seen("n1", "ebpf", true, nil, 0)
	a.Seen("n1", "conntrack", false, nil, 0) // the same node's UDP supplement
	b := a.Flush()
	if b == nil || len(b.Flows) != 0 || len(b.Collectors) != 2 || b.Collectors[0].Node != "n1" || b.Collectors[0].Method != "conntrack" {
		t.Fatalf("batch = %+v", b)
	}
	// A collector that stops reporting drops out after the TTL.
	a.now = func() time.Time { return time.Now().Add(collectorTTL + time.Minute) }
	if b := a.Flush(); b != nil {
		t.Errorf("a vanished collector must not be listed forever: %+v", b)
	}
}

// TestAggregatorCarriesLinkSaturation covers Seen's own per-collector link-saturation reading
// (continuumv1.LinkSaturation): a gauge of the window just reported, replaced wholesale on the next
// Seen for the same node/method, never accumulated across windows the way flows/bytes are.
func TestAggregatorCarriesLinkSaturation(t *testing.T) {
	a := NewAggregator()
	pct := 61.0
	a.Seen("n1", "ebpf", true, []*continuumv1.LinkSaturation{{Iface: "eth0", ThroughputBps: 123, SaturationPct: &pct}}, 0)
	b := a.Flush()
	if b == nil || len(b.Collectors) != 1 {
		t.Fatalf("batch = %+v", b)
	}
	ls := b.Collectors[0].LinkSaturation
	if len(ls) != 1 || ls[0].Iface != "eth0" || ls[0].ThroughputBps != 123 || ls[0].SaturationPct == nil || *ls[0].SaturationPct != 61 {
		t.Errorf("link saturation = %+v", ls)
	}
	// The next window's Seen replaces it wholesale, even with nothing at all (a quiet window on every
	// interface) - stale saturation from a window that has already closed must never linger.
	a.Seen("n1", "ebpf", true, nil, 0)
	b2 := a.Flush()
	if b2 == nil || len(b2.Collectors) != 1 || b2.Collectors[0].LinkSaturation != nil {
		t.Errorf("batch = %+v, want link saturation cleared", b2)
	}
}

// TestAggregatorCarriesSnatExhaustion covers Seen's own per-collector SNAT/ephemeral-port-exhaustion
// reading (continuumv1.FlowReport.snat_exhaustion -> CollectorInfo.snat_exhaustion): wholesale-replaced
// on the next Seen for the same node/method, the same treatment TestAggregatorCarriesLinkSaturation
// above covers for link saturation - even though the number itself is already a running total on the
// collector's own side (see flow.c's snat_exhaustion doc comment), the aggregator never sums reports
// into something larger than what the collector itself is currently reporting.
func TestAggregatorCarriesSnatExhaustion(t *testing.T) {
	a := NewAggregator()
	a.Seen("n1", "ebpf", true, nil, 7)
	b := a.Flush()
	if b == nil || len(b.Collectors) != 1 || b.Collectors[0].SnatExhaustion != 7 {
		t.Fatalf("batch = %+v", b)
	}
	// A collector restart resets its own counter to 0; Seen must carry that back down too, not keep
	// whatever the highest reading ever seen was.
	a.Seen("n1", "ebpf", true, nil, 0)
	b2 := a.Flush()
	if b2 == nil || len(b2.Collectors) != 1 || b2.Collectors[0].SnatExhaustion != 0 {
		t.Errorf("batch = %+v, want snat_exhaustion reset to 0", b2)
	}
}

// A namespace the agent was told to leave out must not turn up as an unknown address in the traffic of the ones it reports.
func TestTrafficTouchingAnOutOfScopeNamespaceIsDropped(t *testing.T) {
	ix := testIndex()
	ix.Hidden = map[string]bool{"10.42.0.30": true, "10.43.0.30": true} // a pod and a Service of the excluded namespace
	r := NewResolver(func() *collect.Index { return ix })
	for name, in := range map[string]*continuumv1.RawFlow{
		"a reported pod calls an excluded pod":     raw(true, "10.42.0.5", "10.42.0.30", 5432),
		"a reported pod calls an excluded Service": raw(true, "10.42.0.5", "10.43.0.30", 5432),
		"an excluded pod calls a reported pod":     raw(false, "10.42.0.5", "10.42.0.30", 8080),
	} {
		if f, ok := r.Resolve(in, "ebpf", true); ok {
			t.Errorf("%s: should be dropped, got %v -> %v", name, f.Src, f.Dst)
		}
	}
	if _, ok := r.Resolve(raw(true, "10.42.0.5", "10.42.0.7", 5432), "ebpf", true); !ok {
		t.Error("traffic between reported namespaces must still be attributed")
	}
}

// A captured report sent again must not count its bytes twice.
func TestReplayedFlowReportIsRejectedAndNotCountedTwice(t *testing.T) {
	secret := []byte("flow-secret-0123456789abcdef0123")
	p := NewPipeline(secret, testIndex, nil)
	now := time.Now()
	p.now = func() time.Time { return now }
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	f := raw(true, "10.42.0.5", "10.43.0.21", 5432)
	f.BytesOut, f.BytesIn, f.Connections = 1000, 2000, 1
	rep := &continuumv1.FlowReport{Method: "ebpf", Node: "n1", BytesKnown: true, Flows: []*continuumv1.RawFlow{f}}
	body, _ := protojson.Marshal(rep)
	ts := strconv.FormatInt(now.Unix(), 10)
	send := func() int {
		req, _ := http.NewRequest("POST", srv.URL+PathReport, bytes.NewReader(body))
		req.Header.Set(probe.HeaderNode, "n1")
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.Sign(secret, ts, "n1", body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := send(); c != 204 {
		t.Fatalf("first: %d", c)
	}
	for i := 0; i < 3; i++ {
		if c := send(); c != http.StatusConflict {
			t.Fatalf("replay %d: %d, want 409", i, c)
		}
	}
	p.Aggregator.now = func() time.Time { return now.Add(30 * time.Second) }
	b := p.Aggregator.Flush()
	if b == nil || len(b.Flows) != 1 || b.Flows[0].BytesOut != 1000 || b.Flows[0].BytesIn != 2000 || b.Flows[0].Connections != 1 {
		t.Fatalf("the replay was counted: %+v", b)
	}
}

func TestFlowReceiverBoundsTheBodyAndHonoursTheWindow(t *testing.T) {
	secret := []byte("flow-secret-0123456789abcdef0123")
	p := NewPipeline(secret, testIndex, nil)
	p.Window = 30 * time.Second
	now := time.Now()
	p.now = func() time.Time { return now }
	h := p.Handler()
	do := func(body []byte, at time.Time, length int64) int {
		ts := strconv.FormatInt(at.Unix(), 10)
		req := httptest.NewRequest("POST", PathReport, bytes.NewReader(body))
		req.ContentLength = length
		req.Header.Set(probe.HeaderNode, "n1")
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.Sign(secret, ts, "n1", body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if MaxBody != 512<<10 {
		t.Fatalf("MaxBody = %d", MaxBody)
	}
	big := bytes.Repeat([]byte("x"), MaxBody+1)
	if c := do(big, now, int64(len(big))); c != 413 {
		t.Errorf("declared oversize: %d", c)
	}
	if c := do(big, now, -1); c != 413 {
		t.Errorf("undeclared oversize: %d", c)
	}
	ok := []byte(`{"method":"conntrack","node":"n1"}`)
	if c := do(ok, now.Add(-20*time.Second), int64(len(ok))); c != 204 {
		t.Errorf("inside the window: %d", c)
	}
	if c := do(ok, now.Add(-2*time.Minute), int64(len(ok))); c != 401 {
		t.Errorf("outside the configured window: %d", c)
	}
}

// The collector keeps at most MaxRawFlows flows per report; that many, with every field at its longest,
// must fit in the receiver's limit, or a busy node's report would be refused every window.
func TestAFullReportFitsTheReceiverLimit(t *testing.T) {
	long := "2001:0db8:85a3:0000:0000:8a2e:0370:7334"
	max := ^uint64(0)
	rep := &continuumv1.FlowReport{Method: "conntrack", Node: strings.Repeat("n", 253), BytesKnown: true, Lost: max}
	for i := 0; i < MaxRawFlows; i++ {
		rep.Flows = append(rep.Flows, &continuumv1.RawFlow{Client: true, LocalIp: long, PeerIp: long, Port: 65535, Protocol: "tcp", Connections: max, BytesOut: max, BytesIn: max})
	}
	body, err := protojson.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	// protojson output is deliberately unstable (random extra spaces); allow for that.
	if len(body) > MaxBody-MaxBody/20 {
		t.Fatalf("%d flows encode to %d bytes; the limit is %d", MaxRawFlows, len(body), MaxBody)
	}
}
