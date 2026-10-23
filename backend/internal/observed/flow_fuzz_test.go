package observed

import (
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"

	"google.golang.org/protobuf/proto"
)

// A flow batch is whatever the agent's bytes say. What the intake accepts must be bounded, be free of node-level ends and of a
// saturation no collector could compute, the checks must find nothing more the second time, and applying it must not panic or wrap a
// counter. The seeds run with every `go test`; `go test -fuzz=FuzzFlowBatch ./internal/observed` looks for more.
func FuzzFlowBatch(f *testing.F) {
	pod := func(ref string) *continuumv1.FlowEndpoint {
		return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: ref}
	}
	sat := func(v float64) *float64 { return &v }
	seeds := []*continuumv1.FlowBatch{
		{WindowSeconds: 30, Flows: []*continuumv1.Flow{{Src: pod("a"), Dst: pod("b"), Port: 80, Protocol: "tcp", Connections: 1<<64 - 1, BytesOut: 1}}},
		{WindowSeconds: 30, Flows: []*continuumv1.Flow{{Src: &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_NODE, Ref: "n"}, Dst: pod("b")}, {Src: pod("a")}}},
		{WindowSeconds: 0},
		{WindowSeconds: 10, Collectors: []*continuumv1.CollectorInfo{{Node: "n1", Method: "ebpf", LinkSaturation: []*continuumv1.LinkSaturation{{Iface: "eth0", SaturationPct: sat(250)}, {Iface: "eth1", SaturationPct: sat(40)}}}, {Node: "", Method: "x"}}},
	}
	for _, s := range seeds {
		b, err := proto.Marshal(s)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		b := &continuumv1.FlowBatch{}
		if proto.Unmarshal(data, b) != nil {
			return
		}
		if _, err := SanitizeFlowBatch(b); err != nil {
			return
		}
		BoundFlowFacts(b)
		if n := BoundFlowFacts(b); n != 0 {
			t.Fatalf("a second pass found %d more impossible saturations", n)
		}
		if d, err := SanitizeFlowBatch(b); err != nil || d.N != 0 {
			t.Fatalf("a sanitised batch lost %d more items on a second pass (%v)", d.N, err)
		}
		for _, fl := range append(append([]*continuumv1.Flow{}, b.Flows...), b.PodFlows...) {
			if fl.Src.Kind == continuumv1.FlowEndpoint_NODE || fl.Dst.Kind == continuumv1.FlowEndpoint_NODE {
				t.Fatalf("a node-level flow was kept: %v", fl)
			}
		}
		for _, c := range b.Collectors {
			if c.Method != "ebpf" && c.Method != "conntrack" {
				t.Fatalf("a collector with method %q was kept", c.Method)
			}
			for _, ls := range c.LinkSaturation {
				if ls != nil && ls.SaturationPct != nil && (*ls.SaturationPct != *ls.SaturationPct || *ls.SaturationPct < 0 || *ls.SaturationPct > 100) {
					t.Fatalf("a saturation of %v was kept", *ls.SaturationPct)
				}
			}
		}
		// Applying the batch twice only ever adds: a counter that is at its maximum stays there and never wraps to a small number.
		tbl := NewFlowTable()
		now := time.Unix(1_700_000_000, 0)
		tbl.Apply(b, now)
		first := map[string]uint64{}
		for k, e := range tbl.Edges {
			first[k] = e.Connections
		}
		tbl.Apply(b, now.Add(time.Second))
		for k, e := range tbl.Edges {
			if e.Connections < first[k] {
				t.Fatalf("edge %s went from %d connections to %d by being applied again", k, first[k], e.Connections)
			}
		}
	})
}
