package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"continuum/internal/chart"
	"continuum/internal/store"
)

// fusionName must give the same prefix the continuum-fusion chart's own "fusion.name" helper does: the operator's
// route endpoints are derived from it, and a mismatch would point an operator at Services that do not exist.
// The cases below are the ones internal/chart/fusion_test.go pins on the chart side.
func TestFusionNameMatchesTheChartHelper(t *testing.T) {
	a50 := strings.Repeat("a", 50)
	cases := []struct{ release, want string }{
		{"fusion", "fusion"},
		{"prod", "prod-fusion"},
		{"my-fusion", "my-fusion"},
		{"fusion-eu", "fusion-eu"},
		{a50, a50}, // 50 + "-fusion" is cut back to 50
		{strings.Repeat("a", 49), strings.Repeat("a", 49)}, // cut at 50 leaves a trailing '-', which the chart trims
		{strings.Repeat("a", 48), strings.Repeat("a", 48) + "-f"},
	}
	for _, c := range cases {
		if got := fusionName(c.release); got != c.want {
			t.Errorf("fusionName(%q) = %q, want %q", c.release, got, c.want)
		}
	}
}

func TestFusionRoutesAreEachStoresInClusterAddress(t *testing.T) {
	d := normalizeFusion(store.Destination{Kind: store.DestinationFusion})
	if d.FusionRelease != "fusion" || d.FusionNamespace != "continuum-system" {
		t.Fatalf("defaults = %+v", d)
	}
	routes := fusionRoutes(d)
	want := []fusionRoute{
		{store.ModalityMetrics, "fusion-prometheus.continuum-system.svc:9090/api/v1/otlp", "http"},
		{store.ModalityLogs, "fusion-loki.continuum-system.svc:3100/otlp", "http"},
		{store.ModalityTraces, "fusion-tempo.continuum-system.svc:4317", "grpc"},
	}
	if len(routes) != len(want) {
		t.Fatalf("routes = %+v", routes)
	}
	for i := range want {
		if routes[i] != want[i] {
			t.Errorf("route %d = %+v, want %+v", i, routes[i], want[i])
		}
	}
	flags := fusionRouteFlags(d, " ")
	for _, w := range []string{
		"--set export.routes.metrics.endpoint=fusion-prometheus.continuum-system.svc:9090/api/v1/otlp",
		"--set export.routes.metrics.protocol=http",
		"--set export.routes.logs.endpoint=fusion-loki.continuum-system.svc:3100/otlp",
		"--set export.routes.traces.protocol=grpc",
		"--set export.routes.traces.tls.insecure=true",
	} {
		if !strings.Contains(flags, w) {
			t.Errorf("route flags lack %q:\n%s", w, flags)
		}
	}
}

func TestValidateFusion(t *testing.T) {
	ok := normalizeFusion(store.Destination{FusionRelease: "eu-1", FusionNamespace: "telemetry"})
	if err := validateFusion(ok); err != nil {
		t.Fatalf("a good destination: %v", err)
	}
	for _, d := range []store.Destination{
		{FusionRelease: "Has Caps", FusionNamespace: "ns"},
		{FusionRelease: "-lead", FusionNamespace: "ns"},
		{FusionRelease: strings.Repeat("a", 51), FusionNamespace: "ns"},
		{FusionRelease: "ok", FusionNamespace: "under_score"},
		{FusionRelease: "ok", FusionNamespace: strings.Repeat("a", 64)},
	} {
		if err := validateFusion(d); err == nil {
			t.Errorf("accepted %+v", d)
		}
	}
}

func TestOperatorsHTTPFusionDestination(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	body := map[string]any{
		"name":             "athens-regional",
		"sourceClusterIds": []string{cl},
		// External-only fields sent along with a fusion kind are dropped, not stored.
		"destination": map[string]any{"kind": "fusion", "fusionRelease": "eu", "endpoint": "ignored:4317", "authSecretName": "x"},
	}
	r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	created := r.json(t)
	dest, _ := created["operator"].(map[string]any)["destination"].(map[string]any)
	if dest["kind"] != "fusion" || dest["fusionRelease"] != "eu" || dest["fusionNamespace"] != "continuum-system" {
		t.Fatalf("destination = %v", dest)
	}
	if e, has := dest["endpoint"]; has && e != "" {
		t.Fatalf("an external endpoint was kept on a fusion destination: %v", dest)
	}
	install, _ := created["install"].(string)
	for _, w := range []string{
		"export.routes.metrics.endpoint=eu-fusion-prometheus.continuum-system.svc:9090/api/v1/otlp",
		"export.routes.logs.endpoint=eu-fusion-loki.continuum-system.svc:3100/otlp",
		"export.routes.traces.endpoint=eu-fusion-tempo.continuum-system.svc:4317",
	} {
		if !strings.Contains(install, w) {
			t.Errorf("operator install lacks %q:\n%s", w, install)
		}
	}
	if strings.Contains(install, "export.otlp.endpoint") {
		t.Errorf("a fusion operator names no default endpoint, every signal has a route:\n%s", install)
	}
	fi, _ := created["fusionInstall"].(string)
	if !strings.Contains(fi, "helm upgrade --install eu ") || !strings.Contains(fi, "--namespace continuum-system --create-namespace") || !strings.Contains(fi, "--version ") {
		t.Fatalf("fusionInstall = %q", fi)
	}

	// The stored destination reads back the same, and an update to another namespace re-derives the routes.
	id, _ := created["operator"].(map[string]any)["id"].(string)
	upd := map[string]any{"sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "fusion", "fusionRelease": "eu", "fusionNamespace": "obs"}}
	r = a.do("POST", "/api/v1/operators/"+id+"/scope", upd, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("update: %d %s", r.Code, r.Body.String())
	}
	if fi, _ := r.json(t)["fusionInstall"].(string); !strings.Contains(fi, "--namespace obs ") {
		t.Fatalf("update response fusionInstall = %q", fi)
	}
}

func TestOperatorsHTTPRejectsABadFusionDestination(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	body := map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "fusion", "fusionRelease": "Not Valid"}}
	if r := a.do("POST", "/api/v1/operators", body, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("bad release name: %d %s", r.Code, r.Body.String())
	}
}

func TestFusionChartIsServed(t *testing.T) {
	rr := httptest.NewRecorder()
	testAdmin(t).Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/charts/"+chart.Fusion.Filename(), nil))
	if rr.Code != http.StatusOK || rr.Body.Len() < 500 {
		t.Fatalf("GET fusion chart: %d, %d bytes", rr.Code, rr.Body.Len())
	}
}
