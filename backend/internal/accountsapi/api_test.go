package accountsapi

import (
	"net/http"
	"testing"

	"ikhnos/internal/httpkit"
)

const open httpkit.Need = -1 // a route that asks nothing of the caller

type recordedRoutes map[string]httpkit.Need

func (r recordedRoutes) Handle(pattern string, n httpkit.Need, h http.HandlerFunc) {
	if h == nil {
		panic("a route with no handler: " + pattern)
	}
	r[pattern] = n
}

func (r recordedRoutes) Open(pattern string, h http.HandlerFunc) { r.Handle(pattern, open, h) }

func TestTheAccountsMountTheirRoutesWithWhatEachAsksOfTheCaller(t *testing.T) {
	got := recordedRoutes{}
	(&API{}).Mount(got)
	const o = "/api/v1/orgs/{org}"
	want := recordedRoutes{
		// Nothing is known of the caller yet: signing in, registering, previewing an invitation, and what the server says of itself.
		"GET /api/v1/server":                          open,
		"POST /api/v1/auth/login":                     open,
		"POST /api/v1/auth/login/2fa":                 open,
		"POST /api/v1/auth/login/2fa/email":           open,
		"POST /api/v1/auth/login/2fa/webauthn/begin":  open,
		"POST /api/v1/auth/login/2fa/webauthn/finish": open,
		"POST /api/v1/auth/register":                  open,
		"POST /api/v1/auth/logout":                    open,
		"POST /api/v1/invites/preview":                open,
		// A person's own account, even while a password change is still pending.
		"GET /api/v1/auth/me":                        httpkit.AnySession,
		"POST /api/v1/auth/password":                 httpkit.AnySession,
		"POST /api/v1/auth/2fa/setup":                httpkit.AnySession,
		"POST /api/v1/auth/2fa/enable":               httpkit.AnySession,
		"POST /api/v1/auth/2fa/disable":              httpkit.AnySession,
		"POST /api/v1/auth/email/request":            httpkit.AnySession,
		"POST /api/v1/auth/email/confirm":            httpkit.AnySession,
		"POST /api/v1/auth/email-otp/enable":         httpkit.AnySession,
		"POST /api/v1/auth/email-otp/disable":        httpkit.AnySession,
		"POST /api/v1/auth/webauthn/register/begin":  httpkit.AnySession,
		"POST /api/v1/auth/webauthn/register/finish": httpkit.AnySession,
		"POST /api/v1/auth/webauthn/{id}/rename":     httpkit.AnySession,
		"POST /api/v1/auth/webauthn/{id}/remove":     httpkit.AnySession,
		"GET /api/v1/auth/tokens":                    httpkit.AnySession,
		"POST /api/v1/auth/tokens":                   httpkit.AnySession,
		"POST /api/v1/auth/tokens/{id}/revoke":       httpkit.AnySession,
		// Organisations: any settled account lists, makes and joins; inside one, the role decides.
		// The server's mail settings are not an organisation's: settled in one is all the guard asks, and the handler asks for an owner of the default organisation.
		"GET /api/v1/mail":                   httpkit.Settled,
		"PUT /api/v1/mail":                   httpkit.Settled,
		"GET /api/v1/orgs":                   httpkit.Settled,
		"POST /api/v1/orgs":                  httpkit.Settled,
		"POST /api/v1/invites/accept":        httpkit.Settled,
		"POST " + o + "/rename":              httpkit.OwnerRole,
		"POST " + o + "/delete":              httpkit.OwnerRole,
		"POST " + o + "/leave":               httpkit.MemberRole,
		"GET " + o + "/members":              httpkit.MemberRole,
		"POST " + o + "/members/{id}/role":   httpkit.AdminRole,
		"POST " + o + "/members/{id}/remove": httpkit.AdminRole,
		"GET " + o + "/invites":              httpkit.AdminRole,
		"POST " + o + "/invites":             httpkit.AdminRole,
		"DELETE " + o + "/invites/{id}":      httpkit.AdminRole,
	}
	if len(got) != len(want) {
		t.Fatalf("mounted %d routes, want %d", len(got), len(want))
	}
	for p, n := range want {
		if got[p] != n {
			t.Errorf("%s asks %v, want %v", p, got[p], n)
		}
	}
}

func TestTheSingleSignOnRouteExistsOnlyWhereTheHeaderIsConfigured(t *testing.T) {
	off, on := recordedRoutes{}, recordedRoutes{}
	(&API{}).Mount(off)
	(&API{Edge: Edge{SSOHeader: "X-User"}}).Mount(on)
	if _, ok := off["GET /api/v1/auth/sso"]; ok {
		t.Error("the endpoint exists on a server that never configured it")
	}
	if on["GET /api/v1/auth/sso"] != open {
		t.Error("the endpoint is missing, or guarded, where the header is set")
	}
}
