package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"continuum/internal/estate"
	"continuum/internal/kernel"
	"continuum/internal/store"
	"continuum/internal/testkit"
)

func TestSettingsNormalizeTheirLinks(t *testing.T) {
	s := kernel.DefaultSettings()
	s.Links = []kernel.LinkSetting{{A: "cl-b", B: "cl-a"}}
	got, err := s.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Links) != 1 || got.Links[0].A != "cl-a" {
		t.Fatalf("links = %v", got.Links)
	}
	s.Links = []kernel.LinkSetting{{A: "x", B: "x"}}
	if _, err := s.Normalize(); err == nil {
		t.Fatal("a self-link was accepted")
	}
}

func linkHub(views map[string]*estate.View) *estate.Hub { return &estate.Hub{Views: views} }

func TestEachPairKeepsItsOwnChoicesAndTheLastEntryOfAPairStands(t *testing.T) {
	got, err := kernel.NormalizeLinks([]kernel.LinkSetting{
		{A: "cl-b", B: "cl-a"},
		{A: "cl-a", B: "cl-b", NoProbe: true, Throughput: &kernel.LinkThroughput{IntervalMinutes: 60, BudgetMB: 2048}},
		{A: "cl-a", B: "cl-c", Throughput: &kernel.LinkThroughput{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].NoProbe || got[0].Throughput == nil || got[0].Throughput.IntervalMinutes != 60 || got[0].Throughput.BudgetMB != 2048 {
		t.Fatalf("the later entry of the pair did not win: %+v", got)
	}
	if got[1].NoProbe || got[1].Throughput == nil {
		t.Fatalf("%+v", got[1])
	}
	// What was returned does not alias the caller's value.
	in := &kernel.LinkThroughput{BudgetMB: 1}
	out, _ := kernel.NormalizeLinks([]kernel.LinkSetting{{A: "a", B: "b", Throughput: in}})
	in.BudgetMB = 99
	if out[0].Throughput.BudgetMB != 1 {
		t.Error("the stored setting shares memory with the request")
	}
	for name, th := range map[string]*kernel.LinkThroughput{
		"interval too short": {IntervalMinutes: 5},
		"interval too long":  {IntervalMinutes: 7*24*60 + 1},
		"budget too big":     {BudgetMB: 1<<20 + 1},
	} {
		if _, err := kernel.NormalizeLinks([]kernel.LinkSetting{{A: "a", B: "b", Throughput: th}}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPairChoicesAreSavedWithTheSettingsUnderTheNamesTheUIUses(t *testing.T) {
	var s kernel.Settings
	in := `{"links":[{"a":"cl-b","b":"cl-a","noProbe":true,"via":"mesh","throughput":{"intervalMinutes":120,"budgetMB":4096}},{"a":"cl-a","b":"cl-c"}]}`
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	links, err := kernel.NormalizeLinks(s.Links)
	if err != nil {
		t.Fatal(err)
	}
	s.Links = links
	out, _ := json.Marshal(s.Links)
	if string(out) != `[{"a":"cl-a","b":"cl-b","noProbe":true,"via":"mesh","throughput":{"intervalMinutes":120,"budgetMB":4096}},{"a":"cl-a","b":"cl-c"}]` {
		t.Errorf("%s", out)
	}
}

// A peer that has only mesh or private addresses is reachable only over a network the clusters share: the pair is measured, but
// the Sites screen is told to hint at it, and the hint goes away when the peer has a public address or the pair has figures.
func TestAPeerWithOnlyPrivateAddressesGetsAHint(t *testing.T) {
	a, b := testkit.Agent("ag-a", "cl-a", "alpha", "", 2), testkit.Agent("ag-b", "cl-b", "beta", "", 2)
	links := []kernel.LinkSetting{{A: "cl-a", B: "cl-b"}}
	h := linkHub(map[string]*estate.View{"ag-a": {EstView: estate.EstView{State: testkit.NodeState("10.0.0.1")}}, "ag-b": {EstView: estate.EstView{State: testkit.NodeState("10.1.0.1", "100.64.0.45")}}})
	got := h.LinkNotes(links, []store.Agent{a, b}, nil)
	if len(got) != 2 || got[0].Code != "private-range" || got[0].Cluster != "cl-a" || got[0].Peer != "cl-b" ||
		!strings.Contains(got[0].Text, "mesh (100.64.0.0/10) and private") || !strings.Contains(got[0].Text, "100.64.0.45") {
		t.Fatalf("%+v", got)
	}
	// a public address of one of the peer's nodes is a way across that needs no shared network
	hPub := linkHub(map[string]*estate.View{"ag-a": {EstView: estate.EstView{State: testkit.NodeState("10.0.0.1")}}, "ag-b": {EstView: estate.EstView{State: testkit.NodeState("10.1.0.1", "198.51.100.9")}}})
	for _, n := range hPub.LinkNotes(links, []store.Agent{a, b}, nil) {
		if n.Code == "private-range" && n.Cluster == "cl-a" {
			t.Errorf("a peer with a public node address: %+v", n)
		}
	}
	// the address the peer's agent connects from is its gateway's, not a node's: it does not silence the hint, and the text says so
	pub := testkit.Agent("ag-b", "cl-b", "beta", "203.0.113.2", 2)
	viaGateway := h.LinkNotes(links, []store.Agent{a, pub}, nil)
	if len(viaGateway) == 0 || viaGateway[0].Code != "private-range" || viaGateway[0].Peer != "cl-b" || !strings.Contains(viaGateway[0].Text, "203.0.113.2") || !strings.Contains(viaGateway[0].Text, "is not counted") {
		t.Errorf("a NATed peer lost the hint: %+v", viaGateway)
	}
	// a pair FUSION holds figures for needs no hint
	one := 0.01
	notes := withoutHintsOfMeasuredPairs(got, []LinkPairLatest{{A: "cl-a", B: "cl-b", RTTSeconds: &one}})
	if len(notes) != 0 {
		t.Errorf("a measured pair keeps its hint: %+v", notes)
	}
	if notes := withoutHintsOfMeasuredPairs(got, []LinkPairLatest{{A: "cl-a", B: "cl-b"}}); len(notes) != 2 {
		t.Errorf("a pair with nothing lost its hint: %+v", notes)
	}
	// and a hint never stands for an unmet step of the checklist: nothing in the setup is made to fail by it
	for _, s := range h.LinkSetups(links, []store.Agent{a, b}, nil, true, time.Now()) {
		for _, st := range s.Steps {
			if st.Code == "addresses" && st.State == "todo" {
				t.Errorf("the hint failed the addresses step: %+v", st)
			}
		}
	}
}
