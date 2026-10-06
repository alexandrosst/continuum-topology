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
	routes := fusionRoutes(store.Destination{Kind: store.DestinationFusion, FusionRelease: "fusion", FusionNamespace: "continuum-system"})
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
}

// FUSION is part of the server and reached through its central operator: nobody creates a "fusion" destination by hand.
func TestOperatorsHTTPRefusesAFusionDestination(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	body := map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "fusion", "fusionRelease": "eu"}}
	r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
	if r.Code != 400 || !strings.Contains(r.Body.String(), "central operator") {
		t.Fatalf("create with a fusion destination: %d %s", r.Code, r.Body.String())
	}
}

func TestFusionChartIsServed(t *testing.T) {
	rr := httptest.NewRecorder()
	testAdmin(t).Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/charts/"+chart.Fusion.Filename(), nil))
	if rr.Code != http.StatusOK || rr.Body.Len() < 500 {
		t.Fatalf("GET fusion chart: %d, %d bytes", rr.Code, rr.Body.Len())
	}
}
