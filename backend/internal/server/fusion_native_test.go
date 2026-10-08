package server

import (
	"net/url"
	"strings"
	"testing"
)

func (d *dataRig) form(path string, form url.Values, opts ...opt) resp {
	return d.post(path, form.Encode(), append([]opt{withHeader("Content-Type", "application/x-www-form-urlencoded")}, opts...)...)
}

func TestTheNativeMirrorServesEachBackendsOwnAnswer(t *testing.T) {
	d := newDataRig(t)
	// Prometheus: the envelope is Prometheus', not FUSION's, and the query got there as written.
	r := d.get("/api/v1/fusion/prometheus/api/v1/query?query=sum%28up%29", withCookie(d.admin))
	j := r.json(t)
	if r.Code != 200 || j["status"] != "success" || j["data"].(map[string]any)["resultType"] != "vector" || !d.stores.askedAbout("prom /api/v1/query?query=sum%28up%29") {
		t.Fatalf("%d %s asked %v", r.Code, r.Body.String(), d.stores.asked)
	}
	// A form POST, which is how Grafana sends a query, and a label-values path.
	r = d.form("/api/v1/fusion/prometheus/api/v1/query_range", url.Values{"query": {"up"}, "start": {"1"}, "end": {"2"}, "step": {"1"}}, withCookie(d.admin))
	if r.Code != 200 || r.json(t)["status"] != "success" {
		t.Fatalf("POST: %d %s", r.Code, r.Body.String())
	}
	if r := d.get("/api/v1/fusion/prometheus/api/v1/label/service_name/values", withCookie(d.admin)); r.Code != 200 || !d.stores.askedAbout("prom /api/v1/label/service_name/values") {
		t.Fatalf("label values: %d %s", r.Code, r.Body.String())
	}
	// Loki and Tempo, under their own paths.
	if r := d.get("/api/v1/fusion/loki/loki/api/v1/query_range?query=%7Bservice_name%3D%22cart%22%7D", withCookie(d.admin)); r.Code != 200 || r.json(t)["data"].(map[string]any)["resultType"] != "streams" {
		t.Fatalf("loki: %d %s", r.Code, r.Body.String())
	}
	if r := d.get("/api/v1/fusion/tempo/api/search?q=%7B%7D&limit=3", withCookie(d.admin)); r.Code != 200 || r.json(t)["traces"] == nil || !d.stores.askedAbout("tempo /api/search?q=%7B%7D&limit=3") {
		t.Fatalf("tempo search: %d %s", r.Code, r.Body.String())
	}
	if r := d.get("/api/v1/fusion/tempo/api/v2/search/tag/resource.service.name/values", withCookie(d.admin)); r.Code != 200 || !d.stores.askedAbout("tempo /api/v2/search/tag/resource.service.name/values") {
		t.Fatalf("tempo tag values: %d %s", r.Code, r.Body.String())
	}
	if r := d.get("/api/v1/fusion/tempo/api/traces/"+testTraceID, withCookie(d.admin)); r.Code != 200 || !d.stores.askedAbout("tempo /api/traces/"+testTraceID) {
		t.Fatalf("tempo trace: %d", r.Code)
	}
	// A bearer token with no limit works the same as an administrator.
	tok, _ := d.mint(t, map[string]any{"name": "grafana"})
	if r := d.get("/api/v1/fusion/prometheus/api/v1/labels", bearer(tok)); r.Code != 200 {
		t.Fatalf("an unlimited token: %d %s", r.Code, r.Body.String())
	}
}

