package twin

import (
	"fmt"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/store"
)

func state(nodes, namespaces, workloads []string) *facts.State {
	st := facts.New()
	for _, k := range nodes {
		st.Nodes[k] = &continuumv1.NodeFacts{Key: k, Name: k}
	}
	for _, k := range namespaces {
		st.Namespaces[k] = &continuumv1.NamespaceFacts{Key: k, Name: k}
	}
	for _, k := range workloads {
		st.Workloads[k] = &continuumv1.WorkloadFacts{Key: k}
	}
	return st
}

func keys(v []Vanished) []string {
	var out []string
	for _, x := range v {
		out = append(out, x.Kind+":"+x.Key)
	}
	return out
}

func TestDisappearedFullPictureRemovesEverythingItLeavesOut(t *testing.T) {
	st := state([]string{"n1", "n2"}, []string{"shop"}, []string{"shop/Deployment/cart", "shop/Deployment/pay"})
	full := &continuumv1.Sync{Full: true,
		Nodes:      []*continuumv1.NodeFacts{{Key: "n1"}},
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "shop"}},
		Workloads:  []*continuumv1.WorkloadFacts{{Key: "shop/Deployment/cart"}}}
	got := Disappeared(st, full, 2)
	want := []string{"node:n2", "service:shop/Deployment/pay"}
	if fmt.Sprint(keys(got)) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", keys(got), want)
	}
	if got[0].Reason == "" || got[1].Reason == "" {
		t.Errorf("every disappearance says why: %+v", got)
	}
	// Nothing is decided by looking: the state is unchanged.
	if len(st.Nodes) != 2 {
		t.Error("Disappeared must not change the state")
	}
}

func TestDisappearedDeltaOnlyDeletesWhatIsHeld(t *testing.T) {
	st := state([]string{"n1"}, nil, []string{"a/Deployment/x"})
	d := &continuumv1.Sync{DeletedWorkloads: []string{"a/Deployment/x", "a/Deployment/never-held"}, DeletedNodes: []string{"ghost"},
		// deleted and re-sent in one message: it is back, not gone
		Nodes: []*continuumv1.NodeFacts{{Key: "n1"}}}
	d.DeletedNodes = append(d.DeletedNodes, "n1")
	got := Disappeared(st, d, 2)
	if fmt.Sprint(keys(got)) != "[service:a/Deployment/x]" {
		t.Errorf("got %v", keys(got))
	}
	if got[0].Reason != "deleted in the cluster" {
		t.Errorf("reason %q", got[0].Reason)
	}
	// A delta that is not a full picture never implies absence.
	if len(Disappeared(st, &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{{Key: "n1"}}}, 2)) != 0 {
		t.Error("a delta must not tombstone what it does not mention")
	}
}

func TestDisappearedBecauseOfLoweredConsentSaysSo(t *testing.T) {
	st := state([]string{"n1"}, []string{"shop"}, []string{"shop/Deployment/cart"})
	full := &continuumv1.Sync{Full: true}
	got := Disappeared(st, full, 1) // infrastructure only: namespaces and workloads are no longer reported
	for _, v := range got {
		switch v.Kind {
		case KindNode:
			if v.Reason != "no longer reported by its agent (deleted, or outside what the agent is told to look at)" {
				t.Errorf("node %q", v.Reason)
			}
		default:
			if v.Reason != "access tier lowered: "+map[string]string{KindNamespace: "namespaces", KindService: "workloads"}[v.Kind]+" are no longer reported" {
				t.Errorf("%s %q", v.Kind, v.Reason)
			}
		}
	}
	for _, v := range Disappeared(st, full, 0) {
		if v.Kind == KindNode && v.Reason != "access tier lowered: nodes are no longer reported" {
			t.Errorf("%+v", v)
		}
	}
}

func TestTombstonesRetentionRevivalAndPersistence(t *testing.T) {
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	ts := NewTombstones(7 * 24 * time.Hour)
	ts.Add(
		store.Tombstone{Kind: "service", ID: "sv-old", Name: "old", GoneAt: now.Add(-8 * 24 * time.Hour)},
		store.Tombstone{Kind: "service", ID: "sv-new", Name: "new", GoneAt: now.Add(-time.Hour)},
		store.Tombstone{Kind: "node", ID: "nd-1", Name: "n1", GoneAt: now.Add(-24 * time.Hour)},
	)
	// Past its retention it is not shown, whatever the sweep has done yet.
	list := ts.List(now)
	if len(list) != 2 || list[0].ID != "sv-new" || list[1].ID != "nd-1" {
		t.Errorf("newest first, retention respected: %+v", list)
	}
	put, del := ts.Dirty()
	if len(put) != 3 || len(del) != 0 {
		t.Errorf("%d %d", len(put), len(del))
	}
	if p, d := ts.Dirty(); len(p)+len(d) != 0 {
		t.Error("Dirty resets")
	}
	if expired := ts.Sweep(now); len(expired) != 1 || expired[0].ID != "sv-old" {
		t.Errorf("sweep: %+v", expired)
	}
	if _, del = ts.Dirty(); len(del) != 1 || del[0].ID != "sv-old" {
		t.Errorf("a swept tombstone must be deleted from the store: %+v", del)
	}
	// A record that comes back is no longer gone.
	if !ts.Revive("service", "sv-new") || ts.Has("service", "sv-new") || ts.Revive("service", "sv-new") {
		t.Error("revive")
	}
	if _, del = ts.Dirty(); len(del) != 1 || del[0].ID != "sv-new" {
		t.Errorf("%+v", del)
	}
	// Loading what was stored marks nothing for saving.
	ts2 := NewTombstones(0)
	ts2.Load([]store.Tombstone{{Kind: "node", ID: "nd-1", GoneAt: now}})
	if p, d := ts2.Dirty(); len(p)+len(d) != 0 || !ts2.Has("node", "nd-1") || ts2.Retention() != DefaultRetention {
		t.Errorf("%v %v", p, d)
	}
}

func TestTombstonesAreBounded(t *testing.T) {
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	ts := NewTombstones(0)
	ts.max = 3
	for i := 0; i < 6; i++ {
		ts.Add(store.Tombstone{Kind: "service", ID: fmt.Sprintf("sv-%d", i), GoneAt: now.Add(time.Duration(i) * time.Minute)})
	}
	list := ts.List(now.Add(time.Hour))
	if len(list) != 3 || list[0].ID != "sv-5" || list[2].ID != "sv-3" {
		t.Errorf("the oldest go first: %+v", list)
	}
}

func TestJobsAreEphemeral(t *testing.T) {
	if !Ephemeral("Job") || Ephemeral("Deployment") {
		t.Error("Ephemeral")
	}
}
