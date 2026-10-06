package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"continuum/internal/fusionapi"
)

// pageRig is the data rig with FUSION fully up, Grafana among it, and fake Prometheus and Grafana pages that remember how
// they were called.
type pageRig struct {
	*dataRig
	kube          *fakeKube
	prom, grafana *httptest.Server
	mu            sync.Mutex
	seen          map[string]*http.Request // "prom" / "grafana" -> the last request that reached it
}

func newPageRig(t *testing.T) *pageRig {
	t.Helper()
	d := newDataRig(t)
	p := &pageRig{dataRig: d, seen: map[string]*http.Request{}}
	p.kube = newFakeKube(append(append([]string{}, fusionNames...), "continuum-fusion-grafana")...)
	for n := range p.kube.replicas {
		p.kube.replicas[n] = 1
	}
	p.kube.allReady()
	capture := func(name string, h http.HandlerFunc) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			p.seen[name] = r.Clone(r.Context())
			p.mu.Unlock()
			h(w, r)
		}))
		t.Cleanup(s.Close)
		return s
	}
	p.prom = capture("prom", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/graph":
			http.Redirect(w, r, "http://localhost:9090/fusion/prometheus/query", http.StatusFound)
			return
		case "/redirect": // sends the person wherever the test says, written exactly as given
			w.Header().Set("Location", r.URL.Query().Get("to"))
			w.WriteHeader(http.StatusFound)
			return
		case "/headers": // says what it was sent, and sends what a page of its own would
			for _, k := range []string{"Connection", "X-Secret", "Keep-Alive", "Te", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "Proxy-Authorization", "X-Http-Method-Override", "X-Http-Method", "X-Method-Override"} {
				if v := r.Header.Get(k); v != "" {
					_, _ = w.Write([]byte(k + "=" + v + "\n"))
				}
			}
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Server", "Prometheus/3.5")
			w.Header().Set("X-Powered-By", "Go")
			w.Header().Set("Via", "1.1 internal")
			w.Header().Set("Strict-Transport-Security", "max-age=1")
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Referrer-Policy", "unsafe-url")
			return
		case "/api/v1/query":
			if r.Method == http.MethodPost {
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil {
					return // the proxy cut the body off
				}
				_, _ = w.Write([]byte("read " + strconv.FormatInt(n, 10)))
				return
			}
		}
		w.Header().Set("Set-Cookie", "prom=1")
		_, _ = w.Write([]byte("prometheus " + r.URL.Path))
	})
	p.grafana = capture("grafana", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'")
		w.Header().Set("Set-Cookie", "grafana_session=abc")
		_, _ = w.Write([]byte("grafana " + r.URL.Path))
	})
	d.a.Fusion = &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: p.kube, Org: "org-1", StatusTTL: -1,
		PrometheusUI: p.prom.URL, GrafanaUI: p.grafana.URL,
		Data: &fusionapi.Client{Prometheus: d.stores.prom.URL, Loki: d.stores.loki.URL, Tempo: d.stores.tempo.URL}}
	return p
}

func (p *pageRig) last(name string) *http.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[name]
}

// Only an administrator of FUSION's organisation, signed in with the session cookie, gets a page; a FUSION access token,
// a viewer and a stranger do not, and nothing reaches the page for them.
func TestFusionPagesAreForAdministratorsOnly(t *testing.T) {
	p := newPageRig(t)
	_, viewer := p.user(t, "vera", RoleViewer)
	secret, _ := p.mint(t, map[string]any{"name": "engine"})
	for _, path := range []string{fusionGrafanaPath, fusionPromPath + "graph"} {
		for name, opts := range map[string][]opt{
			"nobody":   nil,
			"a viewer": {withCookie(viewer)},
			"a token":  {bearer(secret)},
		} {
			if r := p.get(path, opts...); r.Code != 401 && r.Code != 403 {
				t.Errorf("%s at %s: %d", name, path, r.Code)
			}
		}
	}
	if p.last("grafana") != nil || p.last("prom") != nil {
		t.Fatal("a request got through to a page without being allowed")
	}
	if r := p.get(fusionGrafanaPath+"d/abc", withCookie(p.admin)); r.Code != 200 || r.Body.String() != "grafana /fusion/grafana/d/abc" {
		t.Fatalf("an administrator: %d %q", r.Code, r.Body.String())
	}
}

