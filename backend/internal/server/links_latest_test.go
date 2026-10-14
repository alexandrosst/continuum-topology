package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
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
	got := latestPairs(links, rd, time.Unix(1791200200, 0))
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

// linkFleetStore answers the link queries for three clusters: a and b (a measured both directions and b reported too), a and c
// (only c reported), and d and e, whom nobody reported on. It counts the reads of the loss series.
func linkFleetStore(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	row := func(cl, peer, dir, v string) map[string]any {
		return map[string]any{"metric": map[string]string{"cluster": cl, "peer": peer, "direction": dir}, "value": []any{1.0, v}}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		res := []any{}
		switch {
		case strings.Contains(q, "probe_loss_ratio"):
			calls.Add(1)
			res = append(res, row("cl-a", "cl-b", "forward", "0.02"), row("cl-a", "cl-b", "reverse", "0"), row("cl-b", "cl-a", "forward", "0.5"),
				row("cl-c", "cl-a", "forward", "0.1"), row("cl-c", "cl-a", "reverse", "0.2"))
		case strings.Contains(q, "path_mtu"):
			res = append(res, row("cl-a", "cl-b", "", "1500"), row("cl-b", "cl-a", "", "1420"))
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	}))
	t.Cleanup(s.Close)
	return s
}

func linkRig(t *testing.T, calls *atomic.Int32) *dataRig {
	d := newDataRig(t)
	st := linkFleetStore(t, calls)
	d.a.Fusion.Data = &fusionapi.Client{Prometheus: st.URL}
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}, {A: "cl-d", B: "cl-e"}}
	if r := d.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: links}, withCookie(d.admin)); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	return d
}

func pairNames(t *testing.T, r resp) []string {
	t.Helper()
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	var out []string
	for _, p := range r.json(t)["pairs"].([]any) {
		m := p.(map[string]any)
		out = append(out, m["a"].(string)+"-"+m["b"].(string))
	}
	return out
}

