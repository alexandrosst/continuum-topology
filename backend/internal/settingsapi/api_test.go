package settingsapi

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

func TestTheSettingsMountWithTheRolesTheyAsk(t *testing.T) {
	got := recordedRoutes{}
	(&API{}).Mount(got)
	want := recordedRoutes{
		"GET /api/v1/orgs/{org}/settings": httpkit.MemberRole, // any member reads them (the decider's address is trimmed for all but administrators)
		"PUT /api/v1/orgs/{org}/settings": httpkit.AdminRole,
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