// What reaches Grafana: who the person is, in the one header it trusts, set by the server whatever the browser sent; none
// of the person's Ikhnos cookie or credentials. What comes back: no cookies, Grafana's own policy kept, no caching.
func TestFusionGrafanaGetsTheSignedInPersonAndNothingElse(t *testing.T) {
	p := newPageRig(t)
	r := p.get(fusionGrafanaPath+"explore", withCookie(p.admin), withHeader("X-WEBAUTH-USER", "someone-else"), withHeader("X-Webauth-Role", "Admin"),
		bearerLike("not-a-real-token"), withHeader("X-Requested-With", "x"))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	got := p.last("grafana")
	if got.Header.Get("X-WEBAUTH-USER") != "root" {
		t.Errorf("signed in as %q, want the real person (root)", got.Header.Get("X-WEBAUTH-USER"))
	}
	if got.Header.Get("X-Webauth-Role") != "" {
		t.Error("a client-supplied X-WEBAUTH-* header reached Grafana")
	}
	if got.Header.Get("Cookie") != "" || got.Header.Get("Authorization") != "" || got.Header.Get("X-Requested-With") != "" {
		t.Errorf("credentials reached Grafana: %v", got.Header)
	}
	if got.URL.Path != "/fusion/grafana/explore" {
		t.Errorf("Grafana is served from its sub path: got %q", got.URL.Path)
	}
	if r.Header().Get("Set-Cookie") != "" {
		t.Errorf("a cookie from Grafana reached the browser: %q", r.Header().Get("Set-Cookie"))
	}
	if csp := r.Header().Values("Content-Security-Policy"); len(csp) != 1 || !strings.Contains(csp[0], "unsafe-eval") {
		t.Errorf("Grafana's own policy was not the only one: %v", csp)
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control = %q", r.Header().Get("Cache-Control"))
	}
}

func bearerLike(s string) opt { return withHeader("Authorization", "Basic "+s) }

// Prometheus is served at its prefix and told it lives at the root; it sends no policy of its own, so it gets ours; its
// redirects to where it believes it lives are made relative.
func TestFusionPrometheusPageStripsItsPrefixAndKeepsToTheOrigin(t *testing.T) {
	p := newPageRig(t)
	r := p.get(fusionPromPath+"api/v1/query?query=up", withCookie(p.admin))
	if r.Code != 200 || r.Body.String() != "prometheus /api/v1/query" {
		t.Fatalf("%d %q", r.Code, r.Body.String())
	}
	if got := p.last("prom"); got.URL.RawQuery != "query=up" || got.Header.Get("Cookie") != "" || got.Header.Get("X-WEBAUTH-USER") != "" {
		t.Errorf("upstream request = %v %v", got.URL, got.Header)
	}
	if csp := r.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("csp = %q", csp)
	}
	if r.Header().Get("Set-Cookie") != "" {
		t.Error("a cookie from Prometheus reached the browser")
	}
	r = p.get(fusionPromPath+"graph", withCookie(p.admin))
	if r.Code != 302 || r.Header().Get("Location") != "/fusion/prometheus/query" {
		t.Errorf("redirect = %d %q", r.Code, r.Header().Get("Location"))
	}
}