func TestFusionLinksListsEachPairOnceWithTheSitesFigures(t *testing.T) {
	var calls atomic.Int32
	d := linkRig(t, &calls)

	if r := d.get("/api/v1/fusion/links"); r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
	doc := d.get("/api/v1/fusion/links", withCookie(d.admin)).json(t)
	pairs := doc["pairs"].([]any)
	if len(pairs) != 3 || doc["at"] == nil {
		t.Fatalf("%v", doc)
	}
	ab, ac, de := pairs[0].(map[string]any), pairs[1].(map[string]any), pairs[2].(map[string]any)
	// a believed first; the MTU is the smaller of the two clusters'; both clusters supplied something
	if ab["a"] != "cl-a" || ab["b"] != "cl-b" || ab["lossAToB"] != 0.02 || ab["lossBToA"] != 0.0 || ab["mtuBytes"] != 1420.0 || fmt.Sprint(ab["measuredBy"]) != "[cl-a cl-b]" {
		t.Errorf("a-b = %v", ab)
	}
	// only c reported: a to c is c's reverse
	if ac["lossAToB"] != 0.2 || ac["lossBToA"] != 0.1 || fmt.Sprint(ac["measuredBy"]) != "[cl-c]" {
		t.Errorf("a-c = %v", ac)
	}
	// a pair nobody reported on is still listed, with an empty (not null) measuredBy and no figures
	if de["lossAToB"] != nil || de["measuredBy"] == nil || len(de["measuredBy"].([]any)) != 0 {
		t.Errorf("d-e = %v", de)
	}
	// the same figures as the Sites screen, from the same read
	sites := d.do("GET", "/api/v1/links/latest", nil, withCookie(d.admin)).json(t)["pairs"].([]any)
	for i, p := range sites {
		m := p.(map[string]any)
		f := pairs[i].(map[string]any)
		delete(f, "measuredBy")
		if fmt.Sprint(m) != fmt.Sprint(f) {
			t.Errorf("pair %d: sites %v, fusion %v", i, m, f)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("the store was read %d times for three requests", calls.Load())
	}
}

func TestFusionLinksFiltersByClusterAndPeer(t *testing.T) {
	var calls atomic.Int32
	d := linkRig(t, &calls)
	get := func(q string) resp { return d.get("/api/v1/fusion/links"+q, withCookie(d.admin)) }
	for q, want := range map[string]string{
		"?cluster=cl-a":           "cl-a-cl-b cl-a-cl-c",
		"?cluster=cl-b":           "cl-a-cl-b",
		"?cluster=cl-c&peer=cl-a": "cl-a-cl-c",
		"?cluster=cl-a&peer=cl-c": "cl-a-cl-c",
		"?cluster=cl-b&peer=cl-c": "",
		"?cluster=nobody":         "",
		"?cluster=cl-d&peer=cl-e": "cl-d-cl-e",
	} {
		if got := strings.Join(pairNames(t, get(q)), " "); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
	for _, q := range []string{"?peer=cl-a", "?cluster=", "?cluster=a&peer=", "?cluster=" + strings.Repeat("x", 300), "?limit=3", "?service=x"} {
		if r := get(q); r.Code != 400 {
			t.Errorf("%s: %d %s", q, r.Code, r.Body.String())
		}
	}
	// nothing matched, nothing asked of the store
	calls.Store(0)
	*d.now = d.now.Add(linksLatestEvery + time.Second)
	get("?cluster=nobody")
	if calls.Load() != 0 {
		t.Errorf("a request that selects no pair read the store")
	}
}

func TestFusionLinksFollowsTheTokensScope(t *testing.T) {
	var calls atomic.Int32
	d := linkRig(t, &calls)
	get := func(secret string) resp { return d.get("/api/v1/fusion/links", bearer(secret)) }

	all, _ := d.mint(t, map[string]any{"name": "engine"})
	if got := pairNames(t, get(all)); len(got) != 3 {
		t.Errorf("an unlimited token sees %v", got)
	}
	// both clusters of a pair must be readable: a limited token sees b-less pairs of nobody
	ab, _ := d.mint(t, map[string]any{"name": "ab", "clusters": []string{"cl-a", "cl-b"}})
	if got := strings.Join(pairNames(t, get(ab)), " "); got != "cl-a-cl-b" {
		t.Errorf("a token for a and b sees %q", got)
	}
	acd, _ := d.mint(t, map[string]any{"name": "acd", "clusters": []string{"cl-a", "cl-c", "cl-d"}})
	if got := strings.Join(pairNames(t, get(acd)), " "); got != "cl-a-cl-c" {
		t.Errorf("a token for a, c and d sees %q", got)
	}
	// a pair outside the scope is not told apart from one that does not exist
	r := d.get("/api/v1/fusion/links?cluster=cl-d&peer=cl-e", bearer(acd))
	if got := pairNames(t, r); len(got) != 0 {
		t.Errorf("d-e: %v", got)
	}
	// the figures are read with the server's own scope, but a limited token still cannot run PromQL itself
	if r := d.get("/api/v1/fusion/metrics/query?query=up", bearer(ab)); r.Code != 403 {
		t.Errorf("raw query by a limited token: %d", r.Code)
	}
	// a token that may not read metrics, or one limited by namespace (figures belong to no namespace), is refused with a reason
	for name, body := range map[string]map[string]any{
		"no metrics":         {"name": "logs", "signals": []string{"logs", "traces"}},
		"namespaces":         {"name": "ns", "namespaces": []string{"shop"}},
		"namespaces+cluster": {"name": "nsc", "namespaces": []string{"shop"}, "clusters": []string{"cl-a", "cl-b"}},
	} {
		secret, _ := d.mint(t, body)
		r := get(secret)
		if r.Code != 403 || strings.Contains(r.Body.String(), "cl-a") {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
		if name != "no metrics" && !strings.Contains(r.Body.String(), "namespace") {
			t.Errorf("%s: the refusal does not say why: %s", name, r.Body.String())
		}
	}
}

func TestFusionLinksWithoutPairsOrWithAFailingStore(t *testing.T) {
	a := newDataRig(t)
	if doc := a.get("/api/v1/fusion/links", withCookie(a.admin)).json(t); len(doc["pairs"].([]any)) != 0 {
		t.Errorf("no pairs listed: %v", doc)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("panic at /srv/secret.go host=10.2.3.4"))
	}))
	defer bad.Close()
	a.a.Fusion.Data = &fusionapi.Client{Prometheus: bad.URL}
	a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(a.admin))
	r := a.get("/api/v1/fusion/links", withCookie(a.admin))
	if r.Code != 503 || strings.Contains(r.Body.String(), "secret.go") || strings.Contains(r.Body.String(), "10.2.3.4") {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

func TestFusionLinksIsDocumented(t *testing.T) {
	spec := fusionOpenAPI()
	op := spec["paths"].(obj)[fusionAPIPath+"/links"].(obj)["get"].(obj)
	if op["description"] == nil || op["tags"].([]string)[0] != "Links" {
		t.Fatalf("%v", op)
	}
	var names []string
	for _, p := range op["parameters"].([]obj) {
		names = append(names, p["name"].(string))
	}
	if fmt.Sprint(names) != "[cluster peer]" {
		t.Errorf("parameters %v", names)
	}
	sc := spec["components"].(obj)["schemas"].(obj)
	for _, n := range []string{"LinkList", "LinkPair"} {
		if sc[n] == nil {
			t.Errorf("no schema %s", n)
		}
	}
}

func lrr(cluster, peer, dir, reason string, v float64) fusionapi.LinkReading {
	return fusionapi.LinkReading{Cluster: cluster, Peer: peer, Direction: dir, Reason: reason, Value: v}
}

// The store answers in no defined order, and a rolled pod can leave two series for one key. The figure shown must not depend on
// which comes last: the worse loss, the larger delay variation and the smaller MTU win.
func TestTwoSeriesOfOneKeyResolveTheSameWhicheverComesLast(t *testing.T) {
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}}
	now := time.Unix(1791200200, 0)
	loss := []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 1), lr("cl-a", "cl-b", "forward", 0.1)}
	jit := []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.02), lr("cl-a", "cl-b", "forward", 0.001)}
	mtu := []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 1500), lr("cl-a", "cl-b", "", 1420)}
	for _, rev := range []bool{false, true} {
		l, j, m := append([]fusionapi.LinkReading(nil), loss...), append([]fusionapi.LinkReading(nil), jit...), append([]fusionapi.LinkReading(nil), mtu...)
		if rev {
			l[0], l[1], j[0], j[1], m[0], m[1] = l[1], l[0], j[1], j[0], m[1], m[0]
		}
		got := latestPairs(links, fusionapi.LinkReadings{Loss: l, Jitter: j, MTU: m}, now)[0]
		if got.LossAToB == nil || *got.LossAToB != 1 || got.JitterSeconds == nil || *got.JitterSeconds != 0.02 || got.MTUBytes == nil || *got.MTUBytes != 1420 {
			t.Errorf("reversed=%v: loss %v jitter %v mtu %v", rev, got.LossAToB, got.JitterSeconds, got.MTUBytes)
		}
	}
}

