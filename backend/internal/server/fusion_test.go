package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/chart"
	"continuum/internal/testkit"
)

// FUSION is part of the server and reached through its central operator: nobody creates a "fusion" destination by hand.
func TestOperatorsHTTPRefusesAFusionDestination(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	cl := a.approvedCluster(t, testkit.FP)
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
