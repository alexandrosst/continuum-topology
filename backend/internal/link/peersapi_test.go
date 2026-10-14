package link

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

var testSecret = []byte("a-secret-of-sixteen-plus-chars")

func peersServer(t *testing.T, enable bool) (*httptest.Server, *Meter) {
	t.Helper()
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1", "10.0.0.2")})
	m := NewMeter(p)
	if enable {
		m.ServePeers(testSecret)
	}
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	return srv, m
}

func TestTheProberCanFetchThePeersAndNothingElseCan(t *testing.T) {
	srv, _ := peersServer(t, true)
	d, err := FetchPeers(context.Background(), srv.Client(), srv.URL, testSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.ClusterID != "cl-me" || len(d.Peers) != 1 || d.Peers[0].ID != "cl-a" || len(d.Peers[0].Addresses) != 2 {
		t.Fatalf("%+v", d)
	}
	// The returned list goes through the same checks as a Config from the server.
	p := NewPeers()
	if dropped := p.SetFromDoc(d); len(dropped) != 0 || p.Lookup("10.0.0.2") != "cl-a" {
		t.Fatalf("dropped %v, lookup %q", dropped, p.Lookup("10.0.0.2"))
	}

	post := func(mutate func(*http.Request)) int {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+PathPeers, http.NoBody)
		req.Header.Set(probe.HeaderNode, ProberNode)
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, testSecret, ts, ProberNode, nil))
		if mutate != nil {
			mutate(req)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(func(r *http.Request) { r.Header.Del(probe.HeaderSig) }); got != http.StatusUnauthorized {
		t.Errorf("unsigned: %d", got)
	}
	// a flow collector signs as a node; that must not open this door
	if got := post(func(r *http.Request) {
		ts := r.Header.Get(probe.HeaderTime)
		r.Header.Set(probe.HeaderNode, "worker-1")
		r.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, testSecret, ts, "worker-1", nil))
	}); got != http.StatusBadRequest {
		t.Errorf("signed as a node: %d", got)
	}
	// The flow secret is not this API's credential: a request signed with it, whether the way a flow report is signed or the
	// way this API is, is refused; and the right secret signed the way a flow report is signed is refused too.
	flowSecret := []byte("the-flow-secret-of-the-collectors")
	if got := post(func(r *http.Request) {
		ts := r.Header.Get(probe.HeaderTime)
		r.Header.Set(probe.HeaderSig, probe.Sign(flowSecret, ts, ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("signed with the flow secret, as a flow report: %d", got)
	}
	if got := post(func(r *http.Request) {
		ts := r.Header.Get(probe.HeaderTime)
		r.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, flowSecret, ts, ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("signed with the flow secret, as a peers request: %d", got)
	}
	if got := post(func(r *http.Request) {
		ts := r.Header.Get(probe.HeaderTime)
		r.Header.Set(probe.HeaderSig, probe.Sign(testSecret, ts, ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("a flow-report MAC replayed as a peers request: %d", got)
	}
	if got := post(func(r *http.Request) {
		r.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, []byte("another-secret-entirely-1234"), r.Header.Get(probe.HeaderTime), ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", got)
	}
	if got := post(func(r *http.Request) {
		old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
		r.Header.Set(probe.HeaderTime, old)
		r.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, testSecret, old, ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("stale: %d", got)
	}
	// the same signed request twice is a replay (a timestamp of its own, so no earlier request in this test has used it)
	ts := strconv.FormatInt(time.Now().Add(-30*time.Second).Unix(), 10)
	once := func() int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+PathPeers, http.NoBody)
		req.Header.Set(probe.HeaderNode, ProberNode)
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.SignContext(peersContext, testSecret, ts, ProberNode, []byte("x")[:0]))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	first, second := once(), once()
	if first != http.StatusOK || second != http.StatusConflict {
		t.Errorf("a replayed request: %d then %d, want 200 then 409", first, second)
	}
}

// A peers request is not a flow report either: the MAC of one does not verify as the other, so the prober's signature
// cannot be used on the flow receiver even if both were configured with the same secret.
func TestAPeersRequestIsNotValidAsAFlowReport(t *testing.T) {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := probe.SignContext(peersContext, testSecret, ts, ProberNode, nil)
	if err := probe.Verify(testSecret, ts, ProberNode, sig, nil, now); err == nil {
		t.Fatal("a peers request verifies as a flow report")
	}
	if sig == probe.Sign(testSecret, ts, ProberNode, nil) {
		t.Fatal("the two MACs are equal")
	}
}

func TestThePeersPathDoesNotExistUnlessEnabled(t *testing.T) {
	srv, _ := peersServer(t, false)
	if _, err := FetchPeers(context.Background(), srv.Client(), srv.URL, testSecret, time.Now()); err == nil {
		t.Fatal("the agent answered without having been told to")
	}
}

func TestAnAgentAnswerThatIsNotAPeerListIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not json":    "<html>",
		"bad address": `{"cluster_id":"c","peers":[{"id":"a","addresses":["not-an-ip"]}]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
		if _, err := FetchPeers(context.Background(), srv.Client(), srv.URL, testSecret, time.Now()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		srv.Close()
	}
}

func TestThePeersDocumentCarriesTheTrafficTotalsForTheLoadCheck(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m := NewMeter(p)
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", BytesOut: 1000, BytesIn: 400, Connections: 1}})
	d := m.peersDoc()
	if len(d.Peers) != 1 || d.Peers[0].BytesSent != 1000 || d.Peers[0].BytesReceived != 400 {
		t.Fatalf("%+v", d)
	}
	// A peer nothing was seen with has totals of zero, which the JSON leaves out.
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-b", "B", "10.0.0.9")})
	if d := m.peersDoc(); d.Peers[0].BytesSent != 0 {
		t.Errorf("%+v", d)
	}
}

func TestWhatToMeasureTravelsFromTheConfigThroughThePeersDocumentAndIsBounded(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{
		{ClusterId: "cl-a", Name: "A", Addresses: []string{"10.0.0.1"}, NoProbe: true, Throughput: true, ThroughputIntervalSeconds: 3600, ThroughputBudgetBytes: 5 << 30},
		{ClusterId: "cl-b", Name: "B", Addresses: []string{"10.0.1.1"}, Throughput: true, ThroughputIntervalSeconds: 5, ThroughputBudgetBytes: 1 << 50},
		{ClusterId: "cl-c", Name: "C", Addresses: []string{"10.0.2.1"}, ThroughputIntervalSeconds: 3600, ThroughputBudgetBytes: 99}, // not asked to run: the extras mean nothing
	})
	got := map[string]Peer{}
	for _, x := range p.List() {
		got[x.ID] = x
	}
	a, b, c := got["cl-a"], got["cl-b"], got["cl-c"]
	if !a.NoProbe || !a.Throughput || a.ThroughputEvery != time.Hour || a.ThroughputBudget != 5<<30 {
		t.Errorf("a = %+v", a)
	}
	if b.ThroughputEvery != MinThroughputEvery || b.ThroughputBudget != MaxThroughputBudget {
		t.Errorf("b was not clamped: %+v", b)
	}
	if c.Throughput || c.ThroughputEvery != 0 || c.ThroughputBudget != 0 || c.NoProbe {
		t.Errorf("c = %+v", c)
	}
	// agent -> prober
	q := NewPeers()
	q.SetFromDoc(p.Doc())
	for _, x := range q.List() {
		if x.NoProbe != got[x.ID].NoProbe || x.Throughput != got[x.ID].Throughput || x.ThroughputEvery != got[x.ID].ThroughputEvery || x.ThroughputBudget != got[x.ID].ThroughputBudget {
			t.Errorf("%s changed on the way: %+v vs %+v", x.ID, x, got[x.ID])
		}
	}
}

func TestSkippingTheProbeTravelsFromTheConfigThroughThePeersDocument(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{
		{ClusterId: "cl-a", Name: "A", Addresses: []string{"10.0.0.1"}, SkipProbe: true},
		{ClusterId: "cl-b", Name: "B", Addresses: []string{"10.0.1.1"}},
		{ClusterId: "cl-c", Name: "C", Addresses: []string{"10.0.2.1"}, SkipProbe: true, NoProbe: true},
	})
	byID := func(ps *Peers) map[string]Peer {
		out := map[string]Peer{}
		for _, x := range ps.List() {
			out[x.ID] = x
		}
		return out
	}
	got := byID(p)
	if !got["cl-a"].SkipProbe || got["cl-b"].SkipProbe {
		t.Fatalf("%+v", got)
	}
	if got["cl-a"].Probed() || !got["cl-a"].Traced() || !got["cl-b"].Probed() || !got["cl-b"].Traced() || got["cl-c"].Probed() || got["cl-c"].Traced() {
		t.Errorf("what is measured towards each peer: %+v", got)
	}
	q := NewPeers()
	q.SetFromDoc(p.Doc())
	if again := byID(q); !again["cl-a"].SkipProbe || again["cl-b"].SkipProbe {
		t.Errorf("the choice changed on the way to the prober: %+v", again)
	}
	if !p.Has("cl-a") || !p.Has("cl-c") || p.Has("cl-z") || p.Has("") || NewPeers().Has("cl-a") {
		t.Error("Has")
	}
}