func probeStateOf(p LinkPairLatest) (string, string, string, string) {
	f := func(s *LinkProbeLatest) string {
		if s == nil {
			return "-"
		}
		return s.State + "/" + s.Reason
	}
	d := func(s *LinkProbeLatest) string {
		if s == nil {
			return ""
		}
		return s.Diagnosis
	}
	return f(p.ProbeAToB), f(p.ProbeBToA), d(p.ProbeAToB), d(p.ProbeBToA)
}

func TestProbeStateAndDiagnosisRules(t *testing.T) {
	now := time.Unix(1791200000, 0)
	fresh, old := float64(now.Unix()-60), float64(now.Unix()-3600)
	ab, ba := func(v float64) fusionapi.LinkReading { return lr("cl-a", "cl-b", "forward", v) }, func(v float64) fusionapi.LinkReading { return lr("cl-a", "cl-b", "reverse", v) }
	rttOK := []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 0.012)}
	connFail := func(why string) []fusionapi.LinkReading {
		return []fusionapi.LinkReading{lrr("cl-a", "cl-b", "", why, 4)}
	}
	for _, c := range []struct {
		name         string
		rd           fusionapi.LinkReadings
		noProbe      bool
		wantA, wantB string // state/reason of A to B and B to A
		diagA, diagB string
	}{
		{name: "nothing reported", wantA: "-", wantB: "-"},
		{name: "up in both directions, packets arrive", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(0.01), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "ok/"},
		{name: "every packet lost although the run completed", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(1), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "ok/", diagA: "udp-lost"},
		{name: "99 percent counts as lost, 98 does not", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(0.99), ba(0.98)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "ok/", diagA: "udp-lost"},
		{name: "up but no run completed for long", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(1)}, Loss: []fusionapi.LinkReading{ab(0), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(old)}},
			wantA: "ok/", wantB: "ok/", diagB: "stale"},
		{name: "the field case: forward lossy, reverse refused by the responder",
			rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(0)}, Loss: []fusionapi.LinkReading{ab(1)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(old)}, RTT: rttOK,
				ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "reverse", "other", 9)}},
			wantA: "ok/", wantB: "failing/other", diagA: "udp-lost", diagB: "denied"},
		{name: "authentication refused, connections fine", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, Probed: []fusionapi.LinkReading{ab(old), ba(old)}, RTT: rttOK,
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "auth", 2), lrr("cl-a", "cl-b", "reverse", "auth", 2)}},
			wantA: "failing/auth", wantB: "failing/auth", diagA: "denied", diagB: "denied"},
		{name: "nothing listens on the control port", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, ConnectFailure: connFail("refused"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "refused", 5), lrr("cl-a", "cl-b", "reverse", "refused", 5)}},
			wantA: "never/refused", wantB: "never/refused", diagA: "no-responder", diagB: "no-responder"},
		{name: "the control port does not answer", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, ConnectFailure: connFail("timeout"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "timeout", 5)}},
			wantA: "never/timeout", wantB: "never/", diagA: "filtered", diagB: "filtered"},
		{name: "connections failed lately but were also timed: the evidence is mixed", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, RTT: rttOK, ConnectFailure: connFail("refused"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "other", 5)}},
			wantA: "never/other", wantB: "never/"},
		{name: "one direction down with nothing else known", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(fresh)}},
			wantA: "ok/", wantB: "failing/", diagB: "one-way"},
		{name: "pinned to a kind of address the peer lacks says nothing more", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}, ConnectFailure: connFail("timeout"),
			ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "no_such_kind", 5)}},
			wantA: "never/no_such_kind", wantB: "never/", diagB: "filtered"},
		{name: "reported from the second cluster's side", rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{lr("cl-b", "cl-a", "forward", 0), lr("cl-b", "cl-a", "reverse", 1)}, Probed: []fusionapi.LinkReading{lr("cl-b", "cl-a", "reverse", fresh)}},
			wantA: "ok/", wantB: "never/", diagB: "one-way"},
		{name: "probing off for the pair", noProbe: true, rd: fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(0), ba(0)}}, wantA: "-", wantB: "-"},
	} {
		got := latestPairs([]LinkSetting{{A: "cl-a", B: "cl-b", NoProbe: c.noProbe}}, c.rd, now)[0]
		a, b, da, db := probeStateOf(got)
		if a != c.wantA || b != c.wantB || da != c.diagA || db != c.diagB {
			t.Errorf("%s: states %s %s, diagnoses %q %q; want %s %s, %q %q", c.name, a, b, da, db, c.wantA, c.wantB, c.diagA, c.diagB)
		}
	}
	// the time of the last completed run is kept per direction
	got := latestPairs([]LinkSetting{{A: "cl-a", B: "cl-b"}}, fusionapi.LinkReadings{Up: []fusionapi.LinkReading{ab(1), ba(0)}, Probed: []fusionapi.LinkReading{ab(fresh), ba(old)}}, now)[0]
	if got.ProbeAToB.At == nil || got.ProbeAToB.At.Unix() != int64(fresh) || got.ProbeBToA.At == nil || got.ProbeBToA.At.Unix() != int64(old) || got.ProbeBToA.State != "failing" {
		t.Errorf("per-direction times: %+v %+v", got.ProbeAToB, got.ProbeBToA)
	}
}

