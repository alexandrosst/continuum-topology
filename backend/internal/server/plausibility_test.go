package server

import (
	"math"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/model"
)

func f64(v float64) *float64 { return &v }
func i32(v int32) *int32     { return &v }

// TestBoundPctKeepsPlausibleAndNullsImpossible covers boundPct's own contract: 0 and 100 are both real,
// common readings and must pass through untouched; anything outside 0-100, or NaN, must come back nil
// with dropped=true - never clamped to 0 or 100, which would report a percentage nothing measured.
func TestBoundPctKeepsPlausibleAndNullsImpossible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      *float64
		want    *float64
		dropped bool
	}{
		{"nil stays nil", nil, nil, false},
		{"zero is plausible", f64(0), f64(0), false},
		{"a hundred is plausible", f64(100), f64(100), false},
		{"an ordinary reading", f64(42.5), f64(42.5), false},
		{"just over the ceiling", f64(100.0001), nil, true},
		{"negative", f64(-0.0001), nil, true},
		{"NaN", f64(math.NaN()), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := boundPct(tc.in)
			if dropped != tc.dropped {
				t.Fatalf("dropped = %v, want %v", dropped, tc.dropped)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("got = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBoundNonNegKeepsPlausibleAndNullsNegative covers boundNonNeg's own contract: a power reading has
// a floor (never negative) but no ceiling of its own, unlike a percentage.
func TestBoundNonNegKeepsPlausibleAndNullsNegative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      *float64
		dropped bool
	}{
		{"nil stays nil", nil, false},
		{"zero watts", f64(0), false},
		{"an ordinary reading", f64(12.3), false},
		{"negative", f64(-0.1), true},
		{"NaN", f64(math.NaN()), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := boundNonNeg(tc.in)
			if dropped != tc.dropped {
				t.Fatalf("dropped = %v, want %v", dropped, tc.dropped)
			}
			if dropped && got != nil {
				t.Fatalf("a dropped value must come back nil, got %v", *got)
			}
			if !dropped && tc.in != nil && (got == nil || *got != *tc.in) {
				t.Fatalf("a plausible value must pass through unchanged, got %v", got)
			}
		})
	}
}

// TestBoundCountKeepsPlausibleAndNullsNegative covers boundCount's own contract: a pod count can be
// zero (a real, common reading) but never negative.
func TestBoundCountKeepsPlausibleAndNullsNegative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      *int32
		dropped bool
	}{
		{"nil stays nil", nil, false},
		{"zero pods", i32(0), false},
		{"many pods", i32(40), false},
		{"negative", i32(-1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := boundCount(tc.in)
			if dropped != tc.dropped {
				t.Fatalf("dropped = %v, want %v", dropped, tc.dropped)
			}
			if dropped && got != nil {
				t.Fatalf("a dropped count must come back nil, got %v", *got)
			}
		})
	}
}