// A page that is not up is not proxied (and the status offers no link to it); a change made from another origin is refused.
func TestFusionPagesNeedTheirWorkloadsUpAndASameOriginWrite(t *testing.T) {
	p := newPageRig(t)
	p.kube.ready["continuum-fusion-grafana"] = 0
	if r := p.get(fusionGrafanaPath, withCookie(p.admin)); r.Code != 503 {
		t.Fatalf("Grafana still starting: %d", r.Code)
	}
	doc := p.get("/api/v1/orgs/org-1/fusion", withCookie(p.admin)).json(t)
	if doc["state"] != "running" {
		t.Fatalf("Grafana starting late holds FUSION back: %v", doc["state"])
	}
	links, _ := doc["links"].(map[string]any)
	if links["prometheus"] != fusionPromPath || links["grafana"] != nil {
		t.Fatalf("links while Grafana starts = %v", links)
	}
	p.kube.allReady()
	if links, _ := p.get("/api/v1/orgs/org-1/fusion", withCookie(p.admin)).json(t)["links"].(map[string]any); links["grafana"] != fusionGrafanaPath {
		t.Fatalf("links with Grafana up = %v", links)
	}
	p.kube.ready["continuum-fusion-prometheus"] = 0
	if r := p.get(fusionPromPath, withCookie(p.admin)); r.Code != 503 {
		t.Fatalf("Prometheus down: %d", r.Code)
	}
	p.kube.allReady()

	post := func(origin string) int {
		req := httptest.NewRequest("POST", fusionGrafanaPath+"api/ds/query", strings.NewReader("{}"))
		req.RemoteAddr = "10.1.1.1:5555"
		req.AddCookie(&http.Cookie{Name: cookieName, Value: p.admin})
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post("https://evil.example"); c != 403 {
		t.Errorf("a write from another origin: %d", c)
	}
	if c := post("http://" + httptest.NewRequest("GET", "/", nil).Host); c != 200 {
		t.Errorf("a write from this origin: %d", c)
	}
}

// Grafana is optional: switched with the rest where it exists, ignored where it does not; and it does not hold FUSION's
// own state back while it starts.
func TestGrafanaIsSwitchedWithFusionWhereItExists(t *testing.T) {
	a := newAdminRig(t)
	k := newFakeKube(append(append([]string{}, fusionNames...), "continuum-fusion-grafana")...)
	f := &FusionControl{Name: "continuum-fusion", Namespace: "continuum", Kube: k, Org: a.a.C.OrgID, StatusTTL: -1, Data: &fusionapi.Client{}}
	ctx := t.Context()
	if st := f.Status(ctx); len(st.Components) != 5 || st.State != "off" {
		t.Fatalf("with Grafana: %+v", st)
	}
	if err := f.scaleAll(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if k.replicas["continuum-fusion-grafana"] != 1 {
		t.Fatal("Grafana was not switched on")
	}
	if err := f.scaleAll(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if k.replicas["continuum-fusion-grafana"] != 0 {
		t.Fatal("Grafana was not switched off")
	}
	// An install without Grafana (turned off in the chart, or an older release): nothing to scale, nothing missing.
	f2, k2 := newFusion(t, a)
	if err := f2.scaleAll(ctx, 1); err != nil {
		t.Fatalf("scaling an install without Grafana: %v", err)
	}
	if st := f2.Status(ctx); len(st.Components) != 4 || !st.Available {
		t.Fatalf("without Grafana: %+v", st)
	}
	_ = k2
}

// ---- what the proxy lets through ----

func TestFusionProxyPolicy(t *testing.T) {
	for _, tc := range []struct {
		component, method, path string
		status                  int
		allow                   string
	}{
		// Prometheus: read with GET and HEAD; POST only where a long expression needs it.
		{"metrics", "GET", "/graph", 0, ""},
		{"metrics", "HEAD", "/api/v1/query", 0, ""},
		{"metrics", "GET", "/api/v1/label/job/values", 0, ""},
		{"metrics", "GET", "/api/v1/status/config", 0, ""},
		{"metrics", "POST", "/api/v1/query", 0, ""},
		{"metrics", "POST", "/api/v1/query_range", 0, ""},
		{"metrics", "POST", "/api/v1/series", 0, ""},
		{"metrics", "POST", "/api/v1/labels", 0, ""},
		{"metrics", "POST", "/api/v1/query_exemplars", 0, ""},
		{"metrics", "POST", "/graph", 405, "GET, HEAD"},
		{"metrics", "POST", "/api/v1/label/job/values", 405, "GET, HEAD"},
		{"metrics", "PUT", "/api/v1/query", 405, "GET, HEAD, POST"},
		{"metrics", "DELETE", "/api/v1/series", 405, "GET, HEAD, POST"},
		{"metrics", "OPTIONS", "/api/v1/query", 405, "GET, HEAD, POST"},
		{"metrics", "TRACE", "/graph", 405, "GET, HEAD"},
		// ... and never the doors that write, reconfigure or stop it, by any method.
		{"metrics", "POST", "/api/v1/otlp/v1/metrics", 403, ""},
		{"metrics", "GET", "/api/v1/otlp/v1/metrics", 403, ""},
		{"metrics", "POST", "/api/v1/write", 403, ""},
		{"metrics", "POST", "/api/v1/read", 403, ""},
		{"metrics", "POST", "/api/v1/admin/tsdb/delete_series", 403, ""},
		{"metrics", "PUT", "/api/v1/admin/tsdb/snapshot", 403, ""},
		{"metrics", "GET", "/api/v1/admin/tsdb/snapshot", 403, ""},
		{"metrics", "POST", "/-/reload", 403, ""},
		{"metrics", "PUT", "/-/quit", 403, ""},
		{"metrics", "GET", "/-/healthy", 403, ""},
		{"metrics", "GET", "/-", 403, ""},
		{"metrics", "GET", "/debug/pprof/heap", 403, ""},
		// Grafana saves what it is given, so it takes the methods a web page uses; Live is off, and stays unreachable.
		{"grafana", "GET", "/fusion/grafana/d/abc", 0, ""},
		{"grafana", "POST", "/fusion/grafana/api/ds/query", 0, ""},
		{"grafana", "PUT", "/fusion/grafana/api/dashboards/uid/abc", 0, ""},
		{"grafana", "PATCH", "/fusion/grafana/api/user/preferences", 0, ""},
		{"grafana", "DELETE", "/fusion/grafana/api/dashboards/uid/abc", 0, ""},
		{"grafana", "OPTIONS", "/fusion/grafana/api/ds/query", 405, "GET, HEAD, POST, PUT, PATCH, DELETE"},
		{"grafana", "TRACE", "/fusion/grafana/", 405, "GET, HEAD, POST, PUT, PATCH, DELETE"},
		{"grafana", "GET", "/fusion/grafana/api/live/ws", 403, ""},
		{"grafana", "POST", "/fusion/grafana/api/live/publish", 403, ""},
	} {
		status, allow := fusionProxyPolicy(tc.component, tc.method, tc.path)
		if status != tc.status || allow != tc.allow {
			t.Errorf("%s %s %s: %d %q, want %d %q", tc.component, tc.method, tc.path, status, allow, tc.status, tc.allow)
		}
	}
}

func (p *pageRig) send(method, path string, body io.Reader, opts ...opt) resp {
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "10.1.1.1:5555"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: p.admin})
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return resp{rec}
}

// Through the real server: the writing doors and the maintenance endpoints are refused before anything reaches
// Prometheus - whatever the method, and however the path is dressed up.
func TestFusionProxyKeepsTheWritingDoorsShut(t *testing.T) {
	p := newPageRig(t)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", fusionPromPath + "api/v1/otlp/v1/metrics", 403},
		{"GET", fusionPromPath + "api/v1/otlp/v1/metrics", 403},
		{"POST", fusionPromPath + "api/v1/write", 403},
		{"POST", fusionPromPath + "api/v1/admin/tsdb/delete_series?match[]=up", 403},
		{"POST", fusionPromPath + "-/reload", 403},
		{"POST", fusionPromPath + "-/quit", 403},
		{"GET", fusionPromPath + "-/healthy", 403},
		{"GET", fusionPromPath + "debug/pprof/heap", 403},
		// the same doors, written another way
		{"POST", fusionPromPath + "api/v1/otlp%2Fv1%2Fmetrics", 403},
		{"POST", fusionPromPath + "api/v1/%6Ftlp/v1/metrics", 403},
		{"POST", fusionPromPath + "-%2Freload", 403},
		{"POST", fusionPromPath + "api/v1/admin%2Ftsdb/snapshot", 403},
		// other methods than GET, HEAD and the query POSTs
		{"PUT", fusionPromPath + "api/v1/query", 405},
		{"DELETE", fusionPromPath + "api/v1/query", 405},
		{"POST", fusionPromPath + "graph", 405},
		{"OPTIONS", fusionPromPath + "api/v1/query", 405},
		// Grafana Live (a WebSocket channel) is off
		{"GET", fusionGrafanaPath + "api/live/ws", 403},
		{"POST", fusionGrafanaPath + "api/live/publish", 403},
	} {
		r := p.send(tc.method, tc.path, strings.NewReader("{}"))
		if r.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, r.Code, tc.want)
		}
	}
	// Paths that are not in their plain form are not given the chance to be read two ways.
	for _, path := range []string{fusionPromPath + "api/v1/%2e%2e/%2e%2e/-/reload", fusionPromPath + "api/v1//write", fusionPromPath + "api/v1/query%5C..%5C..%5C-/reload"} {
		if r := p.send("POST", path, nil); r.Code == 200 {
			t.Errorf("%s: %d", path, r.Code)
		}
	}
	if p.last("prom") != nil {
		t.Fatalf("a refused request reached Prometheus: %s %s", p.last("prom").Method, p.last("prom").URL)
	}
	if p.last("grafana") != nil {
		t.Fatalf("a refused request reached Grafana: %s", p.last("grafana").URL)
	}
	if r := p.send("OPTIONS", fusionPromPath+"api/v1/query", nil); r.Header().Get("Allow") != "GET, HEAD, POST" {
		t.Errorf("Allow = %q", r.Header().Get("Allow"))
	}
}