// A reason label is whatever the cluster's metrics said, and an address in it is not for a viewer: only the known words go out.
func TestDiagnosisNeverCarriesAnAddressOrWhatTheStoreSaid(t *testing.T) {
	now := time.Unix(1791200000, 0)
	rd := fusionapi.LinkReadings{
		Up:           []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0), lr("cl-a", "cl-b", "reverse", 0)},
		RTT:          []fusionapi.LinkReading{lr("cl-a", "cl-b", "", 0.01)},
		ProbeFailure: []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "dial tcp 10.2.3.4:8861: i/o timeout", 3), lrr("cl-a", "cl-b", "reverse", "auth", 1)},
	}
	got := latestPairs([]LinkSetting{{A: "cl-a", B: "cl-b"}}, rd, now)[0]
	j, _ := json.Marshal(got)
	if strings.Contains(string(j), "10.2.3.4") || strings.Contains(string(j), "dial") {
		t.Fatalf("the store's text reached the answer: %s", j)
	}
	if got.ProbeAToB.Reason != "other" || got.ProbeAToB.Diagnosis != "denied" || got.ProbeBToA.Reason != "auth" {
		t.Errorf("%+v %+v", got.ProbeAToB, got.ProbeBToA)
	}
	known := map[string]bool{"": true, "udp-lost": true, "denied": true, "no-responder": true, "filtered": true, "one-way": true, "stale": true}
	for _, s := range []*LinkProbeLatest{got.ProbeAToB, got.ProbeBToA} {
		if !known[s.Diagnosis] {
			t.Errorf("a diagnosis outside the bounded words: %q", s.Diagnosis)
		}
	}
	// on two reasons of equal count the same one wins in either order
	two := []fusionapi.LinkReading{lrr("cl-a", "cl-b", "forward", "timeout", 2), lrr("cl-a", "cl-b", "forward", "refused", 2)}
	one := linkReasons(two)[[3]string{"cl-a", "cl-b", "forward"}]
	if rev := linkReasons([]fusionapi.LinkReading{two[1], two[0]})[[3]string{"cl-a", "cl-b", "forward"}]; one != rev || one != "refused" {
		t.Errorf("tie: %q / %q", one, rev)
	}
}

