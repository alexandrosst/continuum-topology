package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ikhnos/internal/accounts"
	"ikhnos/internal/fusion"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/fusionhttp"
	"ikhnos/internal/kernel"
	"ikhnos/internal/linksapi"
	"ikhnos/internal/testkit"
)

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
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
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
	a.a.Fusion = &fusion.FusionControl{Name: "ikhnos-fusion", Namespace: "ikhnos", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: st.URL}}
	// no pairs: nothing to read
	if doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(viewer)).json(t); doc["available"] != true || len(doc["pairs"].([]any)) != 0 || calls.Load() != 0 {
		t.Fatalf("no pairs: %v (%d reads)", doc, calls.Load())
	}
	if r := a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin)); r.Code != 200 {
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
	*a.now = a.now.Add(linksapi.LatestEvery + time.Second)
	a.do("GET", "/api/v1/links/latest", nil, withCookie(viewer))
	if calls.Load() != 2 {
		t.Errorf("reads = %d", calls.Load())
	}
}

func TestAStoreThatFailsIsUnavailableAndNothingOfItsErrorIsShown(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("panic at /srv/secret.go host=10.2.3.4"))
	}))
	defer bad.Close()
	a.a.Fusion = &fusion.FusionControl{Name: "ikhnos-fusion", Namespace: "ikhnos", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: bad.URL}}
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin))
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
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}, {A: "cl-d", B: "cl-e"}}
	if r := d.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: links}, withCookie(d.admin)); r.Code != 200 {
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
	*d.now = d.now.Add(linksapi.LatestEvery + time.Second)
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
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(a.admin))
	r := a.get("/api/v1/fusion/links", withCookie(a.admin))
	if r.Code != 503 || strings.Contains(r.Body.String(), "secret.go") || strings.Contains(r.Body.String(), "10.2.3.4") {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

func TestFusionLinksIsDocumented(t *testing.T) {
	spec := fusionhttp.OpenAPI(fusionOps(nil))
	op := spec["paths"].(obj)[fusionhttp.APIPath+"/links"].(obj)["get"].(obj)
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
		case strings.HasPrefix(q, "max by (cluster, peer, direction) (ikhnos_link_probe_up)"):
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
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	a.a.Fusion = &fusion.FusionControl{Name: "ikhnos-fusion", Namespace: "ikhnos", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: probeStore(t).URL}}
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin))
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
	_, owner := a.user(t, "boss", accounts.RoleOwner)
	a.a.Fusion = &fusion.FusionControl{Name: "ikhnos-fusion", Namespace: "ikhnos", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: linkStore(t, &calls, "0.01").URL}}
	r := a.do("POST", "/api/v1/orgs", map[string]any{"name": "Org Two"}, withCookie(owner))
	if r.Code != 201 {
		t.Fatalf("create org two: %d %s", r.Code, r.Body.String())
	}
	org2, _ := r.json(t)["id"].(string)
	// the other organisation lists a pair by the ids of the FUSION organisation's clusters
	if r := a.do("PUT", "/api/v1/orgs/"+org2+"/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(owner)); r.Code != 200 {
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
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(owner))
	if doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(owner)).json(t); doc["available"] != true || calls.Load() != 1 {
		t.Errorf("the FUSION organisation: %v (%d reads)", doc, calls.Load())
	}
}

func TestLinkReportFailsWhenTheAgentsCannotBeReadAndTheScreenSaysSo(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	var calls atomic.Int32
	a.a.Fusion = &fusion.FusionControl{Name: "ikhnos-fusion", Namespace: "ikhnos", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: linkStore(t, &calls, "0.01").URL}}
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: links}, withCookie(admin))
	h := a.hub()
	if _, _, err := h.LinkReport(context.Background(), links, true); err != nil {
		t.Fatal(err)
	}
	real := h.C.Store
	h.C.Store = testkit.AgentsDown{Store: real}
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

func TestAStoreThatStopsAnsweringKeepsTheScreenOnItsLastFiguresMarkedStale(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
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
	a.a.Fusion = &fusion.FusionControl{Name: "ikhnos-fusion", Namespace: "ikhnos", Kube: newFakeKube(fusionNames...), Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: proxy.URL}}
	a.do("PUT", "/api/v1/settings", kernel.Settings{SnapshotMinutes: 7, Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}}, withCookie(admin))
	first := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	if first["available"] != true || first["stale"] != nil {
		t.Fatalf("%v", first)
	}
	down.Store(true)
	*a.now = a.now.Add(linksapi.LatestEvery + time.Second)
	doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t)
	pairs := doc["pairs"].([]any)
	if doc["available"] != true || doc["stale"] != true || doc["reason"] != nil || len(pairs) != 1 || pairs[0].(map[string]any)["lossAToB"] != 0.01 || doc["at"] != first["at"] {
		t.Fatalf("a failed read replaced the last good figures: %v", doc)
	}
	// long after, they are not served as if they were current
	*a.now = a.now.Add(linksapi.LatestStaleFor + time.Minute)
	if doc := a.do("GET", "/api/v1/links/latest", nil, withCookie(admin)).json(t); doc["available"] != false || doc["reason"] != "unavailable" {
		t.Errorf("%v", doc)
	}
}