// What the pages are for still works: reading, and a long expression sent as a POST (with its form, from this origin).
func TestFusionProxyStillServesWhatThePagesNeed(t *testing.T) {
	p := newPageRig(t)
	if r := p.send("GET", fusionPromPath+"graph?g0.expr=up", nil, withHeader("Accept", "text/html")); r.Code != 302 {
		t.Errorf("graph: %d", r.Code)
	}
	for _, path := range []string{"api/v1/query", "api/v1/query_range", "api/v1/series", "api/v1/labels", "api/v1/query_exemplars"} {
		r := p.send("POST", fusionPromPath+path, strings.NewReader("query=up"), withHeader("Content-Type", "application/x-www-form-urlencoded"))
		if r.Code != 200 {
			t.Errorf("POST %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	if r := p.send("POST", fusionPromPath+"api/v1/query", strings.NewReader("query=up"), withHeader("Origin", "https://evil.example")); r.Code != 403 {
		t.Errorf("a query from another origin: %d", r.Code)
	}
	if r := p.send("HEAD", fusionPromPath+"api/v1/query?query=up", nil); r.Code != 200 {
		t.Errorf("HEAD: %d", r.Code)
	}
	for _, m := range []string{"PUT", "PATCH", "DELETE", "POST"} {
		if r := p.send(m, fusionGrafanaPath+"api/dashboards/uid/abc", strings.NewReader("{}")); r.Code != 200 {
			t.Errorf("Grafana %s: %d", m, r.Code)
		}
	}
}

// A WebSocket is a tunnel the proxy's checks cannot see into, and neither page uses one here.
func TestFusionProxyRefusesProtocolUpgrades(t *testing.T) {
	p := newPageRig(t)
	for _, path := range []string{fusionGrafanaPath + "api/live/ws", fusionGrafanaPath + "api/ds/query", fusionPromPath + "graph"} {
		for name, hdr := range map[string][2]string{"Upgrade header": {"Upgrade", "websocket"}, "Connection token": {"Connection", "keep-alive, Upgrade"}} {
			r := p.send("GET", path, nil, withHeader(hdr[0], hdr[1]), withHeader("Sec-WebSocket-Version", "13"))
			if r.Code != 400 {
				t.Errorf("%s at %s: %d", name, path, r.Code)
			}
		}
	}
	if p.last("grafana") != nil || p.last("prom") != nil {
		t.Fatal("an upgrade request reached a page")
	}
}

func TestFusionProxyIsRateLimitedPerPerson(t *testing.T) {
	p := newPageRig(t)
	p.a.fusionRL = NewLimiter(60, 3)
	for i := range 3 {
		if r := p.send("GET", fusionPromPath+"graph", nil); r.Code == 429 {
			t.Fatalf("request %d was limited", i+1)
		}
	}
	r := p.send("GET", fusionPromPath+"graph", nil)
	if r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Fatalf("a fourth request in a burst: %d %v", r.Code, r.Header())
	}
	// It is that person's: someone else is not held up, and the data API is a separate budget from the pages.
	_, other := p.user(t, "olga", RoleAdmin)
	if r := p.send("GET", fusionPromPath+"graph", nil, func(req *http.Request) {
		req.Header.Del("Cookie")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: other})
	}); r.Code == 429 {
		t.Fatal("one person's pages used up another's budget")
	}
	if r := p.get("/api/v1/fusion/status", withCookie(p.admin)); r.Code != 200 {
		t.Fatalf("the data API shares the pages' budget: %d", r.Code)
	}
}