// probeStore answers the link queries of a pair whose forward probe completes but loses everything, and whose reverse probe has
// failed for a while: the bare loss query would still return the reverse direction's last good run, so the store, like the real
// one with the gate in the query, leaves the reverse figures out.
func probeStore(t *testing.T) *httptest.Server {
	t.Helper()
	row := func(dir, v string, kv ...string) map[string]any {
		m := map[string]string{"cluster": "cl-a", "peer": "cl-b"}
		if dir != "" {
			m["direction"] = dir
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return map[string]any{"metric": m, "value": []any{1.0, v}}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		res := []any{}
		switch {
		case strings.Contains(q, "probe_failures_total"):
			res = append(res, row("reverse", "6", "reason", "other"))
		case strings.HasPrefix(q, "max by (cluster, peer, direction) (continuum_link_probe_up)"):
			res = append(res, row("forward", "1"), row("reverse", "0"))
		case strings.Contains(q, "probe_loss_ratio"):
			res = append(res, row("forward", "1"))
		case strings.Contains(q, "probe_last_success"):
			res = append(res, row("forward", fmt.Sprint(time.Now().Unix()-30)), row("reverse", fmt.Sprint(time.Now().Unix()-7200)))
		case strings.Contains(q, "tcp_connect_seconds_sum"):
			res = append(res, row("", "0.012"))
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestAFailingProbeIsShownWithItsReasonAndNotItsLastGoodFigures(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: probeStore(t).URL}}
	a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin))
	doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	p := doc["pairs"].([]any)[0].(map[string]any)
	if p["lossAToB"] != 1.0 || p["lossBToA"] != nil {
		t.Errorf("loss: %v / %v", p["lossAToB"], p["lossBToA"])
	}
	fwd, rev := p["probeAToB"].(map[string]any), p["probeBToA"].(map[string]any)
	if fwd["state"] != "ok" || fwd["diagnosis"] != "udp-lost" || rev["state"] != "failing" || rev["reason"] != "other" || rev["diagnosis"] != "denied" || rev["at"] == nil {
		t.Errorf("states: %v / %v", fwd, rev)
	}
}

