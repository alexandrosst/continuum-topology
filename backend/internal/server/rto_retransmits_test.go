package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/model"
)

// TestFlowTableCarriesRtoRetransmits mirrors TestFlowTableCarriesFailedAttempts/MeshBypassSyns: cumulative
// sums across batches, window holds only the latest batch's own contribution.
func TestFlowTableCarriesRtoRetransmits(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", Retransmits: 5, RtoRetransmits: 2}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.RtoRetransmits != 2 || e.WindowRtoRetransmits != 2 {
		t.Errorf("rtoRetransmits=%d/%d, want 2/2", e.RtoRetransmits, e.WindowRtoRetransmits)
	}
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", Retransmits: 1, RtoRetransmits: 1}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.RtoRetransmits != 3 || e.WindowRtoRetransmits != 1 {
		t.Errorf("cumulative rtoRetransmits=%d window=%d, want 3/1", e.RtoRetransmits, e.WindowRtoRetransmits)
	}
}

// TestDependencyStatsIncludeRtoRetransmits mirrors TestDependencyStatsIncludeRetransmitsAndRTT
// (observed_test.go): the cumulative count surfaces on Dependency.RtoRetransmits, and the per-minute rate
// on Dependency.Stats.RtoRetransmitsPerMin, independently of (and alongside) the plain Retransmits figure.
func TestDependencyStatsIncludeRtoRetransmits(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", Retransmits: 10, RtoRetransmits: 4}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 5432)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.Retransmits != 10 {
		t.Errorf("retransmits = %d, want 10", d.Retransmits)
	}
	if d.RtoRetransmits != 4 {
		t.Errorf("rtoRetransmits = %d, want 4", d.RtoRetransmits)
	}
	if d.Stats == nil || d.Stats.RtoRetransmitsPerMin != 4 {
		t.Errorf("rtoRetransmitsPerMin = %v, want 4 (4 over a 60s window)", d.Stats)
	}
}

func TestApplyMeshBypassFactsIsUnaffectedByRtoRetransmits(t *testing.T) {
	// Sanity check that the two signals threaded through the same functions in the same commits stay
	// independent: a dependency with RTO retransmits but no mesh-bypass evidence is never flagged.
	deps := []model.Dependency{{From: "s1", FromKind: "service", Port: 443, RtoRetransmits: 100}}
	services := []model.Service{{ID: "s1"}}
	applyMeshBypassFacts(deps, services)
	if deps[0].MeshBypass {
		t.Error("RtoRetransmits must never influence MeshBypass")
	}
}