// A request is judged on its own headers: what a person's browser or proxy attached to it for another purpose is not
// passed to the page, and a header it names in Connection is a hop's, not the page's.
func TestFusionProxyDropsHopByHopAndForwardingHeaders(t *testing.T) {
	p := newPageRig(t)
	r := p.send("GET", fusionPromPath+"headers", nil, withHeader("Connection", "keep-alive, X-Secret"), withHeader("X-Secret", "1"),
		withHeader("Keep-Alive", "timeout=5"), withHeader("TE", "gzip"), withHeader("Forwarded", "for=1.2.3.4"),
		withHeader("X-Forwarded-For", "1.2.3.4"), withHeader("X-Forwarded-Host", "evil.example"), withHeader("Proxy-Authorization", "Basic abc"))
	if r.Code != 200 {
		t.Fatalf("%d", r.Code)
	}
	if body := r.Body.String(); body != "" {
		t.Errorf("the page was sent headers that are not its to see:\n%s", body)
	}
}

// The method is judged on the request line, so a header that tells the page to treat it as another is not passed on.
func TestFusionProxyDropsMethodOverrideHeaders(t *testing.T) {
	p := newPageRig(t)
	r := p.send("GET", fusionPromPath+"headers", nil, withHeader("X-HTTP-Method-Override", "DELETE"), withHeader("X-HTTP-Method", "PUT"), withHeader("X-Method-Override", "PATCH"))
	if r.Code != 200 {
		t.Fatalf("%d", r.Code)
	}
	if body := r.Body.String(); body != "" {
		t.Errorf("the page was told to read the request as another method:\n%s", body)
	}
}

