package estate

import (
	"reflect"
	"slices"
	"testing"

	"continuum/internal/model"
)

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
	r.hub.Mu.Lock()
	r.hub.Views[id] = liveView(*r.now, "n1")
	r.hub.Mu.Unlock()
	spy.sets = nil
	r.hub.ScanNow(r.ctx)
	if got := spy.sets["app-core"]; !slices.Contains(got, "sv-ref") {
		t.Fatalf("a scan did not link: %v", spy.sets)
	}
}
