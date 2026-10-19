package fusion

import (
	"net/http"
	"testing"

	"continuum/internal/httpkit"
)

type recordedRoutes map[string]httpkit.Need

func (r recordedRoutes) Handle(pattern string, n httpkit.Need, h http.HandlerFunc) {
	if h == nil {
		panic("a route with no handler: " + pattern)
	}
	r[pattern] = n
}

func (r recordedRoutes) Open(pattern string, h http.HandlerFunc) { r.Handle(pattern, -1, h) }

// FUSION is shared by everything that sends to the server, so every route of its switch is an administrator's, the reads included.
func TestTheSwitchMountsWithTheRolesItAsks(t *testing.T) {
	got := recordedRoutes{}
	(&API{}).Mount(got)
	const o = "/api/v1/orgs/{org}"
	want := recordedRoutes{
		"GET " + o + "/fusion":                httpkit.AdminRole,
		"POST " + o + "/fusion/enable":        httpkit.AdminRole,
		"POST " + o + "/fusion/disable":       httpkit.AdminRole,
		"GET " + o + "/fusion/tokens":         httpkit.AdminRole,
		"POST " + o + "/fusion/tokens":        httpkit.AdminRole, // the one response that carries a raw secret
		"DELETE " + o + "/fusion/tokens/{id}": httpkit.AdminRole,
		"GET " + o + "/fusion/retention":      httpkit.AdminRole,
		"PUT " + o + "/fusion/retention":      httpkit.AdminRole,
	}
	if len(got) != len(want) {
		t.Fatalf("mounted %v, want %v", got, want)
	}
	for p, n := range want {
		if got[p] != n {
			t.Errorf("%s asks %v, want %v", p, got[p], n)
		}
	}
}

// A page is offered only while it is up, at the path the server gives it, and FUSION that is not there offers none.
func TestPageLinksOfferOnlyThePagesThatAreUp(t *testing.T) {
	var none *FusionControl
	if l := none.PageLinks(FusionStatus{Available: true}, "/p/", "/g/"); l != nil {
		t.Fatalf("no control: %+v", l)
	}
	f := &FusionControl{}
	if l := f.PageLinks(FusionStatus{}, "/p/", "/g/"); l != nil {
		t.Fatalf("unavailable: %+v", l)
	}
	if l := f.PageLinks(FusionStatus{Available: true}, "/p/", "/g/"); l != nil {
		t.Fatalf("nothing up: %+v", l)
	}
	up := FusionStatus{Available: true, Components: []FusionComponent{{Component: "metrics", Desired: 1, Ready: 1}, {Component: "grafana", Desired: 1, Ready: 0}}}
	if l := f.PageLinks(up, "/p/", "/g/"); l == nil || l.Prometheus != "/p/" || l.Grafana != "" {
		t.Fatalf("Prometheus up, Grafana not: %+v", l)
	}
}
