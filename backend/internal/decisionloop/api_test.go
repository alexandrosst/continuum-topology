package decisionloop

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

func TestTheDecisionLogMountsItsTwoRoutesWithTheRolesTheyAsk(t *testing.T) {
	got := recordedRoutes{}
	API{}.Mount(got)
	want := recordedRoutes{
		"POST /api/v1/orgs/{org}/decisions": httpkit.EditorRole, // recording is an editor's act, as running an external decider is
		"GET /api/v1/orgs/{org}/decisions":  httpkit.MemberRole, // any member may read it back
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