// What comes back is held to what this server would send itself: no cookies, no CORS, nothing about the page's own
// hosting, and one policy for framing and referrers, ours.
func TestFusionProxyScrubsWhatComesBack(t *testing.T) {
	p := newPageRig(t)
	r := p.send("GET", fusionPromPath+"headers", nil, withHeader("Origin", "https://evil.example"))
	h := r.Header()
	for _, k := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Server", "X-Powered-By", "Via", "Strict-Transport-Security", "Set-Cookie"} {
		if v := h.Values(k); len(v) != 0 {
			t.Errorf("%s reached the browser: %v", k, v)
		}
	}
	if v := h.Values("X-Frame-Options"); len(v) != 1 || v[0] != "DENY" {
		t.Errorf("X-Frame-Options = %v", v)
	}
	if v := h.Values("Referrer-Policy"); len(v) != 1 || v[0] != "no-referrer" {
		t.Errorf("Referrer-Policy = %v", v)
	}
	if v := h.Values("X-Content-Type-Options"); len(v) != 1 || v[0] != "nosniff" {
		t.Errorf("X-Content-Type-Options = %v", v)
	}
	if v := h.Values("Cache-Control"); len(v) != 1 || v[0] != "no-store" {
		t.Errorf("Cache-Control = %v", v)
	}
}

