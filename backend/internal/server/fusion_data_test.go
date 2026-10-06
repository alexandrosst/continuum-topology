package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
)

const testTraceID = "0af7651916cd43dd8448eb211c80319c"

// fusionStores are fake Prometheus, Loki and Tempo servers that remember what they were asked.
type fusionStores struct {
	prom, loki, tempo *httptest.Server
	mu                sync.Mutex
	asked             []string // "<store> <path>?<query>"
}

func (s *fusionStores) record(store string, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, store+" "+r.URL.Path+"?"+r.URL.RawQuery)
}

func (s *fusionStores) askedAbout(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.asked {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
}

func (s *fusionStores) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func attrKV(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

func newFusionStores(t *testing.T) *fusionStores {
	s := &fusionStores{}
	start := time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC).UnixNano()
	span := func(id, parent, name string, off int64) map[string]any {
		return map[string]any{"traceId": testTraceID, "spanId": id, "parentSpanId": parent, "name": name, "kind": 2,
			"startTimeUnixNano": json.Number(itoa64(start + off*1e6)), "endTimeUnixNano": json.Number(itoa64(start + off*1e6 + 40e6)), "status": map[string]any{"code": 0}}
	}
	s.tempo = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record("tempo", r)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/traces/"):
			jsonOut(w, map[string]any{"trace": map[string]any{"resourceSpans": []any{
				map[string]any{"resource": map[string]any{"attributes": []any{attrKV("service.name", "cart"), attrKV("k8s.namespace.name", "shop")}},
					"scopeSpans": []any{map[string]any{"spans": []any{span("00f067aa0ba902b7", "", "GET /cart", 0)}}}},
				map[string]any{"resource": map[string]any{"attributes": []any{attrKV("service.name", "audit"), attrKV("k8s.namespace.name", "secret")}},
					"scopeSpans": []any{map[string]any{"spans": []any{span("00000000000000aa", "00f067aa0ba902b7", "write", 10)}}}},
			}}})
		case r.URL.Path == "/api/search":
			jsonOut(w, map[string]any{"traces": []any{map[string]any{"traceID": testTraceID, "rootServiceName": "gateway", "rootTraceName": "GET /", "startTimeUnixNano": "1791200000000000000", "durationMs": 5}}})
		default:
			jsonOut(w, map[string]any{"tagValues": []any{map[string]any{"value": "cart"}}})
		}
	}))
	s.loki = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record("loki", r)
		if strings.HasSuffix(r.URL.Path, "/values") {
			jsonOut(w, map[string]any{"status": "success", "data": []string{"cart"}})
			return
		}
		jsonOut(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{
			"stream": map[string]string{"service_name": "cart", "k8s_namespace_name": "shop"},
			"values": []any{[]any{"1791200000000000000", "boom", map[string]any{"structuredMetadata": map[string]string{"trace_id": testTraceID, "span_id": "00f067aa0ba902b7"}}}}}}}})
	}))
	s.prom = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record("prom", r)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/label/"):
			jsonOut(w, map[string]any{"status": "success", "data": []string{"cart", "up"}})
		case r.URL.Path == "/api/v1/series":
			jsonOut(w, map[string]any{"status": "success", "data": []map[string]string{{"__name__": "up", "service_name": "cart"}}})
		case r.URL.Path == "/api/v1/query":
			jsonOut(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
		default:
			jsonOut(w, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
		}
	}))
	t.Cleanup(func() { s.prom.Close(); s.loki.Close(); s.tempo.Close() })
	return s
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

// dataRig is an admin rig whose FUSION reads the fake stores, with an administrator signed in.
type dataRig struct {
	*adminRig
	stores  *fusionStores
	admin   string // an administrator's session cookie
	adminID string
}

func newDataRig(t *testing.T) *dataRig {
	t.Helper()
	a := newAdminRig(t)
	st := newFusionStores(t)
	k := newFakeKube(fusionNames...)
	a.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: k, Org: "org-1", StatusTTL: -1,
		Data: &fusionapi.Client{Prometheus: st.prom.URL, Loki: st.loki.URL, Tempo: st.tempo.URL}}
	id, cookie := a.user(t, "root", RoleAdmin)
	return &dataRig{adminRig: a, stores: st, admin: cookie, adminID: id}
}