// TestBoundSyncFactsOmitsOnlyTheImplausibleFieldAcrossARecord is the real ingestion-time backstop's own
// test: a Sync with several implausible fields, mixed in among several plausible ones on the very same
// node and cluster, must come back with only the implausible fields nulled - every plausible field, and
// every other field entirely (Key, Name, a second node) kept exactly as reported - and the anomaly count
// must match exactly how many fields were actually dropped.
func TestBoundSyncFactsOmitsOnlyTheImplausibleFieldAcrossARecord(t *testing.T) {
	s := &continuumv1.Sync{
		Cluster: &continuumv1.ClusterFacts{Uid: fp, PendingPodCount: i32(-4)}, // implausible
		Nodes: []*continuumv1.NodeFacts{
			{
				Key: "n1", Name: "n1", PodCount: i32(-1), // implausible
				Probe: &continuumv1.HostProbe{
					CpuPressurePct:    f64(150),   // implausible: over 100
					MemoryPressurePct: f64(37.5),  // plausible: just inside the range
					IoPressurePct:     f64(-0.5),  // implausible: negative
					HostWatts:         f64(-12.0), // implausible: negative
				},
			},
			{
				Key: "n2", Name: "n2", PodCount: i32(3), // plausible
				Probe: &continuumv1.HostProbe{CpuPressurePct: f64(0)}, // plausible: zero is a real reading
			},
		},
	}
	if n := boundSyncFacts(s); n != 5 {
		t.Fatalf("dropped %d fields, want 5", n)
	}
	if s.Cluster.PendingPodCount != nil {
		t.Errorf("cluster pending pod count = %v, want nil", s.Cluster.PendingPodCount)
	}
	n1 := s.Nodes[0]
	if n1.Key != "n1" || n1.Name != "n1" {
		t.Errorf("node 1's own identity must survive untouched: %+v", n1)
	}
	if n1.PodCount != nil {
		t.Errorf("node 1 pod count = %v, want nil", n1.PodCount)
	}
	if n1.Probe.CpuPressurePct != nil {
		t.Errorf("node 1 cpu pressure = %v, want nil", n1.Probe.CpuPressurePct)
	}
	if n1.Probe.MemoryPressurePct == nil || *n1.Probe.MemoryPressurePct != 37.5 {
		t.Errorf("node 1 memory pressure = %v, want 37.5 kept untouched", n1.Probe.MemoryPressurePct)
	}
	if n1.Probe.IoPressurePct != nil {
		t.Errorf("node 1 io pressure = %v, want nil", n1.Probe.IoPressurePct)
	}
	if n1.Probe.HostWatts != nil {
		t.Errorf("node 1 host watts = %v, want nil", n1.Probe.HostWatts)
	}
	n2 := s.Nodes[1]
	if n2.PodCount == nil || *n2.PodCount != 3 {
		t.Errorf("node 2 pod count = %v, want 3 kept untouched", n2.PodCount)
	}
	if n2.Probe.CpuPressurePct == nil || *n2.Probe.CpuPressurePct != 0 {
		t.Errorf("node 2 cpu pressure = %v, want 0 kept untouched (a real, measured reading)", n2.Probe.CpuPressurePct)
	}
}

// TestBoundFlowFactsOmitsOnlyTheImplausibleSaturation is boundSyncFacts' flow-side counterpart: one bad
// entry among several good ones on the same collector must leave everything else (Iface, ThroughputBps,
// the other entries) untouched.
func TestBoundFlowFactsOmitsOnlyTheImplausibleSaturation(t *testing.T) {
	good, bad := 55.0, 140.0
	b := &continuumv1.FlowBatch{WindowSeconds: 60, Collectors: []*continuumv1.CollectorInfo{
		{Node: "n1", Method: "ebpf", LinkSaturation: []*continuumv1.LinkSaturation{
			{Iface: "eth0", ThroughputBps: 1000, SaturationPct: &good},
			{Iface: "wlan0", ThroughputBps: 2000, SaturationPct: &bad},
			{Iface: "veth1", ThroughputBps: 500}, // unset: no rated speed read, must be left alone
		}},
	}}
	if n := boundFlowFacts(b); n != 1 {
		t.Fatalf("dropped %d entries, want 1", n)
	}
	ls := b.Collectors[0].LinkSaturation
	if ls[0].SaturationPct == nil || *ls[0].SaturationPct != 55.0 {
		t.Errorf("eth0's plausible saturation = %v, want 55 kept untouched", ls[0].SaturationPct)
	}
	if ls[0].Iface != "eth0" || ls[0].ThroughputBps != 1000 {
		t.Errorf("eth0's other fields must survive untouched: %+v", ls[0])
	}
	if ls[1].SaturationPct != nil {
		t.Errorf("wlan0's implausible saturation = %v, want nil", ls[1].SaturationPct)
	}
	if ls[1].Iface != "wlan0" || ls[1].ThroughputBps != 2000 {
		t.Errorf("wlan0's other fields must survive untouched: %+v", ls[1])
	}
	if ls[2].SaturationPct != nil {
		t.Errorf("veth1's unset saturation = %v, want nil (never reported, not dropped)", ls[2].SaturationPct)
	}
}