// A page can redirect the person to a place on this server, and nowhere else.
func TestFusionProxyNeverRedirectsOffTheServer(t *testing.T) {
	p := newPageRig(t)
	host := httptest.NewRequest("GET", "/", nil).Host
	for to, want := range map[string]string{
		"/fusion/prometheus/query": "/fusion/prometheus/query",
		"query?x=1":                "query?x=1",
		"http://localhost:9090/fusion/prometheus/graph":   "/fusion/prometheus/graph",
		"http://" + host + "/fusion/prometheus/graph?a=1": "/fusion/prometheus/graph?a=1",
		p.prom.URL + "/fusion/prometheus/x":               "/fusion/prometheus/x",
	} {
		r := p.send("GET", fusionPromPath+"redirect?to="+url.QueryEscape(to), nil)
		if r.Code != 302 || r.Header().Get("Location") != want {
			t.Errorf("redirect to %q: %d %q, want %q", to, r.Code, r.Header().Get("Location"), want)
		}
	}
	for _, to := range []string{
		"https://evil.example/phish", "http://evil.example", "//evil.example/x", "///evil.example", `/\evil.example`, `\\evil.example`,
		"javascript:alert(1)", "http://localhost:9090@evil.example/", "https://localhost:9091/", "ftp://localhost:9090/", "http://[::1", "http://evil.example:9090",
	} {
		r := p.send("GET", fusionPromPath+"redirect?to="+url.QueryEscape(to), nil)
		if r.Code != http.StatusBadGateway || r.Header().Get("Location") != "" {
			t.Errorf("redirect to %q: %d Location=%q", to, r.Code, r.Header().Get("Location"))
		}
	}
}

func TestFusionProxyBoundsWhatAPageCanSend(t *testing.T) {
	p := newPageRig(t)
	ok := p.send("POST", fusionPromPath+"api/v1/query", strings.NewReader(strings.Repeat("a", 1000)))
	if ok.Code != 200 || ok.Body.String() != "read 1000" {
		t.Fatalf("a normal query: %d %q", ok.Code, ok.Body.String())
	}
	big := p.send("POST", fusionPromPath+"api/v1/query", strings.NewReader(strings.Repeat("a", maxPromBody+10)))
	if big.Code == 200 {
		t.Errorf("a %d byte query went through: %q", maxPromBody+10, big.Body.String())
	}
}

// Every request for a page asset used to read all the workloads; now they share one read.
func TestFusionProxyDoesNotReadTheClusterForEveryAsset(t *testing.T) {
	p := newPageRig(t)
	p.a.Fusion.StatusTTL = 0
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	p.a.Fusion.Now = func() time.Time { return now }
	for range 30 {
		if r := p.send("GET", fusionPromPath+"graph", nil); r.Code != 302 {
			t.Fatalf("%d", r.Code)
		}
	}
	if reads := p.kube.workloadReads(); reads != 5 {
		t.Fatalf("30 page loads read the workloads %d times, want one read of 5", reads)
	}
}

func TestLocalLocation(t *testing.T) {
	up, _ := url.Parse("http://continuum-fusion-prometheus.continuum.svc:9090")
	for loc, want := range map[string]string{
		"/fusion/prometheus/graph": "/fusion/prometheus/graph",
		"graph":                    "graph",
		"?x=1":                     "?x=1",
		"http://localhost:3000/fusion/grafana/login":                  "/fusion/grafana/login",
		"http://continuum-fusion-prometheus.continuum.svc:9090/graph": "/graph",
		"https://ikhnos.example/fusion/grafana/":                      "/fusion/grafana/",
	} {
		if got, err := localLocation(loc, "ikhnos.example", up); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", loc, got, err, want)
		}
	}
	for _, loc := range []string{"https://evil.example/", "//evil.example", `/\evil.example`, "http://%zz", "mailto:a@b", "file:///etc/passwd"} {
		if got, err := localLocation(loc, "ikhnos.example", up); err == nil {
			t.Errorf("%q was passed on as %q", loc, got)
		}
	}
}
