package server

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/estate"
	"continuum/internal/flow"
	"continuum/internal/flow/wire"
	"continuum/internal/observed"
	"continuum/internal/testkit"
)

// ---- the agent-to-server contract ----
//
// What the agent can produce must be accepted by the server, or be dropped and counted - never a refusal, which
// ends the stream. These tests build the agent's output at its limits and run it through the server's own checks.

func ep(kind continuumv1.FlowEndpoint_Kind, i int) *continuumv1.FlowEndpoint {
	switch kind {
	case continuumv1.FlowEndpoint_EXTERNAL:
		return testkit.ExternalEndpoint(fmt.Sprintf("93.184.%d.%d", i/250, i%250+1))
	case continuumv1.FlowEndpoint_WORKLOAD:
		return testkit.WorkloadEndpoint(fmt.Sprintf("ns/Deployment/w%d", i))
	}
	return &continuumv1.FlowEndpoint{Kind: kind, Ref: fmt.Sprintf("node%d", i), Ip: ""}
}

// Every combination of endpoint kinds, method, noise class, protocol, port extreme and TLS outcome goes through the
// agent's own aggregator and out as a batch. The batch is never refused; what is dropped is exactly what cannot be
// used (an unresolved end, two outside ends, a node-level end), and everything kept is stored and survives a restart.
func TestWhatTheAgentCanSendIsAcceptedOrCountedAsDropped(t *testing.T) {
	agg := flow.NewAggregator()
	kinds := []continuumv1.FlowEndpoint_Kind{continuumv1.FlowEndpoint_UNRESOLVED, continuumv1.FlowEndpoint_WORKLOAD, continuumv1.FlowEndpoint_NODE, continuumv1.FlowEndpoint_EXTERNAL}
	var tls []continuumv1.TlsHandshakeOutcome
	for v := range continuumv1.TlsHandshakeOutcome_name {
		tls = append(tls, continuumv1.TlsHandshakeOutcome(v))
	}
	wantKept, wantDropped, n := 0, 0, 0
	for _, sk := range kinds {
		for _, dk := range kinds {
			usable := (sk == continuumv1.FlowEndpoint_WORKLOAD || dk == continuumv1.FlowEndpoint_WORKLOAD) &&
				sk != continuumv1.FlowEndpoint_UNRESOLVED && dk != continuumv1.FlowEndpoint_UNRESOLVED && sk != continuumv1.FlowEndpoint_NODE && dk != continuumv1.FlowEndpoint_NODE
			for _, method := range []string{"ebpf", "conntrack"} {
				for _, noise := range []string{"", "dns", "system"} {
					for _, proto := range []string{"tcp", "udp"} {
						for _, port := range []uint32{1, 65535} {
							for _, outcome := range tls {
								n++
								f := &continuumv1.Flow{Src: ep(sk, n), Dst: ep(dk, n), Port: port, Protocol: proto, Method: method, Noise: noise, TlsHandshake: outcome, Connections: 1, BytesKnown: n%2 == 0,
									SniHost: strings.Repeat("h", 5000), Iface: strings.Repeat("i", 5000), DnsQueryNames: slices.Repeat([]string{strings.Repeat("d", 5000)}, 50),
									SrcPod: strings.Repeat("p", 253), DstPod: strings.Repeat("q", 253)}
								agg.Add(f)
								if usable {
									wantKept++
								} else {
									wantDropped++
								}
							}
						}
					}
				}
			}
		}
	}
	if wantKept+wantDropped > flow.MaxFlowsPerBatch {
		t.Fatalf("the matrix (%d flows) no longer fits one batch (%d)", wantKept+wantDropped, flow.MaxFlowsPerBatch)
	}
	b := agg.Flush()
	if b == nil || len(b.Flows) != wantKept+wantDropped {
		t.Fatalf("the aggregator kept %v flows, want %d", b, wantKept+wantDropped)
	}
	// A pod flow is a flow that names a pod: the batch carries both, and the same rules apply to both.
	d, err := observed.SanitizeFlowBatch(b)
	if err != nil {
		t.Fatalf("a batch of agent-made flows was refused: %v", err)
	}
	if len(b.Flows) != wantKept || len(b.PodFlows) > wantKept || d.N < wantDropped {
		t.Fatalf("%d flows kept, want %d; %d pod flows; %d dropped (%v), want at least %d", len(b.Flows), wantKept, len(b.PodFlows), d.N, d.First, wantDropped)
	}
	for _, f := range append(slices.Clone(b.Flows), b.PodFlows...) {
		if err := wire.Check(f); err != nil || f.Src.Kind == continuumv1.FlowEndpoint_NODE || f.Dst.Kind == continuumv1.FlowEndpoint_NODE {
			t.Fatalf("a kept flow is not acceptable: %v %v", err, f)
		}
		if len(f.SniHost) > wire.MaxName || len(f.Iface) > wire.MaxIface || len(f.DnsQueryNames) > wire.MaxDNSNames {
			t.Fatalf("a kept flow carries unbounded text: %d %d %d", len(f.SniHost), len(f.Iface), len(f.DnsQueryNames))
		}
	}
	tb := observed.NewFlowTable()
	tb.Apply(b, time.Now())
	if len(tb.Edges) != wantKept {
		t.Fatalf("%d edges stored, want %d", len(tb.Edges), wantKept)
	}
	data, err := tb.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if back, err := estate.LoadFlowTable(data); err != nil || len(back.Edges) != wantKept {
		t.Fatalf("the stored table does not load again: %v", err)
	}
}

// Maximum-length references and a full collector list are accepted; one past the limit is dropped, not refused.
func TestFlowReferencesAtTheirLimits(t *testing.T) {
	ref := func(n int) string { return "ns/Deployment/" + strings.Repeat("r", n-len("ns/Deployment/")) }
	atLimit := testkit.FlowOf(testkit.WorkloadEndpoint(ref(wire.MaxRef)), testkit.ExternalEndpoint("1.2.3.4"), 443, 1)
	atLimit.SrcPod = strings.Repeat("p", wire.MaxName)
	over := testkit.FlowOf(testkit.WorkloadEndpoint(ref(wire.MaxRef+1)), testkit.ExternalEndpoint("1.2.3.4"), 443, 1)
	var cs []*continuumv1.CollectorInfo
	for i := 0; i < 5000; i++ {
		cs = append(cs, &continuumv1.CollectorInfo{Node: strings.Repeat("n", wire.MaxName-5) + fmt.Sprint(i%10000), Method: "conntrack"})
	}
	b := &continuumv1.FlowBatch{WindowSeconds: 24 * 3600, Flows: []*continuumv1.Flow{atLimit, over}, PodFlows: []*continuumv1.Flow{atLimit}, Collectors: cs}
	d, err := observed.SanitizeFlowBatch(b)
	if err != nil || d.N != 1 || len(b.Flows) != 1 || len(b.PodFlows) != 1 || len(b.Collectors) != 5000 {
		t.Fatalf("err %v, dropped %d, %d flows, %d pod flows, %d collectors", err, d.N, len(b.Flows), len(b.PodFlows), len(b.Collectors))
	}
}