// TestNoteFlowsAppliesTheWholeBatchDespiteOneImplausibleSaturation covers the "don't reject the whole
// batch" requirement end to end, through the real ingestion function: a batch with one implausible
// field among several good ones is still applied in full (applied=true, err=nil), the good entry's
// reading survives, and the anomaly is counted rather than silently lost.
func TestNoteFlowsAppliesTheWholeBatchDespiteOneImplausibleSaturation(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	r.hub.mu.Lock()
	r.hub.views[id] = newView()
	r.hub.mu.Unlock()

	before := Metrics.implausibleFacts.Load()
	good, bad := 10.0, -5.0
	b := &continuumv1.FlowBatch{WindowSeconds: 60, Collectors: []*continuumv1.CollectorInfo{
		{Node: "n1", Method: "ebpf", LinkSaturation: []*continuumv1.LinkSaturation{
			{Iface: "eth0", ThroughputBps: 100, SaturationPct: &good},
			{Iface: "eth1", ThroughputBps: 200, SaturationPct: &bad},
		}},
	}}
	applied, err := r.hub.noteFlows(id, 2, b, time.Now())
	if !applied || err != nil {
		t.Fatalf("a batch with one implausible field among good ones must still be applied in full: applied=%v err=%v", applied, err)
	}
	if got := Metrics.implausibleFacts.Load() - before; got != 1 {
		t.Fatalf("implausibleFacts counter rose by %d, want 1", got)
	}
	r.hub.mu.Lock()
	ls := r.hub.views[id].linkSat["n1"]
	r.hub.mu.Unlock()
	if len(ls) != 2 {
		t.Fatalf("both collector entries must still be held, just one field nulled: %v", ls)
	}
	for _, e := range ls {
		switch e.Iface {
		case "eth0":
			if e.SaturationPct == nil || *e.SaturationPct != 10.0 {
				t.Errorf("eth0's plausible saturation = %v, want 10 kept", e.SaturationPct)
			}
		case "eth1":
			if e.SaturationPct != nil {
				t.Errorf("eth1's implausible saturation = %v, want nil", e.SaturationPct)
			}
		}
	}
}

// TestApplySyncKeepsTheWholeRecordDespiteOneImplausibleReading is the Sync-side counterpart, exercising
// validateSync+boundSyncFacts+applySync in the exact sequence Connect itself runs for every incoming
// Sync message (see hub.go), then reading the built topology back to confirm the implausible field
// shows up as absent (nil), never a fabricated zero, while a good field on the very same node survives
// and the node itself is still there.
func TestApplySyncKeepsTheWholeRecordDespiteOneImplausibleReading(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	a, err := r.st.GetAgent(r.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	r.hub.mu.Lock()
	v := newView()
	v.connected = true
	r.hub.views[id] = v
	r.hub.mu.Unlock()

	s := &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp}, Nodes: []*continuumv1.NodeFacts{{
		Key: "n1", Name: "n1", Ready: true,
		Probe: &continuumv1.HostProbe{CpuPressurePct: f64(150), MemoryPressurePct: f64(12.5)},
	}}}
	if err := validateSync(s); err != nil {
		t.Fatalf("a shape-valid sync must not be refused: %v", err)
	}
	before := Metrics.implausibleFacts.Load()
	n := boundSyncFacts(s)
	if n != 1 {
		t.Fatalf("dropped %d fields, want 1", n)
	}
	r.hub.noteImplausibleFacts(id, v, n)
	if got := Metrics.implausibleFacts.Load() - before; got != 1 {
		t.Fatalf("implausibleFacts counter rose by %d, want 1", got)
	}
	if _, _, err := r.hub.applySync(a, s, false); err != nil {
		t.Fatalf("a record with one implausible field among good ones must still be applied in full: %v", err)
	}
	doc, err := r.hub.State(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *model.Node
	for i := range doc.Topology.Nodes {
		if doc.Topology.Nodes[i].Name == "n1" {
			got = &doc.Topology.Nodes[i]
		}
	}
	if got == nil {
		t.Fatal("node n1 missing from the built topology")
	}
	if got.CPUPressurePct != nil {
		t.Errorf("cpu pressure = %v, want nil (omitted, not a fabricated 0)", *got.CPUPressurePct)
	}
	if got.MemoryPressurePct == nil || *got.MemoryPressurePct != 12.5 {
		t.Errorf("memory pressure = %v, want 12.5 kept untouched", got.MemoryPressurePct)
	}
}
