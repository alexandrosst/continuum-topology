package fusionhttp

import (
	"net/http"
	"time"

	"continuum/internal/fusionapi"
)

// Range is the time range the request asks for: from and to, in the forms fusionapi.ParseRange reads.
func Range(r *http.Request, now time.Time) (fusionapi.TimeRange, error) {
	q := r.URL.Query()
	return fusionapi.ParseRange(q.Get("from"), q.Get("to"), now)
}

// Scope is the Scope a structured list read runs with: the caller's own, narrowed to one Ikhnos application when the
// request names one (application=<id or name>). The application is resolved here, at read time, from what Ikhnos knows
// now, so changing an application changes what it shows without touching any stored telemetry. The caller's own Scope
// (who.Scope) is what everything not narrowed keeps using: reading one trace, and the fused joins around a search hit.
func (g *Guard) Scope(r *http.Request, who Caller) (fusionapi.Scope, error) {
	q := r.URL.Query()
	if !q.Has("application") {
		return who.Scope, nil
	}
	groups, err := g.Groups(r, who)
	if err != nil {
		return fusionapi.Scope{}, err
	}
	grp, err := fusionapi.FindGroup(groups, q.Get("application"))
	if err != nil {
		return fusionapi.Scope{}, err
	}
	return who.Scope.FocusOn(grp)
}

// Groups are the Ikhnos applications the caller may know of: each with only the services in the caller's namespaces
// and clusters, and only those that have any (fusionapi.VisibleGroups).
func (g *Guard) Groups(r *http.Request, who Caller) ([]fusionapi.AppGroup, error) {
	ex := g.Extras()
	if ex == nil {
		return nil, &fusionapi.Error{Status: http.StatusServiceUnavailable, Msg: "this server does not know the applications"}
	}
	groups, err := ex.Applications(r.Context())
	if err != nil {
		return nil, err
	}
	return fusionapi.VisibleGroups(groups, who.Scope), nil
}
