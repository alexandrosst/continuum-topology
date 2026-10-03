package server

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// u32p is a small helper so test literals can set continuumv1.Flow's optional uint32 fields (pointers)
// without a separate local variable at each call site.
func u32p(v uint32) *uint32 { return &v }

// TestFlowTableCarriesRcvWndBytes is the mirror image of TestFlowTableCarriesMssBytes: for RcvWndBytes
// (and the other three window/buffer gauges - RcvWndBytes/SndWndBytes/WmemQueuedBytes/SndbufBytes - see
// their own doc comments on continuumv1.Flow/model.Dependency) 0 is a real, meaningful sample - the
// zero-window stall this field exists to surface - not a stand-in for "unset" the way it is for MssBytes.
// So unlike MssBytes, where "a zero sample must never clobber a real one" is the correct rule, here the
// correct rule is the opposite: a genuine zero sample MUST clobber a real nonzero one, and only a report
// that truly carries no sample at all (the pointer itself nil, not the value 0) may leave the gauge alone.
func TestFlowTableCarriesRcvWndBytes(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")

	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", RcvWndBytes: u32p(65535)}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.Key.RcvWndBytes == nil || *e.Key.RcvWndBytes != 65535 {
		t.Fatalf("rcvWndBytes = %v, want 65535", e.Key.RcvWndBytes)
	}

	// The receive window genuinely drops to 0 - a real stall, not an absent sample. This MUST overwrite
	// the previous 65535 reading: that is the entire point of this field existing. Before this fix, the
	// guard pattern treated a zero sample as if it were "no sample" and silently kept the stale 65535.
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", RcvWndBytes: u32p(0)}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.Key.RcvWndBytes == nil {
		t.Fatal("rcvWndBytes after zero-window report = nil, want a real sample of 0")
	}
	if *e.Key.RcvWndBytes != 0 {
		t.Errorf("rcvWndBytes after zero-window report = %d, want 0 (the stall must be visible, not clobbered back to the stale 65535)", *e.Key.RcvWndBytes)
	}

	// A later report that genuinely carries no window sample at all (the pointer itself nil, e.g. no
	// eBPF snapshot ran this window) must leave the last real sample - the 0 above - exactly as it was:
	// absence must never read as, or overwrite into, a measured value in either direction.
	f3 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf"}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f3}}, now.Add(2*time.Minute))
	e = tbl.edges[k]
	if e.Key.RcvWndBytes == nil || *e.Key.RcvWndBytes != 0 {
		t.Errorf("rcvWndBytes after a report with no sample = %v, want unchanged 0", e.Key.RcvWndBytes)
	}
}

// TestDependencyStatsIncludeRcvWndBytes mirrors TestDependencyStatsIncludeMssBytes: the latest sample
// surfaces on Dependency.RcvWndBytes, including a real zero-window stall, which must come through as a
// non-nil pointer to 0 - not as nil (which would read as "never sampled") and not as an omitted field.
func TestDependencyStatsIncludeRcvWndBytes(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf", RcvWndBytes: u32p(0)}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 5432)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.RcvWndBytes == nil {
		t.Fatal("rcvWndBytes = nil, want a real sampled 0 (a stalled receive window)")
	}
	if *d.RcvWndBytes != 0 {
		t.Errorf("rcvWndBytes = %d, want 0", *d.RcvWndBytes)
	}
}

// TestDependencyStatsOmitRcvWndBytesWhenNeverSampled guards the other side of the same fix: a dependency
// that has never carried a single real window sample (conntrack-only, or no eBPF snapshot has run yet)
// must report nil, never a fabricated 0 - the same "absence must never read as a measured zero" rule this
// codebase already applies to RAPL energy/OOM-kill count/loss percent.
func TestDependencyStatsOmitRcvWndBytesWhenNeverSampled(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	x, y := "a/Deployment/x", "a/Deployment/y"
	c := cluster("a", "", nil, wk("a", "Deployment", "x"), wk("a", "Deployment", "y"))
	f := &continuumv1.Flow{Src: wep(x), Dst: wep(y), Port: 5432, Protocol: "tcp", Connections: 1, Method: "ebpf"}
	feed(&c, now, 60, f)

	deps, _ := observedTopology("org", []observedCluster{c}, now, 24*time.Hour)
	d := findDep(deps, 5432)
	if d == nil {
		t.Fatal("dependency not found")
	}
	if d.RcvWndBytes != nil {
		t.Errorf("rcvWndBytes = %v, want nil (never sampled, must not be fabricated as 0)", *d.RcvWndBytes)
	}
}

// TestFlowTableCarriesWmemQueuedBytes is the WmemQueuedBytes/SndbufBytes analogue of
// TestFlowTableCarriesRcvWndBytes above: 0 is their common, healthy reading (nothing queued), so a real
// zero-byte sample must overwrite a prior nonzero backlog reading rather than being silently skipped.
func TestFlowTableCarriesWmemQueuedBytes(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tbl := newFlowTable()
	src, dst := wep("a/Deployment/x"), wep("a/Deployment/y")

	f1 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", WmemQueuedBytes: u32p(4096), SndbufBytes: u32p(4096)}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f1}}, now)
	k := flowKey(f1)
	e := tbl.edges[k]
	if e == nil {
		t.Fatal("edge not recorded")
	}
	if e.Key.WmemQueuedBytes == nil || *e.Key.WmemQueuedBytes != 4096 {
		t.Fatalf("wmemQueuedBytes = %v, want 4096", e.Key.WmemQueuedBytes)
	}

	// The write queue drains to 0 - a real, common, healthy sample, which must replace the stale backlog
	// reading rather than being skipped because it looks like "no sample".
	f2 := &continuumv1.Flow{Src: src, Dst: dst, Port: 5432, Protocol: "tcp", Method: "ebpf", WmemQueuedBytes: u32p(0)}
	tbl.apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f2}}, now.Add(time.Minute))
	e = tbl.edges[k]
	if e.Key.WmemQueuedBytes == nil || *e.Key.WmemQueuedBytes != 0 {
		t.Errorf("wmemQueuedBytes after a drained-queue report = %v, want a real sample of 0", e.Key.WmemQueuedBytes)
	}
	// SndbufBytes was not resampled on f2 (pointer nil): it must stay at the earlier real 4096 reading.
	if e.Key.SndbufBytes == nil || *e.Key.SndbufBytes != 4096 {
		t.Errorf("sndbufBytes after a report with no sndbuf sample = %v, want unchanged 4096", e.Key.SndbufBytes)
	}
}
