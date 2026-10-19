package estate

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

func TestTheEstateMountsItsRoutesWithTheRolesTheyAsk(t *testing.T) {
	got := recordedRoutes{}
	(&API{}).Mount(got)
	want := recordedRoutes{
		// What the page polls and saves: reading is any member's, saving the shared workspace an editor's.
		"GET /api/v1/orgs/{org}/state":                          httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/model":                          httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/workspace":                      httpkit.MemberRole,
		"PUT /api/v1/orgs/{org}/workspace":                      httpkit.EditorRole,
		"GET /api/v1/orgs/{org}/telemetry/self":                 httpkit.MemberRole, // the self-reports of the agents and of this server
		"GET /api/v1/orgs/{org}/history":                        httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/history/snapshot":               httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/history/traffic":                httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/history/dependency/{id}/series": httpkit.MemberRole,
		"POST /api/v1/orgs/{org}/history/record":                httpkit.AdminRole, // makes the server take a snapshot now
		// The estate's memory: any member reads it, but the audit names people, so it is an administrator's.
		"GET /api/v1/orgs/{org}/storage":             httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/timeline":            httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/graph/snapshot":      httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/graph/dependents":    httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/graph/dependencies":  httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/graph/diff":          httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/audit":               httpkit.AdminRole,
		"GET /api/v1/orgs/{org}/workspace/revisions": httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/workspace/at":        httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/events":              httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/tokens":              httpkit.AdminRole, // a token is the secret that enrols an agent
		"POST /api/v1/orgs/{org}/tokens":             httpkit.AdminRole,
		"DELETE /api/v1/orgs/{org}/tokens/{id}":      httpkit.AdminRole,
		// Pure computation, nothing stored or issued: any member may ask for the command.
		"POST /api/v1/orgs/{org}/commands/agent-upgrade":           httpkit.MemberRole,
		"GET /api/v1/orgs/{org}/operators/{id}/address-candidates": httpkit.AdminRole,
		"PUT /api/v1/orgs/{org}/operators/{id}/server-cluster":     httpkit.AdminRole,
		"POST /api/v1/orgs/{org}/agents/{id}/approve":              httpkit.AdminRole,
		"POST /api/v1/orgs/{org}/agents/{id}/reject":               httpkit.AdminRole,
		"POST /api/v1/orgs/{org}/agents/{id}/revoke":               httpkit.AdminRole,
		"POST /api/v1/orgs/{org}/agents/{id}/address-tests":        httpkit.AdminRole,
		"GET /api/v1/orgs/{org}/agents/{id}/address-tests":         httpkit.AdminRole,
		// Narrowing an agent's tier or consent is an editor's; widening is an administrator's, which the hub checks.
		"POST /api/v1/orgs/{org}/agents/{id}/tier":    httpkit.EditorRole,
		"POST /api/v1/orgs/{org}/agents/{id}/consent": httpkit.EditorRole,
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
