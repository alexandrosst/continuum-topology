package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"continuum/internal/fusionapi"
	"continuum/internal/store"
)

// fusionExtras gives a fused read what Ikhnos itself knows (fusionapi.Extras): the observed topology of the FUSION
// organisation and the events recorded about it. Both come from the server's own state, not from the three stores.
type fusionExtras struct{ a *Admin }

// extras is nil on a server with no platform behind it (it can then only be asked for what the stores hold).
func (a *Admin) fusionExtras() fusionapi.Extras {
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
	return topologyView(doc), nil
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
