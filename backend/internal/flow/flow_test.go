package flow

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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
		Pods:      map[string]string{"10.42.0.5": "shop/Deployment/cart", "10.42.0.7": "shop/Deployment/db", "10.42.0.9": "kube-system/Deployment/coredns"},
		Services:  map[string][]string{"10.43.0.20": {"shop/Deployment/cart"}, "10.43.0.21": {"shop/Deployment/db"}, "10.43.0.10": {"kube-system/Deployment/coredns"}},
		Nodes:     map[string]string{"192.168.1.10": "n1"},
		Opaque:    map[string]bool{"10.43.0.1": true},
		NodePorts: map[int32][]string{30080: {"shop/Deployment/cart"}},
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
	now = now.Add(10 * time.Second)
	f, ok := r.Resolve(raw(true, "10.42.0.5", "10.43.0.21", 5432), "ebpf", true)
	if !ok || ref(f.Src) != "shop/Deployment/cart" {
		t.Fatalf("a pod that just finished is still attributed: %v %v", f, ok)
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
	a.Add(mk("a", "b", 2))
	a.Add(mk("a", "b", 3))
	a.Add(mk("a", "c", 9))
	a.AddLost(4)
	t0 = t0.Add(60 * time.Second)
	b := a.Flush()
	if b.WindowSeconds != 60 || b.Lost != 4 || len(b.Flows) != 2 || b.Seq != 1 {
		t.Fatalf("batch = %v", b)
	}
	if b.Flows[0].Dst.Ref != "c" || b.Flows[1].Connections != 5 || b.Flows[1].BytesOut != 20 {
		t.Fatalf("busiest first, sums added: %v", b.Flows)
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
	a.Seen("n1", "ebpf", true)
	a.Seen("n1", "conntrack", false) // the same node's UDP supplement
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