func TestTheNativeMirrorServesNothingThatWritesOrAdministers(t *testing.T) {
	d := newDataRig(t)
	before := d.stores.count()
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/fusion/prometheus/api/v1/admin/tsdb/delete_series?match[]=up"},
		{"POST", "/api/v1/fusion/prometheus/api/v1/admin/tsdb/snapshot"},
		{"POST", "/api/v1/fusion/prometheus/-/reload"},
		{"POST", "/api/v1/fusion/prometheus/-/quit"},
		{"POST", "/api/v1/fusion/prometheus/api/v1/write"},
		{"POST", "/api/v1/fusion/prometheus/api/v1/otlp/v1/metrics"},
		{"GET", "/api/v1/fusion/prometheus/api/v1/targets"},
		{"POST", "/api/v1/fusion/loki/loki/api/v1/push"},
		{"POST", "/api/v1/fusion/loki/loki/api/v1/delete"},
		{"GET", "/api/v1/fusion/loki/config"},
		{"GET", "/api/v1/fusion/loki/ring"},
		{"POST", "/api/v1/fusion/loki/flush"},
		{"POST", "/api/v1/fusion/tempo/flush"},
		{"GET", "/api/v1/fusion/tempo/status/config"},
		{"POST", "/api/v1/fusion/tempo/v1/traces"},
		{"GET", "/api/v1/fusion/tempo/api/traces/%2e%2e%2fflush"},
		{"GET", "/api/v1/fusion/prometheus/api/v1/label/a%2Fb/values"},
		{"GET", "/api/v1/fusion/prometheus/api/v1/../admin/tsdb/snapshot"},
	} {
		var r resp
		if c.method == "GET" {
			r = d.get(c.path, withCookie(d.admin))
		} else {
			r = d.form(c.path, url.Values{"x": {"1"}}, withCookie(d.admin))
		}
		// (The mux turns a path with ".." into a redirect to the cleaned path, which is not a route either.)
		if r.Code == 307 && strings.Contains(c.path, "/../") {
			if again := d.get(r.Header().Get("Location"), withCookie(d.admin)); again.Code != 404 {
				t.Errorf("%s: redirected to %s, which answers %d", c.path, r.Header().Get("Location"), again.Code)
			}
			continue
		}
		if r.Code < 400 || r.Code == 500 {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.Code, r.Body.String())
		}
	}
	if d.stores.count() != before {
		t.Fatalf("a store was asked: %v", d.stores.asked[before:])
	}
	// A route that is read-only for the mirror is not offered a verb it does not have.
	if r := d.form("/api/v1/fusion/prometheus/api/v1/metadata", url.Values{"metric": {"up"}}, withCookie(d.admin)); r.Code != 405 {
		t.Errorf("POST to a GET-only route: %d", r.Code)
	}
}

func TestTheNativeMirrorIsForCallersWithoutALimit(t *testing.T) {
	d := newDataRig(t)
	for _, p := range []string{"/api/v1/fusion/prometheus/api/v1/query?query=up", "/api/v1/fusion/loki/loki/api/v1/labels", "/api/v1/fusion/tempo/api/search/tags"} {
		if r := d.get(p); r.Code != 401 {
			t.Errorf("%s with no credential: %d", p, r.Code)
		}
	}
	limited, _ := d.mint(t, map[string]any{"name": "shop", "namespaces": []string{"shop"}})
	traces, _ := d.mint(t, map[string]any{"name": "traces only", "signals": []string{"traces"}})
	before := d.stores.count()
	for _, c := range []struct{ name, path, tok string }{
		{"namespace-limited", "/api/v1/fusion/prometheus/api/v1/query?query=up", limited},
		{"namespace-limited", "/api/v1/fusion/loki/loki/api/v1/labels", limited},
		{"namespace-limited", "/api/v1/fusion/tempo/api/search?q=%7B%7D", limited},
		{"traces only", "/api/v1/fusion/prometheus/api/v1/query?query=up", traces},
	} {
		r := d.get(c.path, bearer(c.tok))
		if r.Code != 403 || !strings.Contains(r.Body.String(), "token") {
			t.Errorf("%s %s: %d %s", c.name, c.path, r.Code, r.Body.String())
		}
	}
	if d.stores.count() != before {
		t.Fatal("a store was asked for a caller who may not use the mirror")
	}
	// The traces-only token may use the Tempo mirror, and only that.
	if r := d.get("/api/v1/fusion/tempo/api/search?q=%7B%7D", bearer(traces)); r.Code != 200 {
		t.Fatalf("traces token on tempo: %d %s", r.Code, r.Body.String())
	}
}

func TestTheNativeMirrorIsDescribedWithTheRestOfTheAPI(t *testing.T) {
	spec := fusionOpenAPI()
	paths := spec["paths"].(obj)
	for _, p := range []string{"/api/v1/fusion/prometheus/api/v1/query", "/api/v1/fusion/loki/loki/api/v1/query_range", "/api/v1/fusion/tempo/api/search"} {
		if paths[p] == nil {
			t.Errorf("%s is not described", p)
		}
	}
	// Each mirrored route is described under its backend's tag, takes only parameters that are described, and says what answers it.
	tags := map[string]bool{}
	for _, op := range fusionOps(nil) {
		if strings.HasPrefix(op.Path, "/prometheus/") || strings.HasPrefix(op.Path, "/loki/") || strings.HasPrefix(op.Path, "/tempo/") {
			tags[op.Tag] = true
			if op.Response != "NativeResponse" || !strings.Contains(op.Description, "exactly as") {
				t.Errorf("%s: %q %q", op.Path, op.Response, op.Description)
			}
		}
	}
	if len(tags) != 3 {
		t.Errorf("tags = %v", tags)
	}
}