func TestLatestFiguresOfAnotherOrganisationThanFusionsAreNotServed(t *testing.T) {
	var calls atomic.Int32
	a := newAdminRig(t)
	_, owner := a.user(t, "boss", RoleOwner)
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: linkStore(t, &calls, "0.01").URL}}
	r := a.do("POST", "/api/v1/orgs", map[string]any{"name": "Org Two"}, withCookie(owner))
	if r.Code != 201 {
		t.Fatalf("create org two: %d %s", r.Code, r.Body.String())
	}
	org2, _ := r.json(t)["id"].(string)
	// the other organisation lists a pair by the ids of the FUSION organisation's clusters
	if r := a.do("PUT", "/api/v1/orgs/"+org2+"/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(owner)); r.Code != 200 {
		t.Fatalf("settings: %d %s", r.Code, r.Body.String())
	}
	doc := a.do("GET", "/api/v1/orgs/"+org2+"/links/latest", nil, withCookie(owner)).json(t)
	if doc["available"] != false || doc["reason"] != "no-fusion" || len(doc["pairs"].([]any)) != 0 || calls.Load() != 0 {
		t.Fatalf("the other organisation read FUSION's figures: %v (%d reads)", doc, calls.Load())
	}
	// it still gets its notes and its checklist, which are about its own agents
	if len(doc["setup"].([]any)) != 1 {
		t.Errorf("setup: %v", doc["setup"])
	}
	// FUSION's own organisation is served
	a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(owner))
	if doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(owner)).json(t); doc["available"] != true || calls.Load() != 1 {
		t.Errorf("the FUSION organisation: %v (%d reads)", doc, calls.Load())
	}
}

type agentsDown struct{ store.Store }

func (agentsDown) ListAgents(context.Context, string) ([]store.Agent, error) {
	return nil, errors.New("database is locked")
}

func TestLinkReportFailsWhenTheAgentsCannotBeReadAndTheScreenSaysSo(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	var calls atomic.Int32
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: linkStore(t, &calls, "0.01").URL}}
	links := []LinkSetting{{A: "cl-a", B: "cl-b"}}
	a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: links}, withCookie(admin))
	h := a.hub()
	if _, _, err := h.LinkReport(context.Background(), links, true); err != nil {
		t.Fatal(err)
	}
	real := h.C.Store
	h.C.Store = agentsDown{real}
	t.Cleanup(func() { h.C.Store = real })
	if n, s, err := h.LinkReport(context.Background(), links, true); err == nil || n != nil || s != nil {
		t.Fatalf("a store that failed was answered with %v %v %v", n, s, err)
	}
	// no links: nothing to read, nothing to fail
	if _, _, err := h.LinkReport(context.Background(), nil, true); err != nil {
		t.Errorf("no pairs: %v", err)
	}
	doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	if doc["setupReason"] != "unavailable" || len(doc["setup"].([]any)) != 0 || strings.Contains(fmt.Sprint(doc), "locked") {
		t.Errorf("%v", doc)
	}
	// the figures are a different read: they are still there
	if doc["available"] != true || len(doc["pairs"].([]any)) != 1 {
		t.Errorf("figures: %v", doc)
	}
}

func readOf(r fusionapi.LinkReadings, err error, calls *atomic.Int32) func(context.Context) (fusionapi.LinkReadings, error) {
	return func(ctx context.Context) (fusionapi.LinkReadings, error) {
		calls.Add(1)
		return r, err
	}
}

func TestACancelledFirstCallerStillCachesTheSharedRead(t *testing.T) {
	var c linkReadingsCache
	t0 := time.Unix(1791200000, 0)
	now := func() time.Time { return t0 }
	var reads atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	good := fusionapi.LinkReadings{Loss: []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.5)}}
	read := func(rc context.Context) (fusionapi.LinkReadings, error) {
		reads.Add(1)
		cancel() // the first caller's request ends while FUSION is being read
		if rc.Err() != nil {
			return fusionapi.LinkReadings{}, rc.Err()
		}
		if _, ok := rc.Deadline(); !ok {
			t.Error("the shared read has no deadline of its own")
		}
		return good, nil
	}
	if _, _, err := c.get(ctx, now, read); err != nil {
		t.Fatalf("the read took the caller's cancellation: %v", err)
	}
	r, s, err := c.get(context.Background(), now, readOf(fusionapi.LinkReadings{}, errors.New("must not be asked"), &reads))
	if err != nil || len(r.Loss) != 1 || reads.Load() != 1 || s.Stale {
		t.Fatalf("the second caller did not get the cached read: %v %v reads=%d", r, err, reads.Load())
	}
}

