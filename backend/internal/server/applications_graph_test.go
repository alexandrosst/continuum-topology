package server

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"continuum/internal/graph"
	"continuum/internal/history"
	"continuum/internal/model"
	"continuum/internal/store"
)

// linkSpy is a store that keeps what the graph would be asked to link.
type linkSpy struct {
	store.Store
	sets map[string][]string
}

func (s *linkSpy) LinkEntitiesBatch(_ context.Context, _ string, _ time.Time, _, _, _ string, sets []graph.MemberSet) error {
	s.sets = map[string][]string{}
	for _, m := range sets {
		s.sets[m.ID] = m.TargetIDs
	}
	return nil
}

// Membership can change without a workspace being saved (a hint starts to apply once its service appears), so the recorder's
// scan links the applications too, with the members the API shows.
func TestTheScanLinksApplicationsToTheirMembers(t *testing.T) {
	r := newHubRig(t)
	spy := &linkSpy{Store: r.hub.C.Store}
	r.hub.C.Store = spy
	ws := []byte(`{"schemaVersion":4,"applications":[{"id":"app-core","name":"Core"},{"id":"app-old","name":"Old","deletedAt":"2026-10-01T00:00:00Z"}],
		"refs":{"sv-ref":{"kind":"service","applicationId":"app-core"}}}`)
	if _, err := spy.PutWorkspace(r.ctx, "org-1", 0, ws, "ann", *r.now); err != nil {
		t.Fatal(err)
	}
	doc := StateDoc{Topology: model.Topology{Services: []model.Service{{ID: "sv-hint", ApplicationHint: "app-core"}, {ID: "sv-old", ApplicationHint: "app-old"}}}}
	r.hub.C.linkApplications(r.ctx, *r.now, doc)
	want := map[string][]string{"app-core": {"sv-hint", "sv-ref"}}
	if !reflect.DeepEqual(spy.sets, want) {
		t.Fatalf("linked %v, want %v", spy.sets, want)
	}

	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = liveView(*r.now, "n1")
	r.hub.mu.Unlock()
	spy.sets = nil
	r.hub.ScanNow(r.ctx)
	if got := spy.sets["app-core"]; !slices.Contains(got, "sv-ref") {
		t.Fatalf("a scan did not link: %v", spy.sets)
	}
}

// The graph and the API must agree on who is in an application: refs, inline records and accepted hints, and nothing that was
// soft-deleted. An application deleted in the workspace leaves the graph, with its membership closed.
func TestTheGraphHoldsTheMembersTheAPIShows(t *testing.T) {
	a, gs := graphRig(t)
	_, aOrg := a.register(t, "alice", "Alice Lab")
	hub := a.core.ForOrg(aOrg)
	hints := map[string]string{"sv-hint": "app-1", "sv-hint-old": "app-gone"}
	hub.AppHints = func(context.Context) map[string]string { return hints }
	doc := StateDoc{Topology: model.Topology{Services: []model.Service{
		{ID: "sv-ref", Name: "ref"}, {ID: "sv-inline", Name: "inline"}, {ID: "sv-hint", Name: "hint", ApplicationHint: "app-1"}, {ID: "sv-hint-old", Name: "hint-old", ApplicationHint: "app-gone"}}}}
	// every service is in the graph, as a topology poll would have put it there
	tp := doc.Topology
	tp.Clusters = []model.Cluster{{ID: "c-1", Name: "edge", Status: "connected"}}
	for i := range tp.Services {
		tp.Services[i].ClusterID = "c-1"
	}
	b, _, _ := history.Encode(history.Compact(tp))
	if err := gs.AddHistory(a.ctx, aOrg, a.now.Add(-time.Hour), b); err != nil {
		t.Fatal(err)
	}

	open := func(app string) []string {
		t.Helper()
		res, err := gs.DB.C.Run(a.ctx, gs.DB.C.For(aOrg).S(`MATCH (:Entity {org:$org, kind:'application', id:$id})-[r:CONTAINS {org:$org}]->(m:Entity) WHERE r.validTo IS NULL RETURN m.id ORDER BY m.id`, map[string]any{"id": app}))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, row := range res[0].Rows {
			out = append(out, row[0].(string))
		}
		return out
	}
	live := func(app string) []string {
		var out []string
		ws, err := gs.GetWorkspace(a.ctx, aOrg)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range appGroups(ws.Data, doc) {
			if g.ID == app {
				for _, m := range g.Members {
					out = append(out, m.ID)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	save := func(rev int64, data string) {
		t.Helper()
		*a.now = a.now.Add(time.Minute)
		if _, err := hub.SaveWorkspace(a.ctx, "ann", rev, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	save(0, `{"schemaVersion":4,"applications":[{"id":"app-1","name":"One"},{"id":"app-gone","name":"Gone"}],
		"refs":{"sv-ref":{"kind":"service","applicationId":"app-1"}},
		"services":[{"id":"sv-inline","source":"manual","name":"inline","applicationId":"app-1"}]}`)
	if got := open("app-1"); !reflect.DeepEqual(got, []string{"sv-hint", "sv-inline", "sv-ref"}) || !reflect.DeepEqual(got, live("app-1")) {
		t.Fatalf("graph %v, live %v", got, live("app-1"))
	}
	if got := open("app-gone"); !reflect.DeepEqual(got, []string{"sv-hint-old"}) || !reflect.DeepEqual(got, live("app-gone")) {
		t.Fatalf("graph %v, live %v", got, live("app-gone"))
	}

	// Soft-deleting an application removes it, and its members, from both.
	save(1, `{"schemaVersion":4,"applications":[{"id":"app-1","name":"One"},{"id":"app-gone","name":"Gone","deletedAt":"2026-10-01T00:00:00Z"}],
		"refs":{"sv-ref":{"kind":"service","applicationId":"app-1"}},
		"services":[{"id":"sv-inline","source":"manual","name":"inline","applicationId":"app-1"}]}`)
	if got := open("app-gone"); len(got) != 0 || len(live("app-gone")) != 0 {
		t.Fatalf("a deleted application still has members: graph %v, live %v", got, live("app-gone"))
	}
	tl, err := gs.Timeline(a.ctx, aOrg, "application", "app-gone", 10)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, ev := range tl.Events {
		removed = removed || ev.Kind == "application-removed"
	}
	if !removed {
		t.Fatalf("no application-removed event: %+v", tl.Events)
	}
	if got := open("app-1"); !reflect.DeepEqual(got, live("app-1")) {
		t.Fatalf("graph %v, live %v", got, live("app-1"))
	}
}
