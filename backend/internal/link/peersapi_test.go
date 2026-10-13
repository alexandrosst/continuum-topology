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
		req.Header.Set(probe.HeaderSig, probe.Sign(testSecret, ts, ProberNode, nil))
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
	// a flow collector holds the same secret but signs as a node; that signature must not open this door
	if got := post(func(r *http.Request) {
		ts := r.Header.Get(probe.HeaderTime)
		r.Header.Set(probe.HeaderNode, "worker-1")
		r.Header.Set(probe.HeaderSig, probe.Sign(testSecret, ts, "worker-1", nil))
	}); got != http.StatusBadRequest {
		t.Errorf("signed as a node: %d", got)
	}
	if got := post(func(r *http.Request) {
		r.Header.Set(probe.HeaderSig, probe.Sign([]byte("another-secret-entirely-1234"), r.Header.Get(probe.HeaderTime), ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", got)
	}
	if got := post(func(r *http.Request) {
		old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
		r.Header.Set(probe.HeaderTime, old)
		r.Header.Set(probe.HeaderSig, probe.Sign(testSecret, old, ProberNode, nil))
	}); got != http.StatusUnauthorized {
		t.Errorf("stale: %d", got)
	}
	// the same signed request twice in one second is a replay
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	once := func() int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+PathPeers, http.NoBody)
		req.Header.Set(probe.HeaderNode, ProberNode)
		req.Header.Set(probe.HeaderTime, ts)
		req.Header.Set(probe.HeaderSig, probe.Sign(testSecret, ts, ProberNode, []byte("x")[:0]))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	first, second := once(), once()
	if first == http.StatusOK && second != http.StatusConflict {
		t.Errorf("a replayed request was answered: %d then %d", first, second)
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
