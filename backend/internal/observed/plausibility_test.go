package observed

import (
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

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
	if n := BoundFlowFacts(b); n != 1 {
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
