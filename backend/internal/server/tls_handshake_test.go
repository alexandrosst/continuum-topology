package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// TestFlowTableCarriesTlsHandshake mirrors TestFlowTableCarriesMssBytes: FlowEdge.Key.TlsHandshake is a
// gauge, not a running total - the latest non-UNKNOWN sample wins, and a later UNKNOWN report (no
// ClientHello seen that window) must never clobber a real decided outcome already recorded.
func TestFlowTableCarriesTlsHandshake(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")
	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Method: "ebpf", TlsHandshake: continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.Key.TlsHandshake != continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK {
		t.Errorf("tlsHandshake = %v, want OK", e.Key.TlsHandshake)
	}
	// A later report whose own connection's handshake instead failed replaces the gauge.
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Method: "ebpf", TlsHandshake: continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.Key.TlsHandshake != continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED {
		t.Errorf("tlsHandshake after second report = %v, want FAILED", e.Key.TlsHandshake)
	}
	// A report with no ClientHello seen this window (UNKNOWN, the zero value) must not erase the last
	// decided outcome - exactly the same "0 means no sample" rule MssBytes/RttUs already follow.
	f3 := &continuumv1.Flow{Src: src, Dst: dst, Port: 443, Protocol: "tcp", Method: "ebpf"}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f3}}, now.Add(2*time.Minute))
	e = tbl.edges[k]
	if e.Key.TlsHandshake != continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED {
		t.Errorf("tlsHandshake after UNKNOWN-sample report = %v, want unchanged FAILED", e.Key.TlsHandshake)
	}
}

// TestDependencyStatsIncludeTlsHandshake mirrors TestDependencyStatsIncludeMssBytes: the proto enum maps
// onto model.Dependency.TlsHandshake's plain "ok"/"failed" string, the one place that mapping happens.
func TestDependencyStatsIncludeTlsHandshake(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf", TlsHandshake: continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 443)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.TlsHandshake != "failed" {
		t.Errorf("tlsHandshake = %q, want %q", d.TlsHandshake, "failed")
	}
}

// TestDependencyStatsLeaveTlsHandshakeUnsetWhenUnknown confirms the UNKNOWN zero value (never observed, or
// name-capture off) does not fabricate a "" string distinct from "ok"/"failed" being mistaken for a real
// reading - it simply stays the Go zero value, which json:",omitempty" then drops entirely.
func TestDependencyStatsLeaveTlsHandshakeUnsetWhenUnknown(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 443, Protocol: "tcp", Connections: 1, Method: "ebpf"}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 443)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.TlsHandshake != "" {
		t.Errorf("tlsHandshake = %q, want empty (unknown)", d.TlsHandshake)
	}
}
