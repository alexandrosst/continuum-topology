package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

// fusionExtras gives a fused read what Ikhnos itself knows (fusionapi.Extras): the observed topology of the FUSION
// organisation and the events recorded about it. Both come from the server's own state, not from the three stores.
type fusionExtras struct{ a *Admin }

// extras is nil on a server with no platform behind it (it can then only be asked for what the stores hold).
func (a *Admin) fusionExtras() fusionapi.Extras {
	if a.extras != nil {
		return a.extras
	}
	if a.P == nil {
		return nil
	}
	return fusionExtras{a}
}

func (e fusionExtras) tenant(ctx context.Context) (*Tenant, error) {
	t, err := e.a.P.Tenant(ctx, e.a.fusionOrg())
	if err != nil || t == nil || t.Hub == nil {
		return nil, &fusionapi.Error{Status: 503, Msg: "the organisation's topology is not available"}
	}
	return t, nil
}

func (e fusionExtras) Topology(ctx context.Context) (*fusionapi.TopologyView, error) {
	t, err := e.tenant(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := t.Hub.State(ctx)
	if err != nil {
		return nil, &fusionapi.Error{Status: 503, Msg: "the organisation's topology is not available"}
	}
	v := topologyView(doc)
	// Say which Ikhnos applications each service is in. The topology is still worth returning when the saved workspace
	// cannot be read, so a failure here leaves the services unannotated.
	annotateApplications(v, e.groups(ctx, t, doc))
	return v, nil
}

// appGroupTTL is how long the applications are reused: an application edited in Ikhnos shows in the API within this.
const appGroupTTL = 10 * time.Second

// appGroupCache is the applications as last worked out, shared by every request of this server.
type appGroupCache struct {
	mu     sync.Mutex
	at     time.Time
	groups []fusionapi.AppGroup
}

// Applications are the Ikhnos applications of the organisation, each with the services in it as the topology knows them.
// A failure is not cached; a result is, for appGroupTTL, and callers get their own copy of the slice header.
func (e fusionExtras) Applications(ctx context.Context) ([]fusionapi.AppGroup, error) {
	c := &e.a.appCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := e.a.C.Now(); !c.at.IsZero() && now.Sub(c.at) >= 0 && now.Sub(c.at) < appGroupTTL {
		return slices.Clone(c.groups), nil
	}
	groups, err := e.applications(ctx)
	if err != nil {
		return nil, err
	}
	c.at, c.groups = e.a.C.Now(), groups
	return slices.Clone(groups), nil
}

func (e fusionExtras) applications(ctx context.Context) ([]fusionapi.AppGroup, error) {
	t, err := e.tenant(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := t.Hub.State(ctx)
	if err != nil {
		return nil, &fusionapi.Error{Status: 503, Msg: "the organisation's topology is not available"}
	}
	ws, err := t.C.Store.GetWorkspace(ctx, t.C.OrgID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &fusionapi.Error{Status: 503, Msg: "the saved applications are not available"}
	}
	return appGroups(ws.Data, doc), nil
}

func (e fusionExtras) groups(ctx context.Context, t *Tenant, doc StateDoc) []fusionapi.AppGroup {
	ws, err := t.C.Store.GetWorkspace(ctx, t.C.OrgID)
	if err != nil {
		return nil
	}
	return appGroups(ws.Data, doc)
}

// appGroups turns the applications of a saved workspace into groups of services by the identity telemetry carries. A member
// is a topology service the workspace names by id; one the topology does not (or no longer) have is left out, since there
// is nothing to match telemetry with. The aliases are the app labels a service's telemetry often takes as its service.name
// instead of the workload name.
func appGroups(data []byte, doc StateDoc) []fusionapi.AppGroup {
	if len(data) == 0 {
		return nil
	}
	apps, err := workspace.Applications(data)
	if err != nil {
		return nil
	}
	byID := map[string]int{}
	for i, s := range doc.Topology.Services {
		byID[s.ID] = i
	}
	out := make([]fusionapi.AppGroup, 0, len(apps))
	for _, a := range apps {
		g := fusionapi.AppGroup{ID: a.ID, Name: a.Name, Description: a.Description, Members: []fusionapi.AppMember{}}
		if g.Name == "" {
			g.Name = a.ID
		}
		for _, id := range a.ServiceIDs {
			i, ok := byID[id]
			if !ok {
				continue
			}
			s := doc.Topology.Services[i]
			m := fusionapi.AppMember{Name: s.Name, Namespace: s.Namespace, Cluster: s.ClusterID, Kind: s.Kind}
			for _, k := range []string{"app", "app.kubernetes.io/name"} {
				if v := s.Labels[k]; v != "" && v != s.Name && !slices.Contains(m.Aliases, v) {
					m.Aliases = append(m.Aliases, v)
				}
			}
			g.Members = append(g.Members, m)
		}
		out = append(out, g)
	}
	return out
}

// annotateApplications names, on each service of the view, the applications it is in.
func annotateApplications(v *fusionapi.TopologyView, groups []fusionapi.AppGroup) {
	if len(groups) == 0 {
		return
	}
	for i := range v.Services {
		sv := &v.Services[i]
		for _, g := range groups {
			for _, m := range g.Members {
				if m.Name == sv.Name && m.Namespace == sv.Namespace && m.Cluster == sv.Cluster {
					sv.Applications = append(sv.Applications, g.Name)
					break
				}
			}
		}
	}
}

// topologyView is the part of the state a fused read joins.
func topologyView(doc StateDoc) *fusionapi.TopologyView {
	topo := doc.Topology
	v := &fusionapi.TopologyView{Externals: map[string]string{}}
	if at, err := time.Parse(time.RFC3339, doc.GeneratedAt); err == nil {
		v.At = at
	}
	for _, s := range topo.Services {
		v.Services = append(v.Services, fusionapi.TopoService{ID: s.ID, Name: s.Name, Namespace: s.Namespace, Cluster: s.ClusterID,
			Kind: s.Kind, Image: s.Image, Status: s.Status, Replicas: int(s.Replicas), Ready: int(s.ReadyReplicas), Restarts: int(s.Restarts), Labels: s.Labels})
	}
	for _, d := range topo.Dependencies {
		v.Links = append(v.Links, fusionapi.TopoLink{From: d.From, To: d.To, FromKind: d.FromKind, ToKind: d.ToKind, Protocol: d.Protocol,
			Port: d.Port, Confidence: d.Confidence, Stale: d.Stale, Noise: d.Noise})
	}
	for _, x := range topo.ExternalEndpoints {
		name := x.Name
		if name == "" {
			name = x.Host
			if x.Port != 0 {
				name = fmt.Sprintf("%s:%d", x.Host, x.Port)
			}
		}
		v.Externals[x.ID] = name
	}
	return v
}

func (e fusionExtras) Changes(ctx context.Context, since, until time.Time, clusters []string, limit int) ([]fusionapi.ChangeEvent, error) {
	t, err := e.tenant(ctx)
	if err != nil {
		return nil, err
	}
	q := store.EventQuery{Since: since, Until: until, Limit: limit}
	if len(clusters) == 1 {
		q.ClusterID = clusters[0]
	}
	evs, err := t.C.Store.ListEvents(ctx, t.C.OrgID, q)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &fusionapi.Error{Status: 503, Msg: "the recorded events are not available"}
	}
	want := map[string]bool{}
	for _, c := range clusters {
		want[c] = true
	}
	out := make([]fusionapi.ChangeEvent, 0, len(evs))
	for _, ev := range evs {
		if len(want) > 0 && !want[ev.ClusterID] {
			continue
		}
		out = append(out, fusionapi.ChangeEvent{Time: ev.At.UTC(), Kind: ev.Kind, TargetKind: ev.TargetKind, TargetID: ev.TargetID, Name: ev.Name,
			Cluster: ev.ClusterID, ClusterName: ev.ClusterName, Detail: ev.Detail, Cause: ev.Cause, Severity: ev.Severity})
	}
	return out, nil
}