func TestACallerWaitingForTheSharedReadStopsWaitingWithItsRequest(t *testing.T) {
	var c linkReadingsCache
	now := func() time.Time { return time.Unix(1791200000, 0) }
	release, started := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.get(context.Background(), now, func(context.Context) (fusionapi.LinkReadings, error) {
			close(started)
			<-release
			return fusionapi.LinkReadings{}, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, _, err := c.get(ctx, now, nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 2*time.Second {
		t.Fatalf("a waiter was held past its own deadline: %v after %v", err, time.Since(t0))
	}
	close(release)
	<-done
}

func TestAFailedReadServesTheLastGoodOneMarkedStaleAndThenGivesUp(t *testing.T) {
	var c linkReadingsCache
	t0 := time.Unix(1791200000, 0)
	now := t0
	clock := func() time.Time { return now }
	var reads atomic.Int32
	good := fusionapi.LinkReadings{Loss: []fusionapi.LinkReading{lr("cl-a", "cl-b", "forward", 0.5)}}
	down := errors.New("store down")
	if _, s, err := c.get(context.Background(), clock, readOf(good, nil, &reads)); err != nil || s.Stale || !s.At.Equal(t0) {
		t.Fatalf("first read: %v %+v", err, s)
	}
	// the next read fails: the last good one is served, with the time it was read and the stale flag
	now = t0.Add(linksLatestEvery + time.Second)
	r, s, err := c.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads))
	if err != nil || len(r.Loss) != 1 || !s.Stale || !s.At.Equal(t0) || reads.Load() != 2 {
		t.Fatalf("after a failed read: %v %+v reads=%d", err, s, reads.Load())
	}
	// the failure is kept for the window, like a success: nobody asks the store again
	now = now.Add(5 * time.Second)
	if _, s, err := c.get(context.Background(), clock, readOf(good, nil, &reads)); err != nil || !s.Stale || reads.Load() != 2 {
		t.Fatalf("inside the window: %v %+v reads=%d", err, s, reads.Load())
	}
	// FUSION is back: fresh again
	now = now.Add(linksLatestEvery)
	if _, s, err := c.get(context.Background(), clock, readOf(good, nil, &reads)); err != nil || s.Stale || !s.At.Equal(now) {
		t.Fatalf("recovered: %v %+v", err, s)
	}
	// a store that stays down is given up on after the bound: the figures would pass for current ones
	good2 := now
	now = good2.Add(linksLatestEvery + time.Second)
	if _, s, err := c.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads)); err != nil || !s.Stale {
		t.Fatalf("still within the bound: %v %+v", err, s)
	}
	now = good2.Add(linksLatestStaleFor + time.Second)
	if r, s, err := c.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads)); !errors.Is(err, down) || s.Stale || len(r.Loss) != 0 {
		t.Fatalf("past the bound: %v %+v %v", err, s, r)
	}
	// and with no good read ever, the failure is what is served
	var fresh linkReadingsCache
	if _, _, err := fresh.get(context.Background(), clock, readOf(fusionapi.LinkReadings{}, down, &reads)); !errors.Is(err, down) {
		t.Fatalf("no good read: %v", err)
	}
}

func TestAStoreThatStopsAnsweringKeepsTheScreenOnItsLastFiguresMarkedStale(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", RoleAdmin)
	var down atomic.Bool
	var mu sync.Mutex
	inner := linkStore(t, new(atomic.Int32), "0.01")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if down.Load() {
			w.WriteHeader(500)
			return
		}
		resp, err := http.Get(inner.URL + r.URL.RequestURI())
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		var b json.RawMessage
		json.NewDecoder(resp.Body).Decode(&b)
		w.Write(b)
	}))
	defer proxy.Close()
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: proxy.URL}}
	a.do("PUT", "/api/v1/settings", Settings{SnapshotMinutes: 7, Links: []LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin))
	first := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	if first["available"] != true || first["stale"] != nil {
		t.Fatalf("%v", first)
	}
	down.Store(true)
	*a.now = a.now.Add(linksLatestEvery + time.Second)
	doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	pairs := doc["pairs"].([]any)
	if doc["available"] != true || doc["stale"] != true || doc["reason"] != nil || len(pairs) != 1 || pairs[0].(map[string]any)["lossAToB"] != 0.01 || doc["at"] != first["at"] {
		t.Fatalf("a failed read replaced the last good figures: %v", doc)
	}
	// long after, they are not served as if they were current
	*a.now = a.now.Add(linksLatestStaleFor + time.Minute)
	if doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t); doc["available"] != false || doc["reason"] != "unavailable" {
		t.Errorf("%v", doc)
	}
}
