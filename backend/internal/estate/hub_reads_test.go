package estate

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// What the shell asks of the Hub is a method each, not the Hub's lock and maps: these hold what each one answers.

func TestConnectedCountsTheLiveStreams(t *testing.T) {
	r := newHubRig(t)
	if n := r.hub.Connected(); n != 0 {
		t.Fatalf("%d streams before any agent connected", n)
	}
	f := r.newFakeAgent(t, fakeAgentCtx(t))
	if n := r.hub.Connected(); n != 1 {
		t.Fatalf("%d streams with one agent connected, want 1", n)
	}
	f.drop()
	deadline := time.Now().Add(5 * time.Second)
	for r.hub.Connected() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a stream the agent dropped is still counted as connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExportHealthOfSaysWhetherTheAgentIsConnectedAndWhatItReports(t *testing.T) {
	r := newHubRig(t)
	h := r.hub
	set := func(id string, v *View) {
		h.Mu.Lock()
		h.Views[id] = v
		h.Mu.Unlock()
	}
	if connected, doc := h.ExportHealthOf("ag-unknown"); connected || doc != nil {
		t.Fatalf("an agent the hub has no view of: connected %v, health %+v", connected, doc)
	}
	bare := NewView()
	bare.Connected = true
	set("ag-bare", bare)
	if connected, doc := h.ExportHealthOf("ag-bare"); !connected || doc != nil {
		t.Fatalf("a connected agent that sent no diagnostics: connected %v, health %+v", connected, doc)
	}
	v := NewView()
	v.NoteDiagnostics(&continuumv1.Diagnostics{AgentVersion: "1", ExportHealth: &continuumv1.ExportHealth{PodsReached: 3, PodsFailed: 1}}, false, time.Now())
	v.Connected = true
	set("ag-1", v)
	connected, doc := h.ExportHealthOf("ag-1")
	if !connected || doc == nil || doc.PodsReached != 3 || doc.PodsFailed != 1 {
		t.Fatalf("a connected agent that reports its exporters: connected %v, health %+v", connected, doc)
	}
	h.Mu.Lock()
	v.Connected = false
	h.Mu.Unlock()
	if connected, doc = h.ExportHealthOf("ag-1"); connected || doc == nil {
		t.Fatalf("the same agent after its stream ended: connected %v, health %+v (what it last reported stays)", connected, doc)
	}
}
