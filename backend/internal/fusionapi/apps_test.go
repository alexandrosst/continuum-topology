package fusionapi

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func testGroups() []AppGroup {
	return []AppGroup{
		{ID: "a1", Name: "Shop", Members: []AppMember{
			{Name: "cart", Namespace: "shop", Cluster: "c1", Aliases: []string{"cart-app"}},
			{Name: "web", Namespace: "shop", Cluster: "c2"},
		}},
		{ID: "a2", Name: "shop"},
		{ID: "a3", Name: "Pay", Members: []AppMember{{Name: "pay", Namespace: "pay"}, {Name: "ledger"}}},
	}
}

func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestFindGroupByIDThenNameAndRefusesAmbiguity(t *testing.T) {
	gs := testGroups()
	if g, err := FindGroup(gs, "a1"); err != nil || g.Name != "Shop" {
		t.Fatalf("by id: %v %v", g, err)
	}
	if g, err := FindGroup(gs, "PAY"); err != nil || g.ID != "a3" {
		t.Fatalf("by name, any case: %v %v", g, err)
	}
	if _, err := FindGroup(gs, "SHOP"); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("two applications share the name: %v", err)
	}
	if _, err := FindGroup(gs, "nope"); statusOf(err) != http.StatusNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := FindGroup(gs, " "); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("empty: %v", err)
	}
}

func TestFocusOnIntersectsTheCallersOwnLimitsAndLeavesThemAlone(t *testing.T) {
	g := &testGroups()[0]
	got, err := AllSignals().FocusOn(g)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.FocusServices, []string{"cart", "cart-app", "web"}) ||
		!reflect.DeepEqual(got.FocusNamespaces, []string{"shop"}) || !reflect.DeepEqual(got.FocusClusters, []string{"c1", "c2"}) {
		t.Fatalf("focus = %+v", got)
	}
	// A choice is not a right: the caller is as unrestricted as before, so it still sees what an unrestricted caller sees.
	if !got.Unrestricted() || len(got.Namespaces) != 0 || len(got.Clusters) != 0 {
		t.Fatalf("a focus must not become a limit: %+v", got)
	}
	// a token limited to one cluster keeps that limit, and the focus lies inside it
	lim := Scope{Signals: Signals, Clusters: []string{"c2", "c9"}}
	got, err = lim.FocusOn(g)
	if err != nil || !reflect.DeepEqual(got.FocusClusters, []string{"c2"}) || !reflect.DeepEqual(got.Clusters, []string{"c2", "c9"}) {
		t.Fatalf("limited: %+v %v", got, err)
	}
	if got.Unrestricted() {
		t.Fatal("a limited caller stays limited")
	}
	// a token that sees none of it gets "not found", as for an application that does not exist
	if _, err = (Scope{Signals: Signals, Namespaces: []string{"other"}}).FocusOn(g); statusOf(err) != http.StatusNotFound {
		t.Fatalf("outside the scope: %v", err)
	}
}

func TestVisibleGroupsShowALimitedCallerOnlyWhatItCanSee(t *testing.T) {
	gs := testGroups()
	all := VisibleGroups(gs, AllSignals())
	if len(all) != len(gs) {
		t.Fatalf("an unrestricted caller sees every application, even one with no services: %d", len(all))
	}
	pay := VisibleGroups(gs, Scope{Signals: Signals, Namespaces: []string{"pay"}})
	if len(pay) != 1 || pay[0].ID != "a3" || len(pay[0].Members) != 1 || pay[0].Members[0].Name != "pay" {
		t.Fatalf("only the part of Pay in the caller's namespaces: %+v", pay)
	}
	if got := VisibleGroups(gs, Scope{Signals: Signals, Namespaces: []string{"nothing"}}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// the lookup of what is not visible fails like a lookup of what does not exist, and the empty and the duplicate are not told apart
	for _, ref := range []string{"a2", "SHOP", "Shop", "nope"} {
		if _, err := FindGroup(pay, ref); statusOf(err) != http.StatusNotFound {
			t.Errorf("%s: %v", ref, err)
		}
	}
}

func TestDescribeGroupsMarksWhatHasTelemetry(t *testing.T) {
	v := DescribeGroups(testGroups()[:1], []Application{{"cart-app", []string{"logs"}}, {"cart", []string{"metrics"}}, {"other", []string{"traces"}}})
	if len(v) != 1 || !reflect.DeepEqual(v[0].Signals, []string{"metrics", "logs"}) {
		t.Fatalf("%+v", v)
	}
	if !reflect.DeepEqual(v[0].Services[0].Signals, []string{"metrics", "logs"}) || len(v[0].Services[1].Signals) != 0 || v[0].Services[1].Signals == nil {
		t.Fatalf("a service with none has [] not null: %+v", v[0].Services)
	}
}

func TestFocusOnLeavesADimensionAloneWhenAMemberLacksIt(t *testing.T) {
	g := &testGroups()[2]
	got, err := AllSignals().FocusOn(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FocusNamespaces) != 0 || len(got.FocusClusters) != 0 {
		t.Fatalf("member without namespace/cluster must not confine it: %+v", got)
	}
	if _, err := AllSignals().FocusOn(&testGroups()[1]); statusOf(err) != http.StatusNotFound {
		t.Fatalf("an application with no services: %v", err)
	}
	big := &AppGroup{Name: "big"}
	for i := 0; i < maxAppServices+1; i++ {
		big.Members = append(big.Members, AppMember{Name: "s" + strings.Repeat("x", 3) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))})
	}
	if _, err := AllSignals().FocusOn(big); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("too many services: %v", err)
	}
}

func TestServicesFocusReachesEveryQuery(t *testing.T) {
	s := Scope{Signals: Signals, FocusServices: []string{"cart", "web"}}
	m, err := MetricFilter{}.matchers(s)
	if err != nil || !strings.Contains(strings.Join(m, ","), `service_name=~"cart|web"`) {
		t.Fatalf("metrics: %v %v", m, err)
	}
	sel, err := LogFilter{}.selector(s)
	if err != nil || !strings.Contains(sel, `service_name=~"cart|web"`) {
		t.Fatalf("logs: %q %v", sel, err)
	}
	q, err := TraceFilter{}.traceQL(s)
	if err != nil || !strings.Contains(q, `resource.service.name`) || !strings.Contains(q, "cart") {
		t.Fatalf("traces: %q %v", q, err)
	}
}
