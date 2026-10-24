package twin

import (
	"fmt"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/facts"
	"ikhnos/internal/store"
)

func state(nodes, namespaces, workloads []string) *facts.State {
	st := facts.New()
	for _, k := range nodes {
		st.Nodes[k] = &ikhnosv1.NodeFacts{Key: k, Name: k}
	}
	for _, k := range namespaces {
		st.Namespaces[k] = &ikhnosv1.NamespaceFacts{Key: k, Name: k}
	}
	for _, k := range workloads {
		st.Workloads[k] = &ikhnosv1.WorkloadFacts{Key: k}
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
	full := &ikhnosv1.Sync{Full: true,
		Nodes:      []*ikhnosv1.NodeFacts{{Key: "n1"}},
		Namespaces: []*ikhnosv1.NamespaceFacts{{Key: "shop"}},
		Workloads:  []*ikhnosv1.WorkloadFacts{{Key: "shop/Deployment/cart"}}}
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
	d := &ikhnosv1.Sync{DeletedWorkloads: []string{"a/Deployment/x", "a/Deployment/never-held"}, DeletedNodes: []string{"ghost"},
		// deleted and re-sent in one message: it is back, not gone
		Nodes: []*ikhnosv1.NodeFacts{{Key: "n1"}}}
	d.DeletedNodes = append(d.DeletedNodes, "n1")
	got := Disappeared(st, d, 2)
	if fmt.Sprint(keys(got)) != "[service:a/Deployment/x]" {
		t.Errorf("got %v", keys(got))
	}
	if got[0].Reason != "deleted in the cluster" {
		t.Errorf("reason %q", got[0].Reason)
	}
	// A delta that is not a full picture never implies absence.
	if len(Disappeared(st, &ikhnosv1.Sync{Nodes: []*ikhnosv1.NodeFacts{{Key: "n1"}}}, 2)) != 0 {
		t.Error("a delta must not tombstone what it does not mention")
	}
}

func TestDisappearedBecauseOfLoweredConsentSaysSo(t *testing.T) {
	st := state([]string{"n1"}, []string{"shop"}, []string{"shop/Deployment/cart"})
	full := &ikhnosv1.Sync{Full: true}
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

// A write that failed is tried again against what is held by the next flush, not what was held when it began: a record that came
// back in between must not have its tombstone put back, and a key recorded again must not be deleted.
func TestRequeueDoesNotUndoWhatHappenedSinceTheWriteBegan(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	tb := NewTombstones(time.Hour)
	gone := store.Tombstone{Kind: "service", ID: "cart", GoneAt: now}
	other := store.Tombstone{Kind: "service", ID: "pay", GoneAt: now}
	tb.Add(gone, other)
	put, _ := tb.Dirty() // the write that is about to fail
	if len(put) != 2 {
		t.Fatalf("put %v", put)
	}
	tb.Revive("service", "cart") // the record is back before the retry
	tb.Requeue(put, nil)
	put, del := tb.Dirty()
	if len(put) != 1 || put[0].ID != "pay" {
		t.Errorf("only the tombstone that is still held is written again: %v", put)
	}
	if len(del) != 1 || del[0].ID != "cart" {
		t.Errorf("the revived one is deleted, and not put back: %v", del)
	}
	if tb.Has("service", "cart") {
		t.Error("the revived record's tombstone came back")
	}

	// A delete that failed is tried again, unless the key has been recorded again since.
	tb2 := NewTombstones(time.Hour)
	tb2.Add(gone)
	tb2.Dirty()
	tb2.Sweep(now.Add(2 * time.Hour))
	_, del = tb2.Dirty()
	if len(del) != 1 {
		t.Fatalf("del %v", del)
	}
	tb2.Add(store.Tombstone{Kind: "service", ID: "cart", GoneAt: now.Add(3 * time.Hour)}) // gone again, a new tombstone
	tb2.Dirty()
	tb2.Requeue(nil, del)
	if _, del := tb2.Dirty(); len(del) != 0 {
		t.Errorf("a key that was recorded again is deleted: %v", del)
	}
	if !tb2.Has("service", "cart") {
		t.Error("the new tombstone was lost")
	}
}