// get calls the data API, which is not under an organisation's path.
func (d *dataRig) get(path string, opts ...opt) resp {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "10.1.1.1:5555"
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	return resp{rec}
}

func bearer(secret string) opt { return withHeader("Authorization", "Bearer "+secret) }

// mint makes a FUSION access token as the administrator and returns its secret.
func (d *dataRig) mint(t *testing.T, body map[string]any) (secret, id string) {
	t.Helper()
	r := d.do("POST", "/api/v1/fusion/tokens", body, withCookie(d.admin))
	if r.Code != 200 {
		t.Fatalf("mint: %d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	secret, _ = j["token"].(string)
	id, _ = j["details"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(secret, fusionTokenPrefix) {
		t.Fatalf("secret = %q", secret)
	}
	return secret, id
}

func TestFusionTokenLifecycle(t *testing.T) {
	d := newDataRig(t)
	_, viewer := d.user(t, "vera", RoleViewer)
	if r := d.do("POST", "/api/v1/fusion/tokens", map[string]any{"name": "x"}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer minted a token: %d", r.Code)
	}
	secret, id := d.mint(t, map[string]any{"name": "decision engine", "signals": []string{"traces", "logs"}, "namespaces": []string{"shop", "shop", " pay "}, "expiresInDays": 30})

	list := d.do("GET", "/api/v1/fusion/tokens", nil, withCookie(d.admin)).jsonArray(t)
	if len(list) != 1 || list[0]["id"] != id || list[0]["name"] != "decision engine" {
		t.Fatalf("list = %v", list)
	}
	if strings.Contains(d.do("GET", "/api/v1/fusion/tokens", nil, withCookie(d.admin)).Body.String(), secret) {
		t.Fatal("the list carries a secret")
	}
	ns, _ := list[0]["namespaces"].([]any)
	sig, _ := list[0]["signals"].([]any)
	if len(ns) != 2 || ns[0] != "shop" || ns[1] != "pay" || len(sig) != 2 || list[0]["clusters"] == nil {
		t.Fatalf("scope as stored = %v", list[0])
	}

	// It authenticates, and the status says what it can do.
	r := d.get("/api/v1/fusion/status", bearer(secret))
	if r.Code != 200 {
		t.Fatalf("status with the token: %d %s", r.Code, r.Body.String())
	}
	whole := r.json(t)
	// A token is not told how the deployment is doing, only whether the data is there.
	for _, k := range []string{"message", "components", "reason", "since"} {
		if _, ok := whole[k]; ok {
			t.Fatalf("a token's status carries %q: %v", k, whole)
		}
	}
	acc := whole["access"].(map[string]any)
	if acc["kind"] != "token" || acc["name"] != "decision engine" || acc["rawQueries"] != false || acc["expiresAt"] == nil {
		t.Fatalf("access = %v", acc)
	}

	// The audit trail says who made and who revoked it, never the secret.
	if r := d.do("DELETE", "/api/v1/fusion/tokens/"+id, nil, withCookie(d.admin)); r.Code != 200 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body.String())
	}
	if r := d.get("/api/v1/fusion/status", bearer(secret)); r.Code != 401 {
		t.Fatalf("a revoked token still works: %d", r.Code)
	}
	rows, _ := d.st.AuditSince(d.ctx, 0, 500)
	var actions []string
	for _, e := range rows {
		if strings.HasPrefix(e.Action, "fusion-token") {
			actions = append(actions, e.Action)
			if strings.Contains(e.Detail, secret) {
				t.Fatal("the audit trail holds a secret")
			}
		}
	}
	if len(actions) != 2 || actions[0] != "fusion-token-created" || actions[1] != "fusion-token-revoked" {
		t.Fatalf("audit = %v", actions)
	}
	if r := d.do("DELETE", "/api/v1/fusion/tokens/"+id, nil, withCookie(d.admin)); r.Code != 404 {
		t.Fatalf("revoking twice: %d", r.Code)
	}
}

func TestFusionTokenValidation(t *testing.T) {
	d := newDataRig(t)
	for name, body := range map[string]map[string]any{
		"no name":           {"name": " "},
		"unknown signal":    {"name": "x", "signals": []string{"events"}},
		"bad namespace":     {"name": "x", "namespaces": []string{"shop;drop"}},
		"blank namespaces":  {"name": "x", "namespaces": []string{" ", ""}},
		"blank clusters":    {"name": "x", "clusters": []string{"  "}},
		"bad cluster":       {"name": "x", "clusters": []string{"cl 1"}},
		"too long lasting":  {"name": "x", "expiresInDays": 4000},
		"negative lifetime": {"name": "x", "expiresInDays": -1},
		"unknown field":     {"name": "x", "admin": true},
	} {
		if r := d.do("POST", "/api/v1/fusion/tokens", body, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
	// Only the organisation FUSION belongs to can hand out access to it.
	d.a.Fusion.Org = "some-other-org"
	if r := d.do("POST", "/api/v1/fusion/tokens", map[string]any{"name": "x"}, withCookie(d.admin)); r.Code != 403 {
		t.Fatalf("another organisation minted a token: %d %s", r.Code, r.Body.String())
	}
}

func TestFusionTokenExpires(t *testing.T) {
	d := newDataRig(t)
	secret, _ := d.mint(t, map[string]any{"name": "short", "expiresInDays": 1})
	if r := d.get("/api/v1/fusion/status", bearer(secret)); r.Code != 200 {
		t.Fatalf("fresh: %d", r.Code)
	}
	*d.now = d.now.Add(25 * time.Hour)
	if r := d.get("/api/v1/fusion/status", bearer(secret)); r.Code != 401 {
		t.Fatalf("expired: %d", r.Code)
	}
}

func TestFusionDataAuthentication(t *testing.T) {
	d := newDataRig(t)
	secret, _ := d.mint(t, map[string]any{"name": "t"})
	_, viewer := d.user(t, "vera", RoleViewer)
	for name, c := range map[string]struct {
		opts []opt
		want int
	}{
		"nobody":                    {nil, 401},
		"a forged token":            {[]opt{bearer(fusionTokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")}, 401},
		"a malformed token":         {[]opt{bearer(fusionTokenPrefix + "nope")}, 401},
		"a viewer's session":        {[]opt{withCookie(viewer)}, 403},
		"an admin's session":        {[]opt{withCookie(d.admin)}, 200},
		"a FUSION token":            {[]opt{bearer(secret)}, 200},
		"a garbage session":         {[]opt{withCookie("cns_nope")}, 401},
		"a bearer that is no token": {[]opt{bearer("hello")}, 401},
	} {
		if r := d.get("/api/v1/fusion/status", c.opts...); r.Code != c.want {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
	// A personal access token acts as its owner: an administrator's reads, a viewer's is refused.
	pat, err := NewAPITokenSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.st.CreateAPIToken(d.ctx, newAPITokenID(), HashSecret(pat), d.adminID, "ci", *d.now); err != nil {
		t.Fatal(err)
	}
	if r := d.get("/api/v1/fusion/status", bearer(pat)); r.Code != 200 || d.get("/api/v1/fusion/status", bearer(pat)).json(t)["access"].(map[string]any)["kind"] != "user" {
		t.Fatalf("an administrator's personal token: %d %s", r.Code, r.Body.String())
	}
	// And a FUSION token opens nothing else.
	if r := d.get("/api/v1/auth/me", bearer(secret)); r.Code != 401 {
		t.Fatalf("a FUSION token signed in to the admin API: %d", r.Code)
	}
	if r := d.get("/api/v1/orgs/org-1/state", bearer(secret)); r.Code != 401 {
		t.Fatalf("a FUSION token read the topology: %d", r.Code)
	}
}

func TestAScopedTokenReadsOnlyItsScope(t *testing.T) {
	d := newDataRig(t)
	secret, _ := d.mint(t, map[string]any{"name": "shop traces", "signals": []string{"traces", "logs"}, "namespaces": []string{"shop"}})
	before := d.stores.count()

	// Metrics are not in its signals: refused before any store is asked.
	if r := d.get("/api/v1/fusion/metrics/names", bearer(secret)); r.Code != 403 || d.stores.count() != before {
		t.Fatalf("metrics: %d %s (%d new store requests)", r.Code, r.Body.String(), d.stores.count()-before)
	}
	// Raw queries are refused for a token with a namespace limit.
	for _, p := range []string{"/api/v1/fusion/logs?query=%7Ba%3D%22b%22%7D", "/api/v1/fusion/traces?q=%7B%20true%20%7D", "/api/v1/fusion/metrics/query?query=up"} {
		if r := d.get(p, bearer(secret)); r.Code != 403 {
			t.Errorf("%s: %d %s", p, r.Code, r.Body.String())
		}
	}
	if d.stores.count() != before {
		t.Fatalf("a refused raw query reached a store: %v", d.stores.asked[before:])
	}

	// A fused trace read: only the shop span comes back, and the log query carried the namespace limit.
	r := d.get("/api/v1/fusion/traces/"+testTraceID+"?include=logs,metrics", bearer(secret))
	if r.Code != 200 {
		t.Fatalf("fused trace: %d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	spans := j["spans"].([]any)
	if len(spans) != 1 || spans[0].(map[string]any)["name"] != "GET /cart" || j["spanCount"] != float64(1) {
		t.Fatalf("spans = %v", spans)
	}
	logs := spans[0].(map[string]any)["logs"].([]any)
	if len(logs) != 1 || logs[0].(map[string]any)["line"] != "boom" {
		t.Fatalf("the log did not land on the span: %v", spans[0])
	}
	src := j["sources"].(map[string]any)
	if src["logs"] != "ok" || src["metrics"] != "not allowed" || src["traces"] != "ok" {
		t.Fatalf("sources = %v", src)
	}
	if !d.stores.askedAbout("k8s_namespace_name%3D~%22shop%22") {
		t.Fatalf("the log query lacked the namespace limit: %v", d.stores.asked)
	}
	if d.stores.askedAbout("prom ") {
		t.Fatalf("Prometheus was asked for a token without metrics: %v", d.stores.asked)
	}

	// The plain trace and the application list are scoped too.
	if r := d.get("/api/v1/fusion/traces/"+testTraceID, bearer(secret)); r.Code != 200 || strings.Contains(r.Body.String(), `"secret"`) || strings.Contains(r.Body.String(), "audit") {
		t.Fatalf("plain trace: %d %s", r.Code, r.Body.String())
	}
	r = d.get("/api/v1/fusion/applications", bearer(secret))
	if r.Code != 200 || r.json(t)["sources"].(map[string]any)["metrics"] != "not allowed" {
		t.Fatalf("applications: %d %s", r.Code, r.Body.String())
	}
	if !d.stores.askedAbout(`resource.k8s.namespace.name+%3D+%22shop%22`) {
		t.Fatalf("the tag-value listing lacked the namespace limit: %v", d.stores.asked)
	}
}

func TestAnAdministratorHasTheFullRead(t *testing.T) {
	d := newDataRig(t)
	for path, field := range map[string]string{
		"/api/v1/fusion/applications":                                        "applications",
		"/api/v1/fusion/metrics/names?service=cart":                          "names",
		"/api/v1/fusion/metrics/series?service=cart":                         "series",
		"/api/v1/fusion/metrics/range?service=cart&step=30s":                 "series",
		"/api/v1/fusion/logs?service=cart&severity=error&order=oldest":       "entries",
		"/api/v1/fusion/traces?service=cart&status=error&min_duration=200ms": "traces",
	} {
		r := d.get(path, withCookie(d.admin))
		if r.Code != 200 || r.json(t)[field] == nil {
			t.Errorf("%s: %d %s", path, r.Code, r.Body.String())
		}
	}
	// A raw PromQL answer comes back in Prometheus' own shape.
	r := d.get("/api/v1/fusion/metrics/query?query=up&evil=1", withCookie(d.admin))
	if r.Code != 200 || r.json(t)["status"] != "success" || r.json(t)["data"].(map[string]any)["resultType"] != "vector" {
		t.Fatalf("raw query: %d %s", r.Code, r.Body.String())
	}
	if d.stores.askedAbout("evil") {
		t.Fatal("an unknown parameter reached Prometheus")
	}
	// The overview of one application.
	if r := d.get("/api/v1/fusion/applications/cart", withCookie(d.admin)); r.Code != 200 || r.json(t)["name"] != "cart" {
		t.Fatalf("overview: %d %s", r.Code, r.Body.String())
	}
}

func TestFusionDataRejectsBadParameters(t *testing.T) {
	d := newDataRig(t)
	for _, p := range []string{
		"/api/v1/fusion/traces?from=yesterday",
		"/api/v1/fusion/traces?limit=0",
		"/api/v1/fusion/traces?status=bogus",
		"/api/v1/fusion/traces?min_duration=fast",
		"/api/v1/fusion/traces/" + testTraceID + "?include=everything",
		"/api/v1/fusion/traces/zzz",
		"/api/v1/fusion/logs?order=sideways",
		"/api/v1/fusion/logs?service=" + strings.Repeat("a", 300),
		"/api/v1/fusion/metrics/range?step=1s&from=now-30d",
		"/api/v1/fusion/metrics/names?metric=(unclosed",
	} {
		if r := d.get(p, withCookie(d.admin)); r.Code != 400 {
			t.Errorf("%s: %d %s", p, r.Code, r.Body.String())
		}
	}
}

func TestFusionDataWhenFusionIsOff(t *testing.T) {
	d := newDataRig(t)
	d.stores.tempo.Close()
	r := d.get("/api/v1/fusion/traces/"+testTraceID, withCookie(d.admin))
	if r.Code != 503 || !strings.Contains(r.Body.String(), "Tempo") {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	// A server installed without FUSION says so.
	d.a.Fusion = nil
	if r := d.get("/api/v1/fusion/traces/"+testTraceID, withCookie(d.admin)); r.Code != 503 {
		t.Fatalf("no FUSION at all: %d", r.Code)
	}
}

func TestFusionDataIsRateLimitedPerCaller(t *testing.T) {
	d := newDataRig(t)
	d.a.fusionRL = NewLimiter(60, 2)
	a, _ := d.mint(t, map[string]any{"name": "a"})
	b, _ := d.mint(t, map[string]any{"name": "b"})
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, d.get("/api/v1/fusion/status", bearer(a)).Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[3] != 429 {
		t.Fatalf("codes = %v", codes)
	}
	if r := d.get("/api/v1/fusion/status", bearer(b)); r.Code != 200 {
		t.Fatalf("another caller shares the first one's limit: %d", r.Code)
	}
}

func TestFusionTokenCap(t *testing.T) {
	d := newDataRig(t)
	for i := 0; i < maxFusionTokens; i++ {
		if err := d.st.CreateFusionToken(d.ctx, store.FusionToken{ID: newFusionTokenID(), OrgID: "org-1", Name: "n", CreatedAt: *d.now, ExpiresAt: d.now.Add(time.Hour)}, HashSecret(randHex(8))); err != nil {
			t.Fatal(err)
		}
	}
	if r := d.do("POST", "/api/v1/fusion/tokens", map[string]any{"name": "one too many"}, withCookie(d.admin)); r.Code != 409 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

func TestTheFusionCardKnowsWhetherTheDataApiIsServed(t *testing.T) {
	d := newDataRig(t)
	if r := d.do("GET", "/api/v1/fusion", nil, withCookie(d.admin)); r.Code != 200 || r.json(t)["data"] != true {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	d.a.Fusion.Org = "some-other-org"
	if r := d.do("GET", "/api/v1/fusion", nil, withCookie(d.admin)); r.json(t)["data"] != false {
		t.Fatalf("another organisation is told the data API is served: %s", r.Body.String())
	}
	// A server without the bundled FUSION cannot serve it.
	d.a.Fusion = nil
	if r := d.do("GET", "/api/v1/fusion", nil, withCookie(d.admin)); r.json(t)["data"] != false {
		t.Fatalf("%s", r.Body.String())
	}
}
