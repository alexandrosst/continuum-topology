package server

import (
	"net/http"

	"continuum/internal/httpkit"
)

// Every route under /api is mounted through Admin.handle, which records what the route asks of the caller. That record is
// what the route-table test reads: it is how a person (or a test) can see, in one list, which routes are open, which need a
// signed-in session, which need a role in the organisation, and which carry their own credential.

// Route is one mounted route and what it asks of the caller.
type Route struct {
	Pattern string // as the mux has it, "GET /api/v1/orgs/{org}/state"
	Access  string // accessPublic, accessFusionCredential, or the name of the need it is guarded by
}

const (
	accessPublic           = "public"            // no credential: sign-in, registration, the API's own description
	accessFusionCredential = "fusion-credential" // authenticates itself: a FUSION access token, or an administrator of the FUSION organisation
	accessFusionUI         = "fusion-ui"         // Prometheus's and Grafana's pages, opened for an administrator through a short-lived grant (admin_fusion_proxy.go)
	accessOperatorSecret   = "operator-secret"   // a regional operator's collector, with its own secret and no session
	accessMount            = "mount"             // not a route of its own: it hands a whole subtree to other handlers (/api/ to the API mux, / to the UI)
)

// handle mounts h at pattern on mux and records the route. It is the only way a route is added to the server's muxes (the
// metrics listener, which is a separate server with its own token, has its own).
func (a *Admin) handle(mux *http.ServeMux, pattern, access string, h http.Handler) {
	mux.Handle(pattern, h)
	a.routes = append(a.routes, Route{Pattern: pattern, Access: access})
}

// routeFunc is a guarded route-adder (Admin.Handler's route) as the httpkit.Router a module mounts its handlers on.
type routeFunc func(pattern string, n httpkit.Need, h http.HandlerFunc)

func (f routeFunc) Handle(pattern string, n httpkit.Need, h http.HandlerFunc) { f(pattern, n, h) }
