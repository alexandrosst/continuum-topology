package fusionhttp

import (
	"context"
	"net/http"
	"time"

	"ikhnos/internal/fusion"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/httpkit"
	"ikhnos/internal/kernel"
)

func (g *Guard) Status(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	access := callerAccessDoc{Kind: who.Kind, Name: who.Name, Signals: fusionapi.SignalsOf(who.Scope.Signals),
		Namespaces: orEmpty(who.Scope.Namespaces), Clusters: orEmpty(who.Scope.Clusters), RawQueries: who.Scope.Unrestricted()}
	if who.ExpiresAt != nil {
		access.ExpiresAt = kernel.RFC(*who.ExpiresAt)
	}
	// A token holder learns whether the stores are up, not how the cluster is set up: the full status carries workload
	// names, the namespace and Kubernetes API error text, which are for administrators. Nor does it read the cluster for
	// them: a token can poll as fast as the rate limit lets it, so it is answered from the last read (TokenStatus).
	fc := g.Control()
	var st fusion.FusionStatus
	if who.Kind == "token" {
		t := fc.TokenStatus(r.Context())
		st = fusion.FusionStatus{Available: t.Available, State: t.State}
	} else {
		st = fc.Status(r.Context())
	}
	httpkit.WriteJSON(w, 200, statusDoc{Fusion: st, Access: access})
}

// Applications lists the Ikhnos applications, each with its services and which signals FUSION has for them in the range.
// An application here is what Ikhnos calls one (a group of services); the services themselves, as the telemetry names them, are
// listed by fusionServices.
func (g *Guard) Applications(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	groups, err := g.Groups(r, who)
	if err != nil {
		g.Err(w, err)
		return
	}
	svcs, sources, warnings, err := c.Applications(r.Context(), who.Scope, tr)
	if err != nil {
		g.Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, applicationsDoc{Applications: fusionapi.DescribeGroups(groups, svcs), Sources: sources, Warnings: orEmpty(warnings), From: tr.From, To: tr.To})
}

// Services lists the services that have telemetry (their service.name), each with its signals and the Ikhnos
// applications it is in. These are what the telemetry itself calls an application, which is not what Ikhnos does.
func (g *Guard) Services(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	scope, err := g.Scope(r, who)
	if err != nil {
		g.Err(w, err)
		return
	}
	svcs, sources, warnings, err := c.Applications(r.Context(), scope, tr)
	if err != nil {
		g.Err(w, err)
		return
	}
	in := map[string][]string{}
	if groups, err := g.Groups(r, who); err == nil { // best effort: the list is useful without it
		for _, grp := range groups {
			for _, n := range grp.ServiceNames() {
				in[n] = append(in[n], grp.Name)
			}
		}
	}
	out := make([]serviceDoc, 0, len(svcs))
	for _, sv := range svcs {
		names := in[sv.Name]
		if names == nil {
			names = []string{}
		}
		out = append(out, serviceDoc{sv, names})
	}
	httpkit.WriteJSON(w, 200, servicesDoc{Services: out, Sources: sources, Warnings: orEmpty(warnings), From: tr.From, To: tr.To})
}

// Application is one Ikhnos application at a glance, named by its id or its name.
func (g *Guard) Application(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	groups, err := g.Groups(r, who)
	if err != nil {
		g.Err(w, err)
		return
	}
	app, err := fusionapi.FindGroup(groups, r.PathValue("name"))
	if err != nil {
		g.Err(w, err)
		return
	}
	o, err := c.ApplicationOverview(r.Context(), who.Scope, app, tr)
	if err != nil {
		g.Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, o)
}

// topologyAter is what the extras add when the server remembers the past (the server's TopologyAt).
type topologyAter interface {
	TopologyAt(ctx context.Context, appID string, at time.Time) (*fusionapi.TopologyView, []fusionapi.AppMember, error)
}

// AppTopology is one application's services and the traffic between them and the rest of the estate, now or (at=) as of a
// past moment, with what changed around them in the window before it.
func (g *Guard) AppTopology(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	q := r.URL.Query()
	window, err := fusionapi.DurationParam(q.Get("window"))
	if err == nil && (window < 0 || window > fusionapi.MaxTopologyWindow) {
		err = &fusionapi.Error{Status: 400, Msg: "window can be at most 7d"}
	}
	if window == 0 {
		window = fusionapi.DefaultTopologyWindow
	}
	var noise bool
	if err == nil {
		noise, err = fusionapi.ParseBool("noise", q.Get("noise"), false)
	}
	var at time.Time
	if err == nil && q.Has("at") {
		if at, err = time.Parse(time.RFC3339, q.Get("at")); err != nil {
			err = &fusionapi.Error{Status: 400, Msg: "at must be an RFC 3339 time"}
		}
	}
	if err != nil {
		g.Err(w, err)
		return
	}
	groups, err := g.Groups(r, who)
	var app *fusionapi.AppGroup
	if err == nil {
		app, err = fusionapi.FindGroup(groups, r.PathValue("name"))
	}
	if err != nil {
		g.Err(w, err)
		return
	}
	ex, until := g.Extras(), g.Now()
	var v *fusionapi.TopologyView
	if !at.IsZero() {
		h, ok := ex.(topologyAter)
		if !ok {
			g.Err(w, &fusionapi.Error{Status: 404, Msg: "history is not enabled on this server: it needs the graph database"})
			return
		}
		var members []fusionapi.AppMember
		if v, members, err = h.TopologyAt(r.Context(), app.ID, at); err == nil {
			then := *app
			then.Members, then.Unresolved = members, 0
			if vis := fusionapi.VisibleGroups([]fusionapi.AppGroup{then}, who.Scope); len(vis) > 0 { // as the caller may see it, then
				app, until = &vis[0], at
			} else {
				err = &fusionapi.Error{Status: 404, Msg: "the application had no services you may see at that time"}
			}
		}
	} else {
		v, err = ex.Topology(r.Context())
	}
	if err != nil {
		g.Err(w, err)
		return
	}
	t := fusionapi.BuildAppTopology(app, v, who.Scope, noise)
	if !at.IsZero() {
		snap := v.At
		t.Source, t.AsOf, t.SnapshotAt = "history", at, &snap
	}
	evs, err := ex.Changes(r.Context(), until.Add(-window), until, nil, t.TargetsFor(who.Scope), fusionapi.MaxAppChanges+1)
	if err != nil {
		g.Err(w, err)
		return
	}
	t.AddChanges(evs)
	httpkit.WriteJSON(w, 200, t)
}

// Service is one service at a glance, by the service.name its telemetry carries.
func (g *Guard) Service(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
	tr, err := Range(r, g.Now())
	if err != nil {
		g.Err(w, err)
		return
	}
	o, err := c.ServiceOverview(r.Context(), who.Scope, r.PathValue("name"), tr)
	if err != nil {
		g.Err(w, err)
		return
	}
	httpkit.WriteJSON(w, 200, o)
}
