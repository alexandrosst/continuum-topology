package operatorsapi

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

// Every operator route asks an administrator, except the list a person choosing a destination reads, which asks an editor and shows no secret.
// The heartbeat receiver is not among them: it has no session, and the server mounts it itself.
func TestTheOperatorsMountWithTheRolesTheyAsk(t *testing.T) {
	got := recordedRoutes{}
	(&API{}).Mount(got)
	const o = "/api/v1/orgs/{org}"
	want := recordedRoutes{
		"GET " + o + "/operators":                     httpkit.AdminRole,
		"POST " + o + "/operators":                    httpkit.AdminRole,
		"GET " + o + "/operators/{id}":                httpkit.AdminRole,
		"GET " + o + "/operator-destinations":         httpkit.EditorRole,
		"POST " + o + "/operators/{id}/scope":         httpkit.AdminRole,
		"POST " + o + "/operators/{id}/install":       httpkit.AdminRole, // credential material
		"GET " + o + "/operators/{id}/certificates":   httpkit.AdminRole,
		"POST " + o + "/operators/{id}/address":       httpkit.AdminRole,
		"POST " + o + "/operators/{id}/address/check": httpkit.AdminRole,
		"GET " + o + "/operators/{id}/senders":        httpkit.AdminRole,
		"POST " + o + "/operators/{id}/heartbeat":     httpkit.AdminRole, // credential material
		"POST " + o + "/operators/{id}/revoke":        httpkit.AdminRole,
		"DELETE " + o + "/operators/{id}":             httpkit.AdminRole,
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
