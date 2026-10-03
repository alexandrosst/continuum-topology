package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// TestFlowTableCarriesMssBytes mirrors TestFlowTableCarriesRtoRetransmits, but for a gauge instead of a
// sum: the latest non-zero sample wins on FlowEdge.Key.MssBytes, not a running total - the same treatment
// RttUs/Cwnd/PacingBps already get.
func TestFlowTableCarriesMssBytes(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", MssBytes: 1460}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.Key.MssBytes != 1460 {
		t.Errorf("mssBytes = %d, want 1460", e.Key.MssBytes)
	}
	// A later report with a smaller MSS (e.g. the path started crossing an overlay tunnel) replaces the
	// gauge - it is not summed, and a zero sample must never clobber a real one.
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", MssBytes: 1400}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.Key.MssBytes != 1400 {
		t.Errorf("mssBytes after second report = %d, want 1400", e.Key.MssBytes)
	}
	f3 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", MssBytes: 0}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f3}}, now.Add(2*time.Minute))
	e = tbl.edges[k]
	if e.Key.MssBytes != 1400 {
		t.Errorf("mssBytes after zero-sample report = %d, want unchanged 1400", e.Key.MssBytes)
	}
}

// TestDependencyStatsIncludeMssBytes mirrors TestDependencyStatsIncludeRtoRetransmits: the latest sample
// surfaces on Dependency.MssBytes, a gauge like RttMs/CwndSegments, not a running total.
func TestDependencyStatsIncludeMssBytes(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", MssBytes: 1448}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 5432)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.MssBytes != 1448 {
		t.Errorf("mssBytes = %d, want 1448", d.MssBytes)
	}
}
