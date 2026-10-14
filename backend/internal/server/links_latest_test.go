package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/fusionapi"
)

func lr(cluster, peer, dir string, v float64) fusionapi.LinkReading {
	return fusionapi.LinkReading{Cluster: cluster, Peer: peer, Direction: dir, Value: v}
}

func TestPairsGetTheirFiguresFromWhicheverClusterMeasuredThem(t *testing.T) {
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}, {A: "cl-d", B: "cl-e"}}
	rd := fusionapi.LinkReadings{
		// a measured towards b; b measured towards a and says something else: a is believed
		Loss:       []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.02), lr("cl-a", "cl-b", "reverse", 0), lr("cl-b", "cl-a", "forward", 0.5), lr("cl-b", "cl-a", "reverse", 0.5)},
		Jitter:     []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.0004), lr("cl-b", "cl-a", "forward", 0.006)},
		RTT:        []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 0.012)},
		MTU:        []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 1500), lr("cl-b", "cl-a", "", 1420)},
		Throughput: []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 9.2e8), lr("cl-a", "cl-b", "reverse", 8.7e8)},
		Probed:     []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 1791200000), lr("cl-b", "cl-a", "reverse", 1791200100)},
		Tested:     []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 1791100000)},
		// c only reports from its side, where "forward" is c to a
	}
	rd.Loss = append(rd.Loss, lr("cl-c", "cl-a", "forward", 0.1), lr("cl-c", "cl-a", "reverse", 0.2))
	got := latestPairs(links, rd)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	ab := got[0]
	f := func(p *float64) float64 {
		if p == nil {
			return -1
		}
		return *p
	}
	if f(ab.LossAToB) != 0.02 || f(ab.LossBToA) != 0 || f(ab.RTTSeconds) != 0.012 || f(ab.JitterSeconds) != 0.006 || f(ab.ThroughputAToB) != 9.2e8 || f(ab.ThroughputBToA) != 8.7e8 {
		t.Errorf("a-b = %+v", ab)
	}
	if ab.MTUBytes == nil || *ab.MTUBytes != 1420 {
		t.Errorf("the smaller MTU of the two clusters is the pair's: %v", ab.MTUBytes)
	}
	if ab.ProbedAt == nil || ab.ProbedAt.Unix() != 1791200100 || ab.TestedAt == nil || ab.TestedAt.Unix() != 1791100000 {
		t.Errorf("times: %v %v", ab.ProbedAt, ab.TestedAt)
	}
	ac := got[1]
	if f(ac.LossAToB) != 0.2 || f(ac.LossBToA) != 0.1 { // a to c is c's reverse; c to a is c's forward
		t.Errorf("a-c measured from c's side: %+v / %+v", f(ac.LossAToB), f(ac.LossBToA))
	}
	de := got[2]
	if de.LossAToB != nil || de.RTTSeconds != nil || de.MTUBytes != nil || de.ProbedAt != nil || de.TestedAt != nil || de.A != "cl-d" {
		t.Errorf("a pair nobody reported on has no figures: %+v", de)
	}
}

// linkStore is a metrics store that answers the link queries, and counts them.
func linkStore(t *testing.T, calls *atomic.Int32, loss string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		res := []any{}
		if strings.Contains(q, "probe_loss_ratio") {
			calls.Add(1)
			res = append(res, map[string]any{"metric": map[string]string{"cluster": "cl-a", "peer": "cl-b", "direction": "forward"}, "value": []any{1.0, loss}})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestLatestFiguresOverHTTP(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	_, viewer := a.user(t, "vera", RoleViewer)
	var calls atomic.Int32
	st := linkStore(t, &calls, "0.01")

	if r := a.do("GET", "/api/v1/links/latest", nil); r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
	// FUSION not installed
	doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(viewer)).json(t)
	if doc["available"] != false || doc["reason"] != "no-fusion" {
		t.Fatalf("without FUSION: %v", doc)
	}
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: st.URL}}
	// no pairs: nothing to read
	if doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(viewer)).json(t); doc["available"] != true || len(doc["pairs"].([]any)) != 0 || calls.Load() != 0 {
		t.Fatalf("no pairs: %v (%d reads)", doc, calls.Load())
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	// a viewer reads the figures, and they are shared: a second read inside the window does not ask the store again
	for i := 0; i < 2; i++ {
		doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(viewer)).json(t)
		pairs, _ := doc["pairs"].([]any)
		if doc["available"] != true || len(pairs) != 1 {
			t.Fatalf("%v", doc)
		}
		p := pairs[0].(map[string]any)
		if p["a"] != "cl-a" || p["lossAToB"] != 0.01 || p["rttSeconds"] != nil {
			t.Fatalf("pair = %v", p)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("the store was read %d times for two requests", calls.Load())
	}
	// after the window it is read again
	*a.now = a.now.Add(linksLatestEvery + time.Second)
	a.do("GET", "/api/v1/links/latest", nil, withCookie(viewer))
	if calls.Load() != 2 {
		t.Errorf("reads = %d", calls.Load())
	}
}

func TestAStoreThatFailsIsUnavailableAndNothingOfItsErrorIsShown(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("panic at /srv/secret.go host=10.2.3.4"))
	}))
	defer bad.Close()
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: bad.URL}}
	a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin))
	r := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin))
	if r.Code != 200 || strings.Contains(r.Body.String(), "secret.go") || strings.Contains(r.Body.String(), "10.2.3.4") {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	if doc["available"] != false || doc["reason"] != "unavailable" {
		t.Fatalf("%v", doc)
	}
}
